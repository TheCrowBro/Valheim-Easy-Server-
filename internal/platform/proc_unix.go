//go:build !windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var consoleMu sync.Mutex

// Proc is a handle to a running process (ours or one found running).
type Proc struct {
	pid   int
	start time.Time

	mu       sync.Mutex
	done     chan struct{}
	exitCode int
}

// HiddenProcAttr puts children in their own process group, so pressing
// Ctrl+C in a terminal running this app doesn't also stop the server.
func HiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
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
	p := &Proc{pid: cmd.Process.Pid, start: procStartTime(cmd.Process.Pid), done: make(chan struct{})}
	if p.start.IsZero() {
		p.start = time.Now()
	}
	go func() {
		err := cmd.Wait() // reap our child so it doesn't linger as a zombie
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		p.mu.Lock()
		p.exitCode = code
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// OpenProc opens a process we didn't start (e.g. after this app restarted).
func OpenProc(pid int) (*Proc, error) {
	if err := syscall.Kill(pid, 0); err != nil {
		return nil, err
	}
	p := &Proc{pid: pid, start: procStartTime(pid), done: make(chan struct{})}
	go func() {
		for processAlive(pid) {
			time.Sleep(500 * time.Millisecond)
		}
		p.mu.Lock()
		p.exitCode = -1 // not our child: the exit code is unknown
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A zombie still answers kill(0); treat it as gone.
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return !errors.Is(err, os.ErrNotExist) || !dirExists("/proc/self")
	}
	if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) {
		return b[i+2] != 'Z'
	}
	return true
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// PID returns the process ID.
func (p *Proc) PID() int { return p.pid }

// StartTime is when the process started.
func (p *Proc) StartTime() time.Time { return p.start }

// Wait blocks until the process exits and returns its exit code.
func (p *Proc) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, nil
}

// Exited reports whether the process has already ended.
func (p *Proc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Interrupt asks the process to stop (SIGINT, like Ctrl+C).
func (p *Proc) Interrupt() error { return syscall.Kill(p.pid, syscall.SIGINT) }

// Kill ends the process immediately.
func (p *Proc) Kill() error { return syscall.Kill(p.pid, syscall.SIGKILL) }

// Close releases resources (nothing to do on Unix).
func (p *Proc) Close() {}

const clockTicks = 100 // USER_HZ on Linux

// Usage returns total CPU time used so far and current memory use.
func (p *Proc) Usage() (time.Duration, uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	f := statFields(string(b))
	if len(f) < 24 {
		return 0, 0, errors.New("unexpected /proc stat format")
	}
	utime, _ := strconv.ParseUint(f[13], 10, 64)
	stime, _ := strconv.ParseUint(f[14], 10, 64)
	rssPages, _ := strconv.ParseUint(f[23], 10, 64)
	cpu := time.Duration(utime+stime) * time.Second / clockTicks
	return cpu, rssPages * uint64(os.Getpagesize()), nil
}

// statFields splits /proc/<pid>/stat, keeping the command name (which can
// contain spaces) as one field.
func statFields(s string) []string {
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return strings.Fields(s)
	}
	out := []string{strings.TrimSpace(s[:open]), s[open+1 : close]}
	return append(out, strings.Fields(s[close+1:])...)
}

func procStartTime(pid int) time.Time {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}
	}
	f := statFields(string(b))
	if len(f) < 22 {
		return time.Time{}
	}
	ticks, err := strconv.ParseUint(f[21], 10, 64)
	if err != nil {
		return time.Time{}
	}
	boot := bootTime()
	if boot.IsZero() {
		return time.Time{}
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicks)
}

func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "btime ") {
			sec, err := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64)
			if err == nil {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}

// FindByExe lists running processes started from exactly this program file.
func FindByExe(exePath string) ([]ProcInfo, error) {
	want, err := filepath.Abs(exePath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil // no /proc (e.g. macOS): nothing to find
	}
	var out []ProcInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil || exe != want || !processAlive(pid) {
			continue
		}
		out = append(out, ProcInfo{PID: pid, Exe: exe, Started: procStartTime(pid)})
	}
	return out, nil
}
