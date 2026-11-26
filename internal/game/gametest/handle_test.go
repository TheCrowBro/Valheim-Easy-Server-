package gametest

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest/demo"
)

// windowsProc stands in for a *platform.Proc as it behaves on Windows,
// where gametest's first Windows run went wrong, so that run's failure can
// be reproduced (and kept fixed) on any system:
//
//   - Interrupt returns only a second after it sent Ctrl+C (platform waits
//     so the Ctrl+C can't reach this app once it stops ignoring it), and
//     a stand-in that saves quickly has ended well before then.
//   - Once Close has released the handle, Exited says false and Kill
//     fails, as WaitForSingleObject and TerminateProcess do on a closed
//     handle. (On Linux and macOS Close does nothing and Exited goes on
//     saying true, which is why the bug never showed there.)
type windowsProc struct {
	start time.Time
	ended chan struct{}
	once  sync.Once

	mu         sync.Mutex
	closed     bool
	interrupts int
}

func newWindowsProc() *windowsProc {
	return &windowsProc{start: time.Now(), ended: make(chan struct{})}
}

// PID is one no test process can have (Windows PIDs are multiples of 4),
// so the failure message's look-up by PID finds nothing of ours.
func (p *windowsProc) PID() int             { return 0x7ffffff3 }
func (p *windowsProc) StartTime() time.Time { return p.start }
func (p *windowsProc) Wait() (int, error)   { <-p.ended; return 0, nil }

func (p *windowsProc) Exited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false // WaitForSingleObject on a closed handle fails
	}
	select {
	case <-p.ended:
		return true
	default:
		return false
	}
}

// Interrupt: the stand-in saves and exits 50 ms after the Ctrl+C, while
// Interrupt is still waiting out its second.
func (p *windowsProc) Interrupt() error {
	p.mu.Lock()
	p.interrupts++
	p.mu.Unlock()
	time.AfterFunc(50*time.Millisecond, p.end)
	time.Sleep(time.Second)
	return nil
}

func (p *windowsProc) Kill() error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("The handle is invalid.")
	}
	p.end()
	return nil
}

func (p *windowsProc) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func (p *windowsProc) end() { p.once.Do(func() { close(p.ended) }) }

// TestCtrlCStopWithWindowsHandles is the first Windows run's failure:
// every stop chain that came down to Ctrl+C said "ended with the server
// still running; it was killed", 30 seconds after a stand-in whose log
// ended "Server stopped". The stand-in had exited at once; gametest had
// lost track of it. Its reaper released the process handle 0.7 s after
// the exit, before Interrupt's one-second wait was over, so every look at
// the handle after that said "still running".
func TestCtrlCStopWithWindowsHandles(t *testing.T) {
	s := newSession(t, demo.Game{ID: "demo"}, Options{Fake: "unused: nothing is installed or started"})
	p := newWindowsProc()
	rec := game.Record{PID: p.PID(), Started: p.start, LogPath: filepath.Join(t.TempDir(), "demo.log")}
	s.mu.Lock()
	mon := s.srv.Watch(rec, s.knownLocked)
	s.mu.Unlock()
	s.attach(p, rec, nil, mon, 1)

	began := time.Now()
	s.Stop() // the demo's chain is Ctrl+C alone (30 s, sent again every 10 s)
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Stop took %v to see a server that ended 50 ms after its Ctrl+C", took)
	}
	if len(s.stopLog) != 1 || s.stopLog[0].Kind != "interrupt" || s.stopLog[0].Err != nil {
		t.Errorf("stop steps = %+v, want one Ctrl+C that went through", s.stopLog)
	}
	p.mu.Lock()
	if p.interrupts != 1 {
		t.Errorf("Ctrl+C was sent %d times, want once", p.interrupts)
	}
	p.mu.Unlock()
	// The reaper releases the handle just after the end is folded in.
	waitFor(t, 5*time.Second, "the process handle released", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.closed
	})
}

// TestStopFailureExplainsItself: when a stop chain really does end with
// the server running, the message says what each way of looking at the
// process said before it was killed, so a hang on a system nobody can
// debug by hand explains itself from the CI log.
func TestStopFailureExplainsItself(t *testing.T) {
	s := newSession(t, demo.Game{ID: "demo"}, Options{Fake: "unused: nothing is installed or started"})
	p := newWindowsProc()
	rec := game.Record{PID: p.PID(), Started: p.start, LogPath: filepath.Join(t.TempDir(), "demo.log")}
	s.mu.Lock()
	mon := s.srv.Watch(rec, s.knownLocked)
	s.mu.Unlock()
	s.attach(p, rec, nil, mon, 1)
	s.mu.Lock()
	a := s.run
	s.mu.Unlock()

	running := a.exitReport()
	for _, want := range []string{"PID 2147483635", "gametest's wait for it hasn't returned", "its handle says it is still running", "looked up afresh by its PID"} {
		if !strings.Contains(running, want) {
			t.Errorf("while it runs, the report %q doesn't say %q", running, want)
		}
	}
	a.kill()
	// The reaper releases the handle just after exited closes.
	waitFor(t, 5*time.Second, "the handle released", func() bool {
		return errors.Is(a.useProc(func(procHandle) error { return nil }), errEnded)
	})
	ended := a.exitReport()
	for _, want := range []string{"gametest's wait for it returned (exit code 0)", "its handle has been released"} {
		if !strings.Contains(ended, want) {
			t.Errorf("once it ended, the report %q doesn't say %q", ended, want)
		}
	}
	// Nothing uses a released handle: Ctrl+C and Kill after the end do
	// nothing (on Windows they would act on a closed handle).
	a.kill()
	if err := a.interrupt(); !errors.Is(err, errEnded) {
		t.Errorf("Ctrl+C after the end gave %v, want errEnded", err)
	}
}
