//go:build !darwin

package platform

// DarkWake reports whether the computer is only half awake, which only
// happens on a Mac.
func DarkWake() bool { return false }
