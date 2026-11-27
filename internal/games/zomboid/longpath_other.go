//go:build !windows

package zomboid

// longPath is path as it is: only Windows has short "8.3" names
// (longpath_windows.go).
func longPath(path string) string { return path }
