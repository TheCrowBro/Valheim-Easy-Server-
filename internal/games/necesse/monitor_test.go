package necesse

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata from what the code does now")

// The console logs in testdata/real were captured from the real 1.3.3
// server (Steam build 24926481) run headless on Linux on 2026-11-26; those
// in testdata/forum are real logs players posted on Steam's Necesse forum
// (older versions, the only real logs with players joining). FACTS.md says
// where each one comes from and what it shows.
//
// replay feeds a log to a new Monitor, as the manager does, and returns
// every event and the lines shown on the Log page.
func replay(t *testing.T, path string, known func(string) string) (*monitor, []game.Event, []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := (&server{}).Watch(game.Record{}, known).(*monitor)
	var evs []game.Event
	var shown []string
	now := time.Date(2026, 11, 26, 21, 0, 0, 0, time.Local)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := m.Line(sc.Text(), now)
		if !l.Hide {
			shown = append(shown, l.Text)
		}
		evs = append(evs, l.Events...)
		now = now.Add(time.Second)
	}
	return m, evs, shown
}

func logs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"real", "forum"} {
		list, err := filepath.Glob(filepath.Join("testdata", dir, "*.log"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, list...)
	}
	if len(out) < 10 {
		t.Fatalf("only %d logs in testdata", len(out))
	}
	return out
}

// TestMonitorGolden pins, for every real log, the events the Monitor finds
// and its state at the end, in testdata/monitor/<log>.txt (go test -update
// rewrites them; read the diff before committing it).
func TestMonitorGolden(t *testing.T) {
	for _, path := range logs(t) {
		name := strings.TrimSuffix(filepath.Base(path), ".log")
		t.Run(name, func(t *testing.T) {
			m, evs, shown := replay(t, path, nil)
			var b strings.Builder
			fmt.Fprintf(&b, "# Events the Monitor finds in %s, then its state at the end.\n", filepath.ToSlash(path))
			for _, e := range evs {
				fmt.Fprintf(&b, "%s", e.Kind)
				if e.Name != "" {
					fmt.Fprintf(&b, " name=%q", e.Name)
				}
				if e.Account != "" {
					fmt.Fprintf(&b, " account=%s", e.Account)
				}
				if e.Kind == "count" {
					fmt.Fprintf(&b, " count=%d hold=%v", e.Count, e.Hold)
				}
				if e.Text != "" {
					fmt.Fprintf(&b, " text=%q", e.Text)
				}
				b.WriteString("\n")
			}
			st := m.State()
			var players []string
			for _, p := range st.Players {
				players = append(players, p.Name+"/"+p.Account)
			}
			fmt.Fprintf(&b, "state: ready=%v version=%q count=%d players=%q saved=%v stopping=%v stopCode=%q bindFailed=%v\n",
				st.Ready, st.Version, st.Count, players, !st.LastSave.IsZero(), st.Stopping, m.stopCode, m.bindFailed)
			fmt.Fprintf(&b, "shown: %d lines\n", len(shown))
			golden(t, filepath.Join("testdata", "monitor", name+".txt"), b.String())
		})
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to write it)", err)
	}
	// Git on Windows may check text files out with CRLF line ends.
	if w := strings.ReplaceAll(string(want), "\r\n", "\n"); w != got {
		t.Errorf("%s differs from what the code does now:\n--- want\n%s\n--- got\n%s", path, w, got)
	}
}

func kinds(evs []game.Event) []string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func count(evs []game.Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// The facts the adapter relies on, each checked on the real log that shows
// it, so a change that breaks one names it.
func TestMonitorRealFacts(t *testing.T) {
	real := func(name string) string { return filepath.Join("testdata", "real", name) }

	t.Run("a new world: ready, version, save and stop", func(t *testing.T) {
		m, evs, shown := replay(t, real("new-world-save-stop.log"), nil)
		if got := kinds(evs); !slices.Equal(got, []string{"online", "count", "saved", "stopping", "saved"}) {
			t.Errorf("events = %q, want online, count (the typed \"players\"), saved (the typed save), stopping, saved (before stopping)", got)
		}
		st := m.State()
		if st.Version != "1.3.3" || !st.Stopping || st.LastSave.IsZero() {
			t.Errorf("state = %+v", st)
		}
		for _, l := range shown {
			if strings.Contains(l, "\x1b") || strings.HasPrefix(l, "[") || strings.Contains(l, "(DEBUG)") || strings.Contains(l, "load level presets") {
				t.Errorf("the Log page shows %q: colour codes, times, debug lines and level loading should be gone", l)
			}
		}
	})

	t.Run("the password is kept off the Log page", func(t *testing.T) {
		_, evs, shown := replay(t, real("load-with-password-then-sigterm.log"), nil)
		if count(evs, "online") != 1 {
			t.Errorf("events = %q", kinds(evs))
		}
		joined := strings.Join(shown, "\n")
		if strings.Contains(joined, "secret1") || !strings.Contains(joined, `with password "•••"`) {
			t.Errorf("shown lines:\n%s", joined)
		}
	})

	t.Run("a taken port: it says it started, but it isn't online", func(t *testing.T) {
		m, evs, shown := replay(t, real("port-in-use.log"), nil)
		if count(evs, "online") != 0 {
			t.Errorf("events = %q: a server that couldn't open its port must not count as online", kinds(evs))
		}
		if m.stopCode != "NETWORK_ERROR" {
			t.Errorf("stop code %q", m.stopCode)
		}
		// Nobody asked for this stop: the manager must report it as a
		// failure with the hint below, not as a normal stop.
		if count(evs, "stopping") != 0 || m.State().Stopping {
			t.Errorf("events = %q, Stopping %v: a stop on a taken port counts as asked for", kinds(evs), m.State().Stopping)
		}
		hint := (&server{}).ExitHint(shown)
		if !strings.Contains(hint, "UDP port 14171") {
			t.Errorf("ExitHint = %q", hint)
		}
	})

	t.Run("a world name Java got as question marks", func(t *testing.T) {
		// Run with an ASCII locale on Linux, where Java turns every
		// character outside it into "?", as it does on Windows with one
		// outside the code page (FACTS.md). The server stops without a
		// stop code: nobody asked for that stop either.
		m, evs, shown := replay(t, real("world-name-mangled-ascii-locale.log"), nil)
		if len(evs) != 0 || m.State().Stopping {
			t.Errorf("events = %q, Stopping %v", kinds(evs), m.State().Stopping)
		}
		if hint := (&server{}).ExitHint(shown); !strings.Contains(hint, "world's name") {
			t.Errorf("ExitHint = %q", hint)
		}
	})

	t.Run("autosave once a minute, hidden from the Log page", func(t *testing.T) {
		_, evs, shown := replay(t, real("autosave-every-minute.log"), nil)
		if n := count(evs, "saved"); n < 10 {
			t.Errorf("%d saved events in 16 minutes, want one a minute", n)
		}
		for _, e := range evs {
			if e.Kind == "saved" && e.Text != "" && !strings.Contains(e.Text, "World saved") {
				t.Errorf("saved event text %q", e.Text)
			}
		}
		for _, l := range shown {
			if strings.Contains(l, "Reloaded file system") {
				t.Errorf("the autosave's line %q is shown every minute", l)
			}
		}
	})

	t.Run("paused while empty: no autosave", func(t *testing.T) {
		_, evs, _ := replay(t, real("paused-when-empty.log"), nil)
		if got := kinds(evs); !slices.Equal(got, []string{"online", "stopping", "saved"}) {
			t.Errorf("events = %q", got)
		}
	})

	t.Run("a stop that didn't save says nothing about a save", func(t *testing.T) {
		m, evs, _ := replay(t, real("first-launch-exit-no-save.log"), nil)
		if count(evs, "saved") != 0 || !m.State().LastSave.IsZero() {
			t.Errorf("events = %q: \"Starting world save\" alone is no save", kinds(evs))
		}
	})

	t.Run("Ctrl+C: nothing more is said", func(t *testing.T) {
		m, evs, _ := replay(t, real("load-then-ctrl-c.log"), nil)
		if !slices.Equal(kinds(evs), []string{"online"}) || m.State().Stopping {
			t.Errorf("events = %q", kinds(evs))
		}
	})

	t.Run("asked for a world: never ready", func(t *testing.T) {
		_, evs, _ := replay(t, real("help-and-world-prompt.log"), nil)
		if len(evs) != 0 {
			t.Errorf("events = %q", kinds(evs))
		}
	})

	t.Run("a password starting with a dash is ignored by the server", func(t *testing.T) {
		_, _, shown := replay(t, real("dash-password-ignored.log"), nil)
		for _, l := range shown {
			if strings.HasPrefix(l, "Started server using port") && strings.Contains(l, "with password") {
				t.Errorf("%q: the real server took the dashed password after all; Validate's rule can go", l)
			}
		}
	})
}

// Joins and leaves, from the real logs players posted (FACTS.md).
func TestMonitorForumJoinLeave(t *testing.T) {
	forum := func(name string) string { return filepath.Join("testdata", "forum", name) }

	t.Run("a new player joins under their account number", func(t *testing.T) {
		m, evs, _ := replay(t, forum("steam-2021-05-09-v0.20.40-join-leave.log"), nil)
		var join, leave game.Event
		for _, e := range evs {
			switch e.Kind {
			case "join":
				join = e
			case "leave":
				leave = e
			}
		}
		if join.Name != "76561198000000204" || join.Account != "76561198000000204" {
			t.Errorf("join = %+v", join)
		}
		// The leave line names the character ("Ketil"); it must still
		// close the join above, or the player would stay "on" for good.
		if leave.Name != "76561198000000204" || leave.Account != "76561198000000204" {
			t.Errorf("leave = %+v", leave)
		}
		if st := m.State(); len(st.Players) != 0 || st.Count != 0 {
			t.Errorf("after the leave: %+v", st)
		}
		if n := count(evs, "saved"); n != 2 {
			t.Errorf("%d saved events, want 2 (\"save\" and \"stop\")", n)
		}
	})

	t.Run("a known player, then a crash", func(t *testing.T) {
		m, evs, shown := replay(t, forum("steam-2022-06-28-join-then-crash.log"), nil)
		// No "stopping": the server stopped itself with SERVER_ERROR, so
		// the manager must count the exit as a crash (its report, and
		// "Start it again if it crashes").
		if !slices.Equal(kinds(evs), []string{"join", "saved"}) {
			t.Errorf("events = %q", kinds(evs))
		}
		st := m.State()
		if st.Stopping || m.stopCode != "SERVER_ERROR" {
			t.Errorf("Stopping %v, stop code %q: a crash counts as a stop someone asked for", st.Stopping, m.stopCode)
		}
		if len(st.Players) != 1 || st.Players[0].Name != "Sigurd" || st.Players[0].Account != "76561198000000201" {
			t.Errorf("players = %+v: the account comes from the \"Loaded player\" line", st.Players)
		}
		if hint := (&server{}).ExitHint(shown); !strings.Contains(hint, "crashed") {
			t.Errorf("ExitHint = %q", hint)
		}
		end := m.End(time.Now())
		if len(end) != 1 || end[0].Kind != "leave" || end[0].Name != "Sigurd" {
			t.Errorf("End = %+v", end)
		}
	})

	t.Run("the account the app knows a name by", func(t *testing.T) {
		known := func(name string) string {
			if name == "Sigurd" {
				return "76561198000000201"
			}
			return ""
		}
		_, evs, _ := replay(t, forum("steam-2022-06-28-join-then-crash.log"), known)
		if evs[0].Kind != "join" || evs[0].Account != "76561198000000201" {
			t.Errorf("join = %+v", evs[0])
		}
	})
}

// Lines no real log here shows, written from the server's own text (the
// fragments in Server.jar 1.3.3, FACTS.md) or from Java itself.
func TestMonitorOtherLines(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	line := func(s string) game.Line { return m.Line(s, now) }

	if l := line("Picked up JAVA_TOOL_OPTIONS: -Xss2m"); !l.Hide {
		t.Errorf("Java's own note is shown: %+v", l)
	}
	if l := line("\x1b[39m[2026-11-26 20:18:04] Players online: 2/10"); len(l.Events) != 1 || l.Events[0].Kind != "count" || l.Events[0].Count != 2 || !l.Events[0].Hold {
		t.Errorf("players line: %+v", l)
	}
	if l := line(`[2026-11-26 20:18:04] Client "Bob" had wrong version (1.3.2)`); len(l.Events) != 1 || l.Events[0].Kind != "bad_version" || l.Events[0].Name != "Bob" {
		t.Errorf("wrong version: %+v", l)
	}
	// A chat line can't pass for a join: chat starts with the name in
	// brackets.
	for _, chat := range []string{
		`[2026-11-26 20:18:04] (Mallory): Client "Eve" connected on slot 1/10.`,
		`[2026-11-26 20:18:04] (Mallory): Alex issued command: /stop`,
		`[2026-11-26 20:18:04] (Mallory): Client "Eve" had wrong version (1.0)`,
		`[2026-11-26 20:18:04] (Mallory): Completed world save`,
	} {
		if l := line(chat); len(l.Events) != 0 || l.Hide {
			t.Errorf("a chat line made events or was hidden: %+v", l)
		}
	}
	// The stop typed by an admin in the game.
	if l := line(`[2026-11-26 20:18:04] Alex issued command: /stop`); len(l.Events) != 1 || l.Events[0].Kind != "stopping" {
		t.Errorf("an admin's /stop: %+v", l)
	}
	// The line's own time is kept.
	if l := line("[2026-11-26 20:18:04] Starting world save"); l.Time.IsZero() || l.Time.Hour() != 20 || l.Text != "Starting world save" {
		t.Errorf("time and text: %+v", l)
	}
}

// Only a stop someone asked for counts as one: the manager restarts a
// server that stopped by itself, when the owner asked for that, and
// reports why it stopped.
func TestMonitorStopAskedOrNot(t *testing.T) {
	end := []string{
		"[2026-11-26 20:18:19] World time: 19, day 0",
		"[2026-11-26 20:18:19] Server has stopped",
		"[2026-11-26 20:18:19] Exiting in 2 seconds...",
	}
	for _, c := range []struct {
		name  string
		lines []string
		asked bool
	}{
		{"typed stop", []string{"[2026-11-26 20:18:16] > stop", "[2026-11-26 20:18:19] Stopped server on 2026/11/26 20:18:19 with code: SERVER_STOPPED"}, true},
		{"typed quit", []string{"[2026-11-26 20:18:16] > quit"}, true},
		{"SERVER_STOPPED alone", []string{"[2026-11-26 20:18:19] Stopped server on 2026/11/26 20:18:19 with code: SERVER_STOPPED"}, true},
		{"a crash", []string{"[2026-11-26 20:18:19] (ERR) Stuck in crash loop, stopping server.", "[2026-11-26 20:18:19] Stopped server on 2026/11/26 20:18:19 with code: SERVER_ERROR"}, false},
		{"a taken port", []string{"[2026-11-26 20:18:19] Stopped server on 2026/11/26 20:18:19 with code: NETWORK_ERROR"}, false},
		{"only the last lines", nil, false},
		// Asked for, then it went wrong: still asked for, so it isn't
		// started again against the wish of whoever stopped it.
		{"asked, then an error", []string{"[2026-11-26 20:18:16] Alex issued command: /stop", "[2026-11-26 20:18:19] Stopped server on 2026/11/26 20:18:19 with code: SERVER_ERROR"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := (&server{}).Watch(game.Record{}, nil).(*monitor)
			now := time.Date(2026, 11, 26, 21, 0, 0, 0, time.Local)
			var evs []game.Event
			for _, l := range append(slices.Clone(c.lines), end...) {
				evs = append(evs, m.Line(l, now).Events...)
			}
			st := m.State()
			if st.Stopping != c.asked || (count(evs, "stopping") == 1) != c.asked || count(evs, "stopping") > 1 {
				t.Errorf("Stopping %v, events %q; want a stop asked for: %v", st.Stopping, kinds(evs), c.asked)
			}
			if c.asked && st.StoppingAt.IsZero() {
				t.Errorf("StoppingAt not set")
			}
		})
	}
}

func TestExitHint(t *testing.T) {
	s := &server{}
	for _, c := range []struct {
		tail []string
		want string
	}{
		{[]string{"Error: Unable to access jarfile Server.jar"}, "Repair"},
		{[]string{"Error occurred during initialization of VM", "Could not reserve enough space for 16777216KB object heap"}, "Lower"},
		{[]string{`[2026-11-26 20:26:54] (ERR) java.lang.OutOfMemoryError: Java heap space`}, "Raise"},
		{[]string{"[2026-11-26 20:26:54] Exiting in 2 seconds..."}, ""},
	} {
		if got := s.ExitHint(c.tail); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("ExitHint(%q) = %q, want one with %q", c.tail, got, c.want)
		}
	}
}
