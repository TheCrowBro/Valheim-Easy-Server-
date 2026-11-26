package vintagestory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// The Monitor against real output: testdata/real holds the console of the
// real 1.22.7 Linux server, run headless for these tests (FACTS.md says how),
// and testdata/issues holds players joining and leaving real servers, from
// logs people posted in the game's issue tracker (FACTS.md names each one).

type replay struct {
	events []game.Event
	lines  []game.Line
	state  game.RunState
}

func play(t *testing.T, file string) replay {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]string{"Remembered": "uid-from-an-earlier-run"}
	m := (&server{}).Watch(game.Record{}, func(name string) string { return known[name] })
	var r replay
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local) // after every line's own time
	for raw := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		l := m.Line(raw, now)
		r.lines = append(r.lines, l)
		r.events = append(r.events, l.Events...)
	}
	r.state = m.State()
	return r
}

// kinds lists the events as "kind" or "kind:name".
func (r replay) kinds() []string {
	var out []string
	for _, e := range r.events {
		k := e.Kind
		if e.Name != "" {
			k += ":" + e.Name
		}
		out = append(out, k)
	}
	return out
}

func (r replay) first(kind string) (game.Event, bool) {
	i := slices.IndexFunc(r.events, func(e game.Event) bool { return e.Kind == kind })
	if i < 0 {
		return game.Event{}, false
	}
	return r.events[i], true
}

func TestMonitorFirstRun(t *testing.T) {
	// No serverconfig.json yet: the server makes its own (with the
	// whitelist on), loads, says it's ready; then "/autosavenow", "/list
	// clients", "/help" and "/stop" typed on its console.
	r := play(t, "real/first-run-no-config.log")
	want := []string{"online", "saved", "stopping", "saved"}
	if got := r.kinds(); !slices.Equal(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if r.state.Version != "1.22.7" {
		t.Errorf("version = %q", r.state.Version)
	}
	if !r.state.Ready || !r.state.Stopping || r.state.LastSave.IsZero() {
		t.Errorf("state = %+v", r.state)
	}
	if r.events[1].Text != "World saved." {
		t.Errorf("the save /autosavenow asked for has text %q", r.events[1].Text)
	}
	online, _ := r.first("online")
	if want := time.Date(2026, 11, 26, 21, 34, 47, 0, time.Local); !online.Time.Equal(want) {
		t.Errorf("online at %v, want the line's own time %v", online.Time, want)
	}
	// The help text and the long loading report are hidden or shown, but
	// never make events; the ready line is shown.
	for _, l := range r.lines {
		if strings.HasPrefix(l.Text, "Dedicated Server now running") && l.Hide {
			t.Error("the ready line is hidden")
		}
		if strings.Contains(l.Text, "Server overloaded") && !l.Hide {
			t.Error("the overloaded warning is shown")
		}
	}
}

func TestMonitorStops(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		// The app's own settings (a world it named, the whitelist off),
		// some /serverconfig commands, then SIGTERM.
		{"real/new-world-sigterm.log", []string{"online", "stopping", "saved"}},
		// An existing world loaded, then Ctrl+C (SIGINT): it saves.
		{"real/existing-world-sigint.log", []string{"online", "stopping", "saved"}},
		// Five minutes idle: the autosave, then SIGTERM.
		{"real/autosave-sigterm.log", []string{"online", "saved", "stopping", "saved"}},
		// A world name with accents and an emoji, stopped with /stop.
		{"real/unicode-world-stop.log", []string{"online", "stopping", "saved"}},
		{"real/exploration-world.log", []string{"online", "stopping", "saved"}},
		{"real/creative-world.log", []string{"online", "stopping", "saved"}},
	} {
		t.Run(filepath.Base(c.file), func(t *testing.T) {
			r := play(t, c.file)
			if got := r.kinds(); !slices.Equal(got, c.want) {
				t.Fatalf("events = %q, want %q", got, c.want)
			}
			if r.state.Version != "1.22.7" {
				t.Errorf("version = %q", r.state.Version)
			}
		})
	}
	// The autosave every 5 minutes isn't in the feed; the save on the way
	// out is.
	r := play(t, "real/autosave-sigterm.log")
	if r.events[1].Text != "" || r.events[3].Text != "World saved." {
		t.Errorf("save texts %q and %q, want the autosave quiet and the last save shown", r.events[1].Text, r.events[3].Text)
	}
}

// A server that gives up by itself must not look like a stop someone asked
// for: the manager then treats the exit as the crash it is (it says why and
// can start it again).
func TestMonitorFailuresAreNotStops(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"real/port-in-use.log", nil},
		{"real/minimal-config.log", nil},
		{"real/damaged-config.log", nil},
		{"real/corrupt-world.log", []string{"load_failed"}},
		{"real/dotnet-wrong-runtime.log", nil},
		{"real/dotnet-missing-dll.log", nil},
	} {
		t.Run(filepath.Base(c.file), func(t *testing.T) {
			r := play(t, c.file)
			if got := r.kinds(); !slices.Equal(got, c.want) {
				t.Errorf("events = %q, want %q", got, c.want)
			}
			if r.state.Stopping || r.state.Ready {
				t.Errorf("state = %+v, want neither ready nor stopping", r.state)
			}
		})
	}
}

func TestMonitorPlayers(t *testing.T) {
	// A 1.20.12 server's own log (server-main.log, "[Event]" without
	// "Server "): four players join, one leaves.
	r := play(t, "issues/7431-v1.20.12-server-main.log")
	want := []string{"join:D4K0T4", "join:Keelaesh", "leave:Keelaesh", "join:vfctffu", "join:Sozuke"}
	if got := r.kinds(); !slices.Equal(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	accounts := map[string]string{"D4K0T4": "MadeUpUid1AAAAAAAAAAAAAA", "Keelaesh": "MadeUpUid3AAAAAAAAAAAAAA", "vfctffu": "MadeUpUid4AAAAAAAAAAAAAA", "Sozuke": "MadeUpUid2AAAAAAAAAAAAA+"}
	for _, e := range r.events {
		if e.Account != accounts[e.Name] {
			t.Errorf("%s %s has account %q, want %q", e.Kind, e.Name, e.Account, accounts[e.Name])
		}
	}
	var on []string
	for _, p := range r.state.Players {
		on = append(on, p.Name)
	}
	if !slices.Equal(on, []string{"D4K0T4", "vfctffu", "Sozuke"}) || r.state.Count != 3 {
		t.Errorf("players on = %q (count %d)", on, r.state.Count)
	}
	if e := r.events[2]; e.Text != "Keelaesh left the game." {
		t.Errorf("leave text %q", e.Text)
	}
	if want := time.Date(2025, 10, 29, 16, 59, 52, 0, time.Local); !r.events[0].Time.Equal(want) {
		t.Errorf("join at %v, want %v", r.events[0].Time, want)
	}

	// A 1.22.3 console: a player whose game crashed is removed, which
	// counts as leaving.
	r = play(t, "issues/9655-v1.22.3-console.log")
	if got := r.kinds(); !slices.Equal(got, []string{"join:Teskey", "leave:Teskey"}) {
		t.Fatalf("events = %q", got)
	}
	if r.events[0].Account != "MadeUp/Uid6AAAAAAAAAAA+A" {
		t.Errorf("account %q", r.events[0].Account)
	}
	if r.events[1].Text != "Teskey left the game (The Players client crashed)." {
		t.Errorf("removed text %q", r.events[1].Text)
	}
	if len(r.state.Players) != 0 {
		t.Errorf("still listed: %v", r.state.Players)
	}

	// Removed players who were never seen joining (the log starts later)
	// make no events.
	if r := play(t, "issues/9914-v1.22.6-removed.log"); len(r.events) != 0 {
		t.Errorf("events = %q", r.kinds())
	}
}

func TestMonitorLines(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, func(name string) string {
		if name == "Remembered" {
			return "uid-from-an-earlier-run"
		}
		return ""
	})
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, time.Local)
	line := func(text string) game.Line { return m.Line("1.12.2026 11:00:00 [Server Event] "+text, now) }

	// A player whose identification line came before the log was read
	// gets the account the app remembers for them.
	if l := line("Remembered [::ffff:192.168.1.5]:50000 joins."); len(l.Events) != 1 || l.Events[0].Account != "uid-from-an-earlier-run" {
		t.Errorf("join = %+v", l.Events)
	}
	// Joining twice lists them once.
	if l := line("Remembered [::ffff:192.168.1.5]:50001 joins."); len(l.Events) != 0 {
		t.Errorf("second join = %+v", l.Events)
	}
	// Chat can't fake a join: a chat line has the speaker before it.
	if l := m.Line("1.12.2026 11:00:01 [Server Chat] 0 | Mallory: Bob [::1]:1 joins.", now); len(l.Events) != 0 {
		t.Errorf("chat made %+v", l.Events)
	}
	if l := m.Line("1.12.2026 11:00:01 [Server Notification] Message to all in group 0: Bob [::1]:1 joins.", now); len(l.Events) != 0 {
		t.Errorf("a message made %+v", l.Events)
	}
	if l := line("Bob fails to join (invalid server password)."); len(l.Events) != 1 || l.Events[0].Kind != "wrong_password" || l.Events[0].Name != "Bob" {
		t.Errorf("wrong password = %+v", l.Events)
	}
	// A line from the future (a clock change) happens now.
	if l := m.Line("1.12.2027 11:00:00 [Server Event] Late [::1]:2 joins.", now); len(l.Events) != 1 || !l.Events[0].Time.Equal(now) {
		t.Errorf("future join = %+v", l.Events)
	}
	if st := m.State(); st.Count != 2 {
		t.Errorf("count = %d", st.Count)
	}
	// The end of the run: whoever is listed leaves.
	evs := m.End(now)
	if len(evs) != 2 || evs[0].Kind != "leave" || m.State().Count != 0 {
		t.Errorf("End = %+v, state %+v", evs, m.State())
	}
	// A line without a time is the rest of the one before it.
	if l := m.Line("   at Vintagestory.Server.ServerMain.Launch()", now); !l.Time.IsZero() || len(l.Events) != 0 || l.Text == "" {
		t.Errorf("continuation = %+v", l)
	}
	// Stop reasons someone asked for, and ones they didn't.
	for reason, asked := range map[string]bool{
		"Shutdown via server command":           true,
		"External close event SIGINT received":  true,
		"External close event SIGTERM received": true,
		"serverconfig.json read error":          false,
		"Failed opening savegame":               false,
	} {
		if askedStop(reason) != asked {
			t.Errorf("askedStop(%q) = %v", reason, !asked)
		}
	}
}

func TestExitHints(t *testing.T) {
	saves := filepath.Join("instance", "saves")
	srv := &server{env: game.Env{Dirs: game.Dirs{Saves: saves}}}
	for file, want := range map[string]string{
		"real/port-in-use.log":   "port",
		"real/corrupt-world.log": "Restore one of its backups",
		// The app can't always repair a file the server can't read, so the
		// owner is told where it is and how to get a new one.
		"real/damaged-config.log": "settings file, " + filepath.Join(saves, "serverconfig.json") + ". Start it again",
		// This one the app sets aside at the next start (TestPlanGenConfig).
		"real/minimal-config.log":       "default player role it doesn't have. Start it again: the app sets that file aside",
		"real/dotnet-wrong-runtime.log": ".NET",
		"real/dotnet-missing-dll.log":   "Repair",
		"real/first-run-no-config.log":  "",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		tail := lines[max(0, len(lines)-40):]
		got := srv.ExitHint(tail)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: ExitHint = %q, want one with %q", file, got, want)
		}
	}
}

// The stand-in prints the lines the Monitor knows, in the real server's
// words: every regexp here must match a line in the real testdata or the
// public logs, so a stand-in can't drift away from the real server
// unnoticed.
func TestPatternsMatchRealLines(t *testing.T) {
	var all []string
	for _, dir := range []string{"testdata/real", "testdata/issues"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for l := range strings.SplitSeq(string(b), "\n") {
				text, _, _ := splitLine(l)
				all = append(all, text)
			}
		}
	}
	for name, re := range map[string]interface{ MatchString(string) bool }{
		"version": reGameVersion, "ready": reReady, "ident": reIdent, "join": reJoin, "left": reLeft,
		"removed": reRemoved, "saved": reSaved, "stop": reStopAsk, "saveCmd": reSaveCmd, "corrupt": reCorrupt,
	} {
		if !slices.ContainsFunc(all, re.MatchString) {
			t.Errorf("no real line matches the %s pattern", name)
		}
	}
}
