package gametest

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// TestBindTakenWildcard: a port held on every IPv4 address (0.0.0.0, as
// real servers listen) counts as taken with real. On Windows and macOS
// opening 127.0.0.1 or Go's dual-stack "every address" beside it can
// work, which made the first real-server run report Terraria's and
// Enshrouded's ports free while the servers held them.
func TestBindTakenWildcard(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skipf("can't listen on 0.0.0.0 here: %v", err)
	}
	defer l.Close()
	if port := l.Addr().(*net.TCPAddr).Port; !bindTaken("tcp", port, true) {
		t.Errorf("bindTaken(tcp, %d, real) = false while this test holds 0.0.0.0:%d", port, port)
	}

	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Skipf("can't listen on 0.0.0.0 here: %v", err)
	}
	defer c.Close()
	if port := c.LocalAddr().(*net.UDPAddr).Port; !bindTaken("udp", port, true) {
		t.Errorf("bindTaken(udp, %d, real) = false while this test holds 0.0.0.0:%d", port, port)
	}
}

// TestBindTakenFree: a free port is free, with real and without.
func TestBindTakenFree(t *testing.T) {
	for _, proto := range []string{"tcp", "udp"} {
		port, err := pickPorts([]game.PortRole{{Role: "game", Protos: []string{proto}}}, true)
		if err != nil {
			t.Fatal(err)
		}
		releasePorts(port)
		for _, real := range []bool{false, true} {
			if bindTaken(proto, port["game"], real) {
				t.Errorf("bindTaken(%s, %d, real=%v) = true for a port nobody holds", proto, port["game"], real)
			}
		}
	}
}

// TestInstallProgress: a real install's progress reaches the test's log at
// each new step and each tenth of the way, not at every percent.
func TestInstallProgress(t *testing.T) {
	rt := &recordingT{TB: t}
	p := installProgress(&testLog{t: rt})
	p(game.Progress{Step: "Connecting to Steam…", Percent: -1})
	p(game.Progress{Step: "Connecting to Steam…", Percent: -1})
	for pct := 0.0; pct <= 100; pct += 2.5 {
		p(game.Progress{Step: "Downloading…", Percent: pct})
	}
	p(game.Progress{Step: "Verifying…", Percent: 5})
	got := rt.logged()
	if n := strings.Count(got, "Connecting to Steam"); n != 1 {
		t.Errorf("the same step without a percent was logged %d times, want once:\n%s", n, got)
	}
	if n := strings.Count(got, "Downloading"); n != 11 {
		t.Errorf("0%% to 100%% of one step was logged %d times, want 11 (each tenth):\n%s", n, got)
	}
	if !strings.Contains(got, "install: Verifying… 5%") {
		t.Errorf("a new step wasn't logged at once:\n%s", got)
	}
}

// flakyProber says the server is down until its third question.
type flakyProber struct{ asked int }

func (f *flakyProber) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	f.asked++
	if f.asked < 3 {
		return game.ProbeResult{}, errors.New("no answer yet")
	}
	return game.ProbeResult{Up: true, Count: 0}, nil
}

// TestProbeUp: the stand-in gets one try; a real server's probe is asked
// again until it answers, as its query port may open a little after the
// log says it's online.
func TestProbeUp(t *testing.T) {
	once := &flakyProber{}
	if res, tries, err := probeUp(once, game.Record{}, false); err == nil || res.Up || tries != 1 || once.asked != 1 {
		t.Errorf("with the stand-in: %+v after %d tries (%v), asked %d times; want one failed try", res, tries, err, once.asked)
	}
	again := &flakyProber{}
	start := time.Now()
	if res, tries, err := probeUp(again, game.Record{}, true); err != nil || !res.Up || tries != 3 {
		t.Errorf("with a real server: %+v after %d tries (%v); want up on the third", res, tries, err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("three tries took %v", took)
	}
}
