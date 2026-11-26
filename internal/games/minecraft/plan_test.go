package minecraft

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// golden compares got with testdata/golden/name (or rewrites it with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if want := readFile(t, path); got != want {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// installed makes an instance whose server files are there (empty
// placeholders: the plan only checks they exist), with fixed secrets and
// clock so the files it writes are the same every time.
func installed(t *testing.T) (*server, game.Env) {
	t.Helper()
	env := testEnv(t)
	s := Game{}.New(env).(*server)
	for _, p := range []string{s.jarPath(), s.Program()} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(env.Dirs.Saves, 0o755)
	oldSecret, oldNow := newSecret, now
	newSecret = func() string { return "RCONPASSWORD0123456789ABCD" }
	now = func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { newSecret, now = oldSecret, oldNow })
	return s, env
}

// defaults is what Add a game saves: each field's default, the world, the
// ports, and the EULA ticked.
func defaults(s *server, world string) game.Saved {
	sv := game.Saved{Server: game.Values{}, Worlds: map[string]game.Values{world: {}}, World: world,
		Ports: map[string]int{roleGame: 25565, roleRCON: 25575}, State: game.Values{stateEULA: "2026-11-26T12:00:00Z"}}
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

func plan(t *testing.T, s *server, sv game.Saved, world string, newWorld game.Values) game.Plan {
	t.Helper()
	s.store.Save(func(x *game.Saved) error { *x = sv.Clone(); return nil })
	p, err := s.Plan(context.Background(), game.PlanRequest{World: world, New: newWorld != nil, NewWorld: newWorld, Saved: sv,
		LogPath: filepath.Join(s.env.Dirs.Logs, "minecraft-2026-11-26_12-00-00.log")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

// commandLine is the plan's command line with the instance's folder as <root>.
func commandLine(p game.Plan, root string) string {
	var b strings.Builder
	for _, a := range append([]string{p.Exe}, p.Args...) {
		b.WriteString(strings.ReplaceAll(filepath.ToSlash(a), filepath.ToSlash(root), "<root>") + "\n")
	}
	b.WriteString("dir " + strings.ReplaceAll(filepath.ToSlash(p.Dir), filepath.ToSlash(root), "<root>") + "\n")
	return strings.ReplaceAll(b.String(), "java.exe", "java") // the same golden file on Windows
}

// TestPlanNewWorld: the first start of a new world on a fresh install,
// with the allow list on "auto": server.properties has the list on before
// the server ever starts, the players who already played (Minecraft's user
// cache) are on it unless banned, and eula.txt says the owner agreed.
func TestPlanNewWorld(t *testing.T) {
	s, env := installed(t)
	cache := `[{"name":"Alex","uuid":"36532b5e-c442-3dbb-a24c-c7e55d0f979a","expiresOn":"2026-12-26 19:40:16 +0000"},` +
		`{"name":"Griefer","uuid":"faa5dca3-c3d4-354b-ae1b-dde9e5a14b3b","expiresOn":"2026-12-26 19:40:16 +0000"}]`
	os.WriteFile(filepath.Join(env.Dirs.Saves, "usercache.json"), []byte(cache), 0o644)
	os.WriteFile(filepath.Join(env.Dirs.Server, "banned-players.json"), []byte(`[{"uuid":"faa5dca3-c3d4-354b-ae1b-dde9e5a14b3b","name":"Griefer","created":"2026-11-26 19:50:12 +0000","source":"Rcon","expires":"forever","reason":"Griefing"}]`), 0o644)

	sv := defaults(s, "Forest")
	sv.Server["serverName"] = "Crow's: Minecraft = fun #1 ä"
	p := plan(t, s, sv, "Forest", game.Values{"seed": "12345", "levelType": "large_biomes", "hardcore": "false"})

	golden(t, "server.properties-new", readFile(t, filepath.Join(env.Dirs.Server, "server.properties")))
	golden(t, "eula.txt", readFile(t, filepath.Join(env.Dirs.Server, "eula.txt")))
	golden(t, "whitelist.json-seeded", readFile(t, filepath.Join(env.Dirs.Server, "whitelist.json")))
	golden(t, "command-line", commandLine(p, env.Dirs.Root))

	if p.Output != game.Capture || p.Console || p.Fingerprint != s.Fingerprint(sv, "Forest") {
		t.Errorf("plan = %+v", p)
	}
	if p.Record.Secrets[roleRCON] != "RCONPASSWORD0123456789ABCD" || p.Record.Ports[roleRCON] != 25575 || p.Record.Port != 25565 {
		t.Errorf("record = %+v", p.Record)
	}
	if st := s.store.Get().State; st[stateAllowListOn] != "2026-11-26T12:00:00Z" {
		t.Errorf("the state doesn't remember the list went on: %v", st)
	}
	if info, err := os.Stat(filepath.Join(env.Dirs.Server, "server.properties")); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("server.properties holds the RCON password but is %v", info.Mode().Perm())
	}

	// The list was filled once; a second start leaves it to the owner, who
	// has taken Alex off.
	os.WriteFile(filepath.Join(env.Dirs.Server, "whitelist.json"), []byte("[]"), 0o644)
	sv = s.store.Get()
	plan(t, s, sv, "Forest", nil)
	if names, _ := s.ReadList("allowed"); len(names) != 0 {
		t.Errorf("a later start put %q back on the list", names)
	}
}

// TestPlanKeepsOtherKeys: an existing server.properties (here the one 26.3
// writes for itself) keeps every key and comment the app doesn't set, and
// "off" turns the list off. An existing world gets no seed or world type.
func TestPlanKeepsOtherKeys(t *testing.T) {
	s, env := installed(t)
	init := readFile(t, filepath.Join("testdata", "server.properties-26.3-init"))
	os.WriteFile(filepath.Join(env.Dirs.Server, "server.properties"), []byte(init), 0o644)
	sv := defaults(s, "Forest")
	sv.Server["allowList"] = allowOff
	sv.Server["maxPlayers"] = "6"
	sv.Worlds["Forest"]["difficulty"] = "hard"
	p := plan(t, s, sv, "Forest", nil)
	got := readFile(t, filepath.Join(env.Dirs.Server, "server.properties"))
	golden(t, "server.properties-existing", got)
	before, after := parseProperties(init), parseProperties(got)
	for _, e := range before.entries {
		if e.key == "" {
			continue
		}
		if _, ok := after.Get(e.key); !ok {
			t.Errorf("key %s was lost", e.key)
		}
	}
	if v, _ := after.Get("level-type"); v != "minecraft:normal" {
		t.Errorf("level-type = %q: an existing world's settings aren't the plan's to change", v)
	}
	if strings.Contains(commandLine(p, env.Dirs.Root), "Forest") == false {
		t.Errorf("the command line doesn't name the world")
	}
}

// TestAllowListRule: "auto" is on unless carrier-grade NAT was found and
// the list was never on; "on" and "off" are fixed.
func TestAllowListRule(t *testing.T) {
	for _, c := range []struct {
		mode  string
		state game.Values
		want  bool
	}{
		{allowAuto, nil, true},
		{allowAuto, game.Values{stateCGNAT: "true"}, false},
		{allowAuto, game.Values{stateCGNAT: "true", stateAllowListOn: "2026-11-26T12:00:00Z"}, true},
		{allowOn, game.Values{stateCGNAT: "true"}, true},
		{allowOff, nil, false},
		{"", nil, true},
	} {
		sv := game.Saved{Server: game.Values{"allowList": c.mode}, State: c.state}
		if got := allowListWanted(sv); got != c.want {
			t.Errorf("mode %q state %v: on = %v", c.mode, c.state, got)
		}
	}
}

// TestPlanRefuses: no start without the EULA, without the files, or with
// settings the panel would reject.
func TestPlanRefuses(t *testing.T) {
	s, _ := installed(t)
	sv := defaults(s, "Forest")
	delete(sv.State, stateEULA)
	_, err := s.Plan(context.Background(), game.PlanRequest{World: "Forest", Saved: sv})
	if err == nil || !strings.Contains(err.Error(), "EULA") {
		t.Errorf("without the EULA: %v", err)
	}
	sv = defaults(s, "Forest")
	sv.Server["serverName"] = strings.Repeat("x", 200)
	var fe game.FieldErrors
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Forest", Saved: sv}); !errors.As(err, &fe) || fe["serverName"] == "" {
		t.Errorf("a 200-character name: %v", err)
	}
	os.Remove(s.jarPath())
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Forest", Saved: defaults(s, "Forest")}); !errors.Is(err, errNotInstalled) {
		t.Errorf("without server.jar: %v", err)
	}
}

// TestProperties: reading and writing .properties the way Java does.
func TestProperties(t *testing.T) {
	p := parseProperties("#c\nlevel-type=minecraft\\:normal\nmotd = A \\u00e4 \\\n  b\nkey\\=x:y\n")
	for k, want := range map[string]string{"level-type": "minecraft:normal", "motd": "A ä b", "key=x": "y"} {
		if got, _ := p.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	p.Set("motd", "Crow's: #1 ä 😀")
	p.Set("new", " lead")
	if got := p.String(); got != "#c\nlevel-type=minecraft\\:normal\nmotd=Crow's\\: \\#1 \\u00E4 \\uD83D\\uDE00\nkey\\=x:y\nnew=\\ lead\n" {
		t.Errorf("written:\n%s", got)
	}
	back := parseProperties(p.String())
	if got, _ := back.Get("motd"); got != "Crow's: #1 ä 😀" {
		t.Errorf("round trip motd = %q", got)
	}
	if got, _ := back.Get("new"); got != " lead" {
		t.Errorf("round trip leading space = %q", got)
	}
}

// TestJoin: friends join by address; the port shows only when it isn't
// Minecraft's own.
func TestJoin(t *testing.T) {
	s, _ := installed(t)
	sv := defaults(s, "Forest")
	j := s.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{roleGame: "open"},
		State: &game.RunState{Version: "26.3", Extra: map[string]string{extraAllowList: allowOn}}})
	if j.How != "address" || j.Address != "203.0.113.7" || j.LAN[0] != "192.168.1.20" || !j.Router || j.Problem != "" {
		t.Errorf("join = %+v", j)
	}
	if !strings.Contains(strings.Join(j.Steps, " "), "Minecraft 26.3") || !strings.Contains(j.Hint, "203.0.113.7") {
		t.Errorf("steps %q hint %q", j.Steps, j.Hint)
	}
	sv.Ports[roleGame] = 25566
	j = s.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", Router: map[string]string{roleGame: "failed"}})
	if j.Address != "203.0.113.7:25566" || j.Problem == "" {
		t.Errorf("join on 25566 with the router failed = %+v", j)
	}
}

// TestRecordExtraRoundTrip: the allow list's fields survive the run file,
// so a copy of the app that takes over still knows them.
func TestRecordExtraRoundTrip(t *testing.T) {
	rec := planned(allowOff, allowAuto)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back game.Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	m := Game{}.New(testEnv(t)).Watch(back, nil).(*monitor)
	if m.mode != allowAuto || m.allowOn {
		t.Errorf("after the run file: mode %q on %v (file %s)", m.mode, m.allowOn, b)
	}
}
