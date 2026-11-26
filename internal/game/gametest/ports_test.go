package gametest

import (
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest/demo"
)

// These act out the race a CI run on macOS lost: another program takes a
// port gametest chose, after the choice and before the stand-in opens it,
// and the stand-in stops with "can't open port N: ... address already in
// use". Here the test itself is the other program, holding the port.

// holdPort opens port on 127.0.0.1 over TCP (the demo opens its TCP port
// first) until the test ends.
func holdPort(t *testing.T, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("holding port %d: %v", port, err)
	}
	t.Cleanup(func() { l.Close() })
}

// holdFreePort holds a port of its own choosing, as another program
// would, until the test ends.
func holdFreePort(t *testing.T) int {
	t.Helper()
	for range 5 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		t.Cleanup(func() { l.Close() })
		return l.Addr().(*net.TCPAddr).Port
	}
	t.Fatal("couldn't open any port")
	return 0
}

// TestStolenPortRetried: the start is tried again on new ports, says so in
// the test's log, and then runs as if nothing happened: the new world is
// made as asked and the stop chain ends it.
func TestStolenPortRetried(t *testing.T) {
	for _, c := range []struct {
		g    demo.Game
		role string
	}{
		{demo.Game{ID: "demosteal", Console: true}, "game"},
		{demo.Game{ID: "demostealapi", API: true}, "rest"}, // a tied role ("game+1")
	} {
		t.Run(c.g.Info().ID, func(t *testing.T) {
			rt := &recordingT{TB: t}
			s := newSession(rt, c.g, Options{NewWorld: game.Values{"seed": "4242"}})
			stolen := s.store.Get().Ports[c.role]
			holdPort(t, stolen)
			s.install()
			s.start()
			s.WaitEvent("online", "", 30*time.Second)

			s.mu.Lock()
			retries, req, lines := s.retries, s.req, strings.Join(s.run.lines, "\n")
			s.mu.Unlock()
			if retries != 1 {
				t.Errorf("the start was tried again %d times, want 1", retries)
			}
			if got := rt.logged(); !strings.Contains(got, fmt.Sprintf("another program took the %s port %d, which gametest chose", c.role, stolen)) ||
				!strings.Contains(got, "starting it again on new ports (retry 1 of 3)") {
				t.Errorf("the test's log doesn't say the start was tried again on new ports:\n%s", got)
			}
			ports, rec := s.store.Get().Ports, s.Record()
			if ports[c.role] == stolen || rec.Ports[c.role] != ports[c.role] {
				t.Errorf("after the retry the %s port is %d (the run's record says %d); the stolen one was %d", c.role, ports[c.role], rec.Ports[c.role], stolen)
			}
			if ports["rest"] != 0 && ports["rest"] != ports["game"]+1 {
				t.Errorf("after the retry the tied rest port is %d, not the game port %d + 1", ports["rest"], ports["game"])
			}
			if req.World != "Conformance" || !req.New || req.NewWorld["seed"] != "4242" {
				t.Errorf("the retry wasn't the same new-world start: %+v", req)
			}
			if !strings.Contains(lines, `Creating world "Conformance" (seed 4242`) {
				t.Errorf("the retry didn't make the new world as asked:\n%s", lines)
			}
			checkPorts(t, s)
			s.Stop()
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, r := range s.stopLog {
				if r.Err != nil {
					t.Errorf("the stop chain's %q step failed: %v", r.Kind, r.Err)
				}
			}
			if rt.failure() != "" {
				t.Errorf("gametest failed the run: %s", rt.failure())
			}
		})
	}
}

// TestStolenPortGivesUp: a port that is taken again on every try fails
// the test after maxPortRetries retries, with the server's own words.
func TestStolenPortGivesUp(t *testing.T) {
	rt := &recordingT{TB: t}
	s := newSession(rt, demo.Game{ID: "demotaken"}, Options{})
	holdPort(t, s.store.Get().Ports["game"])
	s.afterPick = func(ports map[string]int) { holdPort(t, ports["game"]) }
	rt.run(func() {
		s.install()
		s.start()
		s.WaitEvent("online", "", 30*time.Second)
	})
	if f := rt.failure(); !strings.Contains(f, `exited (code 1) without a "online" event`) || !strings.Contains(f, "can't open port") {
		t.Errorf("gametest's failure = %q, want the server's exit and its port error", f)
	}
	if r := s.retryCount(); r != maxPortRetries {
		t.Errorf("the start was tried again %d times, want %d", r, maxPortRetries)
	}
	if got := rt.logged(); !strings.Contains(got, fmt.Sprintf("retry %d of %d", maxPortRetries, maxPortRetries)) {
		t.Errorf("the test's log doesn't show the last retry:\n%s", got)
	}
}

// TestOwnPortNotRetried: a port the test chose itself (Options.Change) is
// never changed behind its back: another program having it fails the
// test at once, as a real server's "port in use" must.
func TestOwnPortNotRetried(t *testing.T) {
	own := holdFreePort(t)
	rt := &recordingT{TB: t}
	s := newSession(rt, demo.Game{ID: "demoown"}, Options{Change: func(sv *game.Saved) { sv.Ports["game"] = own }})
	rt.run(func() {
		s.install()
		s.start()
		s.WaitEvent("online", "", 30*time.Second)
	})
	if f := rt.failure(); !strings.Contains(f, fmt.Sprintf("can't open port %d", own)) {
		t.Errorf("gametest's failure = %q, want the server's port error", f)
	}
	if r := s.retryCount(); r != 0 || strings.Contains(rt.logged(), "new ports") {
		t.Errorf("the start was tried again on new ports (%d times):\n%s", r, rt.logged())
	}
	if p := s.store.Get().Ports["game"]; p != own {
		t.Errorf("the test's own port %d was changed to %d", own, p)
	}
}

// TestStolenPortWhen: which failed runs count as a stolen port.
func TestStolenPortWhen(t *testing.T) {
	const stamp = "[2026-11-26 19:20:38] "
	ports := map[string]int{"game": 24567, "rest": 24568, "rcon": 24569}
	for _, c := range []struct {
		name   string
		lines  []string
		online bool
		own    string // a role the test set itself
		want   bool
	}{
		{name: "the demo on macOS and Linux", want: true,
			lines: []string{stamp + "Creating world", stamp + "Error: can't open port 24567: listen tcp 127.0.0.1:24567: bind: address already in use"}},
		{name: "the demo on Windows", want: true,
			lines: []string{stamp + "Error: can't open API port 24568: listen tcp 127.0.0.1:24568: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."}},
		{name: "a port Windows set aside", want: true,
			lines: []string{"listen udp 127.0.0.1:24567: bind: An attempt was made to access a socket in a way forbidden by its access permissions."}},
		{name: "Minecraft names the port on another line", want: true,
			lines: []string{"[12:00:01] [Server thread/INFO]: Starting Minecraft server on *:24567", "[12:00:01] [Server thread/WARN]: **** FAILED TO BIND TO PORT!",
				"[12:00:01] [Server thread/WARN]: The exception was: java.net.BindException: Address already in use: bind"}},
		{name: "after it was online", online: true,
			lines: []string{stamp + "Error: can't open port 24567: listen tcp 127.0.0.1:24567: bind: address already in use"}},
		{name: "a port the test chose", own: "rcon",
			lines: []string{"Error: can't open RCON port 24569: listen tcp 127.0.0.1:24569: bind: address already in use"}},
		{name: "a port the test chose, named elsewhere", own: "game",
			lines: []string{"Starting on port 24567", "FAILED TO BIND TO PORT!"}},
		{name: "a port that isn't the run's",
			lines: []string{"Starting on port 24567", "Error: can't open query port 27015: listen udp 127.0.0.1:27015: bind: address already in use"}},
		{name: "a port only inside a longer number",
			lines: []string{"Error: can't open port 245670: bind: address already in use"}},
		{name: "no port named anywhere",
			lines: []string{"java.net.BindException: Address already in use"}},
		{name: "another failure",
			lines: []string{"Starting on port 24567", "Fatal error: the demo server crashed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &Session{picked: map[string]int{}}
			for role, p := range ports {
				if role != c.own {
					s.picked[role] = p
				}
			}
			a := &attachment{s: s, rec: game.Record{Ports: ports}, lines: c.lines}
			if c.online {
				a.events = []game.Event{{Kind: "online"}}
			}
			why, got := s.stolenPort(a)
			if got != c.want {
				t.Errorf("stolenPort = %v (%q), want %v", got, why, c.want)
			}
		})
	}
}

func (s *Session) retryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retries
}

// recordingT stands in for the test where gametest is expected to fail
// it: it keeps what gametest logs (passing it on) and its failure, and a
// Fatal ends the goroutine as testing's does, without failing the real
// test.
type recordingT struct {
	testing.TB
	mu   sync.Mutex
	logs []string
	fail string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Log(args ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, fmt.Sprintln(args...))
	r.mu.Unlock()
	r.TB.Log(args...)
}

func (r *recordingT) Logf(format string, args ...any) { r.Log(fmt.Sprintf(format, args...)) }

func (r *recordingT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail += fmt.Sprintf(format, args...) + "\n"
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *recordingT) Failed() bool { return r.failure() != "" }

func (r *recordingT) failure() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fail
}

func (r *recordingT) logged() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "")
}

// run runs f on a goroutine of its own, so a Fatal ends only f.
func (r *recordingT) run(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
}
