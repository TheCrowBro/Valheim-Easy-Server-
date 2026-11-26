//go:build !windows

package palworld

// vcRuntimeOK: only the Windows server needs Microsoft's Visual C++
// runtime, and the installer only asks on Windows.
func vcRuntimeOK() bool { return true }

// vcRuntimeVersion is only known on Windows.
func vcRuntimeVersion() string { return "" }
