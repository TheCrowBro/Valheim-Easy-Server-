package corekeeper

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// The logs in testdata/real were written by the real 1.3.0.7 server (Steam
// build 26331994) run on Linux under Xvfb on 2026-11-26, with this
// machine's folders replaced by the app's layout; the one in
// testdata/forum is a real 1.0.0.14 log a player posted on GitHub, the
// only real log with a player joining and leaving. FACTS.md says where
// each one comes from and what it shows.
//
// replay feeds a log to a new Monitor, as the manager does, and returns
// the Monitor, every event and the lines shown on the Log page.
func replay(t *testing.T, path string) (*monitor, []game.Event, []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	var evs []game.Event
	var shown []string
	now := time.Date(2026, 11, 26, 21, 35, 0, 0, time.UTC)
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
	if len(out) < 5 {
		t.Fatalf("only %d logs in testdata", len(out))
	}
	return out
}

// TestMonitorGolden pins, for every real log, the events the Monitor finds,
// its state at the end and the lines the Log page shows, in
// testdata/monitor/<log>.txt (go test -update rewrites them; read the diff
// before committing it).
func TestMonitorGolden(t *testing.T) {
	for _, path := range logs(t) {
		name := strings.TrimSuffix(filepath.Base(path), ".log")
		t.Run(name, func(t *testing.T) {
			m, evs, shown := replay(t, path)
			var b strings.Builder
			fmt.Fprintf(&b, "# Events the Monitor finds in %s, its state at the end, then the lines the Log page shows.\n", filepath.ToSlash(path))
			for _, e := range evs {
				b.WriteString(e.Kind)
				if e.Name != "" {
					fmt.Fprintf(&b, " name=%q", e.Name)
				}
				if e.Account != "" {
					fmt.Fprintf(&b, " account=%s", e.Account)
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
			fmt.Fprintf(&b, "state: ready=%v version=%q code=%q count=%d players=%q stopping=%v quitHandlers=%v\n",
				st.Ready, st.Version, st.Code, st.Count, players, st.Stopping, m.quitHandlers)
			end := m.End(time.Date(2026, 11, 26, 22, 0, 0, 0, time.UTC))
			fmt.Fprintf(&b, "end: %d leave events, saved=%v\n", len(end), !m.State().LastSave.IsZero())
			fmt.Fprintf(&b, "shown: %d lines\n", len(shown))
			for _, l := range shown {
				if len(l) > 160 {
					l = l[:160] + "…"
				}
				b.WriteString("  " + l + "\n")
			}
			golden(t, filepath.Join("testdata", "monitor", name+".txt"), b.String())
		})
	}
}

// A real join and leave (spec 2.6): the 1.0.0.14 server a player posted.
// The leave line names only the Steam account; the Monitor knows who it is
// from the join line.
func TestMonitorRealJoinLeave(t *testing.T) {
	m, evs, _ := replay(t, filepath.Join("testdata", "forum", "github-2024-10-29-v1.0.0.14-join-leave.log"))
	want := []string{"code", "online", "join:Gonaeng", "leave:Gonaeng", "stopping"}
	if got := kindNames(evs); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("events %q, want %q", got, want)
	}
	if evs[2].Account != "76561198056526695" || evs[3].Account != "76561198056526695" {
		t.Errorf("accounts: join %q, leave %q", evs[2].Account, evs[3].Account)
	}
	st := m.State()
	if st.Code != "bvpvPP6a3VjMucnq5Cu8HifZQ73C" || len(st.Players) != 0 || !st.Stopping {
		t.Errorf("state %+v", st)
	}
	// The save was still pending when the log ends ("Quit blocked"), so it
	// doesn't count as saved.
	m.End(time.Now())
	if !m.State().LastSave.IsZero() {
		t.Errorf("a stop that never got to its quit handlers counts as saved")
	}
}

// The real 1.3.0.7 server: ready with its Game ID, the version, the
// relay warning once, and a Ctrl+C that saved.
func TestMonitorRealStartAndStop(t *testing.T) {
	for _, c := range []struct{ log, code string }{
		{"new-world-ctrl-c", "Ek8c5udkq9vH3cf"},
		{"load-world-ctrl-c", "Ek8c5udkq9vH3cf"},
		{"own-gameid-unicode-folder", "Ab3dEfGh9JkLmNpQrStUvWz12345"},
	} {
		m, evs, shown := replay(t, filepath.Join("testdata", "real", c.log+".log"))
		st := m.State()
		if st.Code != c.code || st.Version != "1.3.0.7" || !st.Ready || !st.Stopping {
			t.Errorf("%s: state %+v", c.log, st)
		}
		// The relay warning comes whenever Steam's relay first fails,
		// before or after the world is up; the rest are in order.
		got := kindNames(evs)
		if relay := slices.Index(got, "relay_failed"); relay >= 0 {
			got = slices.Delete(got, relay, relay+1)
		} else {
			t.Errorf("%s: no relay warning", c.log)
		}
		if strings.Join(got, " ") != "code online stopping" {
			t.Errorf("%s: events %q", c.log, kindNames(evs))
		}
		m.End(time.Now())
		if m.State().LastSave.IsZero() {
			t.Errorf("%s: the Ctrl+C stop that saved doesn't count as saved", c.log)
		}
		for _, l := range shown {
			if strings.HasPrefix(l, "CommandLineArgs") || strings.Contains(l, "GL_ARB") || len(l) > 400 {
				t.Errorf("%s: the Log page shows noise: %.80q", c.log, l)
			}
		}
	}
}

// A server that never gets a display never starts its session: no online.
func TestMonitorNoDisplay(t *testing.T) {
	m, evs, _ := replay(t, filepath.Join("testdata", "real", "no-display-hangs.log"))
	if len(evs) != 0 || m.State().Ready {
		t.Errorf("events %q, ready %v", kindNames(evs), m.State().Ready)
	}
}

// Lines the real server writes that the logs here don't have (the strings
// are in its code, FACTS.md).
func TestMonitorOtherLines(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	feed := func(l string) []game.Event { return m.Line(l, now).Events }
	feed("Started session with info: Ab3dEfGh9JkLmNpQrStUvWz12345")
	feed("timescale = 0")
	feed("[userid:76561198000000001] player Alex connected islocalplayer=False")
	feed("[userid:76561198000000002] player Bo Builder connected islocalplayer=False")
	feed("[userid:76561198000000002] is using new name Bo")
	if st := m.State(); len(st.Players) != 2 || st.Players[1].Name != "Bo" || st.Count != 2 {
		t.Fatalf("players %+v", st.Players)
	}
	// 1.3 puts a space after "userid:" in some lines; the leave takes both.
	if evs := feed("Disconnected from userid: 76561198000000002 with reason ClosedByPeer"); len(evs) != 1 || evs[0].Name != "Bo" {
		t.Errorf("leave: %+v", evs)
	}
	if evs := feed("Rejecting player with userid:76561198000000003 since player limit has been reached"); len(evs) != 1 || evs[0].Kind != "server_full" || evs[0].Account != "76561198000000003" {
		t.Errorf("server full: %+v", evs)
	}
	if evs := feed("Skipping world save due to serialization failure. Compressed data size: 0"); len(evs) != 1 || evs[0].Kind != "save_failed" {
		t.Errorf("save failed: %+v", evs)
	}
	if evs := feed("Checking system requirements: FAIL"); len(evs) != 1 || evs[0].Kind != "bad_graphics" {
		t.Errorf("system requirements: %+v", evs)
	}
	// An unknown leave (never joined in this log) names nobody.
	if evs := feed("Disconnected from userid:76561198000000009 with reason Timeout"); len(evs) != 0 {
		t.Errorf("an unknown leave: %+v", evs)
	}
	feed("Got quit request")
	feed("Running quit handlers")
	evs := m.End(now)
	if len(evs) != 1 || evs[0].Name != "Alex" || m.State().Count != 0 || !m.State().LastSave.IsZero() {
		// The save failed above, so the stop isn't a save.
		t.Errorf("end: %+v, state %+v", evs, m.State())
	}
}

// The session line comes before the world is loaded or made, and a quit
// request in between ends the server without saving (seen with the real
// server, FACTS.md): the server is online only once its world is up.
func TestMonitorOnlineWaitsForTheWorld(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	if evs := m.Line("Started session with info: KUHcBUjHoKAVm5eqUNad4GwhXwzr", now).Events; len(evs) != 1 || evs[0].Kind != "code" || m.State().Ready {
		t.Fatalf("the session line: %+v, ready %v", evs, m.State().Ready)
	}
	m.Line("no existing save file, starting with empty world", now)
	m.Line("Got quit request", now)
	m.Line("Running quit handlers", now)
	// Before the world is up a quit may be the server giving up by itself,
	// so it isn't reported as a stop someone asked for.
	if m.State().Stopping {
		t.Errorf("a quit before the world was up counts as a requested stop")
	}
	m.End(now)
	if m.State().Ready || !m.State().LastSave.IsZero() {
		t.Errorf("a quit before the world was up: %+v", m.State())
	}

	// A world that comes up: online at its first "timescale" line.
	m = (&server{}).Watch(game.Record{}, nil).(*monitor)
	m.Line("Started session with info: KUHcBUjHoKAVm5eqUNad4GwhXwzr", now)
	if evs := m.Line("timescale = 0", now).Events; len(evs) != 1 || evs[0].Kind != "online" || !m.State().Ready {
		t.Errorf("the world up: %+v", evs)
	}
	if evs := m.Line("timescale = 1", now).Events; len(evs) != 0 {
		t.Errorf("a second online: %+v", evs)
	}

	// No world line at all (a later version may word it differently):
	// online worldWait after the session, from Tick.
	m = (&server{}).Watch(game.Record{}, nil).(*monitor)
	if evs := m.Tick(now); len(evs) != 0 {
		t.Errorf("Tick before the session: %+v", evs)
	}
	m.Line("Started session with info: KUHcBUjHoKAVm5eqUNad4GwhXwzr", now)
	if evs := m.Tick(now.Add(worldWait - time.Second)); len(evs) != 0 {
		t.Errorf("Tick too soon: %+v", evs)
	}
	if evs := m.Tick(now.Add(worldWait)); len(evs) != 1 || evs[0].Kind != "online" {
		t.Errorf("Tick after worldWait: %+v", evs)
	}
	if evs := m.Tick(now.Add(2 * worldWait)); len(evs) != 0 {
		t.Errorf("a second online from Tick: %+v", evs)
	}
}

func kindNames(evs []game.Event) []string {
	var out []string
	for _, e := range evs {
		k := e.Kind
		if e.Name != "" {
			k += ":" + e.Name
		}
		out = append(out, k)
	}
	return out
}
