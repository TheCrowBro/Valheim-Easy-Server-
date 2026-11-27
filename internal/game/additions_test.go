package game

import (
	"errors"
	"fmt"
	"testing"

	"valheim-easy-server/internal/worlds"
)

// The optional interfaces of phase 0' can be implemented alongside every
// other capability (see everything in contract_test.go).
var (
	_ interface {
		Monitor
		Replayer
		LogReader
	} = replayingMonitor{}
	_ interface {
		Server
		Supporter
	} = everything{}
	_ interface {
		Game
		Licensed
	} = licensedGame{}
	_ error = &ActionError{}
)

type replayingMonitor struct{ quietMonitor }

func (replayingMonitor) Replaying(bool) {}
func (replayingMonitor) FromLog()       {}

func (everything) Support() (Level, string) { return Runs, "" }

type licensedGame struct{ testGame }

func (licensedGame) EULA() string { return "https://example.invalid/eula" }

// An adapter writes worldstore's Detect with game.Candidate; it must be
// worlds.Candidate itself, or the func wouldn't fit the field.
func TestCandidateIsWorldsCandidate(t *testing.T) {
	var detect func(root string) []worlds.Candidate = func(string) []Candidate {
		return []Candidate{{ID: ".", Name: "Riverbend"}}
	}
	if got := detect("x"); len(got) != 1 || got[0].Name != "Riverbend" {
		t.Fatalf("detect = %+v", got)
	}
}

func TestActionError(t *testing.T) {
	type short struct{ NeedMB int }
	var err error = fmt.Errorf("starting: %w", &ActionError{Msg: "Not enough memory.", Action: "memory", Data: short{10240}})
	var ae *ActionError
	if !errors.As(err, &ae) || ae.Action != "memory" || ae.Data.(short).NeedMB != 10240 {
		t.Fatalf("errors.As = %+v", ae)
	}
	if ae.Error() != "Not enough memory." {
		t.Errorf("Error() = %q", ae.Error())
	}
}

func TestLevelOn(t *testing.T) {
	in := Info{
		OS:        map[string]Level{"windows": Runs, "darwin/amd64": Runs, "darwin/arm64": CantRun},
		NeedsText: map[string]string{"darwin/arm64": "Needs an Intel Mac", "linux": "Needs Windows"},
	}
	for _, c := range []struct {
		goos, goarch string
		level        Level
		text         string
	}{
		{"windows", "amd64", Runs, ""},
		{"windows", "arm64", Runs, ""}, // the GOOS key covers every architecture
		{"darwin", "amd64", Runs, ""},  // the exact key wins
		{"darwin", "arm64", CantRun, "Needs an Intel Mac"},
		{"linux", "amd64", CantRun, "Needs Windows"}, // missing = can't run, with the GOOS text
		{"freebsd", "amd64", CantRun, ""},
	} {
		l, text := in.LevelOn(c.goos, c.goarch)
		if l != c.level || text != c.text {
			t.Errorf("%s/%s = %q %q, want %q %q", c.goos, c.goarch, l, text, c.level, c.text)
		}
	}
	// A game whose GOOS key says no, with the text under the exact key.
	no := Info{OS: map[string]Level{"darwin": CantRun}, NeedsText: map[string]string{"darwin/amd64": "Needs Windows"}}
	if l, text := no.LevelOn("darwin", "amd64"); l != CantRun || text != "Needs Windows" {
		t.Errorf("darwin/amd64 = %q %q", l, text)
	}
}

func TestAddNote(t *testing.T) {
	Env{}.AddNote("info", "nobody listens") // no Note: nothing happens
	var got []string
	env := Env{Note: func(kind, text string) { got = append(got, kind+": "+text) }}
	env.AddNote("warning", "Couldn't check for server updates.")
	if len(got) != 1 || got[0] != "warning: Couldn't check for server updates." {
		t.Fatalf("notes = %q", got)
	}
}

// The values other code writes or compares, pinned like TestNamedValues.
func TestAdditionNames(t *testing.T) {
	for got, want := range map[string]string{
		StateEULA: "eula", StateCGNAT: "cgnat",
		KindOnline: "online", KindJoin: "join", KindLeave: "leave", KindDeath: "death", KindSaved: "saved",
		KindStopping: "stopping", KindCount: "count", KindCode: "code", KindTurnedAway: "turned_away",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
