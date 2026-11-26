package corekeeper

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata from what the code does now")

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// corekeeper personality): install, a new world, online, the scripted join
// and leave, Join with the Game ID, the stop by the quit request, a second
// start on the same world, a takeover whose stop still works, and a backup
// that restores.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// The registry knows Core Keeper once the package is imported (the lead
// adds internal/games/all/corekeeper.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("corekeeper isn't registered")
	}
	in := g.Info()
	if in.Command != "corekeeper" || in.Stage != game.Preview || in.Legacy || in.Order != 70 {
		t.Errorf("Info: command %q, stage %q, legacy %v, order %d", in.Command, in.Stage, in.Legacy, in.Order)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	if len(in.Ports) != 0 || in.Password.Use != "none" || in.Password.NoneText == "" || in.Presence != game.ByName || in.Deaths {
		t.Errorf("Info: ports %v, password %+v, presence %q, deaths %v", in.Ports, in.Password, in.Presence, in.Deaths)
	}
}

func testEnv(t *testing.T) game.Env {
	root := filepath.Join(t.TempDir(), "games", ID)
	return game.Env{Instance: ID, Dirs: game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"),
	}}
}

// defaultSaved is what Add a game saves: the schema's defaults.
func defaultSaved() game.Saved {
	sv := game.Saved{Server: game.Values{}, World: "Main", State: game.Values{}}
	for _, f := range (&server{}).Schema() {
		if f.Scope == game.ServerScope && f.Default != "" {
			sv.Server[f.Key] = f.Default
		}
	}
	return sv
}

// installed makes an adapter whose server program is there (an empty
// file is enough for Plan).
func installed(t *testing.T) *server {
	t.Helper()
	s := Game{}.New(testEnv(t)).(*server)
	if err := os.MkdirAll(s.env.Dirs.Server, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Program(), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// The command line for a new world and for a later start, pinned in
// testdata/golden/commandline.txt (go test -update rewrites it). Paths are
// shown relative to the instance's folder, so the file is the same on
// every system and computer.
func TestCommandLineGolden(t *testing.T) {
	s := installed(t)
	sv := defaultSaved()
	sv.State[gameIDKey] = "Ab3dEfGh9JkLmNpQrStUvWz12345"
	s.store.Save(func(x *game.Saved) error { *x = sv.Clone(); return nil })
	root := s.env.Dirs.Root
	show := func(p game.Plan) string {
		var b strings.Builder
		rel := func(v string) string {
			if r, err := filepath.Rel(root, v); err == nil && filepath.IsAbs(v) {
				return "<instance>/" + filepath.ToSlash(r)
			}
			return v
		}
		b.WriteString("exe: " + strings.TrimSuffix(rel(p.Exe), ".exe") + "\n")
		b.WriteString("dir: " + rel(p.Dir) + "\n")
		for _, a := range p.Args {
			b.WriteString("  " + rel(a) + "\n")
		}
		b.WriteString("output: " + map[game.Output]string{game.Capture: "Capture", game.LogArg: "LogArg", game.GameLog: "GameLog"}[p.Output] + "\n")
		b.WriteString("readyWait: " + p.ReadyWait.String() + "\n")
		b.WriteString("record server: " + p.Record.Server + "\n")
		return b.String()
	}
	var out strings.Builder
	out.WriteString("# The Core Keeper server's command line as the Plan builds it (spec 5.8).\n")
	out.WriteString("# Paths are relative to the instance's folder, games/corekeeper.\n\n")

	logPath := filepath.Join(s.env.Dirs.Logs, "corekeeper-2026-11-26_21-35-10.log")
	newWorld := game.PlanRequest{World: "Crow's Cave", New: true, Saved: sv,
		NewWorld: game.Values{"mode": "hard", "seed": "Nice World"}, LogPath: logPath}
	p, err := s.Plan(t.Context(), newWorld)
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("## A new world, mode hard, seed \"Nice World\"\n" + show(p) + "\n")

	// The server made the world; a later start leaves the mode and seed out.
	os.MkdirAll(filepath.Join(s.worlds.dataPath("Crow's Cave"), "worlds"), 0o755)
	os.WriteFile(filepath.Join(s.worlds.dataPath("Crow's Cave"), "worlds", worldFile), []byte("x"), 0o644)
	later := sv.Clone()
	later.Server["serverName"] = "Friends' Cave"
	later.Server["maxPlayers"] = "4"
	p, err = s.Plan(t.Context(), game.PlanRequest{World: "Crow's Cave", Saved: later, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("## The same world later, renamed server, 4 players\n" + show(p) + "\n")

	// A new world with no seed: the server picks one.
	p, err = s.Plan(t.Context(), game.PlanRequest{World: "Second", New: true, Saved: sv, NewWorld: game.Values{}, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("## A new world with the defaults (no seed: the server picks one)\n" + show(p))
	golden(t, filepath.Join("testdata", "golden", "commandline.txt"), out.String())
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	// Git on Windows may check text files out with CRLF line ends.
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from what the code does now (go test -update rewrites it; read the diff):\n--- want\n%s\n--- got\n%s", path, w, got)
	}
}

// The Game ID is made once, kept in the settings, and given on every
// start; the server's own rules decide what a valid one is.
func TestGameID(t *testing.T) {
	s := installed(t)
	sv := defaultSaved()
	s.store.Save(func(x *game.Saved) error { *x = sv.Clone(); return nil })
	p, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: sv, LogPath: "log"})
	if err != nil {
		t.Fatal(err)
	}
	id := s.store.Get().State[gameIDKey]
	if !validGameID(id) || len(id) != 28 {
		t.Fatalf("the Game ID made is %q", id)
	}
	if i := slices.Index(p.Args, "-gameid"); i < 0 || p.Args[i+1] != id {
		t.Errorf("the plan doesn't give the Game ID: %q", p.Args)
	}
	p2, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", Saved: s.store.Get(), LogPath: "log"})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(p2.Args, "-gameid"); i < 0 || p2.Args[i+1] != id {
		t.Errorf("the second start has another Game ID: %q", p2.Args)
	}
	// The Game ID isn't part of the fingerprint, so making it doesn't ask
	// for a restart.
	if p.Fingerprint != s.Fingerprint(sv, "Main") {
		t.Errorf("the fingerprint depends on the Game ID")
	}
	for _, c := range []struct {
		id string
		ok bool
	}{
		{"Ek8c5udkq9vH3cf", true},                // made by the real server
		{"zSVDbmJgkrTShsuiRDp28VjHEqC3", true},   // 28 characters, from a 1.0 server
		{"Ek8c5udkq9vH3c", false},                // 14
		{"zSVDbmJgkrTShsuiRDp28VjHEqC3a", false}, // 29
		{"Ek8c5udkq9vH3cO", false},               // O
		{"Ek8c5udkq9vH3c0", false},               // 0
		{"Ek8c5udkq9vH3cy", false},               // y
		{"Ek8c5udkq9vH3cx", false},               // x
		{"Ek8c5udkq9vH3cY", false},               // Y
		{"Ek8c5udkq9vH3c-", false},
	} {
		if validGameID(c.id) != c.ok {
			t.Errorf("validGameID(%q) = %v", c.id, !c.ok)
		}
	}
	for range 50 {
		n, err := newGameID()
		if err != nil || !validGameID(n) {
			t.Fatalf("newGameID = %q, %v", n, err)
		}
	}
}

func TestPlanRefuses(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved()}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("not installed: %v", err)
	}
	s = installed(t)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Gone", Saved: defaultSaved()}); err == nil || !strings.Contains(err.Error(), "isn't in Core Keeper's saves folder") {
		t.Errorf("a missing world: %v", err)
	}
	bad := defaultSaved()
	bad.Server["maxPlayers"] = "0"
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: bad}); err == nil {
		t.Errorf("0 players was accepted")
	}
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved(), NewWorld: game.Values{"mode": "nightmare"}}); err == nil {
		t.Errorf("an unknown world mode was accepted")
	}
	// Old GameInfo.txt and GameID.txt go before a start.
	for _, f := range []string{gameInfoFile, gameIDFile} {
		os.WriteFile(filepath.Join(s.env.Dirs.Server, f), []byte("GameID: old"), 0o644)
	}
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved(), LogPath: "log"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{gameInfoFile, gameIDFile} {
		if _, err := os.Stat(filepath.Join(s.env.Dirs.Server, f)); err == nil {
			t.Errorf("the old %s is still there after Plan", f)
		}
	}
}

func TestValidate(t *testing.T) {
	s := Game{}.New(testEnv(t))
	ok := defaultSaved()
	if errs := s.Validate(ok, "Main"); len(errs) > 0 {
		t.Fatalf("the defaults: %v", errs)
	}
	for _, c := range []struct{ key, value, world, want string }{
		{"serverName", "", "Main", "serverName"},
		{"serverName", "   ", "Main", "serverName"},
		{"serverName", strings.Repeat("n", 61), "Main", "serverName"},
		{"serverName", "two\nlines", "Main", "serverName"},
		{"maxPlayers", "0", "Main", "maxPlayers"},
		{"maxPlayers", "65", "Main", "maxPlayers"},
		{"maxPlayers", "eight", "Main", "maxPlayers"},
		{"serverName", "Fine", "-world", "world"},
		{"serverName", "Fine", "a/b", "world"},
		{"serverName", "Fine", "", "world"},
	} {
		sv := ok.Clone()
		sv.Server[c.key] = c.value
		if errs := s.Validate(sv, c.world); errs[c.want] == "" {
			t.Errorf("%s=%q world %q: no error for %s (got %v)", c.key, c.value, c.world, c.want, errs)
		}
	}
	// What the real server took (FACTS.md): spaces, quotes and emoji.
	sv := ok.Clone()
	sv.Server["serverName"] = "Crow's Cave ✓ 🏡"
	if errs := s.Validate(sv, "Crow's Cave ✓"); len(errs) > 0 {
		t.Errorf("names the real server took: %v", errs)
	}
}

func TestJoin(t *testing.T) {
	s := Game{}.New(testEnv(t))
	sv := defaultSaved()
	// Never started: no Game ID yet.
	j := s.Join(game.JoinInput{Saved: sv})
	if j.How != "id" || j.CodeLabel != "Game ID" || j.Code != "" || j.Router || len(j.Steps) == 0 || j.LAN == nil {
		t.Errorf("before the first start: %+v", j)
	}
	// Stopped, with the Game ID the app gives it.
	sv.State[gameIDKey] = "Ab3dEfGh9JkLmNpQrStUvWz12345"
	j = s.Join(game.JoinInput{Saved: sv})
	if j.Code != "Ab3dEfGh9JkLmNpQrStUvWz12345" || !strings.Contains(strings.Join(j.Steps, " "), j.Code) || !strings.Contains(j.Hint, j.Code) {
		t.Errorf("stopped: %+v", j)
	}
	// Running: what the server said wins, and the version is named.
	rec := game.Record{}
	st := game.RunState{Ready: true, Code: "Ek8c5udkq9vH3cf", Version: "1.3.0.7"}
	j = s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: sv})
	if j.Code != "Ek8c5udkq9vH3cf" || !strings.Contains(strings.Join(j.Steps, " "), "1.3.0.7") || j.Problem != "" {
		t.Errorf("running: %+v", j)
	}
	// Running, but no Game ID anywhere.
	st = game.RunState{Ready: true}
	j = s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: defaultSaved()})
	if j.Problem == "" {
		t.Errorf("no Game ID while running: no problem shown: %+v", j)
	}
}

// The stop is one quit request, sent again every minute, waiting 3
// minutes: the window's close on Windows (the real server is a windowed
// program), Ctrl+C elsewhere and for the stand-in, which is a console
// program everywhere.
func TestStopChain(t *testing.T) {
	real := (&server{}).Stop(game.Record{PID: 1})
	fake := (&server{env: game.Env{Fake: &game.Fake{}}}).Stop(game.Record{PID: 1})
	wantReal := "interrupt"
	if runtime.GOOS == "windows" {
		wantReal = "call"
	}
	for _, c := range []struct {
		name  string
		steps []game.StopStep
		kind  string
	}{{"real", real, wantReal}, {"fake", fake, "interrupt"}} {
		st := c.steps
		if len(st) != 1 || st[0].Kind != c.kind || (c.kind == "call") != (st[0].Call != nil) || st[0].Wait != 3*time.Minute || st[0].Resend != time.Minute {
			t.Errorf("%s stop chain = %+v: want one %s step, sent again every minute, waiting 3 minutes", c.name, st, c.kind)
		}
	}
}

// The window's close is never sent to another program that has since got
// the server's process number, nor to one that isn't there.
func TestCloseWindowChecksTheProcess(t *testing.T) {
	me := game.Record{PID: os.Getpid(), Started: time.Now().Add(-time.Hour)}
	if err := closeWindow(me)(t.Context()); err == nil || !strings.Contains(err.Error(), "no longer the Core Keeper server") {
		t.Errorf("another program with the server's number: %v", err)
	}
	if err := closeWindow(game.Record{PID: 1 << 30})(t.Context()); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("no such process: %v", err)
	}
}

// A world copied from the owner's own game is one file named by its slot;
// the start makes it slot 0 of the world's folder, and never guesses
// between several.
func TestTidyWorld(t *testing.T) {
	dir := t.TempDir()
	put := func(rel string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := tidyWorld(dir); moved != "" || err != nil {
		t.Errorf("an empty folder: %q, %v", moved, err)
	}
	put("3.world.gzip")
	put("3.world.gzip.pugbackup")
	moved, err := tidyWorld(dir)
	if err != nil || moved != "3.world.gzip" {
		t.Fatalf("a lone world file: %q, %v", moved, err)
	}
	for _, f := range []string{"worlds/0.world.gzip", "worlds/0.world.gzip.pugbackup"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s isn't there: %v", f, err)
		}
	}
	// Slot 0 is there: nothing more moves.
	put("worlds/5.world.gzip")
	if moved, err := tidyWorld(dir); moved != "" || err != nil {
		t.Errorf("with slot 0 there: %q, %v", moved, err)
	}
	dir2 := t.TempDir()
	dir = dir2
	put("worlds/1.world.gzip")
	put("2.world.gzip")
	if _, err := tidyWorld(dir); err == nil || !strings.Contains(err.Error(), "2 Core Keeper worlds") {
		t.Errorf("two world files: %v", err)
	}
}

func TestExitHint(t *testing.T) {
	s := &server{}
	for _, c := range []struct {
		line, want string
	}{
		{"Checking system requirements: FAIL", "graphics driver"},
		{"Not initializing Steam network since no Steam API available", "Steam's networking"},
		{"Failed to start session", "Steam session"},
		{"Skipping world save due to suspiciously small compressed data size: 12 bytes. ", "couldn't save"},
		{"Something failed during server startup, and server was shut down. Check CoreKeeperServerLog.txt for more info.", "stopped while starting"},
	} {
		if h := s.ExitHint([]string{"Steam API initialized", c.line}); !strings.Contains(h, c.want) {
			t.Errorf("ExitHint(%q) = %q", c.line, h)
		}
	}
	if h := s.ExitHint([]string{"Running quit handlers"}); h != "" {
		t.Errorf("a normal stop has a hint: %q", h)
	}
}

// After an app restart the new Monitor reads the Game ID from the log
// again, so Join still has it.
func TestTakeoverKeepsGameID(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@1"})
	s.WaitEvent("online", "", 30*time.Second)
	code := s.State().Code
	if !validGameID(code) || code != s.Env().Store.Get().State[gameIDKey] {
		t.Fatalf("Code %q, the settings' Game ID %q", code, s.Env().Store.Get().State[gameIDKey])
	}
	s.WaitEvent("join", "Alex", 15*time.Second)
	s.Takeover()
	deadline := time.Now().Add(15 * time.Second)
	for s.State().Code != code || len(s.State().Players) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("after the takeover: %+v", s.State())
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The stand-in wrote GameInfo.txt beside its program, as the real one does.
	b, err := os.ReadFile(filepath.Join(s.Env().Dirs.Server, gameInfoFile))
	if err != nil || !strings.Contains(string(b), "GameID: "+code) {
		t.Errorf("GameInfo.txt: %q, %v", b, err)
	}
	if last := s.Stop(); last.IsZero() {
		t.Errorf("the stop after the takeover didn't save")
	}
}
