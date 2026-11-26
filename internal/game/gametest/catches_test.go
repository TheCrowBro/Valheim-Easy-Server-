package gametest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest/demo"
)

// A Conformance that passes everything would be worse than none, so these
// break Demo Game in one way each and check Conformance fails for that
// reason. Each broken run is this test binary run again (Conformance
// fails the test it is given, so it can't be run inside this one).

// brokenGame is Demo Game with one thing wrong.
type brokenGame struct {
	demo.Game
	mode string
}

func (b brokenGame) Info() game.Info {
	in := b.Game.Info()
	if b.mode == "port" {
		// A public port the server never opens.
		in.Ports = append(in.Ports, game.PortRole{Role: "query", Label: "Query port", Protos: []string{"udp"}, Tie: "game+2", Public: true})
	}
	return in
}

func (b brokenGame) New(env game.Env) game.Server {
	return brokenServer{Server: b.Game.New(env), mode: b.mode}
}

type brokenServer struct {
	game.Server
	mode string
}

func (s brokenServer) Fingerprint(sv game.Saved, world string) string {
	if s.mode == "fingerprint" {
		sv = sv.Clone()
		delete(sv.Server, "maxPlayers") // forgets a setting the server reads at start
	}
	return s.Server.Fingerprint(sv, world)
}

func (s brokenServer) Validate(sv game.Saved, world string) game.FieldErrors {
	if s.mode == "validate" {
		return nil
	}
	return s.Server.Validate(sv, world)
}

func (s brokenServer) Stop(rec game.Record) []game.StopStep {
	switch s.mode {
	case "console":
		// Only the console line: nothing is left after a takeover.
		return []game.StopStep{{Kind: "console", Line: "stop", Wait: 5 * time.Second}}
	case "step":
		// A first step that never gets through (a wrong port, say): Ctrl+C
		// still stops the server, but only as the last resort.
		return append([]game.StopStep{{Kind: "call", Call: func(context.Context) error { return errors.New("connection refused") }}}, s.Server.Stop(rec)...)
	}
	return s.Server.Stop(rec)
}

// TestBrokenAdapter is the broken run itself; it does nothing unless
// TestConformanceCatches started it.
func TestBrokenAdapter(t *testing.T) {
	mode := os.Getenv("GAMETEST_BROKEN")
	if mode == "" {
		t.Skip("run by TestConformanceCatches")
	}
	Conformance(t, brokenGame{Game: demo.Game{ID: "broken", Console: mode == "console"}, mode: mode},
		Options{Fake: os.Getenv("GAMETEST_FAKE")})
}

func TestConformanceCatches(t *testing.T) {
	if os.Getenv("GAMETEST_BROKEN") != "" {
		t.Skip("inside a broken run")
	}
	fake := BuildFakeserver(t)
	for mode, want := range map[string]string{
		"fingerprint": "changing maxPlayers (marked Restart) doesn't change Fingerprint",
		"validate":    "Validate takes a 200-character serverName",
		"port":        "the query port",
		"console":     "ended with the server still running",
		"step":        `the stop chain's "call" step failed (connection refused)`,
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestBrokenAdapter$", "-test.count=1")
			cmd.Env = append(os.Environ(), "GAMETEST_BROKEN="+mode, "GAMETEST_FAKE="+fake)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("Conformance passed an adapter broken by %q:\n%s", mode, out)
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("Conformance failed the %q adapter, but without %q:\n%s", mode, want, out)
			}
		})
	}
}
