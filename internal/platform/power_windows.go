//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powrprof               = windows.NewLazySystemDLL("powrprof.dll")
	procGetPwrCapabilities = powrprof.NewProc("GetPwrCapabilities")
	procSetSuspendState    = powrprof.NewProc("SetSuspendState")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
	procGetTickCount       = kernel32.NewProc("GetTickCount")
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procGetLastInputInfo   = user32.NewProc("GetLastInputInfo")
)

// system32 is where Windows' own programs are, asked of Windows itself
// rather than taken from an environment variable.
func system32(exe string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil || dir == "" {
		dir = `C:\Windows\System32`
	}
	return filepath.Join(dir, exe)
}

// HibernateStatus reports whether Windows can hibernate right now.
func HibernateStatus() (available bool, detail string) {
	// SYSTEM_POWER_CAPABILITIES starts with BOOLEAN fields; SystemS4 is
	// byte 6 and HiberFilePresent byte 8.
	var caps [128]byte
	if r, _, _ := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps[0]))); r == 0 {
		return false, "Windows didn't say whether hibernation is available."
	}
	if caps[6] == 0 || caps[8] == 0 {
		return false, "Hibernation is turned off in Windows. Turn it on from the server computer (it asks for admin permission)."
	}
	return true, ""
}

// Hibernate puts the computer into hibernation. It returns right away;
// Windows hibernates a moment later.
func Hibernate() error {
	cmd := exec.Command(system32("shutdown.exe"), "/h")
	cmd.SysProcAttr = HiddenProcAttr()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Windows refused to hibernate: %v %s", err, out)
	}
	return nil
}

// SleepStatus reports whether Windows can sleep (standby, ACPI S3).
func SleepStatus() (available bool, detail string) {
	// SystemS3 is byte 5 of SYSTEM_POWER_CAPABILITIES.
	var caps [128]byte
	if r, _, _ := procGetPwrCapabilities.Call(uintptr(unsafe.Pointer(&caps[0]))); r == 0 {
		return false, "Windows didn't say whether sleep is available."
	}
	if caps[5] == 0 {
		return false, "Windows can't put this computer to sleep (it may be missing a chipset or graphics driver). Use hibernation instead."
	}
	return true, ""
}

// Sleep puts the computer to sleep. Unlike hibernation it keeps a little
// power on, which most network adapters need to wake the computer with
// Wake-on-LAN. The call returns once the computer has woken up again.
func Sleep() error {
	// SetSuspendState(bHibernate=FALSE, bForce=FALSE, bWakeupEventsDisabled=FALSE)
	if r, _, err := procSetSuspendState.Call(0, 0, 0); r == 0 {
		return fmt.Errorf("Windows refused to sleep: %v", err)
	}
	return nil
}

// EnableHibernate turns hibernation on. Windows shows an admin prompt on
// the server computer's screen; this returns before it's answered.
func EnableHibernate() error {
	return runElevated(system32("powercfg.exe"), "/hibernate on")
}

func runElevated(exe, args string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE)
}

// runElevatedPowerShell runs a script with admin rights. It goes in the
// command line, never as a file that another program could change while
// Windows asks for permission.
func runElevatedPowerShell(script string) error {
	return runElevated(system32(`WindowsPowerShell\v1.0\powershell.exe`),
		"-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand "+psEncode(script))
}

// KeepAwake stops Windows from going to sleep on its own while the server
// runs (otherwise a power plan's sleep timer could cut players off).
// Hibernating on purpose still works.
type KeepAwake struct {
	mu sync.Mutex
	h  uintptr
	on bool
}

type reasonContext struct {
	Version uint32
	Flags   uint32
	Reason  *uint16
	_       [16]byte // rest of the REASON_CONTEXT union
}

const powerRequestSystemRequired = 1

// NewKeepAwake creates the power request (inactive until Set(true)).
func NewKeepAwake(reason string) *KeepAwake {
	k := &KeepAwake{}
	text, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return k
	}
	rc := reasonContext{Version: 0, Flags: 1, Reason: text} // POWER_REQUEST_CONTEXT_SIMPLE_STRING
	h, _, _ := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&rc)))
	if h != 0 && h != uintptr(windows.InvalidHandle) {
		k.h = h
	}
	return k
}

// Set turns the request on or off.
func (k *KeepAwake) Set(on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.h == 0 || k.on == on {
		return
	}
	if on {
		procPowerSetRequest.Call(k.h, powerRequestSystemRequired)
	} else {
		procPowerClearRequest.Call(k.h, powerRequestSystemRequired)
	}
	k.on = on
}

type lastInputInfo struct {
	Size uint32
	Time uint32
}

// IdleTime is how long since someone used the keyboard or mouse on this
// computer.
func IdleTime() (time.Duration, bool) {
	li := lastInputInfo{Size: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0, false
	}
	now, _, _ := procGetTickCount.Call()
	return time.Duration(uint32(now)-li.Time) * time.Millisecond, true
}

// AfterWake has nothing to do on Windows: an unattended wake is a full one.
func AfterWake() {}
