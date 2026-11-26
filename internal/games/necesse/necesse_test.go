package necesse

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// necesse personality): install, a new world, online, the scripted join and
// leave, the save line, the stop by "stop", a second start on the same
// world, and after a takeover a stop that has no console and goes on to
// Ctrl+C.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// The registry knows Necesse once the package is imported (the lead adds
// internal/games/all/necesse.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("necesse isn't registered")
	}
	in := g.Info()
	if in.Command != "necesse" || in.Stage != game.Preview || in.Legacy {
		t.Errorf("Info: command %q, stage %q, legacy %v", in.Command, in.Stage, in.Legacy)
	}
	if in.OS["darwin/amd64"] != game.Runs || in.OS["darwin/arm64"] != game.CantRun || in.OS["windows"] != game.Runs {
		t.Errorf("Info.OS = %v: Windows and an Intel Mac only (the Mac server's Java is x86_64)", in.OS)
	}
	if len(in.Ports) != 1 || in.Ports[0].Default != 14159 || !slices.Equal(in.Ports[0].Protos, []string{"udp"}) || !in.Ports[0].Public {
		t.Errorf("Info.Ports = %+v: one public UDP port, 14159", in.Ports)
	}
}

// The scripted players come and go while the stand-in autosaves (once a
// minute on its clock while someone is on), which writes the server's own
// LATEST_BACKUP1.zip beside the worlds: the world list must not show it.
func TestStandInAutosaveAndBackupCopy(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@1,leave:Alex@5"})
	s.WaitEvent("online", "", 30*time.Second)
	s.WaitEvent("join", "Alex", 15*time.Second)
	if st := s.State(); len(st.Players) != 1 || st.Players[0].Account == "" {
		t.Errorf("after the join, players = %+v: want Alex with the account from \"Loaded player\"", st.Players)
	}
	s.WaitEvent("saved", "", 15*time.Second) // an autosave
	s.WaitEvent("leave", "Alex", 15*time.Second)
	backup := filepath.Join(worldsDir(s.Env().Dirs.Saves), "LATEST_BACKUP1.zip")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("the stand-in's first autosave left no backup copy: %v", err)
	}
	list, _, err := s.Server().Worlds().List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range list {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"Conformance"}) {
		t.Errorf("worlds = %q, want only Conformance (not the server's backup copy)", names)
	}
	s.Stop()
}

// The stand-in is one program for every game, which picks its game from the
// command line. Started as "java", it must take Necesse's command line for
// Necesse and never Minecraft's or Project Zomboid's (spec 5.2, 5.6), which
// also run Java.
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
		out, _ := cmd.CombinedOutput() // both end at once: no world, or no game
		return string(out)
	}
	// Necesse's own command line, without -world: the stand-in knows it
	// and, like the real server, asks for a world instead of starting.
	s := Game{}.New(testEnv(t)).(*server)
	args := slices.DeleteFunc(s.args(defaultSaved(), "World"), func(a string) bool { return a == "-world" || a == "World" })
	if out := run(args...); !strings.Contains(out, "Type a name for the world to create") {
		t.Errorf("the stand-in didn't take Necesse's command line for Necesse:\n%s", out)
	}
	for game, line := range map[string][]string{
		"Minecraft":        {"-Xms512M", "-Xmx2048M", "-jar", "server/server.jar", "--nogui", "--universe", dir, "--world", "W", "--port", "25565"},
		"Minecraft (bare)": {"-Xmx2048M", "-jar", "server.jar", "--nogui"},
		"Project Zomboid":  {"-Djava.awt.headless=true", "-Xmx3072m", "-cp", "java/;java/projectzomboid.jar", "zombie.network.GameServer", "-servername", "W"},
	} {
		// "necesse" in any case: a line two stand-ins took would name both.
		if out := run(line...); strings.Contains(strings.ToLower(out), "necesse") || strings.Contains(out, "dedicated server on version") {
			t.Errorf("%s's command line started the Necesse stand-in:\n%s", game, out)
		}
	}
}

// The stand-in crashes the way the real server does (fakeserver.json's
// crashAfter): its last lines must leave the Monitor saying nobody asked
// for the stop, so the manager reports a crash and can start it again.
func TestStandInCrash(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "fakeserver.json"), []byte(`{"speed":60,"crashAfter":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved()
	sv.Ports["game"] = freeUDPPort(t)
	cmd := exec.Command(java, s.args(sv, "World")...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput() // it ends by itself: the crash
	m := s.Watch(game.Record{}, nil)
	var evs []game.Event
	var shown []string
	for _, l := range strings.Split(string(out), "\n") {
		line := m.Line(l, time.Now())
		evs = append(evs, line.Events...)
		if !line.Hide {
			shown = append(shown, line.Text)
		}
	}
	// "online" comes first unless the computer is too slow to make the
	// world within crashAfter; either way, no stop was asked for.
	if count(evs, "stopping") != 0 || m.State().Stopping || m.(*monitor).stopCode != "SERVER_ERROR" {
		t.Errorf("events %q, Stopping %v, stop code %q: want a crash nobody asked for\n%s",
			kinds(evs), m.State().Stopping, m.(*monitor).stopCode, out)
	}
	if hint := s.ExitHint(shown); !strings.Contains(hint, "crashed") {
		t.Errorf("ExitHint = %q", hint)
	}
}

// freeUDPPort is a UDP port nothing listens on just now.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func testEnv(t *testing.T) game.Env {
	root := filepath.Join(t.TempDir(), "games", ID)
	return game.Env{Instance: ID, Dirs: game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"),
	}}
}

// defaultSaved is what Add a game saves: the schema's defaults and the
// default port.
func defaultSaved() game.Saved {
	sv := game.Saved{Server: game.Values{}, Ports: map[string]int{"game": 14159}, World: "World"}
	for _, f := range (&server{}).Schema() {
		if f.Scope == game.ServerScope && f.Default != "" {
			sv.Server[f.Key] = f.Default
		}
	}
	return sv
}

func TestValidate(t *testing.T) {
	s := Game{}.New(testEnv(t))
	ok := defaultSaved()
	if errs := s.Validate(ok, "World"); len(errs) > 0 {
		t.Fatalf("the defaults: %v", errs)
	}
	for _, c := range []struct {
		key, value, world, want string
	}{
		{"password", "-abc", "World", "password"},
		{"password", " pw", "World", "password"},
		{"password", strings.Repeat("p", 65), "World", "password"},
		{"serverName", "-hello", "World", "serverName"},
		{"serverName", "", "World", "serverName"},
		{"serverName", "two\nlines", "World", "serverName"},
		{"maxPlayers", "0", "World", "maxPlayers"},
		{"maxPlayers", "251", "World", "maxPlayers"},
		{"memoryMB", "512", "World", "memoryMB"},
		{"serverName", "Fine", "-world", "world"},
		{"serverName", "Fine", "LATEST_BACKUP1", "world"},
		{"serverName", "Fine", "latest_backup", "world"},
		{"serverName", "Fine", "a/b", "world"},
	} {
		sv := ok.Clone()
		sv.Server[c.key] = c.value
		errs := s.Validate(sv, c.world)
		if errs[c.want] == "" {
			t.Errorf("%s=%q world %q: no error for %s (got %v)", c.key, c.value, c.world, c.want, errs)
		}
	}
	// Spaces and quotes inside are fine: the real server took
	// "My World's #1" as a world name.
	if errs := s.Validate(ok, "My World's #1"); len(errs) > 0 {
		t.Errorf("a world name with spaces and quotes: %v", errs)
	}
}

func TestProgramPerSystem(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": filepath.Join("srv", "jre", "bin", "java.exe"),
		"darwin":  filepath.Join("srv", "jre", "Contents", "Home", "bin", "java"),
		"linux":   filepath.Join("srv", "jre", "bin", "java"),
	} {
		if got := programFor("srv", goos); got != want {
			t.Errorf("programFor(%s) = %s, want %s", goos, got, want)
		}
	}
}

func TestStopChain(t *testing.T) {
	steps := (&server{}).Stop(game.Record{})
	if len(steps) != 2 || steps[0].Kind != "console" || steps[0].Line != "stop" || steps[1].Kind != "interrupt" {
		t.Fatalf("stop chain = %+v: want console \"stop\", then Ctrl+C", steps)
	}
	if !strings.Contains(steps[1].Say, "autosave") {
		t.Errorf("the Ctrl+C step must say what is lost: %q", steps[1].Say)
	}
}

func TestJoin(t *testing.T) {
	s := Game{}.New(testEnv(t))
	sv := defaultSaved()
	sv.Server["password"] = "secret"
	rec := game.Record{Port: 14160, Password: "secret"}
	st := game.RunState{Ready: true, Version: "1.3.3"}
	j := s.Join(game.JoinInput{Rec: &rec, State: &st, Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"game": "failed"}})
	if j.How != "address" || j.Address != "203.0.113.7:14160" || j.Port != 14160 || !j.Router || j.Password != "secret" {
		t.Errorf("Join = %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:14160"}) {
		t.Errorf("Join.LAN = %q", j.LAN)
	}
	steps := strings.Join(j.Steps, " | ")
	for _, want := range []string{"Join a server", "Direct join", "203.0.113.7", "14160", "password", "Necesse 1.3.3"} {
		if !strings.Contains(steps, want) {
			t.Errorf("Join.Steps %q lack %q", steps, want)
		}
	}
	if !strings.Contains(j.Problem, "UDP port 14160") {
		t.Errorf("Join.Problem = %q", j.Problem)
	}
	// Stopped, with no public address known yet.
	j = s.Join(game.JoinInput{Saved: defaultSaved()})
	if j.Address != "" || len(j.Steps) < 2 || j.Port != 14159 {
		t.Errorf("stopped Join = %+v", j)
	}
}

// The world list as the real server lays it out (FACTS.md): one zip per
// world in saves/worlds, an empty zip for a world never saved, and the
// server's own LATEST_BACKUP1.zip, which isn't a world.
func TestWorlds(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env)
	dir := worldsDir(env.Dirs.Saves)
	os.MkdirAll(dir, 0o755)
	played, err := os.ReadFile(filepath.Join("testdata", "world", "RealCheck.zip"))
	if err != nil {
		t.Fatal(err)
	}
	empty := []byte("PK\x05\x06" + strings.Repeat("\x00", 18)) // 22 bytes: what the server writes for a new world
	for name, data := range map[string][]byte{"RealCheck.zip": played, "Fresh.zip": empty, "LATEST_BACKUP1.zip": played, "notes.txt": []byte("x")} {
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	wl := s.Worlds()
	list, _, err := wl.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range list {
		got[w.Name] = w.NeverSaved
	}
	if len(got) != 2 || got["RealCheck"] || !got["Fresh"] {
		t.Errorf("worlds (name: never saved) = %v, want RealCheck (saved) and Fresh (never saved)", got)
	}
	if !wl.Exists("RealCheck") || wl.Exists("LATEST_BACKUP1") {
		t.Errorf("Exists: RealCheck %v, LATEST_BACKUP1 %v", wl.Exists("RealCheck"), wl.Exists("LATEST_BACKUP1"))
	}
	b, err := wl.Backup("RealCheck", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := wl.Restore("RealCheck", b.ID); err != nil {
		t.Errorf("Restore: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "RealCheck.zip")); !slices.Equal(after, played) {
		t.Errorf("the restored world differs from the backed-up one")
	}
	if err := wl.Rename("RealCheck", "Other"); err != game.ErrCantRename {
		t.Errorf("Rename = %v, want game.ErrCantRename", err)
	}
	if !strings.Contains(wl.NameProblem("LATEST_BACKUP2"), "backups") || wl.NameProblem("Good name") != "" {
		t.Errorf("NameProblem: %q, %q", wl.NameProblem("LATEST_BACKUP2"), wl.NameProblem("Good name"))
	}
}

func TestLocalWorlds(t *testing.T) {
	home := filepath.Join("h", "me")
	got := localWorlds(home)
	if len(got) != 1 {
		t.Fatalf("localWorlds = %q", got)
	}
	switch runtime.GOOS {
	case "darwin":
		if want := filepath.Join(home, "Library", "Application Support", "Necesse", "saves", "worlds"); got[0] != want {
			t.Errorf("localWorlds = %q, want %q", got[0], want)
		}
	case "windows":
		if !strings.HasSuffix(got[0], filepath.Join("Necesse", "saves", "worlds")) || !strings.Contains(strings.ToLower(got[0]), "appdata") {
			t.Errorf("localWorlds = %q, want %%APPDATA%%\\Necesse\\saves\\worlds", got[0])
		}
	default:
		if want := filepath.Join(home, ".config", "Necesse", "saves", "worlds"); got[0] != want {
			t.Errorf("localWorlds = %q, want %q", got[0], want)
		}
	}
}

// installed makes the files the Plan checks for, as an install would.
func installed(t *testing.T, s *server) {
	t.Helper()
	for _, p := range []string{s.Program(), filepath.Join(s.env.Dirs.Server, jarName)} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCommandLineGolden pins the command line the Plan gives Java, for a
// new world with the defaults and for one with a password and more memory
// (testdata/golden/commandline.txt; go test -update rewrites it).
func TestCommandLineGolden(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	installed(t, s)
	var b strings.Builder
	b.WriteString("# The command line the Plan gives the server's Java (<saves> is the instance's saves folder).\n")
	for _, c := range []struct {
		title  string
		change func(*game.Saved)
	}{
		{"A new world with the defaults", func(*game.Saved) {}},
		{"With a password, a name with spaces, 4 players and 4 GB", func(sv *game.Saved) {
			sv.Server["password"] = "two words"
			sv.Server["serverName"] = "Friday night"
			sv.Server["maxPlayers"] = "4"
			sv.Server["memoryMB"] = "4096"
			sv.Ports["game"] = 14160
		}},
	} {
		sv := defaultSaved()
		c.change(&sv)
		plan, err := s.Plan(t.Context(), game.PlanRequest{World: "My World", New: true, Saved: sv, LogPath: filepath.Join(env.Dirs.Logs, "necesse-x.log")})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Exe != s.Program() || plan.Dir != env.Dirs.Server || plan.Output != game.Capture || !plan.Console || plan.SaveLine != "save" {
			t.Errorf("%s: plan = %+v", c.title, plan)
		}
		if plan.Record.Port != sv.Ports["game"] || plan.Record.Password != sv.Server["password"] || plan.Record.Server != sv.Server["serverName"] {
			t.Errorf("%s: record = %+v", c.title, plan.Record)
		}
		if plan.Fingerprint != s.Fingerprint(sv, "My World") {
			t.Errorf("%s: the plan's fingerprint isn't Fingerprint()'s", c.title)
		}
		fmt.Fprintf(&b, "\n## %s\n", c.title)
		for _, a := range plan.Args {
			b.WriteString(strings.ReplaceAll(a, env.Dirs.Saves, "<saves>") + "\n")
		}
	}
	golden(t, filepath.Join("testdata", "golden", "commandline.txt"), b.String())
}

// A start of a world that is gone is refused with a reason, rather than
// letting the server quietly make an empty world under its name.
func TestPlanMissingWorld(t *testing.T) {
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	installed(t, s)
	_, err := s.Plan(t.Context(), game.PlanRequest{World: "Gone", Saved: defaultSaved()})
	if err == nil || !strings.Contains(err.Error(), "backups") {
		t.Errorf("Plan of a missing world: %v", err)
	}
	// And with no server installed, it says so.
	os.Remove(filepath.Join(env.Dirs.Server, jarName))
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "New", New: true, Saved: defaultSaved()}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("Plan without Server.jar: %v", err)
	}
}

// On Windows, Java gets its command line in the computer's old character
// set (charset.go), so values with characters it lacks are refused. Here
// argRuneFits acts as an English Windows, whose Windows-1252 has the
// accented letters of Western Europe but no "ł", "✓" or emoji.
func TestCodePageRule(t *testing.T) {
	orig := argRuneFits
	t.Cleanup(func() { argRuneFits = orig })

	// What every system says: on a Mac every character gets through.
	if runtime.GOOS != "windows" {
		s := Game{}.New(testEnv(t))
		sv := defaultSaved()
		sv.Server["serverName"], sv.Server["password"] = "Our base 🏡", "łódź✓"
		if errs := s.Validate(sv, "Our base 🏡"); len(errs) > 0 {
			t.Errorf("off Windows, Java gets every character, but Validate says %v", errs)
		}
	}

	argRuneFits = func(r rune) bool { return r <= 0xFF }
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	ok := defaultSaved()
	ok.Server["serverName"], ok.Server["password"] = "Café Noël", "fête"
	if errs := s.Validate(ok, "Été"); len(errs) > 0 {
		t.Errorf("letters the code page has: %v", errs)
	}
	for _, c := range []struct {
		key, value, world, want, says string
	}{
		{"serverName", "Our base 🏡", "World", "serverName", `"🏡"`},
		{"password", "pass✓", "World", "password", `"✓"`},
		{"password", "łódź", "World", "password", `"ł" and "ź"`},
		{"serverName", "Fine", "Our base 🏡", "world", `"🏡"`},
	} {
		sv := ok.Clone()
		sv.Server[c.key] = c.value
		got := s.Validate(sv, c.world)[c.want]
		if !strings.Contains(got, c.says) || !strings.Contains(got, "character set") {
			t.Errorf("%s=%q world %q: %s error %q, want one naming %s", c.key, c.value, c.world, c.want, got, c.says)
		}
		if strings.Contains(got, `"ó"`) {
			t.Errorf("%q names a letter the code page has", got)
		}
	}
	if p := s.Worlds().NameProblem("Base 🏡"); !strings.Contains(p, `"🏡"`) {
		t.Errorf("NameProblem = %q", p)
	}

	// The data folder is on the command line too.
	installed(t, s)
	wenv := env
	wenv.Dirs.Saves = filepath.Join(env.Dirs.Root, "Michał", "saves")
	_, err := Game{}.New(wenv).Plan(t.Context(), game.PlanRequest{World: "New", New: true, Saved: defaultSaved()})
	if err == nil || !strings.Contains(err.Error(), `"ł"`) {
		t.Errorf("Plan with a data folder the code page can't name: %v", err)
	}
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "New", New: true, Saved: defaultSaved()}); err != nil {
		t.Errorf("Plan with a plain data folder: %v", err)
	}
}

func TestQuoteRunes(t *testing.T) {
	for in, want := range map[string]string{
		"ł":       `"ł"`,
		"łź":      `"ł" and "ź"`,
		"łźń":     `"ł", "ź" and "ń"`,
		"abcdefg": `"a", "b", "c", "d", "e" and others`,
	} {
		if got := quoteRunes([]rune(in)); got != want {
			t.Errorf("quoteRunes(%q) = %s, want %s", in, got, want)
		}
	}
}
