//go:build realserver && !windows

package zomboid

import "os"

// shortPath is path itself: only Windows has short "8.3" names.
func shortPath(path string) string { return path }

// killTree ends the program (the checks start Java directly off Windows,
// so there is no tree to end).
func killTree(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
