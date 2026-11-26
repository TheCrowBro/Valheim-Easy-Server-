//go:build !windows

package platform

import (
	"errors"
	"os/exec"
	"time"
)

// Start starts a game's server that keeps running if this app closes, as
// StartDetached does, with the game's own standard input and output and
// any extra files it should keep open (o.Inherit, which become its file
// descriptors 3, 4, …).
//
// The game gets a process group of its own (Setpgid, never Setsid: the two
// together fail), so a Ctrl+C in a terminal running this app doesn't reach
// it, while Interrupt still does. A signal this app merely handles (main.go
// uses signal.Notify) goes back to its default in the game; only a signal
// the app ignored would stay ignored, which is why the app never uses
// signal.Ignore for SIGINT or SIGHUP.
//
// The console lock is held while starting, as StartDetached does.
func Start(o StartOptions) (*Proc, error) {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	cmd := exec.Command(o.Exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	// Only set the ones given: a nil *os.File in an io.Reader is not a
	// nil io.Reader, and exec would try to read from it.
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	cmd.ExtraFiles = o.Inherit
	cmd.SysProcAttr = HiddenProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &Proc{pid: cmd.Process.Pid, start: procStartTime(cmd.Process.Pid), done: make(chan struct{})}
	if p.start.IsZero() {
		p.start = time.Now()
	}
	go func() {
		// Reap our child so it doesn't linger as a zombie. Every file
		// above is an *os.File, so exec copies nothing and Wait returns
		// as soon as the game exits.
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		p.mu.Lock()
		p.exitCode = code
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}
