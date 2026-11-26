//go:build !windows && !darwin

package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcName is a process's program name, for "used by <program>"; "" when
// unknown. The program's path gives the whole name, but only for this
// user's processes; for others the kernel's own name for it is used,
// which is cut at 15 characters.
func ProcName(pid int) string {
	if pid <= 0 {
		return ""
	}
	dir := "/proc/" + strconv.Itoa(pid)
	if exe, err := os.Readlink(dir + "/exe"); err == nil && exe != "" {
		return filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
	}
	if b, err := os.ReadFile(dir + "/comm"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}
