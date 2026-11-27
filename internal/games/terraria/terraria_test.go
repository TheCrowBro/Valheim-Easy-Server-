package terraria

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// TestConformance runs everything every adapter must do against the
// stand-in (cmd/fakeserver's terraria personality): install, a new world,
// online, the scripted players, the console's save line, the stop by
// "exit", a second start on the same world, and a takeover whose stop has
// no console and falls back to Ctrl+C.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// TestTakeoverFallsBackToCtrlC: after an app restart the console is gone,
// so "exit" can't be typed; the chain moves on to Ctrl+C at once, which
// ends the server without a save, as the real one does.
func TestTakeoverFallsBackToCtrlC(t *testing.T) {
	// Alex leaves at once, and the server saves for the last player
	// leaving; with nobody on it doesn't autosave, so nothing writes the
	// world after that unless the stop does.
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@1,leave:Alex@1.5"})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("leave", "Alex", 15*time.Second)
	s.WaitEvent("saved", "", 15*time.Second)
	s.Takeover()
	before, _ := s.Server().Worlds().Get("Conformance")
	start := time.Now()
	s.Stop()
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("the stop after a takeover took %v: the console step should have been skipped at once", d)
	}
	after, err := s.Server().Worlds().Get("Conformance")
	if err != nil {
		t.Fatal(err)
	}
	if after.Size != before.Size || !after.Modified.Equal(before.Modified) {
		t.Error("Ctrl+C saved the world; the real server doesn't, and the stand-in must not pretend it does")
	}
	// The world is intact: it starts again from its last save.
	s.StartAgain()
	s.WaitEvent("online", "", 30*time.Second)
	if s.Plan().ReadyWait != readyWaitStart {
		t.Errorf("the second start's ReadyWait is %v: it should load the world, not make one", s.Plan().ReadyWait)
	}
	s.Stop()
}

// TestDenyAndFull: friends turned away become warnings, and the server
// saves when the last player leaves.
func TestDenyAndFull(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{
		Script: "deny:X@1,badversion:Y@1.2,join:Alex@1.5,join:Bea@2,leave:Alex@3",
		Change: func(sv *game.Saved) { sv.Server["maxPlayers"] = "1" },
	})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("wrong_password", "", 15*time.Second)
	e := s.WaitEvent("bad_version", "", 15*time.Second)
	if !strings.Contains(e.Text, "Terraria") {
		t.Errorf("bad_version text %q", e.Text)
	}
	s.WaitEvent("join", "Alex", 15*time.Second)
	s.WaitEvent("server_full", "", 15*time.Second)
	s.WaitEvent("leave", "Alex", 15*time.Second)
	s.WaitEvent("saved", "", 15*time.Second)
	s.Stop()
}

func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("Terraria isn't registered")
	}
	in := g.Info()
	if in.Legacy || in.Stage != game.Preview || in.Presence != game.ByName || in.Deaths || in.Lists || in.Mods || !in.Import {
		t.Errorf("info %+v", in)
	}
	if len(in.Ports) != 1 || in.Ports[0].Default != 7777 || !slices.Equal(in.Ports[0].Protos, []string{"tcp", "udp"}) || !in.Ports[0].Public {
		t.Errorf("ports %+v", in.Ports)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin/amd64"] != game.Runs || in.OS["darwin/arm64"] != game.CantRun ||
		in.NeedsText["darwin/arm64"] != "Needs an Intel Mac" {
		t.Errorf("systems %v %v", in.OS, in.NeedsText)
	}
	if in.Password.Use != "optional" || in.MemoryMB != 1536 || in.MaxPlayers != 8 {
		t.Errorf("info %+v", in)
	}
}

// newTestServer is an adapter with its folders in a temporary data folder,
// as if the real server were installed: the program is an empty file.
func newTestServer(t *testing.T) (*server, string) {
	t.Helper()
	data := t.TempDir()
	root := filepath.Join(data, "games", ID)
	dirs := game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
		Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(data, "steamcmd"),
	}
	s := Game{}.New(game.Env{Instance: ID, Data: data, Dirs: dirs, Log: log.New(&testWriter{t}, "", 0)}).(*server)
	return s, data
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func installProgram(t *testing.T, s *server) {
	t.Helper()
	p := s.Program()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

// withXNA pretends XNA is installed, so Plan's Windows check passes.
func withXNA(t *testing.T) {
	saved := haveXNA
	haveXNA = func() bool { return true }
	t.Cleanup(func() { haveXNA = saved })
}

func testSaved() game.Saved {
	return game.Saved{
		Server: game.Values{"serverName": "Friday Terraria", "password": "p=ss #1", "maxPlayers": "6", "updateOnStart": "true", "autoRestart": "true"},
		Worlds: map[string]game.Values{"Forest": {}},
		World:  "Forest",
		Ports:  map[string]int{"game": 7790},
	}
}

// relative shows a path from the data folder with forward slashes, so the
// goldens are the same on every system.
func relative(data, s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, data, "<data>"), `\`, "/")
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -args -update to make it)", err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s changed.\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// TestPlanGolden pins serverconfig.txt for a new world and for a start of
// an existing one, and the plan around it.
func TestPlanGolden(t *testing.T) {
	withXNA(t)
	s, data := newTestServer(t)
	installProgram(t, s)
	sv := testSaved()
	logPath := filepath.Join(s.env.Dirs.Logs, "terraria-2026-11-26_19-00-00.log")

	plan, err := s.Plan(context.Background(), game.PlanRequest{World: "Forest", New: true, Saved: sv, LogPath: logPath,
		NewWorld: game.Values{"size": "large", "difficulty": "expert", "seed": "for the worthy"}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(s.env.Dirs.Server, configName)
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "serverconfig-new-world.txt", relative(data, string(b)))
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(config); st.Mode().Perm() != 0o600 {
			t.Errorf("serverconfig.txt holds the password but is %v", st.Mode().Perm())
		}
	}
	if !plan.Console || plan.SaveLine != "save" || plan.Output != game.Capture || plan.LogPath != logPath || plan.ReadyWait != readyWaitNew {
		t.Errorf("plan %+v", plan)
	}
	if plan.Record.Server != "Friday Terraria" || plan.Record.Port != 7790 || plan.Record.Password != "p=ss #1" {
		t.Errorf("record %+v", plan.Record)
	}
	if plan.Fingerprint != s.Fingerprint(sv, "Forest") {
		t.Error("the plan's fingerprint isn't Fingerprint's")
	}
	if want := []string{"-config", config}; !slices.Equal(plan.Args, want) || plan.Exe != s.Program() {
		t.Errorf("command line %q %q", plan.Exe, plan.Args)
	}

	// The world is there now: a plain start, without the new-world lines.
	os.MkdirAll(s.env.Dirs.Saves, 0o755)
	os.WriteFile(filepath.Join(s.env.Dirs.Saves, "Forest.wld"), []byte("world"), 0o644)
	plan, err = s.Plan(context.Background(), game.PlanRequest{World: "Forest", Saved: sv, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(config)
	checkGolden(t, "serverconfig-start.txt", relative(data, string(b)))
	if plan.ReadyWait != readyWaitStart {
		t.Errorf("ReadyWait %v", plan.ReadyWait)
	}
}

// TestCommandLineGolden pins how the server is started on each system:
// the program, its arguments, the folder it runs in and the environment
// the zip's scripts would have set.
func TestCommandLineGolden(t *testing.T) {
	s, data := newTestServer(t)
	var b strings.Builder
	for _, goos := range []string{"windows", "darwin", "linux", "fake"} {
		program := programFor(s.env.Dirs.Server, goos)
		if goos == "fake" {
			program = fakeProgram(s.env.Dirs.Server, "linux")
		}
		dir, env := launch(program, goos)
		fmt.Fprintf(&b, "[%s]\nprogram: %s\nargs: %s\ndir: %s\nenv: %s\n\n", goos,
			relative(data, program), relative(data, strings.Join(s.args(), " ")), relative(data, dir), relative(data, strings.Join(env, " ")))
	}
	checkGolden(t, "commandline.txt", b.String())

	// This system's Plan starts exactly that.
	withXNA(t)
	installProgram(t, s)
	plan, err := s.Plan(context.Background(), game.PlanRequest{World: "Forest", New: true, Saved: testSaved(), LogPath: filepath.Join(s.env.Dirs.Logs, "x.log")})
	if err != nil {
		t.Fatal(err)
	}
	dir, env := launch(programFor(s.env.Dirs.Server, runtime.GOOS), runtime.GOOS)
	if plan.Exe != programFor(s.env.Dirs.Server, runtime.GOOS) || plan.Dir != dir || !slices.Equal(plan.Env, env) {
		t.Errorf("plan %q in %q with %q, want %q", plan.Exe, plan.Dir, plan.Env, dir)
	}
}

func TestPlanRefuses(t *testing.T) {
	withXNA(t)
	s, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := s.Plan(ctx, game.PlanRequest{World: "Forest", New: true, Saved: testSaved()}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("not installed: %v", err)
	}
	installProgram(t, s)
	if _, err := s.Plan(ctx, game.PlanRequest{World: "Forest", Saved: testSaved()}); err == nil || !strings.Contains(err.Error(), "isn't in Terraria's saves folder") {
		t.Errorf("a missing world: %v", err)
	}
	_, err := s.Plan(ctx, game.PlanRequest{World: "Forest", New: true, Saved: testSaved(), NewWorld: game.Values{"seed": strings.Repeat("s", 41), "size": "huge"}})
	var fe game.FieldErrors
	if !errors.As(err, &fe) || fe["seed"] == "" || fe["size"] == "" {
		t.Errorf("bad new-world values: %v", err)
	}
	if runtime.GOOS == "windows" {
		haveXNA = func() bool { return false }
		if _, err := s.Plan(ctx, game.PlanRequest{World: "Forest", New: true, Saved: testSaved()}); err == nil || !strings.Contains(err.Error(), "XNA") {
			t.Errorf("no XNA: %v", err)
		}
	}
}

func TestValidate(t *testing.T) {
	s, _ := newTestServer(t)
	ok := testSaved()
	if errs := s.Validate(ok, "Forest"); len(errs) > 0 {
		t.Fatalf("good settings: %v", errs)
	}
	for _, c := range []struct {
		key, value, field string
	}{
		{"serverName", strings.Repeat("a", 65), "serverName"},
		{"serverName", "  ", "serverName"},
		{"serverName", "two\nlines", "serverName"},
		{"password", "new\nline", "password"},
		{"password", " spaced", "password"},
		{"password", strings.Repeat("p", 65), "password"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "256", "maxPlayers"},
		{"maxPlayers", "lots", "maxPlayers"},
	} {
		sv := ok.Clone()
		sv.Server[c.key] = c.value
		if errs := s.Validate(sv, "Forest"); errs[c.field] == "" {
			t.Errorf("%s=%q: no error (%v)", c.key, c.value, errs)
		}
	}
	if errs := s.Validate(ok, "CON"); errs["world"] == "" {
		t.Error("a world name Windows can't use was taken")
	}
	if errs := s.Validate(ok, strings.Repeat("w", maxWorldName+1)); errs["world"] == "" {
		t.Error("a long world name was taken")
	}
}

func TestFingerprint(t *testing.T) {
	s, _ := newTestServer(t)
	sv := testSaved()
	base := s.Fingerprint(sv, "Forest")
	if s.Fingerprint(sv, "Desert") == base {
		t.Error("another world gives the same fingerprint")
	}
	c := sv.Clone()
	c.Server["updateOnStart"] = "false"
	if s.Fingerprint(c, "Forest") != base {
		t.Error("updateOnStart changes the fingerprint, but the running server doesn't use it")
	}
	c = sv.Clone()
	c.Ports["game"] = 7800
	if s.Fingerprint(c, "Forest") == base {
		t.Error("a new port doesn't change the fingerprint")
	}
}

func TestStop(t *testing.T) {
	s, _ := newTestServer(t)
	steps := s.Stop(game.Record{})
	if len(steps) != 2 || steps[0].Kind != "console" || steps[0].Line != "exit" || steps[1].Kind != "interrupt" {
		t.Fatalf("steps %+v", steps)
	}
	if steps[0].Wait != 3*time.Minute || steps[1].Wait != time.Minute {
		t.Errorf("waits %v, %v", steps[0].Wait, steps[1].Wait)
	}
	if steps[1].Say != "Terraria saves every 10 minutes and when the last player leaves; anything since then is lost." {
		t.Errorf("Ctrl+C step says %q", steps[1].Say)
	}
}

func TestJoin(t *testing.T) {
	s, _ := newTestServer(t)
	sv := testSaved()
	rec := game.Record{Port: 7790, Password: "p=ss #1"}
	st := game.RunState{Ready: true, Version: "1.4.6.1"}
	j := s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"},
		Router: map[string]string{"game": "open"}})
	if j.How != "address" || j.Address != "203.0.113.7" || j.Port != 7790 || j.Password != "p=ss #1" || !j.Router || !j.Public {
		t.Errorf("join %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:7790"}) {
		t.Errorf("LAN %q", j.LAN)
	}
	want := []string{
		"In Terraria, open Multiplayer and add a server: the address is 203.0.113.7 and the port is 7790.",
		"Type the server's password when Terraria asks for it.",
		"Use Terraria 1.4.6.1: it must be the same version as the server.",
	}
	if !slices.Equal(j.Steps, want) {
		t.Errorf("steps\n %q\nwant\n %q", j.Steps, want)
	}
	if j.Hint != "Add the server 203.0.113.7, port 7790, in Terraria's Multiplayer menu." || j.Problem != "" {
		t.Errorf("hint %q, problem %q", j.Hint, j.Problem)
	}

	// Stopped, no password, the router failed, the version not known yet.
	sv.Server["password"] = ""
	j = s.Join(game.JoinInput{Saved: sv, Router: map[string]string{"game": "failed"}})
	if j.Password != "" || len(j.Steps) != 2 || !strings.Contains(j.Steps[0], "this computer's address") ||
		!strings.Contains(j.Steps[1], "newest Terraria") || !strings.Contains(j.Problem, "7790") {
		t.Errorf("join %+v", j)
	}
}

func TestExitHint(t *testing.T) {
	s, _ := newTestServer(t)
	tail := func(name string) []string {
		l := readLog(t, name)
		if len(l) > 80 { // the manager hands the newest 80 lines
			l = l[len(l)-80:]
		}
		return l
	}
	if h := s.ExitHint(tail("port-in-use.log")); !strings.Contains(h, "port 17777") {
		t.Errorf("port in use: %q", h)
	}
	if h := s.ExitHint(tail("damaged-world.log")); !strings.Contains(h, "Restore a backup") {
		t.Errorf("damaged world: %q", h)
	}
	if h := s.ExitHint(tail("chat-two-players-exit.log")); h != "" {
		t.Errorf("a normal exit: %q", h)
	}
	if h := s.ExitHint(tail("load-world.log")); h != "" {
		t.Errorf("Ctrl+C after loading: %q", h)
	}
	if h := s.ExitHint([]string{"System.NullReferenceException: Object reference not set to an instance of an object",
		"   at Microsoft.Xna.Framework.Game.Tick()"}); strings.Contains(h, "XNA") {
		t.Errorf("a crash whose stack names XNA was taken for XNA missing: %q", h)
	}
	for line, want := range map[string]string{
		"System.Net.Sockets.SocketException (0x80004005): Only one usage of each socket address (protocol/network address/port) is normally permitted": "Another program is using port",
		"Tried to run two servers on the same PC": "Another program is using port",
		"System.IO.FileNotFoundException: Could not load file or assembly 'Microsoft.Xna.Framework, Version=4.0.0.0, Culture=neutral, PublicKeyToken=842cf8be1de50553' or one of its dependencies.": "XNA",
		"System.DllNotFoundException: GNS_Wrapper": "Repair",
	} {
		if h := s.ExitHint([]string{"Terraria Server v1.4.6.1", line}); !strings.Contains(h, want) {
			t.Errorf("%q: hint %q, want %q in it", line, h, want)
		}
	}
}

func TestWorlds(t *testing.T) {
	s, _ := newTestServer(t)
	w := s.Worlds()
	os.MkdirAll(s.env.Dirs.Saves, 0o755)
	for _, ext := range []string{".wld", ".wld.bak", ".wld.bak2"} {
		os.WriteFile(filepath.Join(s.env.Dirs.Saves, "Forest"+ext), []byte("data"+ext), 0o644)
	}
	got, err := w.Get("Forest")
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != int64(len("data.wld")+len("data.wld.bak")+len("data.wld.bak2")) {
		t.Errorf("the world's files: %d bytes; the backups beside it should count", got.Size)
	}
	if !errors.Is(w.Rename("Forest", "Jungle"), game.ErrCantRename) {
		t.Error("Rename should refuse: the name is inside the world file")
	}
	if _, err := w.Backup("Forest", "test"); err != nil {
		t.Errorf("backup: %v", err)
	}
	for _, dir := range localWorlds("/home/someone") {
		if !strings.Contains(dir, "Terraria") && !strings.Contains(dir, steamAppID) {
			t.Errorf("odd local worlds folder %q", dir)
		}
	}
}
