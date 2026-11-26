package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server is one running stand-in, as its personality sees it.
//
// Every callback the personality gives it (Run, the console, Ctrl+C,
// script steps, timers and the responders' handlers) runs on one goroutine,
// one at a time, so a personality keeps its game's state in plain fields
// with no locks. A callback that takes a while (s.Sleep while "saving")
// holds the others up, as a busy real server does. The methods below are
// safe to call from any goroutine.
type Server struct {
	p    *Personality
	cfg  Config
	call Call

	calls chan func() // run one at a time by loop

	logMu sync.Mutex
	out   *os.File

	mu        sync.Mutex
	players   []string
	ballast   [][]byte
	closers   []io.Closer
	ready     bool
	onCommand func(line string)
	onStop    func()
	onStep    func(Step) bool
	onCrash   func()
}

func newServer(p *Personality, cfg Config, c Call) *Server {
	return &Server{p: p, cfg: cfg, call: c, calls: make(chan func(), 256), out: os.Stdout}
}

// run starts the server and never returns: a handler ends the program with
// Exit.
func (s *Server) run() {
	mem := s.cfg.MemMB
	if mem == 0 {
		mem = s.p.MemMB
	}
	s.hold(mem << 20)

	// Ctrl+C (SIGINT; on Windows CTRL_C_EVENT, which the app sends) and
	// SIGTERM (on Windows also closing the console) ask the server to
	// stop. signal.Notify, never signal.Ignore, so the default comes back
	// for anything this program starts.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range sig {
			s.post(s.interrupted)
		}
	}()

	if s.cfg.CrashAfter > 0 {
		time.AfterFunc(time.Duration(s.cfg.CrashAfter*float64(time.Second)), s.crash)
	}
	s.post(func() { s.p.Run(s) })
	for f := range s.calls {
		f()
	}
}

// post queues f to run on the server's goroutine.
func (s *Server) post(f func()) { s.calls <- f }

// Do runs f on the server's goroutine and waits for it: how a responder's
// handler, which runs on a goroutine of its own, reads the game's state.
// Never call it from a callback: it would wait for itself.
func (s *Server) Do(f func()) {
	done := make(chan struct{})
	s.post(func() { defer close(done); f() })
	<-done
}

// Args is the command line.
func (s *Server) Args() Args { return s.call.Args }

// Config is this copy's fakeserver.json.
func (s *Server) Config() Config { return s.cfg }

// Option is one of the personality's own switches from fakeserver.json
// ("" when not set).
func (s *Server) Option(name string) string { return s.cfg.Options[name] }

// Version is the version the server reports.
func (s *Server) Version() string {
	if s.cfg.Version != "" {
		return s.cfg.Version
	}
	return s.p.Version
}

// Scaled is d on the game's clock: a 10-minute autosave at speed 60 comes
// every 10 seconds.
func (s *Server) Scaled(d time.Duration) time.Duration {
	return time.Duration(float64(d) / s.cfg.Speed)
}

// Sleep waits d on the game's clock (loading, saving). It holds up the
// other callbacks while it waits, as a busy server does.
func (s *Server) Sleep(d time.Duration) { time.Sleep(s.Scaled(d)) }

// Every runs f every d on the game's clock (an autosave), on the server's
// goroutine.
func (s *Server) Every(d time.Duration, f func()) {
	t := time.NewTicker(max(s.Scaled(d), time.Millisecond))
	go func() {
		for range t.C {
			s.post(f)
		}
	}()
}

// After runs f once, d from now on the game's clock, on the server's
// goroutine.
func (s *Server) After(d time.Duration, f func()) {
	time.AfterFunc(s.Scaled(d), func() { s.post(f) })
}

// OnCommand sets what a line typed on the server's console does, and
// starts reading the console (standard input). When the console ends (no
// input at all, or the app that held it is gone) the server simply stops
// reading, as real servers carry on without one; the app's stop chain then
// moves on to Ctrl+C.
func (s *Server) OnCommand(f func(line string)) {
	s.mu.Lock()
	first := s.onCommand == nil
	s.onCommand = f
	s.mu.Unlock()
	if !first {
		return
	}
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			s.post(func() {
				s.mu.Lock()
				h := s.onCommand
				s.mu.Unlock()
				h(line)
			})
		}
	}()
}

// OnStop sets what Ctrl+C (or SIGTERM) does: normally save the world and
// Exit. Without it, Ctrl+C ends the server at once.
func (s *Server) OnStop(f func()) {
	s.mu.Lock()
	s.onStop = f
	s.mu.Unlock()
}

// OnStep sets what the script's steps do. f returns false for a verb the
// personality doesn't know.
func (s *Server) OnStep(f func(Step) bool) {
	s.mu.Lock()
	s.onStep = f
	s.mu.Unlock()
}

// OnCrash sets the last words of a server told to crash (fakeserver.json's
// crashAfter), such as the game's exception line.
func (s *Server) OnCrash(f func()) {
	s.mu.Lock()
	s.onCrash = f
	s.mu.Unlock()
}

// Ready says the server is open for players: it writes lines (unless
// fakeserver.json's noReady hides them) and starts the script. Only the
// first call counts.
func (s *Server) Ready(lines ...string) {
	s.mu.Lock()
	already := s.ready
	s.ready = true
	s.mu.Unlock()
	if already {
		return
	}
	if !s.cfg.NoReady {
		for _, l := range lines {
			s.Log(l)
		}
	}
	if len(s.cfg.steps) > 0 {
		go s.runScript(s.cfg.steps)
	}
}

// Join adds a player to who's on. It says false when they already are.
func (s *Server) Join(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if p == name {
			return false
		}
	}
	s.players = append(s.players, name)
	s.ballast = append(s.ballast, touch(4<<20)) // each player costs the server some memory
	return true
}

// Leave takes a player off who's on. It says false when they weren't.
func (s *Server) Leave(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.players {
		if p == name {
			s.players = append(s.players[:i], s.players[i+1:]...)
			if len(s.ballast) > 1 {
				s.ballast = s.ballast[:len(s.ballast)-1]
			}
			return true
		}
	}
	return false
}

// Players is who's on, in the order they joined.
func (s *Server) Players() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.players...)
}

// Exit ends the server with code, after its last log lines reach the disk.
// It closes the ports first, as a real server does on its way out, but
// waits for that only briefly (closeAll): ending the program releases them
// anyway, and waiting longer could hang for good. Closing the RCON server
// or the A2S responder waits for a command or query in progress, and a
// probe that arrived while the server was saving on its way out is one:
// it is waiting for the server's goroutine (s.Do), which is the one in
// Exit. Each would wait for the other forever, and not even Ctrl+C could
// end it, as nothing runs the server's callbacks any more. Instead the
// probe simply loses its connection when the program ends, as it would
// with a real server.
func (s *Server) Exit(code int) {
	s.closeAll(closeWait)
	s.syncLog()
	exitNow(code)
}

// exitNow ends the program with code. Its last words, "fakeserver:
// exiting now (code N)" on standard error, say it got this far: the app
// (and gametest) capture standard error with the game's output, so when a
// test finds a stand-in still running after this line, it is stuck in the
// system's own exit, not anywhere in the stand-in. It is the stand-in's
// own note, like Warnf's: Demo Game's Monitor hides lines starting
// "fakeserver: ", and no real game's log line looks like it.
func exitNow(code int) {
	fmt.Fprintf(os.Stderr, "fakeserver: exiting now (code %d)\n", code)
	os.Exit(code)
}

// closeWait is how long Exit waits for the ports to close. Closing them
// takes a moment at most, unless a responder is waiting for a probe that
// will never be answered (see Exit).
const closeWait = 250 * time.Millisecond

// closeAll closes every listener and responder at once and waits for them
// to finish closing, but for no longer than wait. One that is still busy
// after that is left to the end of the program.
func (s *Server) closeAll(wait time.Duration) {
	s.mu.Lock()
	closers := s.closers
	s.mu.Unlock()
	done := make(chan struct{}, len(closers))
	for _, c := range closers {
		go func() {
			c.Close()
			done <- struct{}{}
		}()
	}
	deadline := time.After(wait)
	for range closers {
		select {
		case <-done:
		case <-deadline:
			return
		}
	}
}

// interrupted handles Ctrl+C.
func (s *Server) interrupted() {
	s.mu.Lock()
	f := s.onStop
	s.mu.Unlock()
	if f == nil {
		s.Exit(0)
	}
	f()
}

// crash ends the server the way fakeserver.json's crashAfter asks: its
// last words, then exit code 3 without saving. It doesn't wait for the
// server's goroutine, which could be the thing that hung.
func (s *Server) crash() {
	s.mu.Lock()
	f := s.onCrash
	s.mu.Unlock()
	if f != nil {
		f()
	} else {
		s.Log("The stand-in server crashed, as its fakeserver.json asked.")
	}
	s.syncLog()
	exitNow(3)
}

// hold keeps n bytes of memory in use, touching every page so the system
// really counts it.
func (s *Server) hold(n int) {
	s.mu.Lock()
	s.ballast = append(s.ballast, touch(n))
	s.mu.Unlock()
}

func touch(n int) []byte {
	b := make([]byte, n)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	return b
}

// closeOnExit remembers a listener or responder to close in Exit.
func (s *Server) closeOnExit(c io.Closer) {
	s.mu.Lock()
	s.closers = append(s.closers, c)
	s.mu.Unlock()
}
