package gametest

import (
	"bytes"
	"context"
	"errors"
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
	proc  *platform.Proc
	stdin *os.File // the console pipe's write end; nil without one (after a takeover)

	cancel context.CancelFunc
	loops  sync.WaitGroup
	exited chan struct{} // closed once the process ended and End was folded in

	// Under s.mu:
	events   []game.Event
	used     []bool        // events WaitEvent has returned
	changed  chan struct{} // closed and replaced whenever events arrive or the process ends
	lines    []string      // the last lines, for failure messages
	exitCode int
	detached bool
}

// probeEvery is how often the probe runs while the server starts and once
// it's ready (the manager probes every 5 s, then every 30 s; tests want
// answers sooner).
const (
	probeStarting = time.Second
	probeReady    = 2 * time.Second
)

// attach starts watching proc: following its log from the start, the
// Monitor's ticks, the probe and the exit.
func (s *Session) attach(proc *platform.Proc, rec game.Record, stdin *os.File, mon game.Monitor) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &attachment{s: s, mon: mon, rec: rec, proc: proc, stdin: stdin, cancel: cancel,
		exited: make(chan struct{}), changed: make(chan struct{})}
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
		code, _ := proc.Wait()
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
		proc.Close()
	}()
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
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
func (a *attachment) waitExit(wait, resend time.Duration, again func()) bool {
	deadline := time.Now().Add(wait)
	sent := time.Now()
	for {
		if a.proc.Exited() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		if resend > 0 && time.Since(sent) >= resend {
			again()
			sent = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
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
