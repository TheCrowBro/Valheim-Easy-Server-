//go:build darwin

package platform

import "os/exec"

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
