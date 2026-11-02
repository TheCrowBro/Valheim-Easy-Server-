//go:build windows

package platform

import (
	"os/exec"
	"time"
)

// DarkWake reports whether the computer is only half awake, which only
// happens on a Mac.
func DarkWake() bool { return false }

// WokeBy says what woke the computer up, if it woke up after since: a
// device such as the network adapter (Wake-on-LAN) or the power button, or
// a timer. It's unknown until Windows has logged the wake-up, a few seconds
// after it.
func WokeBy(since time.Time) WakeCause {
	cmd := exec.Command(system32("wevtutil.exe"), "qe", "System", "/rd:true", "/c:1", "/f:xml",
		"/q:*[System[Provider[@Name='Microsoft-Windows-Power-Troubleshooter'] and (EventID=1)]]")
	cmd.SysProcAttr = HiddenProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return WakeCauseUnknown
	}
	return parseWindowsWake(string(out), since)
}
