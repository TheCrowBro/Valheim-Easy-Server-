//go:build !windows

package terraria

// xnaInstalled: only the Windows server uses XNA. The Mac and Linux
// servers bring FNA, which does the same job, in their own folders.
func xnaInstalled() bool { return true }
