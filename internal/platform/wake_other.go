//go:build !darwin && !windows

package platform

import "time"

// DarkWake reports whether the computer is only half awake, which only
// happens on a Mac.
func DarkWake() bool { return false }

// WokeBy says what woke the computer up, if it woke up after since. Only
// Windows and macOS say.
func WokeBy(since time.Time) WakeCause { return WakeCauseUnknown }
