package game

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// bedPlayed and bedRunInfo are copies of bed004b's manager.played and
// manager.runInfo (manager.go:173-201), the shape of runtime.json that
// installed copies write and read. A Record must read what they write and
// write what they read, or an update or a rollback could no longer take
// over a running Valheim (invariant I4).
type bedPlayed struct {
	Game     string            `json:"game,omitempty"`
	Mods     map[string]string `json:"mods"`
	Password string            `json:"password,omitempty"`
}

type bedRunInfo struct {
	PID         int       `json:"pid"`
	Started     time.Time `json:"started"`
	Launched    time.Time `json:"launched"`
	LogPath     string    `json:"logPath"`
	World       string    `json:"world"`
	ArgsKey     string    `json:"argsKey"`
	Exe         string    `json:"exe"`
	Announced   string    `json:"announced,omitempty"`
	Posted      time.Time `json:"posted,omitempty"`
	PostError   string    `json:"postError,omitempty"`
	Played      bedPlayed `json:"played"`
	NoCrossplay bool      `json:"noCrossplay,omitempty"`
	Server      string    `json:"server,omitempty"`
	Port        int       `json:"port,omitempty"`
	Password    string    `json:"password,omitempty"`
	ModsCode    string    `json:"modsCode,omitempty"`
	CodeMods    []string  `json:"codeMods,omitempty"`
}

var (
	t0 = time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)
	t1 = t0.Add(time.Second)
	t2 = t0.Add(time.Minute)
)

// bedSample is a runtime.json as bed004b writes it, with every field set.
func bedSample() bedRunInfo {
	return bedRunInfo{
		PID: 4242, Started: t0, Launched: t1, LogPath: "/data/logs/server-2026-11-20_18-30-00.log",
		World: "Midgard", ArgsKey: "0123456789abcdef", Exe: "/data/server/valheim_server.x86_64",
		Announced: "604510", Posted: t2, PostError: "Couldn't reach Discord.",
		Played:      bedPlayed{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: "c876de2de4744f10"},
		NoCrossplay: true, Server: "Viking Night", Port: 2456, Password: "raven-fjord-4821",
		ModsCode: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b", CodeMods: []string{"riverheim", "seasons"},
	}
}

// legacySample is the same run as a Record, the way the Valheim adapter
// keeps it: Valheim's own fields in Extra.
func legacySample() Record {
	return Record{
		PID: 4242, Started: t0, Launched: t1, LogPath: "/data/logs/server-2026-11-20_18-30-00.log",
		World: "Midgard", ArgsKey: "0123456789abcdef", Exe: "/data/server/valheim_server.x86_64",
		Announced: "604510", Posted: t2, PostError: "Couldn't reach Discord.",
		Played: Played{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: "c876de2de4744f10"},
		Server: "Viking Night", Port: 2456, Password: "raven-fjord-4821",
		Extra: map[string]json.RawMessage{
			"noCrossplay": json.RawMessage(`true`),
			"modsCode":    json.RawMessage(`"019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b"`),
			"codeMods":    json.RawMessage(`["riverheim","seasons"]`),
		},
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

func keysOf(t *testing.T, b []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, b)
	}
	return slices.Sorted(maps.Keys(m))
}

func asGeneric(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, b)
	}
	return m
}

// TestRecordReadsLegacyRuntimeGolden reads R1's golden runtime.json (written
// by bed004b's structs) into a Record and writes it back: every key and
// value must survive, with Valheim's own fields carried in Extra.
func TestRecordReadsLegacyRuntimeGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "manager", "testdata", "legacy", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal(golden, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.PID != 4242 || rec.World != "Midgard" || rec.Port != 2456 || rec.Password != "raven-fjord-4821" {
		t.Errorf("named fields not read: %+v", rec)
	}
	wantPlayed := Played{Game: "1.0.21", Mods: map[string]string{"riverheim": "1.2.3"}, Password: "c876de2de4744f10"}
	if !reflect.DeepEqual(rec.Played, wantPlayed) {
		t.Errorf("played = %+v, want %+v", rec.Played, wantPlayed)
	}
	if got := slices.Sorted(maps.Keys(rec.Extra)); !slices.Equal(got, []string{"codeMods", "modsCode", "noCrossplay"}) {
		t.Errorf("Extra keys = %v, want Valheim's codeMods, modsCode, noCrossplay", got)
	}
	if rec.Game != "" || rec.Instance != "" || rec.Name != "" || rec.Ports != nil || rec.Secrets != nil {
		t.Errorf("a legacy file filled other instances' fields: %+v", rec)
	}

	out := mustMarshal(t, rec)
	if got, want := asGeneric(t, out), asGeneric(t, golden); !reflect.DeepEqual(got, want) {
		t.Errorf("written back differently:\n got %s\nwant %s", out, golden)
	}
}

// TestRecordWritesWhatBed004bReads: a Record written by a new build must
// read back field for field in bed004b's runInfo (the rollback case).
func TestRecordWritesWhatBed004bReads(t *testing.T) {
	b := mustMarshal(t, legacySample())
	var old bedRunInfo
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if want := bedSample(); !reflect.DeepEqual(old, want) {
		t.Errorf("bed004b reads\n %+v\nwant\n %+v\nfrom %s", old, want, b)
	}
	// And the same keys bed004b itself writes, nothing more.
	if got, want := keysOf(t, b), keysOf(t, mustMarshal(t, bedSample())); !slices.Equal(got, want) {
		t.Errorf("keys = %v\nwant %v", got, want)
	}
}

// TestRecordReadsWhatBed004bWrites: a runtime.json written by bed004b (the
// update case) reads into a Record with the typed played and Extra.
func TestRecordReadsWhatBed004bWrites(t *testing.T) {
	var rec Record
	if err := json.Unmarshal(mustMarshal(t, bedSample()), &rec); err != nil {
		t.Fatal(err)
	}
	if want := legacySample(); !reflect.DeepEqual(rec, want) {
		t.Errorf("read\n %+v\nwant\n %+v", rec, want)
	}
}

// TestRecordOlderRuntime: a server started by an older build has no port
// and no played mods; it still reads, and Played.Mods stays nil ("started
// by an older version").
func TestRecordOlderRuntime(t *testing.T) {
	var rec Record
	in := `{"pid":7,"started":"2026-11-20T18:30:00Z","launched":"2026-11-20T18:30:01Z","logPath":"l","world":"w","argsKey":"k","exe":"e"}`
	if err := json.Unmarshal([]byte(in), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.PID != 7 || rec.Port != 0 || rec.Played.Mods != nil || rec.Extra != nil {
		t.Errorf("got %+v", rec)
	}
	// Written back, played is there with "mods": null, as bed004b writes it.
	out := string(mustMarshal(t, rec))
	if !strings.Contains(out, `"played":{"mods":null}`) {
		t.Errorf("played not written as bed004b does: %s", out)
	}
}

// TestRecordOtherInstance: the fields only other instances use, and an
// empty (not null) mods map, survive a round trip and stay out of Extra.
func TestRecordOtherInstance(t *testing.T) {
	rec := Record{
		PID: 99, Started: t0, Launched: t1, LogPath: "/d/games/minecraft/logs/minecraft-2026-11-20_18-30-00.log",
		World: "Riverbend", ArgsKey: "fp", Exe: "/d/games/minecraft/runtime/jdk/bin/java",
		Played: Played{Game: "26.3", Mods: map[string]string{}},
		Server: "Friends' Minecraft", Port: 25565,
		Game: "minecraft", Instance: "minecraft", Name: "Minecraft",
		Ports:   map[string]int{"game": 25565, "rcon": 25575},
		Secrets: map[string]string{"rcon": "s3cret"},
		Extra:   map[string]json.RawMessage{"allowList": json.RawMessage(`"auto"`)},
	}
	b := mustMarshal(t, rec)
	if !strings.Contains(string(b), `"mods":{}`) {
		t.Errorf("empty mods not written as {}: %s", b)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rec) {
		t.Errorf("round trip\n got %+v\nwant %+v", back, rec)
	}
}

// TestRecordLegacyNeverWritesOtherFields: the legacy instance leaves the
// other instances' fields empty, so runtime.json never gains them.
func TestRecordLegacyNeverWritesOtherFields(t *testing.T) {
	keys := keysOf(t, mustMarshal(t, legacySample()))
	for _, k := range []string{"game", "instance", "name", "ports", "secrets"} {
		if slices.Contains(keys, k) {
			t.Errorf("runtime.json would gain %q", k)
		}
	}
}

func TestRecordExtraClashIsRefused(t *testing.T) {
	for _, key := range []string{"pid", "World", "PLAYED", "secrets"} {
		rec := legacySample()
		rec.Extra[key] = json.RawMessage(`1`)
		if _, err := json.Marshal(rec); err == nil {
			t.Errorf("Extra key %q that names a field of Record was written", key)
		}
	}
}

func TestRecordExtraMustBeJSON(t *testing.T) {
	rec := legacySample()
	rec.Extra["broken"] = json.RawMessage(`{"a":`)
	if _, err := json.Marshal(rec); err == nil {
		t.Error("an invalid Extra value was written")
	}
}

// TestRecordExtraIsCompactAndSorted: values are compacted and keys sorted,
// so the same record always gives the same bytes.
func TestRecordExtraIsCompactAndSorted(t *testing.T) {
	rec := Record{PID: 1, Extra: map[string]json.RawMessage{
		"zeta":  json.RawMessage(" [ 1 , 2 ] "),
		"alpha": json.RawMessage(`{ "x" : true }`),
		"mid":   nil,
	}}
	out := string(mustMarshal(t, rec))
	if !strings.HasSuffix(out, `,"alpha":{"x":true},"mid":null,"zeta":[1,2]}`) {
		t.Errorf("got %s", out)
	}
}

// TestRecordWithoutExtra is exactly what encoding/json would write for the
// named fields alone.
func TestRecordWithoutExtra(t *testing.T) {
	rec := legacySample()
	rec.Extra = nil
	if got, want := string(mustMarshal(t, rec)), string(mustMarshal(t, recordFields(rec))); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestRecordMarshalForms: the inlining works for a value, a pointer, a
// field of another struct and an indented file (how runtime.json is written).
func TestRecordMarshalForms(t *testing.T) {
	rec := legacySample()
	want := string(mustMarshal(t, rec))
	if got := string(mustMarshal(t, &rec)); got != want {
		t.Errorf("pointer: %s", got)
	}
	wrapped := string(mustMarshal(t, struct{ R Record }{rec}))
	if wrapped != `{"R":`+want+`}` {
		t.Errorf("in a struct: %s", wrapped)
	}
	indented, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(indented), "\n  \"noCrossplay\": true") {
		t.Errorf("Extra not indented with the rest:\n%s", indented)
	}
	var back Record
	if err := json.Unmarshal(indented, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rec) {
		t.Errorf("indented round trip\n got %+v\nwant %+v", back, rec)
	}
}

// TestRecordUnmarshalReplacesExtra: reading into a used Record leaves no
// stale adapter field behind; null leaves it alone, as for any struct.
func TestRecordUnmarshalReplacesExtra(t *testing.T) {
	rec := legacySample()
	if err := json.Unmarshal([]byte(`{"pid":5,"fresh":1}`), &rec); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(rec.Extra)); !slices.Equal(got, []string{"fresh"}) {
		t.Errorf("Extra = %v, want only fresh", got)
	}
	before := legacySample()
	rec = legacySample()
	if err := json.Unmarshal([]byte(`null`), &rec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec, before) {
		t.Errorf("null changed the record: %+v", rec)
	}
	if err := json.Unmarshal([]byte(`[1]`), &rec); err == nil {
		t.Error("a JSON array read as a record")
	}
}
