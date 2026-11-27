package zomboid

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/rcon"
)

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// zomboid personality): install, a new world, online, the scripted join and
// leave seen through RCON's "players", the probe, the stop by RCON "quit"
// (which saves), a second start on the same world, a takeover whose stop
// still goes through RCON, and a backup that restores.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// The registry knows Project Zomboid once the package is imported (the
// lead adds internal/games/all/zomboid.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("zomboid isn't registered")
	}
	in := g.Info()
	if in.Command != "zomboid" || in.Stage != game.Preview || in.Legacy || in.Letter != "PZ" || in.Color != "#b03a2e" {
		t.Errorf("Info: command %q, stage %q, legacy %v, letter %q, colour %q", in.Command, in.Stage, in.Legacy, in.Letter, in.Color)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.OS["darwin/amd64"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	want := []game.PortRole{
		{Role: "game", Protos: []string{"udp"}, Default: 16261, Public: true},
		{Role: "direct", Protos: []string{"udp"}, Default: 16262, Tie: "game+1", Public: true},
		{Role: "rcon", Protos: []string{"tcp"}, Default: 27015, Public: false},
	}
	if len(in.Ports) != len(want) {
		t.Fatalf("Info.Ports = %+v", in.Ports)
	}
	for i, w := range want {
		p := in.Ports[i]
		if p.Role != w.Role || !slices.Equal(p.Protos, w.Protos) || p.Default != w.Default || p.Tie != w.Tie || p.Public != w.Public {
			t.Errorf("port %d = %+v, want %+v", i, p, w)
		}
	}
	if in.Presence != game.ByName || in.Deaths || in.Import || in.Lists || in.Mods || in.Password.Use != "optional" {
		t.Errorf("Info: presence %q, deaths %v, import %v, lists %v, mods %v, password %q", in.Presence, in.Deaths, in.Import, in.Lists, in.Mods, in.Password.Use)
	}
}

// The probe and the scripted players: two friends on at once, one with a
// space in their name, both listed by RCON's "players" and so by the
// Monitor, with a count that holds the computer awake.
func TestStandInPlayersThroughRCON(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@1,join:Sam Lee@1.5,leave:Alex@5,leave:Sam Lee@7"})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("join", "Alex", 15*time.Second)
	s.WaitEvent("join", "Sam Lee", 15*time.Second)
	res, err := s.Server().(game.Prober).Probe(context.Background(), s.Record())
	if err != nil || res.Count != 2 || !slices.Equal(res.Names, []string{"Alex", "Sam Lee"}) {
		t.Errorf("the probe with two on = %+v, %v", res, err)
	}
	s.WaitEvent("count", "", 5*time.Second)
	s.WaitEvent("leave", "Alex", 15*time.Second)
	s.WaitEvent("leave", "Sam Lee", 15*time.Second)
	if st := s.State(); len(st.Players) != 0 || st.Count != 0 {
		t.Errorf("after both left: players %+v, count %d", st.Players, st.Count)
	}
	// The joins went to the server's own user log, never to its console.
	logs, _ := filepath.Glob(filepath.Join(s.Env().Dirs.Saves, "Logs", "*_user.txt"))
	if len(logs) != 1 {
		t.Fatalf("user logs = %q", logs)
	}
	b, _ := os.ReadFile(logs[0])
	if !strings.Contains(string(b), `"Alex" fully connected`) || !strings.Contains(string(b), `"Sam Lee" disconnected player`) {
		t.Errorf("the user log:\n%s", b)
	}
	s.Stop()
}

// RCON "save" saves without stopping, as the real server does, and the
// world's files change.
func TestStandInSaveOverRCON(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{})
	s.WaitEvent("online", "", 30*time.Second)
	waitProbe(t, s)
	mapFile := filepath.Join(multiplayerDir(s.Env().Dirs.Saves), "Conformance", "map_t.bin")
	before, err := os.Stat(mapFile)
	if err != nil {
		t.Fatal(err)
	}
	addr, pw, err := rconAddr(s.Record())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := rcon.Exec(context.Background(), addr, pw, "save")
	if err != nil || reply != "World saved" {
		t.Fatalf("RCON save = %q, %v", reply, err)
	}
	s.WaitEvent("saved", "", 10*time.Second)
	after, err := os.Stat(mapFile)
	if err != nil || after.Size() == before.Size() {
		t.Errorf("the save didn't write the map (%d bytes before, %v after)", before.Size(), after)
	}
	if st := s.State(); st.LastSave.IsZero() || !st.Ready {
		t.Errorf("after the save: %+v", st)
	}
	s.Stop()
}

// waitProbe waits until the server's RCON answers, which it does a moment
// after it says it started.
func waitProbe(t *testing.T, s *gametest.Session) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := s.Server().(game.Prober).Probe(ctx, s.Record())
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("RCON never answered: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The stand-in is one program for every game, which picks its game from the
// command line. Started as "java", it must take Project Zomboid's command
// line for Project Zomboid and never Minecraft's or Necesse's, which also
// run Java (spec 7.1).
func TestStandInMatch(t *testing.T) {
	dir := t.TempDir()
	java := filepath.Join(dir, "java")
	if runtime.GOOS == "windows" {
		java += ".exe"
	}
	b, err := os.ReadFile(gametest.BuildFakeserver(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(java, b, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.Command(java, args...)
		cmd.Dir = dir
		cmd.Stdin = bytes.NewReader(nil) // the console ends at once
		done := make(chan []byte, 1)
		go func() { out, _ := cmd.CombinedOutput(); done <- out }()
		select {
		case out := <-done:
			return string(out)
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("the stand-in kept running with %q", args)
			return ""
		}
	}
	// Project Zomboid's own command line, without -adminpassword and with
	// no world yet: like the real server, it asks for an admin password on
	// its console, and ends when the console ends without one.
	s := Game{}.New(testEnv(t)).(*server)
	l, err := parseBat(string(readTestdata(t, "launch/StartServer64.bat")))
	if err != nil {
		t.Fatal(err)
	}
	args := s.args(l, defaultSaved(t, s), "World", "", s.env.Dirs.Saves)
	args = slices.DeleteFunc(args, func(a string) bool { return a == "-adminpassword" || a == "" })
	out := run(args...)
	if !strings.Contains(out, "version=42.") || !strings.Contains(out, "Enter new administrator password") || !strings.Contains(out, "NoSuchElementException") {
		t.Errorf("the stand-in didn't take Project Zomboid's command line for Project Zomboid:\n%s", out)
	}
	// The plan's command line names the saves folder (-cachedir), so the
	// world's settings file is there.
	if _, err := os.Stat(filepath.Join(s.env.Dirs.Saves, "Server", "World.ini")); err != nil {
		t.Errorf("the stand-in didn't keep its files in -cachedir: %v", err)
	}
	// Without -cachedir the real server uses the Zomboid folder in the
	// user's home, where their own game keeps its saves. The stand-in must
	// use one in its working folder instead and never touch the real one;
	// other games' tests (Necesse's) run it without -cachedir too.
	home, homeErr := os.UserHomeDir()
	homeIni := filepath.Join(home, "Zomboid", "Server", "World.ini")
	before, beforeErr := os.Stat(homeIni)
	run(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return strings.HasPrefix(a, "-cachedir=") })...)
	if _, err := os.Stat(filepath.Join(dir, "Zomboid", "Server", "World.ini")); err != nil {
		t.Errorf("without -cachedir the stand-in didn't use the Zomboid folder in its working folder: %v", err)
	}
	if homeErr == nil && home != "" {
		if after, err := os.Stat(homeIni); err == nil && (beforeErr != nil || !after.ModTime().Equal(before.ModTime())) {
			t.Errorf("without -cachedir the stand-in wrote into the user's own %s", homeIni)
		}
	}
	for game, line := range map[string][]string{
		"Minecraft":        {"-Xms512M", "-Xmx2048M", "-jar", "server/server.jar", "--nogui", "--universe", dir, "--world", "W", "--port", "25565"},
		"Minecraft (bare)": {"-Xmx2048M", "-jar", "server.jar", "--nogui"},
		"Necesse":          {"-Xmx2048M", "-jar", "Server.jar", "-nogui", "-datadir", dir, "-port", "14159", "-slots", "10", "-password", "", "-motd", "M", "-logging", "0"},
	} {
		if out := run(line...); strings.Contains(out, "version=42.") || strings.Contains(out, "SERVER STARTED") || strings.Contains(strings.ToLower(out), "zomboid") {
			t.Errorf("%s's command line started the Project Zomboid stand-in:\n%s", game, out)
		}
	}
}

// Ctrl+C, the stop chain's last resort, runs the server's shutdown
// handling, which saves the world before Java ends (FACTS.md): so does the
// stand-in.
func TestStandInCtrlCSaves(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{})
	s.WaitEvent("online", "", 30*time.Second)
	waitProbe(t, s)
	proc, err := platform.OpenProc(s.Record().PID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Ctrl+C reaches every program on the server's console; this
		// keeps it from ending the test too, as the app's own handler does.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	}
	if err := proc.Interrupt(); err != nil {
		t.Fatal(err)
	}
	s.WaitEvent("saved", "", 15*time.Second)
	if st := s.State(); st.Stopping {
		t.Errorf("Ctrl+C was taken for a stop someone asked for at the console")
	}
	deadline := time.Now().Add(15 * time.Second)
	for !proc.Exited() {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in didn't end after Ctrl+C")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// With its UDP port taken the start fails as the real server's does, the
// exit hint says why, and the stand-in names the port in the system's own
// words first, so gametest can tell a port another program took.
func TestStandInPortTaken(t *testing.T) {
	env := testEnv(t)
	env.Fake.Program = gametest.BuildFakeserver(t)
	env.Fake.Speed = 60 // its minute of loading takes a second
	s := Game{}.New(env).(*server)
	if err := s.Installer().Install(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	held, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.LocalAddr().(*net.UDPAddr).Port
	sv := defaultSaved(t, s)
	sv.Ports = map[string]int{"game": port, "direct": port + 1, "rcon": 1}
	p, err := s.Plan(context.Background(), game.PlanRequest{World: "World", New: true, Saved: sv, LogPath: filepath.Join(env.Dirs.Logs, "zomboid-x.log")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p.Exe, p.Args...)
	cmd.Dir = p.Dir
	out, err := cmd.CombinedOutput()
	if !strings.Contains(string(out), "Connection Startup Failed") || !strings.Contains(string(out), fmt.Sprintf("UDP port %d:", port)) {
		t.Fatalf("exit %v, output:\n%s", err, out)
	}
	if hint := s.ExitHint(strings.Split(string(out), "\n")); !strings.Contains(hint, "UDP ports") {
		t.Errorf("hint %q", hint)
	}
}

// testEnv is an instance's environment in a fresh folder, as the manager
// lays it out, in fake mode.
func testEnv(t *testing.T) game.Env {
	t.Helper()
	root := filepath.Join(t.TempDir(), "games", ID)
	d := game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
		Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(root, "..", "..", "steamcmd"),
	}
	for _, p := range []string{d.Server, d.Saves, d.Backups, d.Logs, d.Staging} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return game.Env{Instance: ID, Dirs: d, Fake: &game.Fake{Program: os.Args[0]}}
}

// defaultSaved is what Add a game saves: every default, world "World".
func defaultSaved(t *testing.T, s *server) game.Saved {
	t.Helper()
	sv := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{"World": {}}, World: "World",
		Ports: map[string]int{"game": 16261, "direct": 16262, "rcon": 27015}, State: game.Values{}}
	for _, f := range s.Schema() {
		switch {
		case f.Default == "":
		case f.Scope == game.ServerScope:
			sv.Server[f.Key] = f.Default
		case f.Scope == game.WorldScope:
			sv.Worlds["World"][f.Key] = f.Default
		}
	}
	return sv
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A world is its map folder, its accounts and its settings files: the
// list shows it once the map folder is there, a backup holds all of them,
// and deleting it removes all of them.
func TestWorldsLayout(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	wl := s.Worlds()
	saves := env.Dirs.Saves
	write := func(rel, text string) {
		p := filepath.Join(saves, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Server/Base.ini", "PublicName=x")
	if wl.Exists("Base") {
		t.Errorf("a world with only its settings (never started) exists")
	}
	write("Saves/Multiplayer/Base/map_t.bin", "map")
	write("db/Base.db", "accounts")
	write("Server/Base_SandboxVars.lua", "SandboxVars = {}")
	write("Server/Other.ini", "PublicName=y") // another world's
	list, _, err := wl.List()
	want := int64(len("map") + len("accounts") + len("PublicName=x") + len("SandboxVars = {}"))
	if err != nil || len(list) != 1 || list[0].Name != "Base" || list[0].Size != want || list[0].NeverSaved {
		t.Fatalf("list %+v, %v", list, err)
	}
	b, err := wl.Backup("Base", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := wl.Delete("Base"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Saves/Multiplayer/Base", "db/Base.db", "Server/Base.ini", "Server/Base_SandboxVars.lua"} {
		if _, err := os.Stat(filepath.Join(saves, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s is still there after the delete", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(saves, "Server", "Other.ini")); err != nil {
		t.Errorf("deleting Base removed another world's settings: %v", err)
	}
	if err := wl.Restore("Base", b.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(saves, "db", "Base.db")); err != nil || string(got) != "accounts" {
		t.Errorf("after the restore the accounts are %q, %v", got, err)
	}
	if err := wl.Rename("Base", "New"); err == nil {
		t.Errorf("a rename was taken: the name is inside the world's files")
	}
	for name, ok := range map[string]bool{"Base 2": true, "my_world-1": true, "Café": false, "a.b": false, " pad": false, "-x": false} {
		if got := wl.NameProblem(name) == ""; got != ok {
			t.Errorf("NameProblem(%q) = %q", name, wl.NameProblem(name))
		}
	}
}
