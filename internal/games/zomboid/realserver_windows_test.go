//go:build realserver && windows

package zomboid

import (
	"os/exec"
	"strconv"
	"syscall"
)

// shortPath is path with its short "8.3" names, as Windows gives them
// (path itself when it has none), for the check that starts the server
// from a folder named that way.
func shortPath(path string) string {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		return path
	}
	return syscall.UTF16ToString(buf[:n])
}

// killTree ends a program and every program it started: the .bat's
// cmd.exe and the Java it runs.
func killTree(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
