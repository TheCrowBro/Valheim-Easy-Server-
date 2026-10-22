//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procK32GetProcessMemoryInfo  = kernel32.NewProc("K32GetProcessMemoryInfo")
)

// consoleMu serialises attaching to another process's console and creating
// processes, because console attachment is process-wide state.
var consoleMu sync.Mutex

// Proc is a handle to a running process (ours or one found running).
type Proc struct {
	pid   int
	h     windows.Handle
	start time.Time
}

// HiddenProcAttr runs console programs without a console window. The
// program still gets an (invisible) console, which is what lets us send
// it Ctrl+C later.
func HiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// StartDetached starts a program that keeps running if this app closes.
func StartDetached(exe string, args []string, dir string, env []string) (*Proc, error) {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = HiddenProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Open our own handle before releasing Go's, so the PID can't be
	// reused in between.
	p, err := OpenProc(cmd.Process.Pid)
	cmd.Process.Release()
	return p, err
}

// OpenProc opens a running process by PID.
func OpenProc(pid int) (*Proc, error) {
	access := uint32(windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_VM_READ | windows.PROCESS_TERMINATE)
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		h, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	}
	if err != nil {
		return nil, err
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &Proc{pid: pid, h: h, start: time.Unix(0, c.Nanoseconds())}, nil
}

// PID returns the process ID.
func (p *Proc) PID() int { return p.pid }

// StartTime is when the process started.
func (p *Proc) StartTime() time.Time { return p.start }

// Wait blocks until the process exits and returns its exit code.
func (p *Proc) Wait() (int, error) {
	if _, err := windows.WaitForSingleObject(p.h, windows.INFINITE); err != nil {
		return -1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.h, &code); err != nil {
		return -1, err
	}
	return int(int32(code)), nil
}

// Exited reports whether the process has already ended.
func (p *Proc) Exited() bool {
	ev, err := windows.WaitForSingleObject(p.h, 0)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// Interrupt asks the process to stop the way pressing Ctrl+C in its window
// would. Valheim saves the world when it gets this, which is why the
// official guide says to stop the server with Ctrl+C.
func (p *Proc) Interrupt() error {
	consoleMu.Lock()
	defer consoleMu.Unlock()

	procFreeConsole.Call() // a GUI app has no console; make sure of it
	if r, _, err := procAttachConsole.Call(uintptr(p.pid)); r == 0 {
		return fmt.Errorf("couldn't reach the server's console: %w", err)
	}
	// Ignore Ctrl+C in this process while we share the server's console.
	procSetConsoleCtrlHandler.Call(0, 1)
	r, _, sendErr := procGenerateConsoleCtrlEvent.Call(windows.CTRL_C_EVENT, 0)
	procFreeConsole.Call()
	// The event is delivered asynchronously; wait before handling Ctrl+C
	// normally again so it can't arrive after we stop ignoring it.
	time.Sleep(time.Second)
	procSetConsoleCtrlHandler.Call(0, 0)
	if r == 0 {
		return fmt.Errorf("couldn't send Ctrl+C to the server: %w", sendErr)
	}
	return nil
}

// Kill ends the process immediately, without saving.
func (p *Proc) Kill() error {
	return windows.TerminateProcess(p.h, 1)
}

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// Usage returns total CPU time used so far and current memory use.
func (p *Proc) Usage() (cpu time.Duration, mem uint64, err error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(p.h, &c, &e, &k, &u); err != nil {
		return 0, 0, err
	}
	cpu = time.Duration((filetimeTicks(k) + filetimeTicks(u)) * 100)
	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(p.h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB)); r != 0 {
		mem = uint64(pmc.WorkingSetSize)
	}
	return cpu, mem, nil
}

func filetimeTicks(f windows.Filetime) int64 {
	return int64(f.HighDateTime)<<32 | int64(f.LowDateTime)
}

// Close releases the handle. Don't call it while Wait is running.
func (p *Proc) Close() {
	if p.h != 0 {
		windows.CloseHandle(p.h)
		p.h = 0
	}
}

// FindByExe lists running processes started from exactly this program file.
func FindByExe(exePath string) ([]ProcInfo, error) {
	want := strings.ToLower(filepath.Clean(exePath))
	base := filepath.Base(want)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var out []ProcInfo
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.ToLower(windows.UTF16ToString(pe.ExeFile[:])) != base {
			continue
		}
		path, start, ok := processPath(pe.ProcessID)
		if ok && strings.ToLower(filepath.Clean(path)) == want {
			out = append(out, ProcInfo{PID: int(pe.ProcessID), Exe: path, Started: start})
		}
	}
	return out, nil
}

func processPath(pid uint32) (string, time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", time.Time{}, false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", time.Time{}, false
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return "", time.Time{}, false
	}
	return windows.UTF16ToString(buf[:n]), time.Unix(0, c.Nanoseconds()), true
}
