package gametest

import (
	"errors"
	"syscall"
	"time"

	"valheim-easy-server/internal/platform"
)

// launch starts a game's server the way the manager does, with
// platform.Start (spec 2.12).
//
// Starting a program that was only just written can fail on Linux with
// "text file busy" when another goroutine of this test program starts some
// other program at the same moment: the new process holds a copy of the
// writer's file handle until it runs its own program (Go issue 22315).
// Sessions running at once install (copy the stand-in) and start at once,
// so launch waits a moment and tries again, as go build itself does.
func launch(o platform.StartOptions) (*platform.Proc, error) {
	for try := 1; ; try++ {
		p, err := launchOnce(o)
		if errors.Is(err, syscall.ETXTBSY) && try < 10 {
			time.Sleep(time.Duration(try) * 20 * time.Millisecond)
			continue
		}
		return p, err
	}
}

// launchOnce starts the program once, as the manager starts every game
// but Valheim: platform.Start, which is Valheim's StartDetached plus the
// game's standard input and output. On Windows that gives the game a
// hidden console of its own, which is how Interrupt reaches it with
// Ctrl+C; elsewhere a process group of its own.
func launchOnce(o platform.StartOptions) (*platform.Proc, error) {
	return platform.Start(o)
}
