package windrose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"valheim-easy-server/internal/platform"
)

// The server makes its settings file (R5/ServerDescription.json: its
// identity with Windrose's online service, its invite code) and its first
// world the first time it starts, and the publisher says to start and stop
// it once before changing anything ("Start and stop the server once to
// generate them", DedicatedServer.md). The app's own settings can only be
// written into that file, so when it isn't there, the plan runs the server
// once, hidden, until it says it is ready, stops it the usual way
// (Ctrl+C, which it answers by saving), and then plans the real start.
//
// It runs at the first start rather than in the install: the server
// listens on the network as soon as it starts, and its firewall rule names
// the installed program, so it can only be in place after the install.
// Run at the install's end, the hidden server could raise Windows'
// firewall question before the owner had the chance to make the rule.
const (
	// firstRunWait is how long the first start may take to make its
	// world: a fresh install took about 45 seconds to write the file
	// (FACTS.md); a slow computer and a slow connection to Windrose's
	// online service take longer.
	firstRunWait = 10 * time.Minute
	// firstStopWait is how long it may take to save and stop after that.
	// Nothing has been played on it, so a server still running then is
	// ended.
	firstStopWait = 2 * time.Minute
)

// firstRun starts the server once so it makes its settings file and its
// first world.
func (s *server) firstRun(ctx context.Context, step func(string, float64)) error {
	step("Starting the Windrose server once, so it makes its settings file and first world (only the first time, about a minute)…", -1)
	if err := s.setLogAside(); err != nil {
		s.log.Printf("couldn't move the server's last log aside: %v", err)
	}
	p, err := startServer(platform.StartOptions{Exe: s.Program(), Args: serverArgs, Dir: s.env.Dirs.Server, Env: os.Environ()})
	if err != nil {
		return fmt.Errorf("Couldn't start the Windrose server: %w", err)
	}
	defer p.Close()
	done := make(chan struct{})
	var code int
	go func() {
		code, _ = p.Wait()
		close(done)
	}()
	kill := func() {
		p.Kill()
		<-done
	}

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(firstRunWait)
	defer deadline.Stop()
	for ready := false; !ready; {
		select {
		case <-ctx.Done():
			kill()
			return ctx.Err()
		case <-done:
			msg := fmt.Sprintf("The Windrose server stopped during its first start (exit code %d), before it had made its settings file and first world.", code)
			if hint := s.ExitHint(s.logTail(40)); hint != "" {
				msg += " " + hint
			}
			return errors.New(msg)
		case <-deadline.C:
			kill()
			return fmt.Errorf("The Windrose server's first start didn't finish making its first world within %v. Its log on the Log page may say why; start it again to retry.", firstRunWait)
		case <-tick.C:
			ready = s.firstRunDone()
		}
	}

	step("Saving and stopping the Windrose server's first start…", -1)
	if err := askToStop(ctx, p); err != nil {
		s.log.Printf("first start: asking the server to stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(firstStopWait):
		s.log.Printf("first start: the server didn't stop within %v of being asked; ending it", firstStopWait)
		kill()
	case <-ctx.Done():
		kill()
		return ctx.Err()
	}
	if _, err := readDescription(s.descriptionPath()); err != nil {
		return fmt.Errorf("The Windrose server's first start didn't leave a settings file the app can read: %w", err)
	}
	return nil
}

// firstRunDone says whether the first start has made everything: the
// settings file names a world, and the log says the server is ready.
func (s *server) firstRunDone() bool {
	d, err := readDescription(s.descriptionPath())
	if err != nil || d.str(keyWorld) == "" {
		return false
	}
	b, err := os.ReadFile(s.logPath())
	return err == nil && (reReady.Match(b) || reWaiting.Match(b))
}

// logTail is the last lines of the server's own log.
func (s *server) logTail(n int) []string {
	b, err := os.ReadFile(s.logPath())
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// startServer starts the server's program. Starting a program that was
// only just written can fail on Linux with "text file busy" while another
// goroutine starts some other program (Go issue 22315): that only happens
// with the stand-in in tests, and a moment later it works.
func startServer(o platform.StartOptions) (*platform.Proc, error) {
	for try := 1; ; try++ {
		p, err := platform.Start(o)
		if errors.Is(err, syscall.ETXTBSY) && try < 10 {
			time.Sleep(time.Duration(try) * 20 * time.Millisecond)
			continue
		}
		return p, err
	}
}
