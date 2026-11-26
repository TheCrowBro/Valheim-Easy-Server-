package gametest

import (
	"errors"
	"os/exec"

	"valheim-easy-server/internal/platform"
)

// launch starts a game's server the way the manager does, with
// platform.Start (spec 2.12). Until that is written (it is stream P's, and
// says ErrUnsupported until then), it starts the program the way
// platform.StartDetached does, which only lacks the extra handles a console
// game keeps (StartOptions.Inherit): the stand-in carries on when its
// console ends, so the sessions behave the same.
func launch(o platform.StartOptions) (*platform.Proc, error) {
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
