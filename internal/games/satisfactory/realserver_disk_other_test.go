//go:build realserver && !windows

package satisfactory

// freeSpace isn't needed off Windows, where the check skips.
func freeSpace(dir string) string { return "not measured on this system" }
