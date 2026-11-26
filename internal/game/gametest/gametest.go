// Package gametest runs one game adapter without the manager, against the
// stand-in server (cmd/fakeserver) or, with Options.Real, against the real
// server (spec 7.1). Every adapter's conformance test and real-server check
// go through it, so a game can be built and checked before the manager
// knows about it.
//
// Phase 0 stub: the exported signatures below are the contract the game
// streams build against; stream F fills in the bodies. Until then each
// helper fails the test that calls it with errors.ErrUnsupported, so a test
// written against it can't pass by accident.
package gametest

import (
	"errors"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// errNotBuilt is what every stub reports.
var errNotBuilt = errors.ErrUnsupported

// BuildFakeserver builds cmd/fakeserver once per test binary and returns
// the program's path.
func BuildFakeserver(t testing.TB) string {
	t.Helper()
	t.Fatalf("gametest.BuildFakeserver: %v", errNotBuilt)
	return ""
}

// Options say how to run the adapter.
type Options struct {
	Fake     string // the built fakeserver; "" with Real
	Real     bool   // use the adapter's real Installer and Plan (the realserver-tagged tests)
	Script   string
	World    string
	NewWorld game.Values
	Change   func(*game.Saved)
}

// Session is one instance of the adapter, installed and started.
type Session struct {
	t testing.TB
}

// Start installs the game (the stand-in unless o.Real), plans a start and
// launches it, as the manager would.
func Start(t *testing.T, g game.Game, o Options) *Session {
	t.Helper()
	t.Fatalf("gametest.Start: %v", errNotBuilt)
	return &Session{t: t}
}

// WaitEvent waits for the Monitor to report an event of kind (for name,
// when name isn't "") and fails the test if none comes within the time.
func (s *Session) WaitEvent(kind, name string, within time.Duration) game.Event {
	s.t.Helper()
	s.t.Fatalf("gametest.WaitEvent: %v", errNotBuilt)
	return game.Event{}
}

// State is the Monitor's view of the running server.
func (s *Session) State() game.RunState {
	s.t.Helper()
	s.t.Fatalf("gametest.State: %v", errNotBuilt)
	return game.RunState{}
}

// Takeover acts as after an app restart: a new Monitor from the run record, no stdin pipe.
func (s *Session) Takeover() {
	s.t.Helper()
	s.t.Fatalf("gametest.Takeover: %v", errNotBuilt)
}

// Stop runs the stop chain and returns when the world was last saved.
func (s *Session) Stop() (saved time.Time) {
	s.t.Helper()
	s.t.Fatalf("gametest.Stop: %v", errNotBuilt)
	return time.Time{}
}

// Conformance checks everything every adapter must do (spec 7.1's list).
func Conformance(t *testing.T, g game.Game, o Options) {
	t.Helper()
	t.Fatalf("gametest.Conformance: %v", errNotBuilt)
}
