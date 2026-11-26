//go:build darwin

package platform

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ProcName is a process's program name, for "used by <program>" (java,
// TerrariaServer.bin.osx); "" when unknown. The path it was started with
// gives the whole name, but only for this user's processes; for others
// the kernel's own name for it is used, which is cut at 16 characters.
func ProcName(pid int) string {
	if pid <= 0 {
		return ""
	}
	kp, ok := kinfo(pid)
	if !ok {
		return ""
	}
	if exe := procExe(pid); exe != "" {
		return filepath.Base(exe)
	}
	return unix.ByteSliceToString(kp.Proc.P_comm[:])
}
