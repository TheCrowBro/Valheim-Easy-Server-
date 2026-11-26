package satisfactory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// The registry knows Satisfactory once the package is imported (the lead
// adds internal/games/all/satisfactory.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("satisfactory isn't registered")
	}
	in := g.Info()
	if in.Command != "satisfactory" || in.Stage != game.Preview || in.Legacy || in.Order != 90 || in.Letter != "S" || in.Color != "#d35400" {
		t.Errorf("Info: command %q, stage %q, legacy %v, order %d, %s %s", in.Command, in.Stage, in.Legacy, in.Order, in.Letter, in.Color)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.OS["darwin/amd64"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	want := []game.PortRole{
		{Role: "game", Protos: []string{"udp", "tcp"}, Default: 7777, Public: true},
		{Role: "reliable", Protos: []string{"tcp"}, Default: 8888, Public: true},
	}
	if !slices.EqualFunc(in.Ports, want, func(a, b game.PortRole) bool {
		return a.Role == b.Role && a.Default == b.Default && slices.Equal(a.Protos, b.Protos) && a.Public == b.Public && a.Tie == ""
	}) {
		t.Errorf("Info.Ports = %+v, want game UDP+TCP 7777 and reliable TCP 8888, both public, neither tied", in.Ports)
	}
	if in.Presence != game.ByCount || in.Deaths || !in.Import || in.Lists || in.Mods || in.Password.Use != "optional" ||
		in.MemoryMB != 12288 || in.MaxPlayers != 4 || in.DownloadMB < 3000 || in.DiskMB < 15500 {
		t.Errorf("Info = %+v", in)
	}
}

func TestJoin(t *testing.T) {
	s := testServer(t, true)
	sv := s.store.Get()
	sv.Server["password"] = "letmein"
	// Stopped: from the settings.
	j := s.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"game": "open", "reliable": "failed"}})
	if j.How != "address" || j.Address != "203.0.113.7:7777" || j.Port != 7777 || !slices.Equal(j.LAN, []string{"192.168.1.20:7777"}) ||
		j.Password != "letmein" || !j.Router || j.Public || j.Code != "" {
		t.Errorf("Join = %+v", j)
	}
	steps := strings.Join(j.Steps, "\n")
	for _, want := range []string{"Server Manager", "Add Server", "203.0.113.7 as the address and 7777 as the port", "certificate", "password", "same version"} {
		if !strings.Contains(steps, want) {
			t.Errorf("the steps don't say %q:\n%s", want, steps)
		}
	}
	if !strings.Contains(j.Problem, "7777 (UDP and TCP)") || !strings.Contains(j.Problem, "8888 (TCP)") {
		t.Errorf("Problem = %q", j.Problem)
	}
	if j.Hint == "" {
		t.Errorf("no hint")
	}
	// Running: what the run uses, and the fingerprint and version the
	// server wrote in its log.
	rec := game.Record{Port: 7800, Ports: map[string]int{"game": 7800, "reliable": 8900}, Password: "running-pw"}
	st := game.RunState{Version: "1.2.0 (build 520140)", Extra: map[string]string{extraFingerprint: "SHA256:abc="}}
	j = s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: sv, LAN: []string{"192.168.1.20"}})
	steps = strings.Join(j.Steps, "\n")
	if j.Port != 7800 || j.Password != "running-pw" || j.Address != "" || !strings.Contains(steps, "SHA256:abc=") ||
		!strings.Contains(steps, "Satisfactory 1.2.0 (build 520140)") || !strings.Contains(steps, "this computer's address as the address and 7800") || j.Problem != "" {
		t.Errorf("running Join = %+v", j)
	}
}

// A server found running without its run file gets the ports and the admin
// password the app keeps, so the probe and the stop still work.
func TestRecover(t *testing.T) {
	s := testServer(t, true)
	s.store.Save(func(sv *game.Saved) error { sv.State[stateAdmin] = "the-admin"; return nil })
	started := time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	rec := s.Recover(4242, started)
	if rec.PID != 4242 || !rec.Started.Equal(started) || rec.World != "Factory" || rec.Exe != s.Program() || rec.LogPath != s.logPath() ||
		rec.Ports["game"] != 7777 || rec.Ports["reliable"] != 8888 || rec.Secrets[secretAdmin] != "the-admin" || rec.Game != ID ||
		rec.Played.Mods == nil || !s.Pending(rec) {
		t.Errorf("Recover = %+v", rec)
	}
}

// Removing the game sets the server's settings for its port aside, so the
// app can claim it again if the game comes back; the worlds stay.
func TestRemove(t *testing.T) {
	s := testServer(t, true)
	if err := s.Remove(context.Background()); err != nil {
		t.Errorf("Remove before any claim: %v", err)
	}
	writeTestSave(t, s.savesDir(), "Factory", "Factory", time.Now())
	path := s.settingsFile(7777)
	os.WriteFile(path, []byte("settings"), 0o600)
	s.store.Save(func(sv *game.Saved) error {
		sv.State[stateClaimed], sv.State[stateClaimPort] = "2026-11-26T20:00:00Z", "7777"
		return nil
	})
	if err := s.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the settings file is still there")
	}
	aside, _ := filepath.Glob(path + ".removed-*")
	if len(aside) != 1 {
		t.Errorf("set aside: %q", aside)
	}
	if !s.worlds.Exists("Factory") {
		t.Errorf("the world went with the game")
	}
}

func TestExitHint(t *testing.T) {
	s := testServer(t, true)
	for _, tc := range []struct {
		tail []string
		want string
	}{
		{[]string{"[x][0]LogNet: Error: Failed to bind the reliable messaging port 8888: address already in use"}, "Another program"},
		{[]string{"[x][0]LogMemory: Fatal error: Ran out of memory allocating 1234 bytes"}, "ran out of memory"},
		{[]string{"[x][0]LogWindows: Error: === Critical error: ==="}, "crashed"},
		{nil, "stopped before it started up"},
		{[]string{"LogInit: Build: ++FactoryGame+rel-main-1.2.0-CL-520140", "[x][0]LogExit: Exiting."}, ""},
	} {
		got := s.ExitHint(tc.tail)
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("ExitHint(%q) = %q, want it to say %q", tc.tail, got, tc.want)
		}
	}
}
