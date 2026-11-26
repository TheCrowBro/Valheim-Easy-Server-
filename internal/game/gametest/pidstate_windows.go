//go:build windows

package gametest

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// pidState looks the process up afresh by its PID, for a failure message,
// sharing nothing with the handle gametest keeps: whether it still runs
// and, once it has ended, its exit code. Windows keeps an ended process's
// exit code while anyone holds a handle to it (gametest's own does until
// the end has been folded in); once nobody does, the PID is simply gone.
// started tells a PID Windows has given to another program since.
func pidState(pid int, started time.Time) string {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "no process has that PID any more"
	}
	if err != nil {
		return fmt.Sprintf("it can't be opened (%v)", err)
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err == nil && !started.IsZero() {
		if d := time.Unix(0, c.Nanoseconds()).Sub(started); d > 2*time.Second || d < -2*time.Second {
			return "that PID now belongs to another program, so the server has ended"
		}
	}
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return fmt.Sprintf("it can't be checked (%v)", err)
	}
	var code uint32
	codeErr := windows.GetExitCodeProcess(h, &code)
	switch {
	case ev != windows.WAIT_OBJECT_0:
		return "it is still running"
	case codeErr != nil:
		return fmt.Sprintf("it has ended (its exit code can't be read: %v)", codeErr)
	}
	return fmt.Sprintf("it has ended, with exit code %d", int32(code))
}
