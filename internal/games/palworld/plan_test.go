package palworld

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// testServer is an instance in a fresh folder with the real server's files
// the plan needs: the program (an empty file) and the shipped default
// settings. Ids and the admin password are fixed, for the goldens.
func testServer(t *testing.T) *server {
	t.Helper()
	root := t.TempDir()
	dirs := game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Staging: filepath.Join(root, "uploads"),
		Runtime: filepath.Join(root, "runtime"), SteamCMD: filepath.Join(root, "steamcmd")}
	// Fake mode gives the Windows layout on every system (the app offers
	// Palworld only on Windows), so the goldens are the same everywhere;
	// the plan itself is the real one.
	s := Game{}.New(game.Env{Instance: ID, Data: root, Dirs: dirs, Log: log.New(io.Discard, "", 0), Fake: &game.Fake{}}).(*server)
	for _, p := range []string{s.Program(), s.defaultSettingsPath()} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(s.Program(), nil, 0o755)
	os.WriteFile(s.defaultSettingsPath(), defaultSettings, 0o644)

	ids := 0
	oldID, oldSecret := newWorldID, newSecret
	newWorldID = func() string { ids++; return fmt.Sprintf("%032X", 0xA0+ids) }
	newSecret = func() string { return "AdminSecretForTheTest" }
	t.Cleanup(func() { newWorldID, newSecret = oldID, oldSecret })
	return s
}

// defaultSaved is what Add a game saves: each field's default, a world, the
// default ports.
func defaultSaved(s *server, world string) game.Saved {
	sv := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{world: {}}, World: world,
		Ports: map[string]int{"game": 8211, "rest": 8212}, State: game.Values{}}
	for _, f := range s.Schema() {
		if f.Default == "" {
			continue
		}
		switch f.Scope {
		case game.ServerScope:
			sv.Server[f.Key] = f.Default
		case game.WorldScope:
			sv.Worlds[world][f.Key] = f.Default
		}
	}
	return sv
}

// golden compares got with testdata/golden/name (written with -update).
// Line ends don't count: git may give the file Windows line ends on a
// Windows checkout. The files' own line ends are checked elsewhere.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(string(b)) != norm(got) {
		t.Errorf("%s differs from the golden file (run with -update if the change is wanted):\n--- got\n%s\n--- want\n%s", name, got, b)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The first start of a new install: PalWorldSettings.ini is the shipped
// default with the app's values set, GameUserSettings.ini names the new
// world's id, and the command line is the real server's own.
func TestPlanNewInstallGolden(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	sv.Server["serverName"] = "Our Palworld, (friends)"
	sv.Server["password"] = "hunter 2"
	sv.Server["maxPlayers"] = "8"
	sv.Worlds["Island"]["expRate"] = "2"
	sv.Worlds["Island"]["deathPenalty"] = "None"
	sv.Ports = map[string]int{"game": 8215, "rest": 8216}
	s.store.Save(func(x *game.Saved) error { *x = sv; return nil })

	plan, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv, LogPath: "/logs/palworld-x.log"})
	if err != nil {
		t.Fatal(err)
	}
	settings := read(t, s.settingsPath())
	if !strings.Contains(settings, "\r\n") {
		t.Error("PalWorldSettings.ini lost the shipped file's Windows line ends")
	}
	golden(t, "PalWorldSettings-new.ini", settings)
	golden(t, "GameUserSettings-new.ini", read(t, s.userSettingsPath()))
	golden(t, "command-line", planText(t, s, plan))

	if st := s.store.Get().State; st[statePlanned] != "000000000000000000000000000000A1" || st["world.000000000000000000000000000000A1"] != "Island" {
		t.Errorf("State = %v", st)
	}
	if plan.Record.Secrets[restSecret] != "AdminSecretForTheTest" || plan.Record.Ports["rest"] != 8216 || plan.Record.Port != 8215 || plan.Record.Password != "hunter 2" {
		t.Errorf("Record = %+v", plan.Record)
	}
	if fp := s.Fingerprint(sv, "Island"); fp != plan.Fingerprint {
		t.Errorf("Fingerprint = %s, plan's %s", fp, plan.Fingerprint)
	}
}

// An owner's own PalWorldSettings.ini: the app's values replace theirs,
// every other option and line is kept as it was (their quoted text with a
// comma and brackets in it among them), and the file keeps its own line
// ends.
func TestPlanKeepsOwnerSettingsGolden(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	s.store.Save(func(x *game.Saved) error { *x = sv; return nil })
	os.MkdirAll(s.configDir(), 0o755)
	os.WriteFile(s.settingsPath(), []byte(read(t, "testdata/ini/PalWorldSettings-owner-edited.ini")), 0o644)
	os.WriteFile(s.userSettingsPath(), []byte(read(t, "testdata/ini/GameUserSettings-dedicated.ini")), 0o644)

	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	got := read(t, s.settingsPath())
	golden(t, "PalWorldSettings-owner-edited.ini", got)
	for _, keep := range []string{`bIsPvP=True`, `ServerDescription="Friends only, (no griefing)"`, `AutoSaveSpan=60.000000`, `CrossplayPlatforms=(Steam,Xbox,PS5,Mac)`, "; Written for the tests"} {
		if !strings.Contains(got, keep) {
			t.Errorf("the owner's %s was lost", keep)
		}
	}
	for _, gone := range []string{`"Old name"`, `"set by hand"`, `RESTAPIEnabled=False`, `ExpRate=3.000000`} {
		if strings.Contains(got, gone) {
			t.Errorf("%s is still there", gone)
		}
	}
	golden(t, "GameUserSettings-existing.ini", read(t, s.userSettingsPath()))
}

// A PalWorldSettings.ini with no options line (an empty file) is filled in
// from the shipped default, keeping what else it holds.
func TestPlanFillsEmptySettings(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	os.MkdirAll(s.configDir(), 0o755)
	os.WriteFile(s.settingsPath(), []byte("[/Script/Pal.PalGameWorldSettings]\r\n"), 0o644)
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	f := parseINI(read(t, s.settingsPath()))
	opts, ok := f.optionsLine()
	if !ok {
		t.Fatal("no options line written")
	}
	for k, want := range map[string]string{"RESTAPIEnabled": "True", "bIsUseBackupSaveData": "True", "ServerName": `"Palworld server"`, "AdminPassword": `"AdminSecretForTheTest"`} {
		if v, _ := opts.get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if n := strings.Count(f.String(), "[/Script/Pal.PalGameWorldSettings]"); n != 1 {
		t.Errorf("the section is there %d times", n)
	}

	// With the installed default gone too, the copy built into the app is used.
	os.Remove(s.defaultSettingsPath())
	os.Remove(s.settingsPath())
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	if opts, _ := parseINI(read(t, s.settingsPath())).optionsLine(); len(opts) < 100 {
		t.Errorf("only %d options from the built-in default", len(opts))
	}
}

// Plans refuse what can't start: no server yet, a world that's gone, bad
// settings.
func TestPlanRefuses(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", Saved: sv}); err == nil || !strings.Contains(err.Error(), "isn't in Palworld's saves folder") {
		t.Errorf("an old world that isn't there: %v", err)
	}
	bad := sv.Clone()
	bad.Server["serverName"] = `Say "hi"`
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: bad}); err == nil {
		t.Error("a server name with a quote was taken")
	}
	os.Remove(s.Program())
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", New: true, Saved: sv}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("no server: %v", err)
	}
}

// planText is the plan as the golden file shows it, with paths relative
// to the instance's server folder.
func planText(t *testing.T, s *server, p game.Plan) string {
	t.Helper()
	rel := func(path string) string {
		r, err := filepath.Rel(s.env.Dirs.Server, path)
		if err != nil {
			t.Fatal(err)
		}
		return "server/" + filepath.ToSlash(r)
	}
	exe := strings.TrimSuffix(rel(p.Exe), ".exe")
	var b strings.Builder
	fmt.Fprintf(&b, "exe: %s(.exe on Windows)\n", exe)
	for _, a := range p.Args {
		fmt.Fprintf(&b, "arg: %s\n", a)
	}
	fmt.Fprintf(&b, "dir: %s\n", rel(p.Dir))
	fmt.Fprintf(&b, "output: capture (%v), console: %v\n", p.Output == game.Capture, p.Console)
	fmt.Fprintf(&b, "ready wait: %v\n", p.ReadyWait)
	fmt.Fprintf(&b, "record: server %q, port %d, password %q, ports %v, secrets %d (rest)\n", p.Record.Server, p.Record.Port, p.Record.Password, p.Record.Ports, len(p.Record.Secrets))
	return b.String()
}
