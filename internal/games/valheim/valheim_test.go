package valheim

import (
	"encoding/json"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/steamcmd"
)

// The goldens recorded from bed004b's code (internal/manager/testdata/golden)
// hold what Valheim did before it moved here. The adapter must give exactly
// the same again (spec 7.2 and I12); the manager's own golden tests check
// the same files through the manager.
const goldenDir = "../../manager/testdata/golden"

func readGolden(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// testStore is a LegacyStore over settings held in memory.
type testStore struct {
	mu    sync.Mutex
	s     config.Settings
	data  string
	notes []string
}

func newStore(s config.Settings, data string) *testStore { return &testStore{s: s, data: data} }

func (t *testStore) Settings() config.Settings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.Clone()
}
func (t *testStore) Paths() config.Paths { return config.ResolvePaths(t.data, t.Settings()) }
func (t *testStore) Update(change func(*config.Settings) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.s.Clone()
	if err := change(&next); err != nil {
		return err
	}
	t.s = next
	return nil
}
func (t *testStore) Installer() steamcmd.Installer { return nil }
func (t *testStore) ModsURL() string               { return "" }
func (t *testStore) Note(kind, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.notes = append(t.notes, kind+": "+text)
}

func newServer(t *testing.T, s config.Settings) (*Server, *testStore) {
	t.Helper()
	st := newStore(s, t.TempDir())
	srv := Game{}.New(game.Env{Instance: ID, Legacy: st, Log: log.New(io.Discard, "", 0)}).(*Server)
	return srv, st
}

func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok || game.Legacy() != g {
		t.Fatalf("Valheim isn't registered as the legacy game: %v %v", ok, game.Legacy())
	}
	in := g.Info()
	if !in.Legacy || in.Short != "Valheim" || in.Stage != game.Ready || in.Presence != game.ByName || in.MemoryMB != 4096 {
		t.Fatalf("info = %+v", in)
	}
}

// The fingerprint is runtime.json's argsKey: the same settings must give
// the key bed004b wrote, through the game values and back.
func TestFingerprintGolden(t *testing.T) {
	var cases []struct {
		Name     string          `json:"name"`
		Settings config.Settings `json:"settings"`
		SaveDir  string          `json:"saveDir"`
		ArgsKey  string          `json:"argsKey"`
	}
	readGolden(t, "serverargs.json", &cases)
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		if got := ArgsKey(c.Settings, c.SaveDir); got != c.ArgsKey {
			t.Errorf("%s: ArgsKey %q, want %q", c.Name, got, c.ArgsKey)
		}
		srv, _ := newServer(t, c.Settings)
		// The store's folders are under a temporary data folder; the
		// golden's save folder is given as an override.
		s := c.Settings.Clone()
		s.SaveDir = c.SaveDir
		srv.store = newStore(s, t.TempDir())
		got := srv.Fingerprint(srv.Store().Get(), "")
		if got != c.ArgsKey {
			t.Errorf("%s: Fingerprint %q, want %q", c.Name, got, c.ArgsKey)
		}
	}
}

func TestExitHintsGolden(t *testing.T) {
	var cases []struct {
		Name  string   `json:"name"`
		Lines []string `json:"lines"`
		Hint  string   `json:"hint"`
	}
	readGolden(t, "exithints.json", &cases)
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		// The manager hands the adapter the newest 80 lines.
		tail := c.Lines
		if len(tail) > 80 {
			tail = tail[len(tail)-80:]
		}
		if got := ExitHint(tail); got != c.Hint {
			t.Errorf("%s: %q, want %q", c.Name, got, c.Hint)
		}
	}
}

type modRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

func modInfos(t *testing.T, refs []modRef) []mods.Info {
	var out []mods.Info
	for _, r := range refs {
		md, ok := mods.Find(r.ID)
		if !ok {
			t.Fatalf("no mod %q", r.ID)
		}
		out = append(out, mods.Info{Mod: md, Version: r.Version})
	}
	return out
}

func TestOnlineTextGolden(t *testing.T) {
	var g struct {
		Online []struct {
			Name, Server, World, Code, Address, Password, ModsCode, Text string
			Mods, Optional                                               []modRef
			Reminder                                                     []string
		} `json:"online"`
	}
	readGolden(t, "discord.json", &g)
	if len(g.Online) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range g.Online {
		got := OnlineMessage{Server: c.Server, World: c.World, Code: c.Code, Address: c.Address, Password: c.Password,
			Mods: modInfos(t, c.Mods), Optional: modInfos(t, c.Optional), ModsCode: c.ModsCode, Reminder: c.Reminder}.Text()
		if got != c.Text {
			t.Errorf("%s:\n%s\nwant:\n%s", c.Name, got, c.Text)
		}
	}
}

const localTime = "2006-01-02 15:04:05"

func localString(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.In(time.Local).Format(localTime)
}

// What the Monitor makes of each sample log is what bed004b's tracker made
// of it: the Log page's lines, the feed, who's online, and the records.
func TestMonitorGolden(t *testing.T) {
	type activity struct {
		Kind       string `json:"kind"`
		At         string `json:"at"`
		Name       string `json:"name,omitempty"`
		PlatformID string `json:"platformId,omitempty"`
		Text       string `json:"text"`
	}
	type player struct {
		Name       string `json:"name"`
		PlatformID string `json:"platformId,omitempty"`
		Since      string `json:"since"`
	}
	type eventAt struct {
		At   string `json:"at"`
		Kind string `json:"kind"`
		Name string `json:"name"`
	}
	type state struct {
		Ready           bool   `json:"ready"`
		JoinCode        string `json:"joinCode"`
		PublicAddr      string `json:"publicAddr"`
		Version         string `json:"version"`
		SteamAt         string `json:"steamAt"`
		LastSave        string `json:"lastSave"`
		Stopping        bool   `json:"stopping"`
		StoppingAt      string `json:"stoppingAt"`
		CrossplayFailed bool   `json:"crossplayFailed"`
	}
	type tcase struct {
		Name       string            `json:"name"`
		Log        string            `json:"log"`
		Known      map[string]string `json:"known,omitempty"`
		NoJoinCode bool              `json:"noJoinCode,omitempty"`
		Lines      []string          `json:"lines"`
		Activities []activity        `json:"activities"`
		Players    []player          `json:"players"`
		State      state             `json:"state"`
		Records    []eventAt         `json:"records"`
	}
	var cases []tcase
	readGolden(t, "tracker.json", &cases)
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	clock, err := time.ParseInLocation(localTime, "2026-11-20 12:00:00", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range cases {
		path := filepath.Join(goldenDir, "tracker", want.Log)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		known := func(name string) string { return want.Known[name] }
		var rec game.Record
		setNoCrossplay(&rec, want.NoJoinCode)
		mon := NewMonitor(rec, known)
		got := tcase{Name: want.Name, Log: want.Log, Known: want.Known, NoJoinCode: want.NoJoinCode,
			Lines: []string{}, Activities: []activity{}, Players: []player{}, Records: []eventAt{}}
		// As a running server's log: each line as it comes, at this
		// computer's time, which here is the line's own.
		now := clock
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimRight(line, "\r")
			if lt := lineTime(line); !lt.IsZero() {
				now = lt
			}
			l := mon.Line(line, now)
			if l.Hide {
				continue
			}
			got.Lines = append(got.Lines, l.Text)
			for _, e := range l.Events {
				if e.Text == "" {
					continue // a count, which the feed doesn't show
				}
				got.Activities = append(got.Activities, activity{Kind: e.Kind, At: localString(e.Time), Name: e.Name, PlatformID: e.Account, Text: e.Text})
			}
		}
		st := mon.State()
		for _, p := range st.Players {
			got.Players = append(got.Players, player{Name: p.Name, PlatformID: p.Account, Since: localString(p.Since)})
		}
		got.State = state{Ready: st.Ready, JoinCode: st.Code, PublicAddr: st.Extra["publicAddr"], Version: st.Version,
			SteamAt: localString(mon.SteamAt), LastSave: localString(st.LastSave), Stopping: st.Stopping,
			StoppingAt: localString(st.StoppingAt), CrossplayFailed: st.Extra["crossplayFailed"] == "true"}

		// And as the records read a finished run's log afterwards.
		back := NewMonitor(game.Record{}, known)
		back.FromLog()
		var last time.Time
		add := func(evs []game.Event) {
			for _, e := range evs {
				if (e.Kind == "join" || e.Kind == "leave" || e.Kind == "death") && e.Name != "" && !e.Time.IsZero() {
					got.Records = append(got.Records, eventAt{At: localString(time.Unix(e.Time.Unix(), 0)), Kind: e.Kind, Name: e.Name})
				}
			}
		}
		for _, line := range strings.Split(string(b), "\n") {
			l := back.Line(line, time.Now())
			if !l.Time.IsZero() {
				last = l.Time
			}
			add(l.Events)
		}
		add(back.End(last))

		if !reflect.DeepEqual(got.Lines, want.Lines) {
			t.Errorf("%s: lines\n got %q\nwant %q", want.Name, got.Lines, want.Lines)
		}
		if !reflect.DeepEqual(got.Activities, want.Activities) {
			t.Errorf("%s: activities\n got %+v\nwant %+v", want.Name, got.Activities, want.Activities)
		}
		if !reflect.DeepEqual(got.Players, want.Players) {
			t.Errorf("%s: players\n got %+v\nwant %+v", want.Name, got.Players, want.Players)
		}
		if got.State != want.State {
			t.Errorf("%s: state\n got %+v\nwant %+v", want.Name, got.State, want.State)
		}
		if !reflect.DeepEqual(got.Records, want.Records) {
			t.Errorf("%s: records\n got %+v\nwant %+v", want.Name, got.Records, want.Records)
		}
	}
}

// lineTime is a log line's own time, or zero, as the golden reads it.
func lineTime(line string) time.Time {
	return NewMonitor(game.Record{}, nil).Line(line, time.Time{}).Time
}

// Values read from settings.json and handed back unchanged give exactly
// the same settings, and a changed value lands in its field.
func TestStoreRoundTrip(t *testing.T) {
	s := config.Defaults()
	s.Password, s.World, s.Port, s.ExtraArgs = "raven-fjord-4821", "Midgard", 2500, `-instanceid 2 "x y"`
	s.WorldOptions = map[string]config.WorldOptions{
		"Midgard": {Preset: "hard", Modifiers: map[string]string{"combat": "veryhard"}, Keys: []string{"nomap", "fire"},
			Off: []string{"passivemobs"}, Mods: []string{"riverheim", "farlands"}},
		"Asgard": {Off: []string{"fire"}},
	}
	srv, st := newServer(t, s)
	saved := srv.Store().Get()
	if got := toSettings(s, saved); !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, s)
	}
	if saved.Ports["game"] != 2500 || saved.Ports["query"] != 2501 || saved.Server["port"] != "2500" || saved.World != "Midgard" {
		t.Fatalf("saved = %+v", saved)
	}
	if v := saved.Worlds["Midgard"]; v["preset"] != "hard" || v["modifier.combat"] != "veryhard" || v["key.fire"] != "true" || v["mods"] != "riverheim,farlands" {
		t.Fatalf("Midgard's values = %v", v)
	}

	// A change through the store reaches settings.json's fields, keeps the
	// app-wide ones, and turning a key off asks for -resetmodifiers.
	err := srv.Store().Save(func(sv *game.Saved) error {
		sv.Server["serverName"] = "Renamed"
		sv.Server["crossplay"] = "false"
		sv.Ports["game"] = 2600
		sv.Worlds["Midgard"] = maps.Clone(sv.Worlds["Midgard"])
		delete(sv.Worlds["Midgard"], "key.fire")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := st.Settings()
	o := got.WorldOptions["Midgard"]
	if got.ServerName != "Renamed" || got.Crossplay || got.Port != 2600 || got.DiscordStopped != s.DiscordStopped ||
		!slices.Equal(o.Keys, []string{"nomap"}) || !slices.Contains(o.Off, "fire") || o.Preset != "hard" ||
		!slices.Equal(o.Mods, []string{"riverheim", "farlands"}) {
		t.Fatalf("after saving: %+v, Midgard %+v", got, o)
	}
	if _, ok := got.WorldOptions["Asgard"]; !ok {
		t.Fatal("a world with only keys turned off lost its options")
	}

	// Values that break settings.json's rules are refused with its fields.
	err = srv.Store().Save(func(sv *game.Saved) error { sv.Server["password"] = "ab"; return nil })
	if fe, ok := err.(config.FieldErrors); !ok || fe["password"] == "" {
		t.Fatalf("a short password: %v", err)
	}
	if errs := srv.Validate(srv.Store().Get(), "bad/name"); errs["world"] == "" {
		t.Fatalf("a bad world name: %v", errs)
	}
	if errs := srv.Validate(srv.Store().Get(), ""); errs != nil {
		t.Fatalf("good settings: %v", errs)
	}
}

// The run record keeps Valheim's fields at the top level of runtime.json,
// only when set, as older builds wrote them.
func TestRecordFields(t *testing.T) {
	rec := game.Record{PID: 1, Extra: RecordExtra(true, "abc", []string{"riverheim"})}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"noCrossplay":true`, `"modsCode":"abc"`, `"codeMods":["riverheim"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s has no %s", b, want)
		}
	}
	var back game.Record
	if err := json.Unmarshal(b, &back); err != nil || !NoCrossplay(back) || ModsCode(back) != "abc" || !slices.Equal(CodeMods(back), []string{"riverheim"}) {
		t.Fatalf("read back %+v, %v", back, err)
	}
	if !srvPending(back, false) {
		t.Fatal("a record with its mods code is pending")
	}
	SetModsCode(&back, "")
	if !srvPending(back, true) {
		t.Fatal("a record without its mods code isn't pending")
	}
	if r := (game.Record{Extra: RecordExtra(false, "", nil)}); r.Extra != nil {
		t.Fatalf("empty fields written: %v", r.Extra)
	}
}

func TestEscapeIsDiscords(t *testing.T) {
	for _, s := range []string{"", "plain", "*Bold* _V_ ~~of~~ `the` |north| > #1 [x](y) <@1> \\ ÆØ ⚔"} {
		if got, want := escape(s), discord.Escape(s); got != want {
			t.Errorf("escape(%q) = %q, want %q", s, got, want)
		}
	}
}

func srvPending(rec game.Record, want bool) bool { return (&Server{}).Pending(rec) == want }

func TestJoin(t *testing.T) {
	s := config.Defaults()
	s.Password, s.Port, s.Public = "raven-fjord-4821", 2456, true
	srv, _ := newServer(t, s)
	saved := srv.Store().Get()

	stopped := srv.Join(game.JoinInput{Saved: saved, PublicIP: "203.0.113.4", LAN: []string{"192.168.1.20"}})
	if stopped.How != "code" || !stopped.Crossplay || stopped.Port != 2456 || stopped.Password != s.Password || !stopped.Public ||
		stopped.Address != "203.0.113.4:2456" || !slices.Equal(stopped.LAN, []string{"192.168.1.20:2456"}) || stopped.Router {
		t.Fatalf("stopped: %+v", stopped)
	}

	// Running: what it started with, the code and the session's address.
	rec := game.Record{Port: 2600, Password: "older-pass-1"}
	setNoCrossplay(&rec, false)
	st := game.RunState{Ready: true, Code: "604510", Extra: map[string]string{"publicAddr": "198.51.100.1:2600"}}
	j := srv.Join(game.JoinInput{Rec: &rec, State: &st, Saved: saved, PublicIP: "203.0.113.4"})
	if j.Code != "604510" || j.Port != 2600 || j.Password != "older-pass-1" || j.Address != "198.51.100.1:2600" || j.Problem != "" {
		t.Fatalf("running: %+v", j)
	}
	st = game.RunState{Ready: true, Extra: map[string]string{"crossplayFailed": "true"}}
	if j := srv.Join(game.JoinInput{Rec: &rec, State: &st, Saved: saved}); j.Problem != "crossplay" {
		t.Fatalf("crossplay failed: %+v", j)
	}
	// Started by an older build: no port in the record, so the settings'.
	old := game.Record{}
	setNoCrossplay(&old, true)
	if j := srv.Join(game.JoinInput{Rec: &old, State: &game.RunState{}, Saved: saved}); j.How != "address" || j.Port != 2456 || j.Password != s.Password || !j.Router {
		t.Fatalf("older build's record: %+v", j)
	}
}
