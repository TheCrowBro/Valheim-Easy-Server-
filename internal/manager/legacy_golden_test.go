package manager

// The files in the data folder that every copy of the app shares
// (docs/dev/multigame/spec.md 1.3, I2 and I4). After an update the new copy
// reads what the old one wrote, and after a rollback the old copy reads
// what the new one wrote, so each file keeps its name, its keys and their
// types. A file may only gain a key on the allowed list below.
//
// testdata/legacy holds the files as bed004b's code wrote them. The tests
// write the same data with today's code and compare: every key in the
// recorded file must still be there with the same JSON type, and a new key
// must be on the allowed list. They also check that today's code reads the
// recorded files back with the same values.
//
// Goldens are recorded once and never changed to make a test pass: the
// code must match them. VES_RECORD_GOLDEN=1 writes a golden file that
// doesn't exist yet (for a new file); it never overwrites one.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/stats"
)

const recordGoldenEnv = "VES_RECORD_GOLDEN"

// golden compares got with the golden file at path (under testdata), or
// records it there if the file doesn't exist and recording is on.
func golden(t *testing.T, path string, got []byte) []byte {
	t.Helper()
	path = filepath.Join("testdata", path)
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv(recordGoldenEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s", path)
		return got
	}
	if err != nil {
		t.Fatalf("golden %s: %v (set %s=1 to record a new one)", path, err, recordGoldenEnv)
	}
	return want
}

// indentedJSON is v as the golden files hold it.
func indentedJSON(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// legacyAllowed are the keys a legacy file may gain (spec 1.3), as paths:
// $ is the file, .key an object's key and [] an array's items.
var legacyAllowed = map[string][]string{
	"activity.json": {"$[].game"},      // which game an entry is about
	"jetkvm.json":   {"$.setup.games"}, // the waker's commands for several games
}

// legacyAt is when everything in the sample files happened.
var legacyAt = time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)

func legacyRunInfo() runInfo {
	return runInfo{PID: 4242, Started: legacyAt, Launched: legacyAt.Add(time.Second),
		LogPath: "/data/logs/server-2026-11-20_18-30-00.log", World: "Midgard", ArgsKey: "0123456789abcdef",
		Exe: "/data/server/valheim_server.x86_64", Announced: "604510", Posted: legacyAt.Add(time.Minute),
		PostError:   "Couldn't reach Discord.",
		Played:      played{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: hashPassword("raven-fjord-4821")},
		NoCrossplay: true, Server: "Viking Night", Port: 2456, Password: "raven-fjord-4821",
		ModsCode: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b", CodeMods: []string{"riverheim"}}
}

func legacySettings() config.Settings {
	return config.Settings{
		ServerName: "Viking Night", Password: "raven-fjord-4821", World: "Midgard", Port: 2456,
		Public: true, Crossplay: true, SaveIntervalMinutes: 15, Backups: 4, BackupShortMinutes: 120, BackupLongMinutes: 720,
		UpdateOnStart: true, AutoUpdate: true, AutoRestart: true, StartWithApp: true, StartAfterWake: true,
		IdleHibernate: true, IdleMinutes: 60, RestMode: config.RestSleep,
		Discord: true, DiscordWebhook: "https://discord.com/api/webhooks/123/abc", DiscordPassword: true,
		DiscordStopped: true, DiscordDaily: true, DiscordBotToken: "bot-token", JetKVM: "192.168.1.30", DiscordWake: true,
		WorldOptions: map[string]config.WorldOptions{"Midgard": {
			Preset: "hard", Modifiers: map[string]string{"combat": "hard"}, Keys: []string{"nomap"},
			Off: []string{"fire"}, Mods: []string{"riverheim", "planteverything"},
		}},
		ServerDir: "/srv/valheim/server", SaveDir: "/srv/valheim/saves", ExtraArgs: "-instanceid 2",
	}
}

func legacyRecords() recordsData {
	return recordsData{Runs: map[string][]records.Event{"server-2026-11-20_18-30-00.log": {
		{T: legacyAt.Unix(), Kind: records.Join, Name: "Astrid"},
		{T: legacyAt.Add(time.Hour).Unix(), Kind: records.Death, Name: "Astrid"},
		{T: legacyAt.Add(2 * time.Hour).Unix(), Kind: records.Leave, Name: "Astrid"},
	}}, Posted: "2026-11-19"}
}

func legacyWaker() wakerFile {
	return wakerFile{HostKey: []byte("ssh-host-key"), AppID: "app1", Bot: "Valheim Bot",
		Setup: wakerSetup{Version: "build-7", MAC: "3C:07:54:5A:21:9F", Query: "192.168.1.20:2457", Name: "Viking Night", Guild: "112233"},
		At:    legacyAt}
}

func legacyCodes() map[string]savedCode {
	return map[string]savedCode{"Midgard|Gurebu-Riverheim-1.2.3": {Code: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b", Made: legacyAt}}
}

// bareManager is a manager that has loaded the data folder but not
// started: enough to save and load its files.
func bareManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m, err := New(Options{DataDir: dir, Logger: log.New(io.Discard, "", 0), NoPublicIP: true,
		HibernateStatus: func() (bool, string) { return true, "" }, SleepStatus: func() (bool, string) { return true, "" }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// writeLegacyFiles writes every legacy file with sample data, the way the
// app writes each one.
func writeLegacyFiles(t *testing.T, dir string) {
	t.Helper()
	m := bareManager(t, dir)
	if err := config.Save(filepath.Join(dir, "settings.json"), legacySettings()); err != nil {
		t.Fatal(err)
	}
	saveRuntime(m.dataFile("runtime.json"), legacyRunInfo())

	m.mu.Lock()
	m.known = map[string]*KnownPlayer{"Astrid": {Name: "Astrid", PlatformID: "Steam_76561198000000001",
		FirstSeen: legacyAt, LastSeen: legacyAt.Add(2 * time.Hour), PlaySeconds: 7200, Sessions: 3}}
	m.recs = legacyRecords()
	m.lastPlayed = &played{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: hashPassword("raven-fjord-4821")}
	m.wk.file = legacyWaker()
	m.mu.Unlock()
	m.byPlayers = &stats.ByPlayers{Since: legacyAt, Buckets: map[int]*stats.Bucket{1: {
		Players: 1, Seconds: 600, CPU: 12.5, CPUMax: 30, CPUSys: 25, Mem: 2.5e9, MemMax: 3e9, GPU: 4, Net: 1500}}}
	// history.json keeps the last day only, so this sample is recent.
	m.history.Add(stats.Sample{T: time.Now().Add(-time.Minute).UnixMilli(), CPU: 12.5, CPUSys: 25, Mem: 2500000000,
		MemSys: 8000000000, GPU: 4, NetUp: 1000, NetDown: 500, Players: 1, Running: true})
	m.addActivityAt(legacyAt, "join", "Astrid", "Astrid joined the game.")

	m.saveData() // players, stats, history, activity
	m.saveRecords()
	m.savePlayed()
	m.saveWaker()
	m.saveUpdateRecord(updateRecord{From: 41, To: 42, Title: "Safety nets before several games", Failed: 40})
	// modscodes.json is written by modsCode, which asks Thunderstore for
	// the code first; this is how it writes the file.
	b, err := json.MarshalIndent(legacyCodes(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(m.dataFile("modscodes.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

var legacyFiles = []string{
	"settings.json", "runtime.json", "players.json", "records.json", "lastplayed.json", "stats.json",
	"history.json", "activity.json", "modscodes.json", "jetkvm.json", "update.json",
}

func TestLegacyFileShapes(t *testing.T) {
	dir := t.TempDir()
	writeLegacyFiles(t, dir)
	for _, name := range legacyFiles {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s isn't written where it was: %v", name, err)
			continue
		}
		want := golden(t, filepath.Join("legacy", name), got)
		wantShape, err := jsonShape(want)
		if err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		gotShape, err := jsonShape(got)
		if err != nil {
			t.Errorf("%s isn't JSON any more: %v", name, err)
			continue
		}
		for _, problem := range compareShapes(wantShape, gotShape, legacyAllowed[name]) {
			t.Errorf("%s: %s", name, problem)
		}
	}
}

// Today's code reads the files bed004b wrote with the same values.
func TestLegacyFilesRead(t *testing.T) {
	dir := t.TempDir()
	for _, name := range legacyFiles {
		b, err := os.ReadFile(filepath.Join("testdata", "legacy", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := bareManager(t, dir) // loads them all

	if got := m.Settings(); !reflect.DeepEqual(got, legacySettings()) {
		t.Errorf("settings.json reads as %+v\nwant %+v", got, legacySettings())
	}
	if got, err := loadRuntime(m.dataFile("runtime.json")); err != nil || !reflect.DeepEqual(got, legacyRunInfo()) {
		t.Errorf("runtime.json reads as %+v (%v)\nwant %+v", got, err, legacyRunInfo())
	}
	if p := m.known["Astrid"]; p == nil || p.PlatformID != "Steam_76561198000000001" || !p.FirstSeen.Equal(legacyAt) ||
		!p.LastSeen.Equal(legacyAt.Add(2*time.Hour)) || p.PlaySeconds != 7200 || p.Sessions != 3 {
		t.Errorf("players.json reads as %+v", p)
	}
	if !reflect.DeepEqual(m.recs, legacyRecords()) {
		t.Errorf("records.json reads as %+v", m.recs)
	}
	if want := (played{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: hashPassword("raven-fjord-4821")}); m.lastPlayed == nil || !reflect.DeepEqual(*m.lastPlayed, want) {
		t.Errorf("lastplayed.json reads as %+v", m.lastPlayed)
	}
	if got := m.wk.file; !bytes.Equal(got.HostKey, legacyWaker().HostKey) || got.AppID != "app1" || got.Bot != "Valheim Bot" ||
		got.Setup != legacyWaker().Setup || !got.At.Equal(legacyAt) {
		t.Errorf("jetkvm.json reads as %+v", got)
	}
	if acts, _ := m.activity.all(); len(acts) != 1 || acts[0].Kind != "join" || acts[0].Name != "Astrid" ||
		acts[0].Text != "Astrid joined the game." || !acts[0].Time.Equal(legacyAt) || acts[0].Seq != 1 {
		t.Errorf("activity.json reads as %+v", acts)
	}
	if b := m.byPlayers.Buckets[1]; !m.byPlayers.Since.Equal(legacyAt) || b == nil || *b != (stats.Bucket{
		Players: 1, Seconds: 600, CPU: 12.5, CPUMax: 30, CPUSys: 25, Mem: 2.5e9, MemMax: 3e9, GPU: 4, Net: 1500}) {
		t.Errorf("stats.json reads as since %v, %+v", m.byPlayers.Since, b)
	}
	// history.json: the app keeps only the last day, so the values are
	// read as the app's own type.
	var samples []stats.Sample
	if b, _ := os.ReadFile(m.dataFile("history.json")); json.Unmarshal(b, &samples) != nil || len(samples) != 1 ||
		samples[0].CPU != 12.5 || samples[0].Mem != 2500000000 || samples[0].Players != 1 || !samples[0].Running {
		t.Errorf("history.json reads as %+v", samples)
	}
	var rec updateRecord
	if b, _ := os.ReadFile(m.dataFile("update.json")); json.Unmarshal(b, &rec) != nil ||
		rec != (updateRecord{From: 41, To: 42, Title: "Safety nets before several games", Failed: 40}) {
		t.Errorf("update.json reads as %+v", rec)
	}
	codes := map[string]savedCode{}
	if b, _ := os.ReadFile(m.dataFile("modscodes.json")); json.Unmarshal(b, &codes) != nil || !reflect.DeepEqual(codes, legacyCodes()) {
		t.Errorf("modscodes.json reads as %+v", codes)
	}
}

// bed004bRunInfo is runtime.json as bed004b reads and writes it: a copy of
// its runInfo, so changing the real one can't change this one too.
type bed004bRunInfo struct {
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Launched  time.Time `json:"launched"`
	LogPath   string    `json:"logPath"`
	World     string    `json:"world"`
	ArgsKey   string    `json:"argsKey"`
	Exe       string    `json:"exe"`
	Announced string    `json:"announced,omitempty"`
	Posted    time.Time `json:"posted,omitempty"`
	PostError string    `json:"postError,omitempty"`
	Played    struct {
		Game     string            `json:"game,omitempty"`
		Mods     map[string]string `json:"mods"`
		Password string            `json:"password,omitempty"`
	} `json:"played"`
	NoCrossplay bool     `json:"noCrossplay,omitempty"`
	Server      string   `json:"server,omitempty"`
	Port        int      `json:"port,omitempty"`
	Password    string   `json:"password,omitempty"`
	ModsCode    string   `json:"modsCode,omitempty"`
	CodeMods    []string `json:"codeMods,omitempty"`
}

func bed004bSample() bed004bRunInfo {
	r := bed004bRunInfo{PID: 4242, Started: legacyAt, Launched: legacyAt.Add(time.Second),
		LogPath: "/data/logs/server-2026-11-20_18-30-00.log", World: "Midgard", ArgsKey: "0123456789abcdef",
		Exe: "/data/server/valheim_server.x86_64", Announced: "604510", Posted: legacyAt.Add(time.Minute),
		PostError: "Couldn't reach Discord.", NoCrossplay: true, Server: "Viking Night", Port: 2456,
		Password: "raven-fjord-4821", ModsCode: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b", CodeMods: []string{"riverheim"}}
	r.Played.Game, r.Played.Mods, r.Played.Password = "1.0.21", map[string]string{"riverheim": "1.2.3"}, hashPassword("raven-fjord-4821")
	return r
}

// runtime.json is how a copy of the app takes over a Valheim server that
// another copy started, across an update or a rollback (I4).
func TestRuntimeJSONCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")

	// Written now, read by bed004b.
	saveRuntime(path, legacyRunInfo())
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old bed004bRunInfo
	if err := json.Unmarshal(b, &old); err != nil || !reflect.DeepEqual(old, bed004bSample()) {
		t.Errorf("bed004b reads today's runtime.json as %+v (%v)\nwant %+v", old, err, bed004bSample())
	}

	// Written by bed004b, read now.
	b, _ = json.MarshalIndent(bed004bSample(), "", "  ")
	os.WriteFile(path, b, 0o600)
	if got, err := loadRuntime(path); err != nil || !reflect.DeepEqual(got, legacyRunInfo()) {
		t.Errorf("bed004b's runtime.json reads as %+v (%v)\nwant %+v", got, err, legacyRunInfo())
	}

	// Written by a copy from before the port and password were kept: a
	// server it started is still taken over, with port 0 meaning "the
	// settings' port".
	os.WriteFile(path, []byte(`{"pid": 4242, "started": "2026-11-20T18:30:00Z", "launched": "2026-11-20T18:30:01Z",
  "logPath": "/data/logs/server-2026-11-20_18-30-00.log", "world": "Midgard", "argsKey": "0123456789abcdef",
  "exe": "/data/server/valheim_server.x86_64"}`), 0o600)
	want := runInfo{PID: 4242, Started: legacyAt, Launched: legacyAt.Add(time.Second),
		LogPath: "/data/logs/server-2026-11-20_18-30-00.log", World: "Midgard", ArgsKey: "0123456789abcdef",
		Exe: "/data/server/valheim_server.x86_64"}
	if got, err := loadRuntime(path); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("an older runtime.json reads as %+v (%v)\nwant %+v", got, err, want)
	}
}

// jsonShape lists every place in a JSON document with its JSON type: $ is
// the document, .key an object's key and [] an array's items (all items
// together).
func jsonShape(b []byte) (map[string]string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out := map[string]string{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		kind := jsonKind(v)
		if prev, ok := out[path]; ok && prev != kind {
			kinds := strings.Split(prev, "|")
			if !contains(kinds, kind) {
				kinds = append(kinds, kind)
				sort.Strings(kinds)
			}
			kind = strings.Join(kinds, "|")
		}
		out[path] = kind
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				walk(path+"."+k, c)
			}
		case []any:
			for _, c := range x {
				walk(path+"[]", c)
			}
		}
	}
	walk("$", v)
	return out, nil
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// compareShapes lists what's wrong with got compared to want: a place
// that's gone, one whose type changed, or a new one not under a path in
// allowed.
func compareShapes(want, got map[string]string, allowed []string) []string {
	var out []string
	for _, p := range sortedPaths(want) {
		if g, ok := got[p]; !ok {
			out = append(out, p+" is gone: older copies of the app need it")
		} else if g != want[p] {
			out = append(out, p+" is a "+g+", was a "+want[p])
		}
	}
	for _, p := range sortedPaths(got) {
		if _, ok := want[p]; ok {
			continue
		}
		ok := false
		for _, a := range allowed {
			ok = ok || p == a || strings.HasPrefix(p, a+".") || strings.HasPrefix(p, a+"[]")
		}
		if !ok {
			out = append(out, p+" is new and not on the allowed list")
		}
	}
	return out
}

func sortedPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
