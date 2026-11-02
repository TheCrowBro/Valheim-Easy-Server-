//go:build darwin

package platform

import (
	"os/exec"
	"time"
)

// DarkWake reports whether the Mac is only half awake (a "dark wake"):
// macOS wakes it now and then for network or maintenance work with the
// screen off, and a Wake-on-LAN signal can leave it that way too. It's
// false when macOS doesn't say.
func DarkWake() bool {
	out, err := exec.Command("/usr/bin/pmset", "-g", "systemstate").Output()
	if err != nil {
		return false
	}
	return parseDarkWake(string(out))
}

// WokeBy says what woke the Mac up: a device such as the network adapter
// (Wake-on-LAN) or the power button, or macOS itself, for maintenance.
// macOS only keeps the latest reason, which is the one asked about: since
// is for Windows, which keeps a list.
func WokeBy(since time.Time) WakeCause {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.wakereason").Output()
	if err != nil {
		return WakeCauseUnknown
	}
	return parseMacWakeReason(string(out))
}
