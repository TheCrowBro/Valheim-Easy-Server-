package windrose

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
)

const (
	exampleWorld = "DB57768A8A7746899683D0EEE91F97BF" // the publisher's example file's world
	newID        = "0123456789ABCDEF0123456789ABCDEF"
)

// golden compares got with testdata/golden/<name> (go test -update
// rewrites it), ignoring Windows' line ends either side so a checkout that
// changes them still compares.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
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
		t.Fatalf("%v (go test -update writes it)", err)
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(got) != norm(string(want)) {
		t.Errorf("%s differs from what the code makes now (go test -update rewrites it):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// planned plans a start of the publisher's example server, whose world is
// on disk and named "Crow's Nest" in the app.
func planned(t *testing.T, change func(*game.Saved), req game.PlanRequest) (*server, game.Plan, error) {
	t.Helper()
	fixedNetwork(t)
	notRunning(t)
	s := installed(t, "ServerDescription-publisher-example.json")
	makeWorld(t, s, exampleWorld, "The Archipelago")
	sv := defaultSaved("Crow's Nest")
	sv.State[statePrefix+exampleWorld] = "Crow's Nest"
	if change != nil {
		change(&sv)
	}
	s.store.Save(func(x *game.Saved) error { *x = sv.Clone(); return nil })
	req.Saved = s.store.Get()
	if req.World == "" {
		req.World = sv.World
	}
	p, err := s.Plan(t.Context(), req)
	return s, p, err
}

// The command line and the settings file a start writes, pinned in
// testdata/golden (go test -update rewrites them). Every key the app
// doesn't own keeps its value and its place; the server's own address,
// which this computer no longer has, is replaced with the one it has.
func TestPlanGolden(t *testing.T) {
	s, p, err := planned(t, func(sv *game.Saved) {
		sv.Server["serverName"] = "Crow's Nest ✓ <&>"
		sv.Server["password"] = `parrot "& co"`
		sv.Server["maxPlayers"] = "6"
		sv.Server["region"] = "EU"
	}, game.PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root := s.env.Dirs.Root
	rel := func(v string) string {
		if r, err := filepath.Rel(root, v); err == nil && filepath.IsAbs(v) {
			return "<instance>/" + filepath.ToSlash(r)
		}
		return v
	}
	var b strings.Builder
	b.WriteString("# The Windrose server's command line as the Plan builds it (spec 5.11).\n")
	b.WriteString("# Paths are relative to the instance's folder, games/windrose.\n\n")
	b.WriteString("exe: " + strings.TrimSuffix(rel(p.Exe), ".exe") + "\n")
	b.WriteString("dir: " + rel(p.Dir) + "\n")
	for _, a := range p.Args {
		b.WriteString("  " + a + "\n")
	}
	b.WriteString("output: GameLog " + rel(p.LogPath) + "\n")
	b.WriteString("readyWait: " + p.ReadyWait.String() + "\n")
	b.WriteString("record: server " + p.Record.Server + ", password set " + map[bool]string{true: "yes", false: "no"}[p.Record.Password != ""] + "\n")
	b.WriteString("record extra: inviteCode " + extraString(p.Record, "inviteCode") + ", worldId " + extraString(p.Record, "worldId") + "\n")
	golden(t, "commandline.txt", b.String())
	if p.Output != game.GameLog || p.Console {
		t.Errorf("output %v, console %v", p.Output, p.Console)
	}

	desc, err := os.ReadFile(s.descriptionPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desc), "\r\n") {
		t.Errorf("the file has no Windows line ends, unlike the server's own")
	}
	golden(t, "ServerDescription-planned.json", string(desc))
	if st := s.store.Get().State; st[stateInvite] != "d6221bb7" || st[stateDirect] != "" {
		t.Errorf("State %v", st)
	}
}

// No password: IsPasswordProtected must be false with it (the publisher:
// otherwise "it may cause unexpected behavior").
func TestPlanNoPassword(t *testing.T) {
	s, _, err := planned(t, func(sv *game.Saved) { sv.Server["password"] = "" }, game.PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := readDescription(s.descriptionPath())
	if err != nil {
		t.Fatal(err)
	}
	if d.boolean(keyProtected) || d.str(keyPassword) != "" {
		t.Errorf("protected %v, password %q", d.boolean(keyProtected), d.str(keyPassword))
	}
}

// A new world, once the server has a settings file: a new id in
// WorldIslandId, its name kept in State, and a start that makes the world
// waits longer.
func TestPlanNewWorld(t *testing.T) {
	fixedIDs(t, newID)
	s, p, err := planned(t, nil, game.PlanRequest{World: "Second Sea", New: true})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := readDescription(s.descriptionPath())
	if d.str(keyWorld) != newID {
		t.Errorf("WorldIslandId %q", d.str(keyWorld))
	}
	st := s.store.Get().State
	if st[statePrefix+newID] != "Second Sea" || st[statePlanned] != newID || st[statePrefix+exampleWorld] != "Crow's Nest" {
		t.Errorf("State %v", st)
	}
	if p.ReadyWait != readyWaitNew {
		t.Errorf("ReadyWait %v", p.ReadyWait)
	}
	// A start that stopped before the server made it: the next one gives
	// the same id again rather than leave a name behind.
	fixedIDs(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Second Sea", New: true, Saved: s.store.Get()}); err != nil {
		t.Fatal(err)
	}
	if d, _ := readDescription(s.descriptionPath()); d.str(keyWorld) != newID {
		t.Errorf("the retry gave %q", d.str(keyWorld))
	}
}

// The server made the new world under an id of its own and wrote that
// into the file: the world is still the one the owner named, and the next
// plan keeps its name for good.
func TestPlanNewWorldServerID(t *testing.T) {
	fixedIDs(t, newID)
	s, _, err := planned(t, nil, game.PlanRequest{World: "Second Sea", New: true})
	if err != nil {
		t.Fatal(err)
	}
	// What the server did at that start.
	own := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	makeWorld(t, s, own, "The Archipelago")
	d, _ := readDescription(s.descriptionPath())
	d.pers.set(keyWorld, own)
	d.write(s.descriptionPath())

	if !s.Worlds().Exists("Second Sea") || !s.Worlds().Exists("Crow's Nest") {
		t.Fatalf("worlds: %v", worldList(t, s))
	}
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Second Sea", Saved: s.store.Get()}); err != nil {
		t.Fatal(err)
	}
	st := s.store.Get().State
	if st[statePrefix+own] != "Second Sea" || st[statePrefix+newID] != "" || st[statePlanned] != "" {
		t.Errorf("State %v", st)
	}
	if d, _ := readDescription(s.descriptionPath()); d.str(keyWorld) != own {
		t.Errorf("WorldIslandId %q", d.str(keyWorld))
	}
}

// The server made a new world under an id of its own, and the next plan is
// for yet another new world: the first keeps its name for good, rather
// than being listed under its WorldName once "planned" moves on, and
// picking it again starts the world friends played on, not a fresh one.
func TestPlanNewWorldServerIDThenAnother(t *testing.T) {
	fixedIDs(t, newID, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	s, _, err := planned(t, nil, game.PlanRequest{World: "Second Sea", New: true})
	if err != nil {
		t.Fatal(err)
	}
	// What the server does at a start for a new world: it makes it under
	// an id of its own and writes that into the file.
	serverMakes := func(own string) {
		t.Helper()
		makeWorld(t, s, own, "The Archipelago")
		d, _ := readDescription(s.descriptionPath())
		d.pers.set(keyWorld, own)
		d.write(s.descriptionPath())
	}
	second, third := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	serverMakes(second)

	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Third Sea", New: !s.Worlds().Exists("Third Sea"), Saved: s.store.Get()}); err != nil {
		t.Fatal(err)
	}
	st := s.store.Get().State
	if st[statePrefix+second] != "Second Sea" || st[statePrefix+newID] != "" || st[statePlanned] != "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF" {
		t.Errorf("after planning the third world, State %v", st)
	}
	if got := worldList(t, s); !slices.Equal(got, []string{"Crow's Nest", "Second Sea"}) {
		t.Errorf("worlds %q", got)
	}
	serverMakes(third)
	if got := worldList(t, s); !slices.Equal(got, []string{"Crow's Nest", "Second Sea", "Third Sea"}) {
		t.Errorf("worlds %q", got)
	}

	if !s.Worlds().Exists("Second Sea") {
		t.Fatal("the second world isn't there any more")
	}
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "Second Sea", Saved: s.store.Get()}); err != nil {
		t.Fatal(err)
	}
	if d, _ := readDescription(s.descriptionPath()); d.str(keyWorld) != second {
		t.Errorf("WorldIslandId %q, want the second world's %s", d.str(keyWorld), second)
	}
	st = s.store.Get().State
	for k, v := range st {
		if id, ok := strings.CutPrefix(k, statePrefix); ok && id != exampleWorld && id != second && id != third {
			t.Errorf("a name left on an id that isn't on disk: %s = %q", k, v)
		}
	}
	if st[statePrefix+third] != "Third Sea" || st[statePlanned] != "" {
		t.Errorf("State %v", st)
	}
}

// A plan whose first start has just made the settings file uses that
// start's world for a new world, even when an earlier start gave the
// name an id the server never made a world under.
func TestPlanFirstStartWorldWins(t *testing.T) {
	fixedIDs(t, newID)
	s := installed(t, "ServerDescription-publisher-example.json")
	makeWorld(t, s, exampleWorld, "The Archipelago") // the first start's world
	ghost := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s.store.Save(func(sv *game.Saved) error {
		*sv = defaultSaved("W")
		sv.State[statePrefix+ghost] = "W"
		return nil
	})
	d, err := readDescription(s.descriptionPath())
	if err != nil {
		t.Fatal(err)
	}
	id, made, err := s.worldFor(true, "W", d, true)
	if err != nil {
		t.Fatal(err)
	}
	if id != exampleWorld || made {
		t.Errorf("worldFor = %s, made %v; want the first start's world %s", id, made, exampleWorld)
	}
	if st := s.store.Get().State; st[statePrefix+exampleWorld] != "W" || st[statePlanned] != exampleWorld {
		t.Errorf("State %v", st)
	}
}

func worldList(t *testing.T, s *server) []string {
	t.Helper()
	l, _, err := s.Worlds().List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, w := range l {
		out = append(out, w.Name)
	}
	return out
}

// A world that's gone isn't made again silently.
func TestPlanMissingWorld(t *testing.T) {
	_, _, err := planned(t, nil, game.PlanRequest{World: "Gone"})
	if err == nil || !strings.Contains(err.Error(), "isn't in Windrose's saves folder") {
		t.Errorf("err %v", err)
	}
}

// Settings the server can't use are refused before anything is written.
func TestPlanRefusesBadSettings(t *testing.T) {
	s, _, err := planned(t, func(sv *game.Saved) { sv.Server["maxPlayers"] = "11" }, game.PlanRequest{})
	var fe game.FieldErrors
	if !errors.As(err, &fe) || fe["maxPlayers"] == "" {
		t.Fatalf("err %v", err)
	}
	b, _ := os.ReadFile(s.descriptionPath())
	if !strings.Contains(string(b), `"MaxPlayerCount": 4`) {
		t.Errorf("the file was changed")
	}
}

// A settings file the app can't read stops the start with the reason,
// rather than the server starting with a new identity.
func TestPlanBrokenDescription(t *testing.T) {
	fixedNetwork(t)
	s := installed(t, "")
	os.MkdirAll(filepath.Dir(s.descriptionPath()), 0o755)
	os.WriteFile(s.descriptionPath(), []byte(`{"Version": 1}`), 0o644)
	_, err := s.Plan(t.Context(), game.PlanRequest{World: "W", New: true, Saved: defaultSaved("W")})
	if err == nil || !strings.Contains(err.Error(), "ServerDescription_Persistent") {
		t.Errorf("err %v", err)
	}
}

func TestPlanNotInstalled(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	if _, err := s.Plan(t.Context(), game.PlanRequest{World: "W", Saved: defaultSaved("W")}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("err %v", err)
	}
}

// The fingerprint is the plan's, for the selected world named or not, and
// changes with every setting that needs a restart.
func TestFingerprint(t *testing.T) {
	s, p, err := planned(t, nil, game.PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sv := s.store.Get()
	if s.Fingerprint(sv, "") != p.Fingerprint || s.Fingerprint(sv, sv.World) != p.Fingerprint {
		t.Errorf("Fingerprint(saved, \"\") %q, (saved, world) %q, plan %q", s.Fingerprint(sv, ""), s.Fingerprint(sv, sv.World), p.Fingerprint)
	}
	for _, f := range s.Schema() {
		if !f.Restart {
			continue
		}
		c := sv.Clone()
		v := "something else"
		switch f.Kind {
		case "number":
			v = "7"
		case "choice":
			v = f.Choices[len(f.Choices)-1].Value
		}
		c.Server[f.Key] = v
		if s.Fingerprint(c, "") == p.Fingerprint {
			t.Errorf("changing %s doesn't change the fingerprint", f.Key)
		}
	}
	if s.Fingerprint(sv, "Other") == p.Fingerprint {
		t.Errorf("another world has the same fingerprint")
	}
}

// The server's network address: kept while this computer has it, kept
// when it's a loopback or empty, replaced when the computer's address
// has changed.
func TestFixProxyAddress(t *testing.T) {
	fixedNetwork(t)
	for in, want := range map[string]string{
		"192.168.1.20": "192.168.1.20", "127.0.0.1": "127.0.0.1", "": "", "0.0.0.0": "0.0.0.0",
		"192.168.31.49": "192.168.1.20", "not an address": "not an address",
	} {
		if got, _ := fixProxyAddress(in); got != want {
			t.Errorf("fixProxyAddress(%q) = %q, want %q", in, got, want)
		}
	}
	outboundIP = func() net.IP { return nil }
	if got, changed := fixProxyAddress("192.168.31.49"); got != "192.168.31.49" || changed {
		t.Errorf("without a known address: %q %v", got, changed)
	}
}

// The settings file round-trips: keys in their order, values the app
// doesn't own as they were, a byte-order mark tolerated.
func TestDescriptionRoundTrip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "ServerDescription-publisher-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := parseDescription(append([]byte(byteOrderMark), b...))
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(b) {
		t.Errorf("not the same file:\n%s\nwant\n%s", out, b)
	}
	if d.str(keyInviteCode) != "d6221bb7" || d.version() != "0.10.0.0.251" || d.boolean(keyDirect) {
		t.Errorf("invite %q version %q direct %v", d.str(keyInviteCode), d.version(), d.boolean(keyDirect))
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "[]", `{"ServerDescription_Persistent": 3}`, "{"} {
		if _, err := parseDescription([]byte(bad)); err == nil {
			t.Errorf("parseDescription(%q) took it", bad)
		}
	}
}

// The last run's log is set aside the way the server does it, so its
// lines are never read as this run's.
func TestSetLogAside(t *testing.T) {
	s := installed(t, "")
	os.MkdirAll(filepath.Dir(s.logPath()), 0o755)
	for range 2 {
		os.WriteFile(s.logPath(), []byte("old\n"), 0o644)
		if err := s.setLogAside(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(s.logPath()); !os.IsNotExist(err) {
		t.Errorf("R5.log is still there")
	}
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(s.logPath()), "R5-backup-*.log"))
	if len(m) != 2 {
		t.Errorf("backups %v", m)
	}
	if err := s.setLogAside(); err != nil {
		t.Errorf("with no log: %v", err)
	}
}
