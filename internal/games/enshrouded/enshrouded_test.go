package enshrouded

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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

// withPassword is what Add a game saves: Enshrouded asks for a password
// (Info.Password.Use is "required"), so a server without one never starts.
func withPassword(sv *game.Saved) {
	sv.Server["password"] = "ember-wood"
}

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// enshrouded personality): install, a new world, online, the scripted join
// and leave by name with the query port's count, Join, the stop by Ctrl+C,
// a second start on the same world, a takeover whose stop still works, and
// a backup that restores.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{Change: withPassword})
}

// The registry knows Enshrouded once the package is imported (the lead
// adds internal/games/all/enshrouded.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("enshrouded isn't registered")
	}
	in := g.Info()
	if in.Command != "enshrouded" || in.Stage != game.Preview || in.Legacy || in.Order != 60 || in.Letter != "E" || in.Color != "#16a085" {
		t.Errorf("Info: command %q, stage %q, legacy %v, order %d, letter %q, color %q", in.Command, in.Stage, in.Legacy, in.Order, in.Letter, in.Color)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	want := []game.PortRole{{Role: "query", Label: "Port", Protos: []string{"udp"}, Default: 15637, Public: true}}
	if len(in.Ports) != 1 || in.Ports[0].Role != want[0].Role || !slices.Equal(in.Ports[0].Protos, want[0].Protos) ||
		in.Ports[0].Default != want[0].Default || !in.Ports[0].Public {
		t.Errorf("Info.Ports = %+v, want %+v", in.Ports, want)
	}
	if in.Password.Use != "required" || in.Presence != game.ByName || in.Deaths || in.Import || in.Lists || in.Mods || in.MaxPlayers != 16 {
		t.Errorf("Info: password %+v, presence %q, deaths %v, import %v, lists %v, mods %v, max players %d",
			in.Password, in.Presence, in.Deaths, in.Import, in.Lists, in.Mods, in.MaxPlayers)
	}
	if in.MemoryMB != 6144 || in.DownloadMB < 8000 || in.DiskMB < 13000 {
		t.Errorf("Info: memory %d MB, download %d MB, disk %d MB", in.MemoryMB, in.DownloadMB, in.DiskMB)
	}
	var srv game.Server = Game{}.New(testEnv(t))
	if _, ok := srv.(game.Prober); !ok {
		t.Error("the adapter has no Probe: the query port's count is how the app knows players are on")
	}
	if _, ok := srv.(game.Recoverer); !ok {
		t.Error("the adapter has no Recover: a server found running must be followed in its own log")
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

// defaultSaved is what Add a game saves: the schema's defaults, a password
// and the port.
func defaultSaved() game.Saved {
	sv := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{}, World: "Main", State: game.Values{},
		Ports: map[string]int{"query": 15637}}
	for _, f := range (&server{}).Schema() {
		if f.Scope == game.ServerScope && f.Default != "" {
			sv.Server[f.Key] = f.Default
		}
	}
	withPassword(&sv)
	return sv
}

// installed makes an adapter whose server program is there (an empty
// file is enough for Plan), outside fake mode.
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

// golden compares got with a file in testdata, or rewrites the file with
// -update. Line endings don't count: a Windows checkout may turn them into
// CRLF.
func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from what the code makes now (go test -update rewrites it):\n--- got\n%s\n--- want\n%s", path, got, w)
	}
}

// The settings file the Plan writes, pinned in testdata/golden: on a new
// install (no file yet), and over the 1.0 server's own default file (from
// the readme Steam installs beside it), whose keys the app doesn't own
// (tags, voice chat mode, the difficulty values, the bans) must survive.
func TestConfigGolden(t *testing.T) {
	c := configValues{name: "Tom & Ana's \"Embervale\"", saveDir: "<saves>/Crow's Rest", logDir: "<logs>/game",
		port: 15637, slots: 8, textChat: true, voiceChat: false, preset: "Hard", password: "ember-wood"}

	fresh, err := renderConfig(nil, c.set())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, filepath.Join("testdata", "golden", "enshrouded_server-new.json"), string(fresh))

	old, err := readConfig(filepath.Join("testdata", "real", "enshrouded_server-1.0.0.0-readme-default.json"))
	if err != nil {
		t.Fatal(err)
	}
	over, err := renderConfig(old, c.set())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, filepath.Join("testdata", "golden", "enshrouded_server-over-default.json"), string(over))

	for _, b := range [][]byte{fresh, over} {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("the file isn't JSON: %v\n%s", err, b)
		}
		var groups []userGroup
		json.Unmarshal(doc["userGroups"], &groups)
		if len(groups) != 1 || groups[0] != friendsGroup("ember-wood") {
			t.Errorf("userGroups = %+v, want only the Friends group", groups)
		}
		g := groups[0]
		if g.CanKickBan || !g.CanAccessInventories || !g.CanEditWorld || !g.CanEditBase || !g.CanExtendBase || g.ReservedSlots != 0 {
			t.Errorf("the Friends group's rights are %+v", g)
		}
		if _, ok := doc["password"]; ok {
			t.Error("the old top-level password is still there: a non-empty one would make a second group")
		}
		var name, ip string
		var port, slots int
		json.Unmarshal(doc["name"], &name)
		json.Unmarshal(doc["ip"], &ip)
		json.Unmarshal(doc["queryPort"], &port)
		json.Unmarshal(doc["slotCount"], &slots)
		if name != c.name || ip != "0.0.0.0" || port != 15637 || slots != 8 {
			t.Errorf("name %q, ip %q, queryPort %d, slotCount %d", name, ip, port, slots)
		}
		if bytes.Contains(b, []byte(`\u0026`)) {
			t.Error("the file escapes & as \\u0026, which reads badly in Notepad")
		}
	}
	var doc map[string]json.RawMessage
	json.Unmarshal(over, &doc)
	for _, k := range []string{"tags", "voiceChatMode", "gameSettings", "bannedAccounts"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("the server's own key %q was dropped", k)
		}
	}
	if !bytes.Contains(over, []byte(`"flameLevelFogRemoval": 11`)) {
		t.Error("the difficulty values in gameSettings weren't kept as they were")
	}
}

// The command line for a new world and a later start, pinned in
// testdata/golden/commandline.txt (go test -update rewrites it). Paths are
// shown relative to the instance's folder, so the file is the same on
// every system and computer.
func TestCommandLineGolden(t *testing.T) {
	s := installed(t)
	sv := defaultSaved()
	root := s.env.Dirs.Root
	rel := func(v string) string {
		if r, err := filepath.Rel(root, v); err == nil && filepath.IsAbs(v) {
			return "<instance>/" + filepath.ToSlash(r)
		}
		return v
	}
	show := func(p game.Plan) string {
		var b strings.Builder
		b.WriteString("exe: " + rel(p.Exe) + "\n")
		b.WriteString("dir: " + rel(p.Dir) + "\n")
		list := func(l []string) string {
			if len(l) == 0 {
				return "(none)"
			}
			return strings.Join(l, " ")
		}
		b.WriteString("args: " + list(p.Args) + "\n")
		b.WriteString("env: " + list(p.Env) + "\n")
		b.WriteString("output: " + map[game.Output]string{game.Capture: "Capture", game.LogArg: "LogArg", game.GameLog: "GameLog"}[p.Output] + "\n")
		b.WriteString("log: " + rel(p.LogPath) + "\n")
		b.WriteString("console: " + map[bool]string{true: "yes", false: "no"}[p.Console] + "\n")
		b.WriteString("readyWait: " + p.ReadyWait.String() + "\n")
		fmt.Fprintf(&b, "record: server %s, port %d, ports %v, password set %v\n", p.Record.Server, p.Record.Port, p.Record.Ports, p.Record.Password != "")
		return b.String()
	}
	var out strings.Builder
	out.WriteString("# How the app starts the Enshrouded server (spec 5.7): its program, with no\n")
	out.WriteString("# arguments, from its own folder; everything else is in enshrouded_server.json.\n")
	out.WriteString("# Paths are relative to the instance's folder, games/enshrouded.\n\n")

	logPath := filepath.Join(s.env.Dirs.Logs, "enshrouded-2026-11-26_21-35-10.log")
	p, err := s.Plan(t.Context(), game.PlanRequest{World: "Crow's Rest", New: true, Saved: sv, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("## A new world\n" + show(p) + "\n")

	later := sv.Clone()
	later.Server["serverName"] = "Friends' Embervale"
	later.Ports["query"] = 15700
	p, err = s.Plan(t.Context(), game.PlanRequest{World: "Crow's Rest", Saved: later, LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("## The same world later, renamed, on port 15700\n" + show(p))
	golden(t, filepath.Join("testdata", "golden", "commandline.txt"), out.String())
	if want := filepath.Join(s.env.Dirs.Server, "enshrouded_server.exe"); p.Exe != want {
		t.Errorf("Exe = %s, want %s (never a launcher)", p.Exe, want)
	}
}

// The Plan writes the world's settings into the server's file, with the
// real folders, keeps what the server wrote there itself (bans, its own
// keys), and the file is private to the owner.
func TestPlanWritesConfig(t *testing.T) {
	s := installed(t)
	path := filepath.Join(s.env.Dirs.Server, configName)
	// What the server left last time: its own keys and a ban.
	own := `{"name": "old", "password": "legacy", "queryPort": 1, "voiceChatMode": "Global",
		"bannedAccounts": [{"accountIDHash": "ab12", "displayName": "Grief", "characterName": "G", "banDate": 1790000000}],
		"useAlternativeP2P": false}`
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	sv := defaultSaved()
	sv.Server["serverName"] = "Embervale"
	sv.Server["maxPlayers"] = "6"
	sv.Server["voiceChat"] = "true"
	sv.Worlds["Crow's Rest"] = game.Values{"difficulty": "survival"}
	sv.Ports["query"] = 15700
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Crow's Rest", New: true, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name           string            `json:"name"`
		Password       *string           `json:"password"`
		SaveDirectory  string            `json:"saveDirectory"`
		LogDirectory   string            `json:"logDirectory"`
		QueryPort      int               `json:"queryPort"`
		SlotCount      int               `json:"slotCount"`
		VoiceChatMode  string            `json:"voiceChatMode"`
		EnableVoice    bool              `json:"enableVoiceChat"`
		EnableText     bool              `json:"enableTextChat"`
		Preset         string            `json:"gameSettingsPreset"`
		UserGroups     []userGroup       `json:"userGroups"`
		BannedAccounts []json.RawMessage `json:"bannedAccounts"`
		AltP2P         *bool             `json:"useAlternativeP2P"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	world := filepath.Join(s.env.Dirs.Saves, "Crow's Rest")
	if doc.Name != "Embervale" || doc.SaveDirectory != world || doc.LogDirectory != s.gameLogDir() || doc.QueryPort != 15700 ||
		doc.SlotCount != 6 || !doc.EnableVoice || !doc.EnableText || doc.Preset != "Survival" {
		t.Errorf("the file has %+v", doc)
	}
	if doc.Password != nil {
		t.Errorf("the old top-level password %q is still there", *doc.Password)
	}
	if len(doc.UserGroups) != 1 || doc.UserGroups[0].Password != "ember-wood" || doc.UserGroups[0].Name != "Friends" {
		t.Errorf("userGroups = %+v", doc.UserGroups)
	}
	if doc.VoiceChatMode != "Global" || len(doc.BannedAccounts) != 1 || doc.AltP2P == nil {
		t.Errorf("the server's own keys weren't kept: voiceChatMode %q, bans %d, useAlternativeP2P %v", doc.VoiceChatMode, len(doc.BannedAccounts), doc.AltP2P)
	}
	if st, err := os.Stat(world); err != nil || !st.IsDir() {
		t.Errorf("the world's folder (the saveDirectory) wasn't made: %v", err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("the settings file holds the password but is %v, not 0600", st.Mode().Perm())
		}
	}
	// No temporary files left beside it.
	entries, _ := os.ReadDir(s.env.Dirs.Server)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left: %s", e.Name())
		}
	}
}

// A settings file that isn't JSON (hand-edited badly) is kept beside the
// new one, so bans in it aren't lost, and the start goes on.
func TestPlanKeepsUnreadableConfig(t *testing.T) {
	s := installed(t)
	path := filepath.Join(s.env.Dirs.Server, configName)
	os.WriteFile(path, []byte(`{"name": "oops",`), 0o644)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved()}); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(path + ".unreadable-*")
	if len(kept) != 1 {
		t.Fatalf("the unreadable file wasn't kept aside: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != `{"name": "oops",` {
		t.Errorf("the kept file has %q", b)
	}
	if _, err := readConfig(path); err != nil {
		t.Errorf("the new file can't be read: %v", err)
	}
}

// Before a start the last run's log goes into backup/, so the app, which
// follows the log from its first line, never takes the old run's lines for
// this one's; the backup folder keeps only the newest few.
func TestPlanSetsAsideOldLog(t *testing.T) {
	s := installed(t)
	os.MkdirAll(s.gameLogDir(), 0o755)
	os.WriteFile(s.gameLog(), []byte("[I 00:00:01,284] [Session] 'HostOnline' (up)!\n"), 0o644)
	backup := filepath.Join(s.gameLogDir(), "backup")
	os.MkdirAll(backup, 0o755)
	for i := range keepGameLogs + 3 {
		p := filepath.Join(backup, fmt.Sprintf("enshrouded_server_2026-01-01_00-00-%02d.log", i))
		os.WriteFile(p, []byte("old"), 0o644)
		at := time.Now().Add(-time.Duration(100+i) * time.Hour)
		os.Chtimes(p, at, at)
	}
	os.WriteFile(filepath.Join(backup, "notes.txt"), []byte("mine"), 0o644)

	p, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved()})
	if err != nil {
		t.Fatal(err)
	}
	if p.LogPath != s.gameLog() || p.Output != game.GameLog {
		t.Errorf("the plan follows %s (output %d), not the server's own log", p.LogPath, p.Output)
	}
	if _, err := os.Stat(s.gameLog()); !os.IsNotExist(err) {
		t.Errorf("the last run's log is still where the server writes this run's: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(backup, "enshrouded_server_*.log"))
	if len(logs) != keepGameLogs {
		t.Errorf("backup/ keeps %d logs, want %d", len(logs), keepGameLogs)
	}
	found := false
	for _, l := range logs {
		if b, _ := os.ReadFile(l); strings.Contains(string(b), "HostOnline") {
			found = true
		}
	}
	if !found {
		t.Error("the last run's log isn't among the backups (the newest must be kept)")
	}
	if _, err := os.Stat(filepath.Join(backup, "notes.txt")); err != nil {
		t.Error("pruning removed a file that isn't one of the server's logs")
	}
}

// A start refuses what the server couldn't use, before anything is written.
func TestPlanRefuses(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: defaultSaved()}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("not installed: %v", err)
	}
	s = installed(t)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Gone", Saved: defaultSaved()}); err == nil || !strings.Contains(err.Error(), "isn't in Enshrouded's saves folder") {
		t.Errorf("a world that isn't there: %v", err)
	}
	bad := defaultSaved()
	bad.Server["password"] = ""
	_, err := s.Plan(t.Context(), game.PlanRequest{World: "Main", New: true, Saved: bad})
	if fe, ok := err.(game.FieldErrors); !ok || fe["password"] == "" {
		t.Errorf("no password: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.env.Dirs.Server, configName)); !os.IsNotExist(err) {
		t.Error("a refused start wrote the settings file")
	}
}

func TestValidate(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	cases := []struct {
		key, value, field string
	}{
		{"serverName", "", "serverName"},
		{"serverName", strings.Repeat("a", 61), "serverName"},
		{"serverName", "two\nlines", "serverName"},
		{"password", "", "password"},
		{"password", "abcd", "password"},
		{"password", strings.Repeat("p", 33), "password"},
		{"password", "tab\there", "password"},
		{"maxPlayers", "0", "maxPlayers"},
		{"maxPlayers", "17", "maxPlayers"},
		{"maxPlayers", "many", "maxPlayers"},
	}
	if errs := s.Validate(defaultSaved(), "Main"); len(errs) != 0 {
		t.Fatalf("the defaults with a password: %v", errs)
	}
	for _, c := range cases {
		sv := defaultSaved()
		sv.Server[c.key] = c.value
		if errs := s.Validate(sv, "Main"); errs[c.field] == "" {
			t.Errorf("%s = %q: no error for %s (%v)", c.key, c.value, c.field, errs)
		}
	}
	sv := defaultSaved()
	sv.Worlds["Main"] = game.Values{"difficulty": "nightmare"}
	if errs := s.Validate(sv, "Main"); errs["difficulty"] == "" {
		t.Error("an unknown difficulty passed")
	}
	if errs := s.Validate(defaultSaved(), "CON"); errs["world"] == "" {
		t.Error("a world name Windows can't use passed")
	}
	sv = defaultSaved()
	sv.Server["password"] = "Ünïcödé ✓"
	if errs := s.Validate(sv, "Main"); len(errs) != 0 {
		t.Errorf("a password with accents: %v", errs)
	}
}

// Fingerprint changes exactly when the server would start differently.
func TestFingerprint(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	base := defaultSaved()
	fp := s.Fingerprint(base, "Main")
	for _, change := range []func(*game.Saved){
		func(sv *game.Saved) { sv.Server["password"] = "another-one" },
		func(sv *game.Saved) { sv.Server["serverName"] = "Other" },
		func(sv *game.Saved) { sv.Server["maxPlayers"] = "4" },
		func(sv *game.Saved) { sv.Server["textChat"] = "false" },
		func(sv *game.Saved) { sv.Ports["query"] = 15800 },
		func(sv *game.Saved) { sv.Worlds["Main"] = game.Values{"difficulty": "hard"} },
	} {
		c := base.Clone()
		change(&c)
		if s.Fingerprint(c, "Main") == fp {
			t.Errorf("a change the server reads at start kept the fingerprint: %+v", c)
		}
	}
	c := base.Clone()
	c.Server["updateOnStart"] = "false"
	c.Server["autoRestart"] = "false"
	if s.Fingerprint(c, "Main") != fp {
		t.Error("update on start or auto-restart changed the fingerprint")
	}
	if s.Fingerprint(base, "Other world") == fp {
		t.Error("another world kept the fingerprint")
	}
}

// The manager passes world "" to mean the selected world: it checks the
// settings that way before every start and on every status, and compares a
// running server's fingerprint with Fingerprint(saved, ""). Read as a
// world with no name, every start would be refused and a running server
// would always need a restart.
func TestSelectedWorld(t *testing.T) {
	s := installed(t)
	sv := defaultSaved()
	sv.World = "Crow's Rest"
	sv.Worlds["Crow's Rest"] = game.Values{"difficulty": "hard"}
	if errs := s.Validate(sv, ""); len(errs) != 0 {
		t.Errorf("Validate(saved, \"\") with a world selected: %v", errs)
	}
	bad := sv.Clone()
	bad.Worlds["Crow's Rest"] = game.Values{"difficulty": "nightmare"}
	if errs := s.Validate(bad, ""); errs["difficulty"] == "" {
		t.Error("Validate(saved, \"\") didn't check the selected world's difficulty")
	}
	none := sv.Clone()
	none.World = ""
	if errs := s.Validate(none, ""); errs["world"] == "" {
		t.Error("no world selected passed")
	}

	if s.Fingerprint(sv, "") != s.Fingerprint(sv, "Crow's Rest") {
		t.Error("Fingerprint(saved, \"\") isn't the selected world's")
	}
	p, err := s.Plan(t.Context(), game.PlanRequest{World: "Crow's Rest", New: true, Saved: sv})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Fingerprint(sv, ""); got != p.Fingerprint {
		t.Errorf("Fingerprint(saved, \"\") = %s, the plan's is %s: a running server would always say restart to apply", got, p.Fingerprint)
	}
	other := sv.Clone()
	other.World = "Main"
	if s.Fingerprint(other, "") == p.Fingerprint {
		t.Error("selecting another world kept the fingerprint")
	}

	// A Plan for "" starts the selected world, in its own folder.
	p, err = s.Plan(t.Context(), game.PlanRequest{Saved: sv})
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != s.Fingerprint(sv, "Crow's Rest") {
		t.Error("a Plan for \"\" didn't start the selected world")
	}
}

func TestJoin(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved()
	sv.Server["serverName"] = "Embervale"
	j := s.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"query": "open"}})
	if j.How != "address" || j.Address != "203.0.113.7:15637" || j.Port != 15637 || j.Password != "ember-wood" || !j.Router || !j.Public {
		t.Errorf("Join = %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:15637"}) {
		t.Errorf("LAN = %v", j.LAN)
	}
	all := strings.Join(j.Steps, " ")
	for _, want := range []string{"Show dedicated servers", `"Embervale"`, "203.0.113.7:15637", "press T", "Steam"} {
		if !strings.Contains(all, want) {
			t.Errorf("the steps don't say %q: %q", want, j.Steps)
		}
	}
	if j.Hint == "" || strings.Contains(j.Hint, "ember-wood") || j.Problem != "" {
		t.Errorf("Hint %q (it must not carry the password), Problem %q", j.Hint, j.Problem)
	}

	// Running: the record's name, port and password win over settings
	// changed since (they apply after a restart).
	rec := game.Record{Server: "Old name", Port: 15700, Password: "old-pass"}
	sv.Server["password"] = "new-pass"
	j = s.Join(game.JoinInput{Rec: &rec, State: &game.RunState{Ready: true}, Saved: sv, PublicIP: "203.0.113.7", Router: map[string]string{"query": "failed"}})
	if j.Port != 15700 || j.Password != "old-pass" || !strings.Contains(strings.Join(j.Steps, " "), `"Old name"`) || !strings.Contains(j.Problem, "15700 (UDP)") {
		t.Errorf("running Join = %+v", j)
	}
	// No public address known yet.
	j = s.Join(game.JoinInput{Saved: sv})
	if j.Address != "" || len(j.Steps) == 0 {
		t.Errorf("Join without a public address = %+v", j)
	}
}

func TestExitHint(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	cases := []struct {
		tail []string
		want string
	}{
		{nil, "before it wrote its log"},
		{[]string{"[I 00:00:00,450] [online] Couldn't bind dedicated server socket. error='Only one usage of each socket address'"}, "port"},
		{[]string{"Internal Error: user groups passwords must be unique"}, "user groups passwords must be unique"},
		{[]string{"[I 00:00:01,035] [online] Server failed to connected to Steam 3"}, "Steam"},
		{[]string{"[E 00:00:02,000] [server] Failed to load Server Save Game"}, "backups"},
		// A server that gives up saving: the retries, then (with players on)
		// the message it sends them. Its strings; no posted log has these.
		{[]string{"[E 03:00:00,000] [server] Failed to Save. Retry 3", "[I 03:00:05,000] [game_server] Switching state from Run to Shutdown after 10805.00 s"}, "couldn't save"},
		{[]string{"[I 03:00:05,000] [server] Send Save Data Error Shutdown Message to Player 'Ash'"}, "couldn't save"},
	}
	for _, c := range cases {
		if got := s.ExitHint(c.tail); !strings.Contains(got, c.want) {
			t.Errorf("ExitHint(%q) = %q, want it to mention %q", c.tail, got, c.want)
		}
	}
	if got := s.ExitHint([]string{"[I 00:12:22,566] [app] Trigger gameflow shutdown, exit: Ctrl_C"}); got != "" {
		t.Errorf("a normal stop has a hint: %q", got)
	}
	// The server says goodbye to its players at every stop; only the
	// save-error goodbye means it couldn't save.
	if got := s.ExitHint([]string{"[I 00:12:22,570] [server] Send Shutdown Message to Player 'Ash'"}); got != "" {
		t.Errorf("a normal goodbye to a player has a hint: %q", got)
	}
}

// The stop chain is Ctrl+C alone: the server has no console commands, RCON
// or web API.
func TestStop(t *testing.T) {
	steps := Game{}.New(testEnv(t)).Stop(game.Record{PID: 4})
	if len(steps) != 1 || steps[0].Kind != "interrupt" || steps[0].Wait != 4*time.Minute || steps[0].Resend != 90*time.Second {
		t.Errorf("Stop = %+v", steps)
	}
}

// A server found running without its run file is followed in its own log.
func TestRecover(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved()
	sv.Ports["query"] = 15700
	s.store.Save(func(x *game.Saved) error { *x = sv; return nil })
	started := time.Now().Add(-time.Hour)
	rec := s.Recover(1234, started)
	if rec.LogPath != s.gameLog() || rec.Port != 15700 || rec.Ports["query"] != 15700 || rec.World != "Main" || rec.PID != 1234 || !rec.Started.Equal(started) {
		t.Errorf("Recover = %+v", rec)
	}
}

// The stand-in's players: a join by name with the Steam account from its
// connection line, a player turned away because the server is full, the
// query port's count while they're on, and the leave; the settings the
// app wrote reached the server, and what the server wrote back when it
// stopped (its own keys) is kept by the next start.
func TestStandIn(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Script: "join:Alex@1,join:Bea@1.5,deny:Cy@2,leave:Alex@4", Change: func(sv *game.Saved) {
		withPassword(sv)
		sv.Server["serverName"] = "Friends' Embervale"
		sv.Server["maxPlayers"] = "2"
	}})
	s.WaitEvent("online", "", 30*time.Second)
	alex := s.WaitEvent("join", "Alex", 15*time.Second)
	if len(alex.Account) != 17 || !strings.HasPrefix(alex.Account, "7656119") {
		t.Errorf("Alex's account is %q, not a Steam id", alex.Account)
	}
	s.WaitEvent("join", "Bea", 15*time.Second)
	full := s.WaitEvent("server_full", "", 15*time.Second)
	if !strings.Contains(full.Text, "full") {
		t.Errorf("the turned-away text is %q", full.Text)
	}
	ctx := t.Context()
	res, err := s.Server().(game.Prober).Probe(ctx, s.Record())
	if err != nil || !res.Up || res.Count != 2 {
		t.Errorf("the probe while two are on: %+v, %v", res, err)
	}
	s.WaitEvent("leave", "Alex", 15*time.Second)
	if st := s.State(); len(st.Players) != 1 || st.Players[0].Name != "Bea" {
		t.Errorf("after Alex left, the players are %+v", st.Players)
	}
	s.Stop()
	if ev := s.WaitEvent("leave", "Bea", 5*time.Second); ev.Name != "Bea" {
		t.Error("Bea didn't leave when the server stopped")
	}
	if s.State().LastSave.IsZero() {
		t.Error("the Monitor didn't count the stop as a save")
	}
	cfg := filepath.Join(s.Env().Dirs.Server, configName)
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "bannedAccounts") || !strings.Contains(string(b), "Friends' Embervale") {
		t.Fatalf("the stand-in didn't write its settings back as the server does:\n%s", b)
	}
	s.StartAgain()
	s.WaitEvent("online", "", 30*time.Second)
	b, _ = os.ReadFile(cfg)
	if !strings.Contains(string(b), "bannedAccounts") || !strings.Contains(string(b), `"slotCount": 2`) {
		t.Errorf("the next start dropped the server's own keys or the app's:\n%s", b)
	}
	s.Stop()
}

// Someone edits the settings file by hand while the server is stopped: a
// group that may build in bases but not change the world, which the real
// server refuses to start with, and a second group. The next start puts
// the app's one group back, so it starts, and only the app's password lets
// friends in.
func TestPlanRepairsHandEditedGroups(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: withPassword})
	s.WaitEvent("online", "", 30*time.Second)
	s.Stop()
	cfg := filepath.Join(s.Env().Dirs.Server, configName)
	b, _ := os.ReadFile(cfg)
	b = bytes.Replace(b, []byte(`"canEditWorld": true`), []byte(`"canEditWorld": false`), 1)
	b = bytes.Replace(b, []byte(`"userGroups": [`), []byte(`"userGroups": [{"name": "Guest", "password": "letmein"},`), 1)
	os.WriteFile(cfg, b, 0o600)
	s.StartAgain()
	s.WaitEvent("online", "", 30*time.Second)
	s.Stop()
	b, _ = os.ReadFile(cfg)
	if bytes.Contains(b, []byte("letmein")) || bytes.Contains(b, []byte(`"canEditWorld": false`)) {
		t.Errorf("the hand-made groups are still there:\n%s", b)
	}
}
