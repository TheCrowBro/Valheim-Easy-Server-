package gametest

import (
	"errors"
	"os/exec"
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

// launchOnce starts the program once. Until platform.Start is written (it
// is stream P's, and says ErrUnsupported until then), it starts the
// program the way platform.StartDetached does, which only lacks the extra
// handles a console game keeps (StartOptions.Inherit): the stand-in
// carries on when its console ends, so the sessions behave the same.
func launchOnce(o platform.StartOptions) (*platform.Proc, error) {
	p, err := platform.Start(o)
	if err == nil || !(errors.Is(err, errors.ErrUnsupported) || errors.Is(err, platform.ErrUnsupported)) {
		return p, err
	}
	cmd := exec.Command(o.Exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	// A nil *os.File in an io.Reader isn't a nil io.Reader: set only
	// those given.
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	cmd.SysProcAttr = platform.HiddenProcAttr()
	if err := platform.StartCmd(cmd); err != nil {
		return nil, err
	}
	// The handle the rest of the session uses; then reap the child here
	// when it ends, so it never lingers as a zombie.
	p, err = platform.OpenProc(cmd.Process.Pid)
	go cmd.Wait()
	if err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	return p, nil
}
