package gametest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// attachment is one process being watched: from a start until it exits,
// or until a takeover hands it to a new attachment (and a new Monitor).
type attachment struct {
	s     *Session
	mon   game.Monitor
	rec   game.Record
	proc  procHandle
	stdin *os.File // the console pipe's write end; nil without one (after a takeover)
	try   int      // which try of a start this run is (1 unless retryOnNewPorts started it again); 0 after a takeover

	cancel context.CancelFunc
	loops  sync.WaitGroup
	// ended is closed as soon as gametest's wait for the process returns:
	// the process is gone. exited comes later, once its last log lines
	// were read and End folded in. waitCode, waitErr and endedAt are
	// written before ended is closed and read only after.
	ended    chan struct{}
	waitCode int
	waitErr  error
	endedAt  time.Time
	exited   chan struct{} // closed once the process ended and End was folded in

	// procMu guards the handle: the reaper releases it (proc.Close) under
	// procMu, and everything else uses it only through useProc, which
	// holds procMu and refuses once it is released.
	procMu       sync.Mutex
	procReleased bool

	// Under s.mu:
	events   []game.Event
	used     []bool        // events WaitEvent has returned
	changed  chan struct{} // closed and replaced whenever events arrive or the process ends
	lines    []string      // the last lines, for failure messages
	exitCode int
	detached bool
}

// procHandle is what gametest uses of a *platform.Proc, so a test can
// stand in for one that behaves as Windows' does (handle_test.go).
//
// After Close, a Proc must not be used. On Windows that isn't only good
// manners: Close releases the handle, after which Exited says false and
// Kill fails, whether or not the process is still there (on Linux and
// macOS Close does nothing and Exited goes on saying true). So gametest
// asks "has it ended?" of its own wait (attachment.ended), never of a
// handle that may have been released, and touches the handle only through
// useProc.
type procHandle interface {
	PID() int
	StartTime() time.Time
	Wait() (int, error)
	Exited() bool
	Interrupt() error
	Kill() error
	Close()
}

var _ procHandle = (*platform.Proc)(nil)

// probeEvery is how often the probe runs while the server starts and once
// it's ready (the manager probes every 5 s, then every 30 s; tests want
// answers sooner).
const (
	probeStarting = time.Second
	probeReady    = 2 * time.Second
)

// attach starts watching proc: following its log from the start, the
// Monitor's ticks, the probe and the exit.
func (s *Session) attach(proc procHandle, rec game.Record, stdin *os.File, mon game.Monitor, try int) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &attachment{s: s, mon: mon, rec: rec, proc: proc, stdin: stdin, try: try, cancel: cancel,
		ended: make(chan struct{}), exited: make(chan struct{}), changed: make(chan struct{})}
	s.mu.Lock()
	s.run = a
	srv := s.srv
	s.mu.Unlock()

	a.loops.Add(2)
	go func() {
		defer a.loops.Done()
		follow(ctx, rec.LogPath, a.line)
	}()
	go func() {
		defer a.loops.Done()
		t := time.NewTicker(400 * time.Millisecond) // as the manager ticks
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.mu.Lock()
				a.addLocked(mon.Tick(now))
				s.mu.Unlock()
			}
		}
	}()
	if p, ok := srv.(game.Prober); ok {
		a.loops.Add(1)
		go func() {
			defer a.loops.Done()
			a.probe(ctx, p)
		}()
	}
	go func() {
		code, err := proc.Wait()
		a.waitCode, a.waitErr, a.endedAt = code, err, time.Now()
		close(a.ended)                     // the stop chain's wait sees this at once
		time.Sleep(700 * time.Millisecond) // let the log reader catch the last lines, as the manager does
		cancel()
		a.loops.Wait()
		s.mu.Lock()
		if !a.detached {
			a.addLocked(mon.End(time.Now()))
		}
		a.exitCode = code
		close(a.exited)
		a.notifyLocked()
		s.mu.Unlock()
		// Release the handle only now, and under procMu, so it is never
		// closed under a Ctrl+C or a kill in progress, and nothing uses it
		// afterwards (see procHandle).
		a.procMu.Lock()
		a.procReleased = true
		proc.Close()
		a.procMu.Unlock()
	}()
}

// errEnded is what useProc gives once the process has ended and its
// handle was released.
var errEnded = errors.New("the server's process has already ended")

// useProc runs f with the process's handle, unless the process has ended
// and the handle was released: then it gives errEnded without calling f.
func (a *attachment) useProc(f func(p procHandle) error) error {
	a.procMu.Lock()
	defer a.procMu.Unlock()
	if a.procReleased {
		return errEnded
	}
	return f(a.proc)
}

// interrupt sends the process Ctrl+C (SIGINT on Unix).
func (a *attachment) interrupt() error {
	return a.useProc(func(p procHandle) error { return p.Interrupt() })
}

// kill ends the process at once, unless it has already ended.
func (a *attachment) kill() {
	a.useProc(func(p procHandle) error { return p.Kill() })
}

// hasEnded says whether the process has ended: gametest's wait for it
// returned. Unlike isExited it doesn't wait for the last log lines.
func (a *attachment) hasEnded() bool {
	select {
	case <-a.ended:
		return true
	default:
		return false
	}
}

// exitReport says, for a failure message, what each way of looking at the
// process says about whether it is still running: gametest's own wait for
// it, the handle's own check (Proc.Exited), and a fresh look-up by its PID
// that shares nothing with that handle (pidState; on Windows it also gives
// the exit code of a process that has ended). If they disagree, gametest
// lost track of a process that ended; if they all say it runs while the
// stand-in's log says "fakeserver: exiting now", the program is stuck on
// its way out.
func (a *attachment) exitReport() string {
	pid := a.proc.PID()
	var b strings.Builder
	fmt.Fprintf(&b, "what the server's process (PID %d) looked like: ", pid)
	select {
	case <-a.ended:
		if a.waitErr != nil {
			fmt.Fprintf(&b, "gametest's wait for it failed (%v) %s ago", a.waitErr, time.Since(a.endedAt).Round(time.Millisecond))
		} else {
			fmt.Fprintf(&b, "gametest's wait for it returned (exit code %d) %s ago", a.waitCode, time.Since(a.endedAt).Round(time.Millisecond))
		}
	default:
		b.WriteString("gametest's wait for it hasn't returned")
	}
	a.procMu.Lock()
	switch {
	case a.procReleased:
		b.WriteString("; its handle has been released")
	case a.proc.Exited():
		b.WriteString("; its handle says it has ended")
	default:
		b.WriteString("; its handle says it is still running")
	}
	a.procMu.Unlock()
	fmt.Fprintf(&b, "; looked up afresh by its PID, %s", pidState(pid, a.proc.StartTime()))
	return b.String()
}

// line feeds one log line to the Monitor.
func (a *attachment) line(text string) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if a.detached {
		return
	}
	if len(a.lines) == 60 {
		a.lines = a.lines[1:]
	}
	a.lines = append(a.lines, text)
	a.addLocked(a.mon.Line(text, time.Now()).Events)
}

// addLocked records the Monitor's events.
func (a *attachment) addLocked(evs []game.Event) {
	if len(evs) == 0 || a.detached {
		return
	}
	for _, e := range evs {
		if e.Kind == "join" && e.Account != "" {
			a.s.known[e.Name] = e.Account
		}
		a.events = append(a.events, e)
		a.used = append(a.used, false)
	}
	a.notifyLocked()
}

func (a *attachment) notifyLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

// probe runs the adapter's probe as the manager does (spec 2.6): often
// while the server starts, then regularly once it's ready, folding each
// answer in through the Monitor.
func (a *attachment) probe(ctx context.Context, p game.Prober) {
	fails := 0
	for {
		a.s.mu.Lock()
		ready := a.mon.State().Ready
		a.s.mu.Unlock()
		wait := probeStarting
		if ready {
			wait = probeReady
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		pctx, cancel := context.WithTimeout(ctx, probeLimit)
		res, err := p.Probe(pctx, a.rec)
		cancel()
		if err != nil {
			if fails++; fails == 3 && ready {
				a.s.tlog.Printf("the probe failed three times in a row: %v", err)
			}
			continue
		}
		fails = 0
		a.s.mu.Lock()
		a.addLocked(a.mon.Probed(res, time.Now()))
		a.s.mu.Unlock()
	}
}

// isExited says whether the process has ended (and End was folded in).
func (a *attachment) isExited() bool {
	select {
	case <-a.exited:
		return true
	default:
		return false
	}
}

// detach stops watching without stopping the process, as an app that
// exits does: the loops end and the console pipe closes. The process is
// still reaped when it ends.
func (a *attachment) detach() {
	a.s.mu.Lock()
	a.detached = true
	stdin := a.stdin
	a.stdin = nil
	a.s.mu.Unlock()
	a.cancel()
	a.loops.Wait()
	if stdin != nil {
		stdin.Close()
	}
}

// typeLine types a line on the game's console. A game that stopped
// reading would leave the write waiting, so it gives up after 5 seconds.
func (a *attachment) typeLine(line string) error {
	a.s.mu.Lock()
	w := a.stdin
	a.s.mu.Unlock()
	if w == nil {
		return game.ErrNoConsole
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, line+"\n")
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("the game's console isn't taking input")
	}
}

// waitExit waits up to wait for the process to exit, calling again every
// resend (when it isn't 0). It says whether the process exited.
//
// It asks gametest's own wait for the process (ended), never the handle:
// on Windows, Interrupt returns a second after sending Ctrl+C, by which
// time a stand-in that saves quickly has exited and the reaper may have
// released the handle, which then says "still running" for good. That
// made every Ctrl+C stop fail on Windows after its whole wait.
func (a *attachment) waitExit(wait, resend time.Duration, again func()) bool {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	var resends <-chan time.Time
	if resend > 0 {
		t := time.NewTicker(resend)
		defer t.Stop()
		resends = t.C
	}
	for {
		select {
		case <-a.ended:
			return true
		case <-deadline.C:
			return a.hasEnded()
		case <-resends:
			again()
		}
	}
}

// tail is the last lines of the log, for a failure message.
func (a *attachment) tail() string {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if len(a.lines) == 0 {
		return "(the server wrote nothing to its log)"
	}
	return "the server's last lines:\n  " + strings.Join(a.lines, "\n  ")
}

// follow reads the log at path from its start, giving each line to line,
// until ctx ends (then it reads what's left once more). It follows the
// path, so a file the game replaced (rotation) is read from its start.
func follow(ctx context.Context, path string, line func(string)) {
	var f *os.File
	var partial []byte
	var offset int64
	buf := make([]byte, 32<<10)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	read := func() {
		if f != nil {
			if st, err := os.Stat(path); err == nil {
				if cur, err := f.Stat(); err == nil && !os.SameFile(st, cur) {
					f.Close()
					f, offset, partial = nil, 0, nil
				}
			}
		}
		if f == nil {
			if f = openLog(path); f == nil {
				return
			}
		}
		if st, err := f.Stat(); err == nil && st.Size() < offset {
			f.Seek(0, io.SeekStart) // truncated
			offset, partial = 0, nil
		}
		for {
			n, err := f.Read(buf)
			if n > 0 {
				offset += int64(n)
				partial = append(partial, buf[:n]...)
				for {
					i := bytes.IndexByte(partial, '\n')
					if i < 0 {
						break
					}
					line(string(bytes.TrimRight(partial[:i], "\r")))
					partial = partial[i+1:]
				}
			}
			if err != nil || n == 0 {
				return
			}
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		read()
		select {
		case <-ctx.Done():
			read()
			if len(partial) > 0 {
				line(string(bytes.TrimRight(partial, "\r")))
			}
			return
		case <-tick.C:
		}
	}
}

// openLog opens a log the game may rename or delete while it's open
// (platform.OpenShared), or plainly where that isn't available yet.
func openLog(path string) *os.File {
	if f, err := platform.OpenShared(path); err == nil {
		return f
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	return f
}
