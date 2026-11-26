package minecraft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// agreed is what Add a game saves when the owner ticks "I agree to the
// Minecraft EULA": without it the server never starts.
func agreed(sv *game.Saved) {
	sv.State[stateEULA] = "2026-11-26T12:00:00Z"
}

// TestConformance runs every check each adapter must pass (spec 7.1)
// against the stand-in, with the allow list on its default, "auto".
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{Change: agreed})
}

// TestStandInAllowList: with "auto" and no sign that the internet can't
// reach this computer, the list is on before the server starts; someone not
// on it is turned away, and Allow (WriteList over RCON while it runs) puts
// them on it, in the file the server reads.
func TestStandInAllowList(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: agreed, Script: "deny:Bob@1"})
	props := parseProperties(readFile(t, filepath.Join(s.Env().Dirs.Server, "server.properties")))
	for _, k := range []string{"white-list", "enforce-whitelist"} {
		if v, _ := props.Get(k); v != "true" {
			t.Errorf("%s = %q before the start, want true", k, v)
		}
	}
	s.WaitEvent("online", "", 30*time.Second)
	if on := s.State().Extra[extraAllowList]; on != allowOn {
		t.Errorf("State().Extra[allowList] = %q, want on", on)
	}
	away := s.WaitEvent("turned_away", "Bob", 15*time.Second)
	if !strings.Contains(away.Text, "allow list") {
		t.Errorf("turned-away text %q doesn't say why", away.Text)
	}
	lister := s.Server().(game.Lister)
	rec := s.Record()
	if err := lister.WriteList(t.Context(), &rec, "allowed", []string{"Bob"}); err != nil {
		t.Fatalf("Allow while running: %v", err)
	}
	names, err := lister.ReadList("allowed")
	if err != nil || len(names) != 1 || names[0] != "Bob" {
		t.Errorf("after Allow the list is %q (%v), want [Bob]", names, err)
	}
	if err := lister.WriteList(t.Context(), &rec, "allowed", nil); err != nil {
		t.Fatalf("taking Bob off while running: %v", err)
	}
	if names, _ := lister.ReadList("allowed"); len(names) != 0 {
		t.Errorf("after taking Bob off the list is %q", names)
	}
	s.Stop()
}

// TestStandInAllowListOff: "off" leaves the list off, and the run record
// says so, so the Monitor never turns it on by itself.
func TestStandInAllowListOff(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: func(sv *game.Saved) {
		agreed(sv)
		sv.Server["allowList"] = allowOff
	}})
	props := parseProperties(readFile(t, filepath.Join(s.Env().Dirs.Server, "server.properties")))
	if v, _ := props.Get("white-list"); v != "false" {
		t.Errorf("white-list = %q with the setting off", v)
	}
	s.WaitEvent("online", "", 30*time.Second)
	if st := s.Env().Store.Get().State; st[stateAllowListOn] != "" {
		t.Errorf("with the list off the state says it was turned on: %v", st)
	}
	s.Stop()
}

// TestRecover: a server found running without its run file still gets
// the RCON port and password it started with, from server.properties, so
// the probe and the gentle stop work.
func TestRecover(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: agreed})
	s.WaitEvent("online", "", 30*time.Second)
	want := s.Record()
	got := s.Server().(game.Recoverer).Recover(want.PID, want.Started)
	if got.Ports[roleRCON] != want.Ports[roleRCON] || got.Secrets[roleRCON] != want.Secrets[roleRCON] || got.Port != want.Port {
		t.Errorf("recovered ports %v secrets %v port %d; started with %v %v %d", got.Ports, got.Secrets, got.Port, want.Ports, want.Secrets, want.Port)
	}
	if got.LogPath != want.LogPath || got.World != want.World || got.Exe != want.Exe {
		t.Errorf("recovered log %q world %q exe %q; want %q %q %q", got.LogPath, got.World, got.Exe, want.LogPath, want.World, want.Exe)
	}
	var res game.ProbeResult
	var err error
	for range 20 {
		if res, err = s.Server().(game.Prober).Probe(t.Context(), got); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !res.Up {
		t.Errorf("probe with the recovered record: %+v, %v", res, err)
	}
	s.Stop()
}

// readFile reads a file with "\n" line ends: Git may check testdata out
// with "\r\n" on Windows, and Java writes "\r\n" there.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
