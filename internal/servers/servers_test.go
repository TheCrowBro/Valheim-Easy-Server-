package servers

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// full is a servers.json with every field this build knows set, and keys
// it doesn't know at every level: the top, "app", an instance, its
// "discord", and a whole instance of a game this build doesn't have.
const full = `{
  "schema": 1,
  "futureTop": {"nested": [1, 2, {"x": null}]},
  "app": {
    "stars": ["valheim", "minecraft"],
    "activeOpen": false,
    "inactiveOpen": true,
    "disabledGames": ["palworld"],
    "routerPorts": false,
    "futureApp": "kept"
  },
  "instances": [
    {
      "id": "valheim", "game": "valheim", "legacy": true, "added": "2026-12-01T18:00:00Z",
      "hidden": true,
      "discord": {"on": false, "webhook": "https://discord.com/api/webhooks/1/a", "stopped": false, "daily": false, "password": false}
    },
    {
      "id": "minecraft", "game": "minecraft", "added": "2026-12-03T19:12:00Z",
      "settings": {"serverName": "Friends' Minecraft", "maxPlayers": "10", "allowList": "auto", "updateOnStart": "true"},
      "world": "Riverbend",
      "worlds": {"Riverbend": {"difficulty": "normal", "gamemode": "survival"}},
      "ports": {"game": 25565, "rcon": 25575},
      "portsChosen": ["game"],
      "state": {"eula": "2026-12-03T19:12:00Z"},
      "autoStopMinutes": 0,
      "startWithApp": true,
      "presenceSeen": "2026-12-03T19:40:00Z",
      "discord": {"on": true, "webhook": "https://discord.com/api/webhooks/2/b", "stopped": true, "daily": true, "password": true, "futureDiscord": 7},
      "futureInstance": {"a": "b"}
    },
    {
      "id": "spacegame", "game": "spacegame", "added": "2027-01-01T00:00:00Z",
      "settings": {"galaxy": "far"}, "warp": [1, 2, 3], "discord": {"on": true, "stopped": true, "daily": true, "password": false, "pings": "all"}
    }
  ]
}
`

// generic reads JSON into plain maps and slices, so two files can be
// compared by meaning (key order and spacing aside).
func generic(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return v
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// snapshot is every file under dir with its contents, to check that
// something changed nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestServersPassthrough: an older build that changes the list writes back
// everything a newer one put there, at every level, including a whole
// instance of a game it doesn't have (spec 3.2).
func TestServersPassthrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	writeFile(t, path, full)

	f, st, err := Load(path)
	if err != nil || st != OK {
		t.Fatalf("Load = %v, %v", st, err)
	}
	if got := string(f.Unknown["futureTop"]); got != `{"nested":[1,2,{"x":null}]}` {
		t.Errorf("top-level kept key = %s", got)
	}
	if got := string(f.App.Unknown["futureApp"]); got != `"kept"` {
		t.Errorf("app kept key = %s", got)
	}
	mc := f.Instance("minecraft")
	if mc == nil || string(mc.Unknown["futureInstance"]) != `{"a":"b"}` || string(mc.Discord.Unknown["futureDiscord"]) != "7" {
		t.Fatalf("minecraft = %+v", mc)
	}
	if sg := f.Instance("spacegame"); sg == nil || sg.Game != "spacegame" || string(sg.Unknown["warp"]) != "[1,2,3]" || sg.Settings["galaxy"] != "far" {
		t.Fatalf("the unknown game's instance = %+v", sg)
	}

	// A change by this build: star a game and change a setting.
	if err := Update(path, func(f *File) error {
		f.App.Stars = append(f.App.Stars, "spacegame")
		f.Instance("minecraft").Settings["maxPlayers"] = "12"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := generic(t, []byte(full)).(map[string]any)
	app := want["app"].(map[string]any)
	app["stars"] = []any{"valheim", "minecraft", "spacegame"}
	want["instances"].([]any)[1].(map[string]any)["settings"].(map[string]any)["maxPlayers"] = "12"
	if got := generic(t, readFile(t, path)); !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("written back:\n%s", gb)
	}
	// The file before the change is the backup, byte for byte.
	if got := string(readFile(t, path+BackupSuffix)); got != full {
		t.Errorf("backup = %s", got)
	}
}

// TestRoundTrip: every known field survives reading and writing, and an
// empty list is written with [] rather than null.
func TestRoundTrip(t *testing.T) {
	var f File
	if err := json.Unmarshal([]byte(full), &f); err != nil {
		t.Fatal(err)
	}
	mc := f.Instance("minecraft")
	seen := time.Date(2026, 12, 3, 19, 40, 0, 0, time.UTC)
	if mc.AutoStopMinutes == nil || *mc.AutoStopMinutes != 0 || mc.AutoStop() != 0 || !mc.StartWithApp || !mc.PresenceSeen.Equal(seen) ||
		mc.Ports["rcon"] != 25575 || !slices.Equal(mc.PortsChosen, []string{"game"}) || mc.Worlds["Riverbend"]["gamemode"] != "survival" ||
		mc.State["eula"] == "" || mc.World != "Riverbend" || !mc.Discord.Password {
		t.Errorf("minecraft read as %+v", *mc)
	}
	v := f.Instance("valheim")
	if !v.Legacy || !v.Hidden || v.Discord.Webhook == "" || f.App.ActiveListOpen() || !f.App.InactiveListOpen() || f.App.RouterPortsOn() ||
		!slices.Equal(f.App.DisabledGames, []string{"palworld"}) {
		t.Errorf("read as %+v / %+v", *v, f.App)
	}
	b, err := encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := generic(t, b), generic(t, []byte(full)); !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n%s", b)
	}

	empty, err := json.Marshal(File{Schema: Schema})
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{"schema":1,"app":{"stars":[]},"instances":[]}` {
		t.Errorf("empty list = %s", empty)
	}
	// A zero instance leaves out everything optional.
	one, _ := json.Marshal(Instance{ID: "terraria", Game: "terraria"})
	if string(one) != `{"id":"terraria","game":"terraria","added":"0001-01-01T00:00:00Z"}` {
		t.Errorf("bare instance = %s", one)
	}
}

// TestKeptKeys: a kept key reads back the same from an indented file, a
// key that differs from a field only in case is the field (as
// encoding/json reads it), and a kept key that names a field is refused.
func TestKeptKeys(t *testing.T) {
	var in Instance
	if err := json.Unmarshal([]byte(`{"ID": "x", "game": "x", "extra": { "a" : [ 1 ] }}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.ID != "x" || len(in.Unknown) != 1 || string(in.Unknown["extra"]) != `{"a":[1]}` {
		t.Errorf("read %+v", in)
	}
	in.Unknown["Game"] = json.RawMessage(`"y"`)
	if _, err := json.Marshal(in); err == nil {
		t.Error("a kept key named like a field was written")
	}
	bad := App{Unknown: map[string]json.RawMessage{"x": json.RawMessage(`{`)}}
	if _, err := json.Marshal(bad); err == nil {
		t.Error("a kept value that isn't JSON was written")
	}
	// Reading a second time replaces what was kept.
	if err := json.Unmarshal([]byte(`{"id":"x","game":"x"}`), &in); err != nil || in.Unknown != nil {
		t.Errorf("read again: %v %v", in.Unknown, err)
	}
	// null leaves things as they are, as for any struct.
	var f File
	if err := json.Unmarshal([]byte(`{"schema":1,"app":null,"instances":[null],"x":null}`), &f); err != nil ||
		f.Schema != 1 || len(f.Instances) != 1 || f.Instances[0].ID != "" || string(f.Unknown["x"]) != "null" {
		t.Errorf("nulls read as %+v, %v", f, err)
	}
	if fmt.Sprint(OK, Missing, Damaged, Newer, Status(9)) != "ok missing damaged newer Status(9)" {
		t.Error("Status names")
	}
	// A copy of the rules doesn't share the kept keys.
	d := Discord{Unknown: map[string]json.RawMessage{"k": json.RawMessage(`1`)}}
	c := Instance{Discord: &d}.DiscordRules()
	c.Unknown["k"][0] = '2'
	if string(d.Unknown["k"]) != "1" {
		t.Error("DiscordRules shares its kept keys")
	}
}

func TestDefaults(t *testing.T) {
	var in Instance
	if got := in.DiscordRules(); !got.On || !got.Stopped || !got.Daily || got.Password || got.Webhook != "" {
		t.Errorf("nil Discord means %+v", got)
	}
	if in.AutoStop() != 60 {
		t.Errorf("nil auto-stop = %d", in.AutoStop())
	}
	for set, want := range map[int]int{0: 0, 30: 30, -5: 0, 120: 120} {
		in.AutoStopMinutes = &set
		if got := in.AutoStop(); got != want {
			t.Errorf("auto-stop %d = %d, want %d", set, got, want)
		}
	}
	var a App
	if !a.ActiveListOpen() || a.InactiveListOpen() || !a.RouterPortsOn() {
		t.Errorf("app defaults: %v %v %v", a.ActiveListOpen(), a.InactiveListOpen(), a.RouterPortsOn())
	}
	for id, ok := range map[string]bool{
		"valheim": true, "minecraft-2": true, "a": true, "x" + strings.Repeat("a", 63): true,
		"": false, "Minecraft": false, "2minecraft": false, "../x": false, "a/b": false, `a\b`: false,
		".hidden": false, "c:": false, "a.b": false, "x" + strings.Repeat("a", 64): false, "mine craft": false,
	} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) = %v", id, !ok)
		}
	}
	f := New("valheim", time.Date(2026, 12, 1, 19, 0, 0, 0, time.FixedZone("CET", 3600)))
	b, _ := json.Marshal(f)
	if string(b) != `{"schema":1,"app":{"stars":["valheim"]},"instances":[{"id":"valheim","game":"valheim","legacy":true,"added":"2026-12-01T18:00:00Z"}]}` {
		t.Errorf("first list = %s", b)
	}
	if b, _ := json.Marshal(New("", time.Now())); string(b) != `{"schema":1,"app":{"stars":[]},"instances":[]}` {
		t.Errorf("first list without a legacy game = %s", b)
	}
}

func TestLoad(t *testing.T) {
	good := `{"schema":1,"app":{"stars":["valheim"]},"instances":[{"id":"valheim","game":"valheim","legacy":true,"added":"2026-12-01T18:00:00Z"}]}`
	older := `{"schema":1,"app":{"stars":[]},"instances":[]}`
	newer := `{"schema":2,"app":{"stars":["valheim"],"routerPorts":"changed meaning"},"instances":[{"id":"valheim","game":"valheim"}],"next":true}`
	damaged := map[string]string{
		"invalid JSON":   `{"schema":1,"app":`,
		"empty":          ``,
		"not an object":  `[1,2]`,
		"wrong type":     `{"schema":1,"app":{"stars":"valheim"},"instances":[]}`,
		"no schema":      `{"app":{"stars":[]},"instances":[]}`,
		"schema 0":       `{"schema":0,"instances":[]}`,
		"schema text":    `{"schema":"1","instances":[]}`,
		"bad time":       `{"schema":1,"instances":[{"id":"a","game":"a","added":"yesterday"}]}`,
		"trailing bytes": good + `}`,
	}
	type want struct {
		status Status
		stars  []string // nil: no list (Schema 0)
		err    bool
		newer  bool
	}
	cases := map[string]struct {
		file, bak *string
		want      want
	}{
		"ok":                      {file: &good, want: want{status: OK, stars: []string{"valheim"}}},
		"ok with a backup":        {file: &good, bak: &older, want: want{status: OK, stars: []string{"valheim"}}},
		"missing":                 {want: want{status: Missing}},
		"missing with a backup":   {bak: &good, want: want{status: Missing}}, // Open restores it, Load doesn't
		"newer":                   {file: &newer, bak: &good, want: want{status: Newer, stars: []string{"valheim"}, err: true, newer: true}},
		"damaged, backup newer":   {file: ptr(damaged["invalid JSON"]), bak: &newer, want: want{status: Damaged, err: true, newer: true}},
		"damaged, no backup":      {file: ptr(damaged["invalid JSON"]), want: want{status: Damaged, err: true}},
		"damaged, backup damaged": {file: ptr(damaged["invalid JSON"]), bak: ptr(damaged["wrong type"]), want: want{status: Damaged, err: true}},
	}
	for name, d := range damaged {
		cases["damaged: "+name] = struct {
			file, bak *string
			want      want
		}{file: &d, bak: &older, want: want{status: Damaged, stars: []string{}}}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if c.file != nil {
				writeFile(t, path, *c.file)
			}
			if c.bak != nil {
				writeFile(t, path+BackupSuffix, *c.bak)
			}
			before := snapshot(t, dir)
			f, st, err := Load(path)
			if st != c.want.status || (err != nil) != c.want.err || errors.Is(err, ErrNewer) != c.want.newer {
				t.Fatalf("Load = %v, %v; want %v, error %v, newer %v", st, err, c.want.status, c.want.err, c.want.newer)
			}
			if c.want.stars == nil && f.Schema != 0 || c.want.stars != nil && !slices.Equal(f.App.Stars, c.want.stars) {
				t.Errorf("list = %+v", f)
			}
			if after := snapshot(t, dir); !maps.Equal(before, after) {
				t.Errorf("Load changed files: %v", after)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// knows is the registry stand-in: these games are registered.
func knows(ids ...string) func(string) bool {
	return func(id string) bool { return slices.Contains(ids, id) }
}

var now = time.Date(2026, 12, 5, 20, 30, 15, 0, time.Local)

func TestOpenFirstStart(t *testing.T) {
	dir := t.TempDir()
	f, o, err := Open(dir, "valheim", knows("valheim", "minecraft"), now)
	if err != nil || !o.Created || o.Rebuilt || o.FromBackup || o.SafeMode || o.WriteErr != nil || o.Status != Missing || o.Warning() != "" {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	want := New("valheim", now)
	if !reflect.DeepEqual(f, want) {
		t.Errorf("list = %+v", f)
	}
	again, st, err := Load(filepath.Join(dir, FileName))
	if err != nil || st != OK || !reflect.DeepEqual(again, want) {
		t.Errorf("written list reads back as %+v, %v, %v", again, st, err)
	}
	// The next start just reads it.
	if _, o, err := Open(dir, "valheim", nil, now.Add(time.Hour)); err != nil || o.Status != OK || o.Created {
		t.Errorf("second Open = %+v, %v", o, err)
	}
}

// TestDamagedUsesBackup: a damaged list is set aside, its backup is used
// and written back, and changes can be saved again at once.
func TestDamagedUsesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if _, _, err := Open(dir, "valheim", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(f *File) error {
		f.Instances = append(f.Instances, Instance{ID: "minecraft", Game: "minecraft", Settings: game.Values{"maxPlayers": "10"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	backup := string(readFile(t, path+BackupSuffix)) // the first-start list
	stamp := time.Date(2026, 11, 20, 9, 5, 0, 0, time.Local)
	if err := os.Chtimes(path+BackupSuffix, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	const broken = `{"schema":1,"app":{"sta`
	writeFile(t, path, broken)

	f, o, err := Open(dir, "valheim", knows("minecraft"), now)
	if err != nil || o.Status != Damaged || !o.FromBackup || o.SafeMode || o.WriteErr != nil || o.Rebuilt || o.Created {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	if !o.BackupTime.Equal(stamp) {
		t.Errorf("backup time = %v", o.BackupTime)
	}
	if want := "The list of games was damaged; the app used its backup from 20 November 2026 at 09:05."; o.Warning() != want {
		t.Errorf("warning = %q", o.Warning())
	}
	if f.Instance("valheim") == nil || f.Instance("minecraft") != nil {
		t.Errorf("list = %+v", f)
	}
	if want := path + ".damaged-" + now.Format("20060102-150405"); o.Kept != want || string(readFile(t, want)) != broken {
		t.Errorf("damaged file kept as %q", o.Kept)
	}
	if got := string(readFile(t, path)); got != backup {
		t.Errorf("servers.json = %s, want the backup", got)
	}
	if err := Update(path, func(f *File) error { f.App.Stars = nil; return nil }); err != nil {
		t.Errorf("a change after the fallback: %v", err)
	}
	// The next start finds a good list and says nothing.
	if _, o, err := Open(dir, "valheim", nil, now); err != nil || o.Status != OK || o.Warning() != "" {
		t.Errorf("next start: %+v, %v", o, err)
	}
}

// TestUnreadableLeftAlone: a servers.json that can't even be read (here a
// folder in its place; on a real computer a permission or a lock) is
// neither moved nor replaced, since it may be a good list: the backup is
// used for this run, and changes are refused.
func TestUnreadableLeftAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	writeFile(t, path+BackupSuffix, full)
	writeFile(t, filepath.Join(path, "inside"), "x")
	before := snapshot(t, dir)
	f, o, err := Open(dir, "valheim", nil, now)
	if err != nil || o.Status != Damaged || !o.FromBackup || o.WriteErr == nil || o.Kept != "" || o.SafeMode {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	if f.Instance("spacegame") == nil {
		t.Errorf("list = %+v", f)
	}
	if err := Update(path, func(*File) error { return nil }); err == nil {
		t.Error("Update changed an unreadable list")
	}
	if after := snapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("files changed: %v", after)
	}
}

func TestMissingUsesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	writeFile(t, path+BackupSuffix, full)
	writeFile(t, filepath.Join(dir, "games", "minecraft", "run.json"), "{}")
	f, o, err := Open(dir, "valheim", knows("minecraft"), now)
	if err != nil || o.Status != Missing || !o.FromBackup || o.Rebuilt || o.Kept != "" || o.WriteErr != nil {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	if !strings.HasPrefix(o.Warning(), "The list of games was missing; the app used its backup from ") {
		t.Errorf("warning = %q", o.Warning())
	}
	if f.Instance("spacegame") == nil || string(readFile(t, path)) != full {
		t.Errorf("the backup wasn't used and written back: %+v", f)
	}
}

// TestMissingRebuilds: with no list and no usable backup, but games/
// folders, the list is made again from the folders of registered games.
func TestMissingRebuilds(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"minecraft/run.json", "terraria/saves/w.wld", "spacegame/x", "valheim/x", "Necesse/x", "notes.txt"} {
		writeFile(t, filepath.Join(dir, "games", p), "x")
	}
	writeFile(t, filepath.Join(dir, FileName+BackupSuffix), "not JSON") // a damaged backup is no help
	f, o, err := Open(dir, "valheim", knows("valheim", "minecraft", "terraria", "necesse", "notes.txt"), now)
	if err != nil || o.Status != Missing || !o.Rebuilt || o.Created || o.FromBackup || o.SafeMode || o.WriteErr != nil {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	if !slices.Equal(o.Found, []string{"minecraft", "terraria"}) {
		t.Errorf("found %v", o.Found)
	}
	if !strings.HasPrefix(o.Warning(), "The list of games was missing, so the app made it again") {
		t.Errorf("warning = %q", o.Warning())
	}
	var ids []string
	for _, in := range f.Instances {
		ids = append(ids, in.ID)
		if in.ID != "valheim" && (in.Game != in.ID || in.Legacy || in.Ports != nil || in.Settings != nil || in.Discord != nil || !in.Added.Equal(now)) {
			t.Errorf("rebuilt %+v", in)
		}
	}
	if !slices.Equal(ids, []string{"valheim", "minecraft", "terraria"}) || !slices.Equal(f.App.Stars, []string{"valheim"}) {
		t.Errorf("rebuilt list: %v, stars %v", ids, f.App.Stars)
	}
	if again, st, err := Load(filepath.Join(dir, FileName)); err != nil || st != OK || !reflect.DeepEqual(again, f) {
		t.Errorf("written list: %+v %v %v", again, st, err)
	}

	// An empty games/ folder is still a sign there was a list: rebuilt,
	// with the warning, but nothing found.
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, "games"), 0o755)
	if f, o, err := Open(dir, "valheim", nil, now); err != nil || !o.Rebuilt || len(o.Found) != 0 || len(f.Instances) != 1 || o.Warning() == "" {
		t.Errorf("empty games/: %+v, %+v, %v", o, f, err)
	}
}

// TestSafeMode: a newer list, or a damaged one without a usable backup,
// is never touched, and Update refuses to change it.
func TestSafeMode(t *testing.T) {
	newer := `{"schema":2,"app":{"stars":["valheim","minecraft"]},"instances":[{"id":"valheim","game":"valheim","legacy":true}]}`
	cases := map[string]struct {
		file, bak *string
		newer     bool
	}{
		"newer":                   {file: &newer, bak: ptr(`{"schema":1,"app":{"stars":[]},"instances":[]}`), newer: true},
		"damaged, no backup":      {file: ptr(`{`)},
		"damaged, backup damaged": {file: ptr(`{`), bak: ptr(`[]`)},
		"damaged, backup newer":   {file: ptr(`{`), bak: &newer, newer: true},
		"missing, backup newer":   {bak: &newer, newer: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if c.file != nil {
				writeFile(t, path, *c.file)
			}
			if c.bak != nil {
				writeFile(t, path+BackupSuffix, *c.bak)
			}
			writeFile(t, filepath.Join(dir, "games", "minecraft", "run.json"), "{}")
			before := snapshot(t, dir)
			_, o, err := Open(dir, "valheim", knows("minecraft"), now)
			if err == nil || !o.SafeMode || errors.Is(err, ErrNewer) != c.newer || o.Warning() != "" || o.Kept != "" {
				t.Fatalf("Open = %+v, %v", o, err)
			}
			err = Update(path, func(f *File) error { t.Error("change called"); return nil })
			if err == nil || errors.Is(err, ErrNewer) != (name == "newer") {
				t.Errorf("Update = %v", err)
			}
			if after := snapshot(t, dir); !maps.Equal(before, after) {
				t.Errorf("files changed: %v", after)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Update(path, func(*File) error { return nil }); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("Update of a missing list = %v", err)
	}
	if _, _, err := Open(dir, "valheim", nil, now); err != nil {
		t.Fatal(err)
	}
	first := string(readFile(t, path))

	// An error from the change writes nothing.
	oops := errors.New("oops")
	if err := Update(path, func(f *File) error { f.App.Stars = nil; return oops }); err != oops {
		t.Errorf("Update = %v", err)
	}
	// Neither does a change that changes nothing.
	if err := Update(path, func(*File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + BackupSuffix); !os.IsNotExist(err) || string(readFile(t, path)) != first {
		t.Errorf("a no-op wrote something (backup: %v)", err)
	}

	// A change keeps the previous list as the backup.
	add := func(id string, legacy bool) func(*File) error {
		return func(f *File) error {
			f.Instances = append(f.Instances, Instance{ID: id, Game: strings.TrimSuffix(id, "-2"), Legacy: legacy})
			return nil
		}
	}
	if err := Update(path, add("minecraft", false)); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, path+BackupSuffix)) != first {
		t.Error("the backup isn't the previous list")
	}

	// A change that would leave the list unsafe is refused.
	for name, change := range map[string]func(*File) error{
		"twice":       add("minecraft", false),
		"bad id":      add("../minecraft", false),
		"two legacy":  add("minecraft-2", true),
		"schema hack": func(f *File) error { f.Instances[1].ID = ""; return nil },
	} {
		before := snapshot(t, dir)
		if err := Update(path, change); err == nil {
			t.Errorf("%s: change saved", name)
		}
		if after := snapshot(t, dir); !maps.Equal(before, after) {
			t.Errorf("%s: files changed", name)
		}
	}
	// A problem already in the file doesn't block other changes.
	writeFile(t, path, `{"schema":1,"app":{"stars":[]},"instances":[{"id":"Odd","game":"odd"}]}`)
	if err := Update(path, func(f *File) error { f.App.Stars = []string{"Odd"}; return nil }); err != nil {
		t.Errorf("Update with an old problem: %v", err)
	}
}

// TestUpdateOneAtATime: changes made at once all land.
func TestUpdateOneAtATime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if _, _, err := Open(dir, "", nil, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := Update(path, func(f *File) error {
				f.App.Stars = append(f.App.Stars, fmt.Sprint("g", i))
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f, _, err := Load(path)
	if err != nil || len(f.App.Stars) != 20 {
		t.Errorf("stars = %v (%v)", f.App.Stars, err)
	}
}
