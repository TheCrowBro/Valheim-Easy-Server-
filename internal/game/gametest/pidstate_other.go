//go:build !windows

package gametest

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// pidState looks the process up afresh by its PID, for a failure message,
// sharing nothing with the handle gametest keeps: whether a process with
// that PID is still there. (Unix keeps no exit code once the process has
// been reaped, which platform does as soon as it ends.)
func pidState(pid int, _ time.Time) string {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return "a process with that PID is still there"
	case errors.Is(err, syscall.ESRCH):
		return "no process has that PID any more"
	case errors.Is(err, syscall.EPERM):
		return "that PID now belongs to another user's program, so the server has ended"
	}
	return fmt.Sprintf("it can't be checked (%v)", err)
}
