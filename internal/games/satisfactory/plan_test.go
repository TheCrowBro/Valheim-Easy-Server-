package satisfactory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// placeProgram puts placeholder program files where the plan expects them.
func placeProgram(t *testing.T, s *server) {
	t.Helper()
	os.MkdirAll(filepath.Dir(s.Program()), 0o755)
	if err := os.WriteFile(s.Program(), []byte("stand-in"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// rel shows a path inside the instance's folder the same way on every
// system.
func rel(s *server, p string) string {
	r, err := filepath.Rel(s.env.Dirs.Root, p)
	if err != nil {
		return p
	}
	return "<instance>/" + filepath.ToSlash(r)
}

// The command line, the program and the plan's other fields, for the
// default settings and for chosen ports and players.
func TestCommandLine(t *testing.T) {
	var b strings.Builder
	for _, tc := range []struct {
		name   string
		change func(*game.Saved)
		isNew  bool
	}{
		{"defaults, a new world", func(sv *game.Saved) {}, true},
		{"ports 7800 and 8900, 8 players, a password", func(sv *game.Saved) {
			sv.Ports = map[string]int{"game": 7800, "reliable": 8900}
			sv.Server["maxPlayers"] = "8"
			sv.Server["password"] = "letmein"
		}, false},
	} {
		s := testServer(t, true)
		placeProgram(t, s)
		s.store.Save(func(sv *game.Saved) error {
			sv.State[stateAdmin] = "the-admin"
			tc.change(sv)
			return nil
		})
		if !tc.isNew {
			writeTestSave(t, s.savesDir(), "Factory", "Factory", time.Now())
		}
		p, err := s.Plan(context.Background(), game.PlanRequest{World: "Factory", New: tc.isNew,
			NewWorld: game.Values{"startingLocation": "Northern Forest"}, Saved: s.store.Get(), LogPath: "ignored"})
		if err != nil {
			t.Fatal(err)
		}
		extra, _ := json.Marshal(p.Record.Extra)
		ports, _ := json.Marshal(p.Record.Ports)
		secrets := slices.Sorted(func(yield func(string) bool) {
			for k := range p.Record.Secrets {
				if !yield(k) {
					return
				}
			}
		})
		var env []string
		for _, e := range p.Env {
			k, v, _ := strings.Cut(e, "=")
			env = append(env, k+"="+rel(s, v))
		}
		b.WriteString("== " + tc.name + "\n")
		b.WriteString("exe: " + rel(s, p.Exe) + "\n")
		b.WriteString("args: " + strings.Join(p.Args, " ") + "\n")
		b.WriteString("dir: " + rel(s, p.Dir) + "\n")
		b.WriteString("env (fake mode only): " + strings.Join(env, " ") + "\n")
		b.WriteString("output: " + map[game.Output]string{game.Capture: "Capture", game.LogArg: "LogArg", game.GameLog: "GameLog"}[p.Output] + "\n")
		b.WriteString("log: " + rel(s, p.LogPath) + "\n")
		b.WriteString("ready wait: " + p.ReadyWait.String() + "\n")
		b.WriteString("record: server " + p.Record.Server + ", port " + itoa(p.Record.Port) + ", password " + p.Record.Password +
			", ports " + string(ports) + ", secrets " + strings.Join(secrets, ",") + ", extra " + string(extra) + "\n")
		if p.Console || p.SaveLine != "" {
			t.Errorf("the server has no console the app uses: %+v", p)
		}
	}
	golden(t, "command-line.txt", strings.ReplaceAll(b.String(), ".exe", ""))
}

// The real server's program on each system: the shipping program Steam's
// launcher starts (FACTS.md).
func TestProgram(t *testing.T) {
	if got := filepath.ToSlash(programFor("S", "windows", "windows")); got != "S/Engine/Binaries/Win64/FactoryServer-Win64-Shipping-Cmd.exe" {
		t.Errorf("Windows: %s", got)
	}
	if got := filepath.ToSlash(programFor("S", "windows", "linux")); got != "S/Engine/Binaries/Win64/FactoryServer-Win64-Shipping-Cmd" {
		t.Errorf("fake mode on Linux: %s", got)
	}
	if got := filepath.ToSlash(programFor("S", "linux", "linux")); got != "S/Engine/Binaries/Linux/FactoryServer-Linux-Shipping" {
		t.Errorf("Linux: %s", got)
	}
}

// The last run's log is renamed as the server itself would, before the
// start, so the app never reads it as this run's.
func TestLogSetAside(t *testing.T) {
	s := testServer(t, true)
	placeProgram(t, s)
	log := s.logPath()
	os.MkdirAll(filepath.Dir(log), 0o755)
	stamp := time.Date(2026, 11, 25, 9, 8, 7, 0, time.UTC)
	for i := 0; i < 2; i++ {
		os.WriteFile(log, []byte("last run"), 0o644)
		os.Chtimes(log, stamp, stamp)
		if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Factory", New: true, Saved: s.store.Get()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("FactoryGame.log is still there: %v", err)
	}
	for _, name := range []string{"FactoryGame-backup-2026.11.25-09.08.07.log", "FactoryGame-backup-2026.11.25-09.08.07_1.log"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(log), name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPlanRefuses(t *testing.T) {
	s := testServer(t, true)
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Factory", Saved: s.store.Get()}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("not installed: %v", err)
	}
	placeProgram(t, s)
	sv := s.store.Get()
	sv.Server["maxPlayers"] = "40"
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Factory", Saved: sv}); err == nil {
		t.Errorf("40 players planned")
	}
}

func TestValidate(t *testing.T) {
	s := testServer(t, true)
	for _, tc := range []struct {
		key, value, field string
	}{
		{"serverName", "", "serverName"},
		{"serverName", strings.Repeat("x", 65), "serverName"},
		{"serverName", "two\nlines", "serverName"},
		{"password", " spaced", "password"},
		{"password", strings.Repeat("p", 65), "password"},
		{"adminPassword", "tab\there", "adminPassword"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "17", "maxPlayers"},
		{"maxPlayers", "four", "maxPlayers"},
	} {
		sv := s.store.Get()
		sv.Server[tc.key] = tc.value
		if errs := s.Validate(sv, "Factory"); errs[tc.field] == "" {
			t.Errorf("%s=%q: no error for %s (%v)", tc.key, tc.value, tc.field, errs)
		}
	}
	sv := s.store.Get()
	sv.Server["password"], sv.Server["adminPassword"] = "same", "same"
	if errs := s.Validate(sv, "Factory"); errs["adminPassword"] == "" {
		t.Errorf("the same password for friends and admin is allowed")
	}
	if errs := s.Validate(s.store.Get(), "Bad/Name"); errs["world"] == "" {
		t.Errorf("a new world named Bad/Name is allowed")
	}
	if errs := s.Validate(s.store.Get(), "Factory"); len(errs) != 0 {
		t.Errorf("the defaults: %v", errs)
	}
}

// The admin password isn't part of a start; the friends' password and the
// name are, through the setup.
func TestFingerprint(t *testing.T) {
	s := testServer(t, true)
	sv := s.store.Get()
	base := s.Fingerprint(sv, "Factory")
	for key, restart := range map[string]bool{"adminPassword": false, "updateOnStart": false, "password": true, "serverName": true, "maxPlayers": true} {
		c := sv.Clone()
		c.Server[key] = "7"
		if changed := s.Fingerprint(c, "Factory") != base; changed != restart {
			t.Errorf("changing %s changes the fingerprint: %v", key, changed)
		}
	}
	if s.Fingerprint(sv, "Other") == base {
		t.Errorf("another world has the same fingerprint")
	}
}

func TestStartingLocation(t *testing.T) {
	for in, want := range map[string]string{"": "Grass Fields", "Dune Desert": "Dune Desert", randomStart: "", "Mars": ""} {
		if got := startingLocation(game.Values{"startingLocation": in}); got != want {
			t.Errorf("startingLocation(%q) = %q, want %q", in, got, want)
		}
	}
}
