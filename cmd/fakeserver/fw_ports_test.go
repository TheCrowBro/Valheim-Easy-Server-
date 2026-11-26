package main

import (
	"fmt"
	"math/rand/v2"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ports for these tests.
//
// Every system hands out "ephemeral" ports from a range of its own, both
// for a listen on port 0 and for the local end of every outgoing
// connection any program makes: 49152-65535 on Windows and macOS,
// 32768-60999 on Linux. A port picked there can be taken by somebody's
// outgoing connection before the stand-in opens it, which is how a CI run
// on macOS failed while other packages' tests ran at the same time. So
// these tests pick below all three ranges, where a port is only taken by a
// program that opens it on purpose: 30000-31999, beside the 20000-29999
// that internal/game/gametest uses, so the two test programs (which go
// test runs at once) never pick the same port. A pick can still lose to
// another program, so the tests open their own ports with openFree and
// the stand-in's start is tried again on new ports (demoRun.retry).
const (
	testPortsFrom = 30000
	testPortsTo   = 32000 // exclusive
)

// picked holds the ports handed to tests that are still running, so two
// never get the same one.
var picked = struct {
	sync.Mutex
	ports map[int]bool
}{ports: map[int]bool{}}

// freePort finds a port that TCP and UDP can both open on 127.0.0.1 and
// that no other running test has.
func freePort(t testing.TB) int {
	t.Helper()
	picked.Lock()
	defer picked.Unlock()
	for range 500 {
		p := testPortsFrom + rand.IntN(testPortsTo-testPortsFrom)
		if picked.ports[p] || portTaken("tcp", p) || portTaken("udp", p) {
			continue
		}
		picked.ports[p] = true
		t.Cleanup(func() {
			picked.Lock()
			delete(picked.ports, p)
			picked.Unlock()
		})
		return p
	}
	t.Fatalf("found no free port in %d-%d", testPortsFrom, testPortsTo-1)
	return 0
}

// openFree runs open (which opens a port of the test's) on a free port,
// and on another one if another program took that one first.
func openFree(t *testing.T, open func(port int) error) int {
	t.Helper()
	var err error
	for range 4 {
		port := freePort(t)
		if err = open(port); err == nil {
			return port
		}
		t.Logf("opening port %d: %v; trying another port", port, err)
	}
	t.Fatal(err)
	return 0
}

// portTaken says whether this process can't open the port on 127.0.0.1,
// where stand-ins listen.
func portTaken(proto string, port int) bool {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	if proto == "udp" {
		c, err := net.ListenPacket("udp", addr)
		if err == nil {
			c.Close()
		}
		return err != nil
	}
	l, err := net.Listen("tcp", addr)
	if err == nil {
		l.Close()
	}
	return err != nil
}

// TestStolenPortRetried acts out that race: another program takes the
// port chosen for the stand-in before it opens it. The stand-in stops
// with "can't open port", as a real server does, and the test's start is
// tried again on new ports.
func TestStolenPortRetried(t *testing.T) {
	r := newDemoRun(t, startDemoDir(t, `{"speed":60}`), "-api", "{api}")
	stolen := r.ports["api"]
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(stolen))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r.launch()
	r.waitLine("Server ready")
	if r.tries != 2 || r.ports["api"] == stolen {
		t.Errorf("after %d tries the API port is %d; the stolen one was %d", r.tries, r.ports["api"], stolen)
	}
	if !portTaken("tcp", r.ports["api"]) {
		t.Error("the new API port isn't open")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slicesContain(r.lines, `Creating world "Test World"`) {
		t.Errorf("the retry didn't make the world again:\n%s", strings.Join(r.lines, "\n"))
	}
}

// TestStolenGamePort: the same race on the game port, the one the ready
// line names. After the retry, the line waitLine returns and r.port are
// both the new run's, which is why TestDemo checks the port in the line it
// got rather than waiting for a line with the first port in it.
func TestStolenGamePort(t *testing.T) {
	r := newDemoRun(t, startDemoDir(t, `{"speed":60}`))
	stolen := r.ports["game"]
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(stolen))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r.launch()
	ready := r.waitLine(`Server ready: players can join "Test" on port `)
	if r.tries != 2 || r.port == stolen || r.port != r.ports["game"] {
		t.Errorf("after %d tries the game port is %d (r.ports says %d); the stolen one was %d", r.tries, r.port, r.ports["game"], stolen)
	}
	if want := "on port " + strconv.Itoa(r.port); !strings.HasSuffix(ready, want) {
		t.Errorf("the ready line %q doesn't end with %q", ready, want)
	}
}

// TestStolenPortGivesUp: a port taken on every try fails the test after
// the retries, with the stand-in's own words.
func TestStolenPortGivesUp(t *testing.T) {
	ft := &fatalT{T: t}
	r := newDemoRun(ft, startDemoDir(t, `{"speed":60}`))
	r.beforeLaunch = func() { // another program takes the game port every time
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(r.ports["game"]))
		if err != nil {
			t.Errorf("holding port %d: %v", r.ports["game"], err)
			return
		}
		t.Cleanup(func() { l.Close() })
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.launch()
		r.waitLine("Server ready")
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("waitLine never gave up")
	}
	if !strings.Contains(ft.msg, "exited (code 1)") || !strings.Contains(ft.msg, "can't open port") {
		t.Errorf("the failure = %q, want the stand-in's exit and its port error", ft.msg)
	}
	if r.tries != 1+stolenPortRetries {
		t.Errorf("the start was tried %d times, want %d", r.tries, 1+stolenPortRetries)
	}
}

// fatalT keeps a Fatal's message and ends the goroutine, as testing does,
// without failing the real test.
type fatalT struct {
	*testing.T
	msg string
}

func (f *fatalT) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}
