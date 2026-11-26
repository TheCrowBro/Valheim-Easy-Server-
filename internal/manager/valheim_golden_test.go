package manager

// What bed004b does for Valheim, recorded in testdata/golden
// (docs/dev/multigame/spec.md 7.2 and I12): the server's command line and
// the key that says when a restart is needed, the Discord posts, the
// reminders for players, the hints after a crash, and what the app makes
// of a server log (the Log page, the activity feed, who's online, and the
// records). When the Valheim code moves into its own game adapter, the
// adapter must give exactly these again, so Valheim behaves as it always
// has.
//
// Each golden file holds the inputs with the outputs. The tests work the
// outputs out again with today's code and compare the whole file, byte for
// byte. A golden is never re-recorded to make a test pass: the code must
// match it. VES_RECORD_GOLDEN=1 writes a golden file that doesn't exist
// yet (for new cases, put them in a new file); it never overwrites one.

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/logparse"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/records"
)

// checkGolden compares got, as indented JSON, with the golden file at path
// (under testdata), and shows the lines that differ.
func checkGolden(t *testing.T, path string, got any) {
	t.Helper()
	b := indentedJSON(t, got)
	// A Windows checkout may turn the file's line endings into CRLF.
	want := bytes.ReplaceAll(golden(t, path, b), []byte("\r\n"), []byte("\n"))
	if bytes.Equal(b, want) {
		return
	}
	gl, wl := strings.Split(string(b), "\n"), strings.Split(string(want), "\n")
	shown := 0
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w && shown < 10 {
			t.Errorf("%s line %d:\n got: %s\nwant: %s", path, i+1, g, w)
			shown++
		}
	}
	if shown == 0 {
		t.Errorf("%s differs", path)
	}
}

// --- the command line -------------------------------------------------------

type argsGolden struct {
	Name     string          `json:"name"`
	Settings config.Settings `json:"settings"`
	SaveDir  string          `json:"saveDir"`
	LogFile  string          `json:"logFile"`
	Args     []string        `json:"args"`            // valheim_server's arguments
	Error    string          `json:"error,omitempty"` // why there are none
	ArgsKey  string          `json:"argsKey"`         // runtime.json's argsKey for a server started so
}

func TestServerArgsGolden(t *testing.T) {
	base := func() config.Settings {
		s := config.Defaults()
		s.Password = "raven-fjord-4821"
		return s
	}
	with := func(change func(*config.Settings)) config.Settings {
		s := base()
		change(&s)
		return s
	}
	world := func(o config.WorldOptions) func(*config.Settings) {
		return func(s *config.Settings) { s.WorldOptions = map[string]config.WorldOptions{s.World: o} }
	}
	cases := []argsGolden{
		{Name: "first start", Settings: base()},
		{Name: "crossplay off, public, another port", Settings: with(func(s *config.Settings) {
			s.Crossplay, s.Public, s.Port = false, true, 2500
			s.SaveIntervalMinutes, s.Backups, s.BackupShortMinutes, s.BackupLongMinutes = 30, 0, 60, 1440
		})},
		{Name: "difficulty and every modifier and key", Settings: with(world(config.WorldOptions{Preset: "hard",
			Modifiers: map[string]string{"combat": "veryhard", "deathpenalty": "casual", "resources": "more", "raids": "none", "portals": "casual"},
			Keys:      []string{"fire", "nomap", "passivemobs", "playerevents", "nobuildcost"}}))},
		{Name: "a key turned off again", Settings: with(world(config.WorldOptions{Keys: []string{"nomap"}, Off: []string{"fire"}}))},
		{Name: "another world's options don't count", Settings: with(func(s *config.Settings) {
			s.WorldOptions = map[string]config.WorldOptions{"Asgard": {Preset: "casual", Mods: []string{"riverheim"}}}
		})},
		{Name: "extra arguments", Settings: with(func(s *config.Settings) {
			s.ExtraArgs = `-instanceid 2  "-logAppend" "C:\Valheim Server\extra dir"`
		})},
		{Name: "extra arguments with an unclosed quote", Settings: with(func(s *config.Settings) { s.ExtraArgs = `-x "y` })},
		{Name: "server mods and a mod just for players", Settings: with(world(config.WorldOptions{Mods: []string{"farlands", "riverheim", "advancedportals"}}))},
		{Name: "only a mod just for players", Settings: with(world(config.WorldOptions{Mods: []string{"farlands"}}))},
		{Name: "names with spaces and other letters", Settings: with(func(s *config.Settings) {
			s.ServerName, s.World = "Ægir's Hall ⚔", "Niflheim Ødegård"
		})},
	}
	for i := range cases {
		c := &cases[i]
		c.SaveDir, c.LogFile = "/data/saves", "/data/logs/server-2026-11-20_18-30-00.log"
		if c.Name == "names with spaces and other letters" {
			c.SaveDir, c.LogFile = `C:\Users\Astrid\Valheim Server\saves`, `C:\Users\Astrid\Valheim Server\logs\server-2026-11-20_18-30-00.log`
		}
		args, err := c.Settings.ServerArgs(c.SaveDir, c.LogFile)
		c.Args = args
		if err != nil {
			c.Error = err.Error()
		}
		c.ArgsKey = argsKey(c.Settings, c.SaveDir)
	}
	checkGolden(t, "golden/serverargs.json", cases)
}

// --- Discord ------------------------------------------------------------

type modRef struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

type onlineGolden struct {
	Name     string   `json:"name"`
	Server   string   `json:"server"`
	World    string   `json:"world"`
	Code     string   `json:"code,omitempty"`
	Address  string   `json:"address,omitempty"`
	Password string   `json:"password,omitempty"`
	Mods     []modRef `json:"mods,omitempty"`     // the mods players need
	Optional []modRef `json:"optional,omitempty"` // just for players' own games
	ModsCode string   `json:"modsCode,omitempty"`
	Reminder []string `json:"reminder,omitempty"`
	Text     string   `json:"text"`
}

type stoppedGolden struct {
	Server string `json:"server"`
	Reason string `json:"reason"` // stopped, restart, rest, crash, crash-restart
	Code   bool   `json:"code"`   // the online post had a join code
	Text   string `json:"text"`
}

type eventAt struct {
	At   string `json:"at"`   // this computer's time: 2006-01-02 15:04:05
	Kind string `json:"kind"` // join, leave or death
	Name string `json:"name"`
}

type dailyGolden struct {
	Name   string    `json:"name"`
	Day    string    `json:"day"`  // 2006-01-02
	When   string    `json:"when"` // today (posted at 23:59) or yesterday (posted the next day)
	Now    string    `json:"now"`  // when it's posted
	Events []eventAt `json:"events"`
	Text   string    `json:"text"` // empty when nothing is posted
}

type testPostGolden struct {
	Server    string `json:"server"`
	Crossplay bool   `json:"crossplay"`
	Text      string `json:"text"`
}

type discordGolden struct {
	Online  []onlineGolden   `json:"online"`
	Stopped []stoppedGolden  `json:"stopped"`
	Daily   []dailyGolden    `json:"daily"`
	Test    []testPostGolden `json:"test"` // the Test button on the Settings page
}

func modInfos(t *testing.T, refs []modRef) []ModInfo {
	var out []ModInfo
	for _, r := range refs {
		md, ok := mods.Find(r.ID)
		if !ok {
			t.Fatalf("no mod %q", r.ID)
		}
		out = append(out, ModInfo{Mod: md, Version: r.Version})
	}
	return out
}

const localTime = "2006-01-02 15:04:05"

func parseLocal(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation(localTime, s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

var recordKinds = map[string]string{"join": records.Join, "leave": records.Leave, "death": records.Death}

func TestDiscordTextsGolden(t *testing.T) {
	var g discordGolden

	g.Online = []onlineGolden{
		{Name: "join code", Server: "Viking Night", World: "Midgard", Code: "604510"},
		{Name: "address, without crossplay", Server: "Viking Night", World: "Midgard", Address: "203.0.113.4:2456"},
		{Name: "no address known", Server: "Viking Night", World: "Midgard"},
		{Name: "with the password", Server: "Viking Night", World: "Midgard", Code: "604510", Password: "raven-fjord-4821"},
		{Name: "names Discord would format", Server: "*Bold* _Vikings_ ~~of~~ `the` |north| > #1", World: "W_orld*", Address: "203.0.113.4:2456", Password: "pa||ss_*"},
		{Name: "a mod, with the mods code", Server: "Viking Night", World: "Midgard", Code: "604510",
			Mods: []modRef{{"riverheim", "1.2.3"}}, ModsCode: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b"},
		{Name: "mods, no mods code, one version unknown", Server: "Viking Night", World: "Midgard", Code: "604510",
			Mods: []modRef{{"riverheim", "1.2.3"}, {"planteverything", ""}}},
		{Name: "only optional mods, with the mods code", Server: "Viking Night", World: "Midgard", Code: "604510",
			Optional: []modRef{{"farlands", ""}}, ModsCode: "019a3b4c-5d6e-7f80-9a1b-2c3d4e5f6a7b"},
		{Name: "mods and optional mods, by address", Server: "Viking Night", World: "Midgard", Address: "203.0.113.4:2456",
			Mods: []modRef{{"advancedportals", "2.0.1"}}, Optional: []modRef{{"farlands", ""}}},
		{Name: "a reminder", Server: "Viking Night", World: "Midgard", Code: "604510",
			Reminder: []string{"update Valheim to 1.0.21", "install Riverheim", "use the new password"}},
	}
	for i := range g.Online {
		c := &g.Online[i]
		c.Text = onlineMessage{Server: c.Server, World: c.World, Code: c.Code, Address: c.Address, Password: c.Password,
			Mods: modInfos(t, c.Mods), Optional: modInfos(t, c.Optional), ModsCode: c.ModsCode, Reminder: c.Reminder}.text()
	}

	reasons := []struct {
		name string
		why  stopReason
	}{{"stopped", stopStopped}, {"restart", stopRestart}, {"rest", stopRest}, {"crash", stopCrash}, {"crash-restart", stopCrashRestart}}
	for _, server := range []string{"Viking Night", "*Odd* _name_"} {
		for _, r := range reasons {
			for _, code := range []bool{true, false} {
				g.Stopped = append(g.Stopped, stoppedGolden{Server: server, Reason: r.name, Code: code, Text: stoppedText(server, r.why, code)})
			}
		}
	}

	play := func(at, kind, name string) eventAt { return eventAt{At: "2026-11-20 " + at, Kind: kind, Name: name} }
	g.Daily = []dailyGolden{
		{Name: "nobody died, one viking", Events: []eventAt{play("18:00:00", "join", "Astrid"), play("20:00:00", "leave", "Astrid")}},
		{Name: "nobody died, several", Events: []eventAt{play("18:00:00", "join", "Astrid"), play("18:05:00", "join", "Bjorn"),
			play("20:00:00", "leave", "Astrid"), play("21:00:00", "leave", "Bjorn")}},
		{Name: "one died once", Events: []eventAt{play("18:00:00", "join", "Astrid"), play("18:05:00", "join", "Bjorn"),
			play("19:00:00", "death", "Bjorn"), play("20:00:00", "leave", "Astrid"), play("21:00:00", "leave", "Bjorn")}},
		{Name: "twice, and others died too", Events: []eventAt{play("18:00:00", "join", "Astrid"), play("18:05:00", "join", "Bjorn"),
			play("18:06:00", "join", "_Sigrid_"), play("19:00:00", "death", "Bjorn"), play("19:30:00", "death", "Bjorn"),
			play("19:40:00", "death", "_Sigrid_"), play("20:00:00", "leave", "Astrid"), play("21:00:00", "leave", "Bjorn"),
			play("21:30:00", "leave", "_Sigrid_")}},
		{Name: "three times each, a tie", Events: []eventAt{play("18:00:00", "join", "Astrid"), play("18:05:00", "join", "Bjorn"),
			play("18:30:00", "death", "Astrid"), play("18:40:00", "death", "Astrid"), play("18:50:00", "death", "Astrid"),
			play("19:00:00", "death", "Bjorn"), play("19:10:00", "death", "Bjorn"), play("19:20:00", "death", "Bjorn"),
			play("22:00:00", "leave", "Astrid"), play("22:00:00", "leave", "Bjorn")}},
		{Name: "today at 23:59, someone still on", When: "today", Now: "2026-11-20 23:59:00",
			Events: []eventAt{play("21:00:00", "join", "Astrid"), play("22:00:00", "death", "Astrid")}},
		{Name: "nobody played that day", Events: []eventAt{{At: "2026-11-19 18:00:00", Kind: "join", Name: "Astrid"}, {At: "2026-11-19 19:00:00", Kind: "leave", Name: "Astrid"}}},
	}
	for i := range g.Daily {
		c := &g.Daily[i]
		c.Day = "2026-11-20"
		if c.When == "" {
			c.When, c.Now = "yesterday", "2026-11-21 08:00:00"
		}
		day := parseLocal(t, c.Day+" 00:00:00")
		var events []records.Event
		for _, e := range c.Events {
			events = append(events, records.Event{T: parseLocal(t, e.At).Unix(), Kind: recordKinds[e.Kind], Name: e.Name})
		}
		// What checkDaily posts: nothing when nobody played.
		if sum := records.Compute(events, day, day.AddDate(0, 0, 1), parseLocal(t, c.Now)); len(sum.Players) > 0 {
			c.Text = dailyText(sum, c.When)
		}
	}

	discord := newFakeDiscord(t)
	m, err := New(Options{DataDir: t.TempDir(), Logger: log.New(io.Discard, "", 0), NoPublicIP: true, DiscordURL: discord.URL,
		HibernateStatus: func() (bool, string) { return true, "" }, SleepStatus: func() (bool, string) { return true, "" }})
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"Viking Night", "*Odd* _name_"} {
		for _, crossplay := range []bool{true, false} {
			m.mu.Lock()
			m.settings.ServerName, m.settings.Crossplay = server, crossplay
			m.mu.Unlock()
			before := len(discord.all())
			if err := m.TestDiscord("https://discord.com/api/webhooks/123/abc"); err != nil {
				t.Fatal(err)
			}
			posts := discord.all()
			if len(posts) != before+1 {
				t.Fatalf("the test post wasn't posted: %q", posts)
			}
			g.Test = append(g.Test, testPostGolden{Server: server, Crossplay: crossplay, Text: posts[len(posts)-1]})
		}
	}
	checkGolden(t, "golden/discord.json", g)
}

// --- reminders ----------------------------------------------------------

type remindGolden struct {
	Name     string   `json:"name"`
	Last     *played  `json:"last"` // what players last played with (lastplayed.json); null before anyone has
	Now      played   `json:"now"`  // what the running server is played with
	Reminder []string `json:"reminder"`
}

func TestRemindersGolden(t *testing.T) {
	pw, newPW := hashPassword("raven-fjord-4821"), hashPassword("odin-mead-1234")
	was := played{Game: "1.0.20", Mods: map[string]string{"riverheim": "1.2.3", "planteverything": "1.18.0"}, Password: pw}
	cases := []remindGolden{
		{Name: "nobody has played yet", Now: was},
		{Name: "nothing changed", Last: &was, Now: was},
		{Name: "Valheim updated", Last: &was, Now: played{Game: "1.0.21", Mods: was.Mods, Password: pw}},
		{Name: "Valheim's version not known yet", Last: &was, Now: played{Mods: was.Mods, Password: pw}},
		{Name: "a mod added", Last: &was, Now: played{Game: "1.0.20", Mods: map[string]string{"riverheim": "1.2.3", "planteverything": "1.18.0", "seasonality": "3.5.0"}, Password: pw}},
		{Name: "a mod turned off", Last: &was, Now: played{Game: "1.0.20", Mods: map[string]string{"riverheim": "1.2.3"}, Password: pw}},
		{Name: "a mod updated", Last: &was, Now: played{Game: "1.0.20", Mods: map[string]string{"riverheim": "1.2.4", "planteverything": "1.18.0"}, Password: pw}},
		{Name: "a mod's version not known", Last: &was, Now: played{Game: "1.0.20", Mods: map[string]string{"riverheim": "", "planteverything": "1.18.0"}, Password: pw}},
		{Name: "a new password", Last: &was, Now: played{Game: "1.0.20", Mods: was.Mods, Password: newPW}},
		{Name: "a password from an older copy that didn't note it", Last: &played{Game: "1.0.20", Mods: was.Mods}, Now: played{Game: "1.0.20", Mods: was.Mods, Password: newPW}},
		{Name: "everything at once, in the mods' order", Last: &was, Now: played{Game: "1.0.21",
			Mods: map[string]string{"farlands": "1.0.0", "seasonality": "3.5.0", "riverheim": "1.3.0"}, Password: newPW}},
	}
	for i := range cases {
		cases[i].Reminder = remind(cases[i].Last, cases[i].Now)
		if cases[i].Reminder == nil {
			cases[i].Reminder = []string{}
		}
	}
	checkGolden(t, "golden/reminders.json", cases)
}

// --- hints after the server stops unexpectedly ------------------------------

type hintGolden struct {
	Name  string   `json:"name"`
	Lines []string `json:"lines"` // the end of the server's log, as the Log page shows it
	Hint  string   `json:"hint"`
}

func TestExitHintsGolden(t *testing.T) {
	filler := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("Unloading %d unused assets", i))
		}
		return out
	}
	cases := []hintGolden{
		{Name: "nothing recognised", Lines: []string{"Valheim version: l-0.219.16 (network version 34)", "Crash!!! NullReferenceException"}},
		{Name: "the password", Lines: []string{"Error bad password, the password can't be part of the server name"}},
		{Name: "the world file", Lines: []string{"World load failed: Midgard"}},
		{Name: "the port, in other words", Lines: []string{"Socket error: Address Already In Use"}},
		{Name: "the port", Lines: []string{"Failed to bind to port 2456"}},
		{Name: "the newest problem counts", Lines: []string{"World load failed: Midgard", "Failed to bind to port 2456", "Shutting down"}},
		{Name: "a problem more than 80 lines back", Lines: append([]string{"World load failed: Midgard"}, filler(80)...)},
		{Name: "a problem 80 lines back", Lines: append([]string{"World load failed: Midgard"}, filler(79)...)},
	}
	for i := range cases {
		m := &Manager{logs: newRing[LogLine](3000)}
		for _, l := range cases[i].Lines {
			m.logs.add(func(seq int64) LogLine { return LogLine{Seq: seq, Text: l} })
		}
		cases[i].Hint = m.exitHint()
	}
	checkGolden(t, "golden/exithints.json", cases)
}

// --- the server's log ------------------------------------------------------

type trackerGolden struct {
	Name       string            `json:"name"`
	Log        string            `json:"log"`                  // in testdata/golden/tracker
	Known      map[string]string `json:"known,omitempty"`      // the account each name last played from
	NoJoinCode bool              `json:"noJoinCode,omitempty"` // started without crossplay
	Lines      []string          `json:"lines"`                // what the Log page shows
	Activities []activityAt      `json:"activities"`           // what the activity feed shows
	Players    []playerAt        `json:"players"`              // who's online at the end
	State      trackerState      `json:"state"`
	Records    []eventAt         `json:"records"` // the joins, leaves and deaths kept from the log
}

type activityAt struct {
	Kind       string `json:"kind"`
	At         string `json:"at"`
	Name       string `json:"name,omitempty"`
	PlatformID string `json:"platformId,omitempty"`
	Text       string `json:"text"`
}

type playerAt struct {
	Name       string `json:"name"`
	PlatformID string `json:"platformId,omitempty"`
	Since      string `json:"since"`
}

type trackerState struct {
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

// trackerClock is this computer's time for lines read before any line with
// a time of its own.
const trackerClock = "2026-11-20 12:00:00"

func localString(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.In(time.Local).Format(localTime)
}

func TestTrackerGolden(t *testing.T) {
	friends := map[string]string{"Astrid": "Steam_76561198000000001", "Bjorn": "Steam_76561198000000002"}
	cases := []trackerGolden{
		{Name: "a Steam session, friends known", Log: "steam-session.log", Known: friends, NoJoinCode: true},
		{Name: "a Steam session, friends new", Log: "steam-session.log", NoJoinCode: true},
		{Name: "a crossplay session", Log: "crossplay-session.log"},
		{Name: "leaves the log missed", Log: "missed-leaves.log", NoJoinCode: true},
		{Name: "turned away", Log: "turned-away.log", NoJoinCode: true},
		{Name: "crossplay can't start", Log: "crossplay-failed.log"},
		{Name: "crossplay's lines in a server without it", Log: "crossplay-failed.log", NoJoinCode: true},
		{Name: "lines players write", Log: "player-text.log"},
		{Name: "a Mac with mods starting up", Log: "mac-mods-startup.log"},
		{Name: "problems", Log: "problems.log"},
	}
	for i := range cases {
		c := &cases[i]
		path := filepath.Join("testdata", "golden", "tracker", c.Log)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Read as the app reads a running server's log: each line as it
		// comes, at this computer's time, which here is the line's own.
		tr := logparse.Tracker{NoJoinCode: c.NoJoinCode, KnownPlatformID: func(name string) string { return c.Known[name] }}
		now := parseLocal(t, trackerClock)
		c.Lines, c.Activities, c.Players, c.Records = []string{}, []activityAt{}, []playerAt{}, []eventAt{}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimRight(line, "\r")
			if logparse.IsNoise(line) {
				continue
			}
			ev := logparse.Parse(line)
			c.Lines = append(c.Lines, ev.Text)
			if !ev.Time.IsZero() {
				now = ev.Time
			}
			for _, a := range tr.Apply(ev, now) {
				c.Activities = append(c.Activities, activityAt{Kind: a.Kind, At: localString(a.Time), Name: a.Name, PlatformID: a.PlatformID, Text: a.Text})
			}
		}
		for _, p := range tr.Players() {
			c.Players = append(c.Players, playerAt{Name: p.Name, PlatformID: p.PlatformID, Since: localString(p.Since)})
		}
		c.State = trackerState{Ready: tr.Ready, JoinCode: tr.JoinCode, PublicAddr: tr.PublicAddr, Version: tr.Version,
			SteamAt: localString(tr.SteamAt), LastSave: localString(tr.LastSave), Stopping: tr.Stopping,
			StoppingAt: localString(tr.StoppingAt), CrossplayFailed: tr.CrossplayFailed}

		// And as the records read a finished server's log afterwards.
		events, err := eventsFromLog(path, c.Known)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			kind := map[string]string{records.Join: "join", records.Leave: "leave", records.Death: "death"}[e.Kind]
			c.Records = append(c.Records, eventAt{At: localString(time.Unix(e.T, 0)), Kind: kind, Name: e.Name})
		}
	}
	checkGolden(t, "golden/tracker.json", cases)
}
