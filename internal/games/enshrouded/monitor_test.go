package enshrouded

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// feed gives every line of a log in testdata/real to a new Monitor, one
// second apart, and returns the Monitor, the lines as the Log page shows
// them and every event in order.
func feed(t *testing.T, file string, rec game.Record, known func(string) string) (*monitor, []game.Line, []game.Event) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "real", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := Game{}.New(testEnv(t)).Watch(rec, known).(*monitor)
	now := time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	var lines []game.Line
	var evs []game.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := m.Line(sc.Text(), now)
		lines = append(lines, l)
		evs = append(evs, l.Events...)
		now = now.Add(time.Second)
	}
	return m, lines, evs
}

func kinds(evs []game.Event) []string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind+":"+e.Name)
	}
	return k
}

// A real 1.0 log (CompaBot's test data, trimmed from a server's log of
// 2026-11-07): online at "HostOnline (up)", Eydis joins with the
// Steam account of the one connection waiting, and leaves.
func TestMonitorReal1_0JoinLeave(t *testing.T) {
	m, lines, evs := feed(t, "compabot-2026-11-07-join-leave.log", game.Record{}, nil)
	if got, want := kinds(evs), []string{"online:", "join:Eydis", "leave:Eydis"}; !slices.Equal(got, want) {
		t.Fatalf("events %q, want %q", got, want)
	}
	if evs[1].Account != "76561198000000202" {
		t.Errorf("the join's account is %q, not the Steam id from its connection line", evs[1].Account)
	}
	if evs[1].Text != "Eydis joined the game." || evs[2].Text != "Eydis left the game." {
		t.Errorf("feed texts %q, %q", evs[1].Text, evs[2].Text)
	}
	st := m.State()
	if !st.Ready || len(st.Players) != 0 || st.Count != 0 {
		t.Errorf("at the end: %+v", st)
	}
	// The line as the Log page shows it: without the level and time; the
	// engine's own state lines are hidden.
	if lines[1].Text != "[Session] 'HostOnline' (up)!" || lines[1].Hide {
		t.Errorf("the online line shows as %+v", lines[1])
	}
	if !lines[0].Hide {
		t.Errorf("the OnlineProviderSteam line isn't hidden: %+v", lines[0])
	}
	if lines[7].Hide || !strings.Contains(lines[7].Text, "CanAccessInventories") {
		t.Errorf("the permission line (which group the player joined with) should show: %+v", lines[7])
	}
}

// A real 2024 log (jsknnr/enshrouded-server issue 79, Game Version 602428,
// under Proton): the build, online, a join by the older connection lines,
// and the start-up tables and session dumps hidden.
func TestMonitorReal2024StartJoin(t *testing.T) {
	known := func(name string) string {
		if name == "Hakon" {
			return "should-not-be-needed"
		}
		return ""
	}
	m, lines, evs := feed(t, "jsknnr-79-2024-11-07-start-join.log", game.Record{}, known)
	if got, want := kinds(evs), []string{"online:", "join:Hakon"}; !slices.Equal(got, want) {
		t.Fatalf("events %q, want %q", got, want)
	}
	if evs[1].Account != "76561198000000205" {
		t.Errorf("Hakon's account is %q, not the one from \"Session accepted with peer\"", evs[1].Account)
	}
	st := m.State()
	if st.Version != "602428" || !st.Ready || len(st.Players) != 1 || st.Count != 1 {
		t.Errorf("state %+v", st)
	}
	shown := 0
	for _, l := range lines {
		if !l.Hide {
			shown++
		}
		for _, noisy := range []string{"[task]", "Machines:", "m#0(128)", "  0   256", "[os] Started thread", "Added a '"} {
			if strings.Contains(l.Text, noisy) && !l.Hide {
				t.Errorf("a noise line shows: %q", l.Text)
			}
		}
	}
	if shown > 40 {
		t.Errorf("%d of %d lines show on the Log page; the start-up noise should be hidden", shown, len(lines))
	}
}

// A real 1.0 stop (mbround18/enshrouded-docker issue 68, 2026-10-30): the
// Ctrl+C line marks the stop as asked for, and once the server has gone the
// world counts as saved (1.0 logs no line when it saves).
func TestMonitorRealCtrlC(t *testing.T) {
	m, _, evs := feed(t, "mbround18-68-2026-10-30-ctrl-c.log", game.Record{}, nil)
	if got := kinds(evs); !slices.Equal(got, []string{"stopping:"}) {
		t.Fatalf("events %q, want only stopping", got)
	}
	st := m.State()
	if !st.Stopping || st.StoppingAt.IsZero() {
		t.Errorf("state %+v", st)
	}
	end := time.Now()
	m.End(end)
	if !m.State().LastSave.Equal(end) {
		t.Errorf("LastSave = %v after a clean stop", m.State().LastSave)
	}
}

// A server that stops by itself is no stop someone asked for, so the
// manager sees the crash; nor does its end count as a save.
//
// The lines of a server that gives up saving are its program's strings
// (FACTS.md, "Saving and stopping"): no posted log has one. Where its
// "exit:" line would come, and what reason it would give, nobody knows, so
// the test gives one the program has no name for.
func TestMonitorStopsByItself(t *testing.T) {
	m := Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	m.Line("[I 00:00:01,284] [Session] 'HostOnline' (up)!", now)
	m.Line("[I 00:30:00,000] [server] Player 'Eydis' logged in with Permissions:", now)
	m.Line("[I 00:31:00,000] [server] Player 'Ash' logged in with Permissions:", now)
	if l := m.Line("[E 03:00:00,000] [server] Failed to Save. Retry 3", now); len(l.Events) != 1 || l.Events[0].Kind != "save_failed" {
		t.Errorf("a failed save: events %q", kinds(l.Events))
	}
	var quit []game.Event
	for _, p := range []string{"Eydis", "Ash"} {
		quit = append(quit, m.Line("[I 03:00:05,000] [server] Send Save Data Error Shutdown Message to Player '"+p+"'", now).Events...)
	}
	if len(quit) != 1 || quit[0].Kind != "save_failed" || !strings.Contains(quit[0].Text, "couldn't save") {
		t.Errorf("the server quitting over saves, told to two players: events %+v", quit)
	}
	m.Line("[I 03:00:05,100] [app] Trigger gameflow shutdown, exit: [9]", now)
	m.Line("[I 03:00:05,110] [game_server] Switching state from Run to Shutdown after 10805.11 s", now)
	if m.State().Stopping {
		t.Errorf("a stop by the server itself counted as one someone asked for: %+v", m.State())
	}
	m.End(now)
	if !m.State().LastSave.IsZero() {
		t.Error("a server that gave up saving counted as saved")
	}

	// Asked to stop, but the save system says it couldn't save.
	m = Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
	m.Line("[I 00:12:22,566] [app] Trigger gameflow shutdown, exit: Ctrl_C", now)
	m.Line("[I 00:12:22,580] [game_server] Switching state from Run to Shutdown after 730.20 s", now)
	l := m.Line("[E 00:12:23,000] Error while shutting down Server Save System ", now)
	if len(l.Events) != 1 || l.Events[0].Kind != "save_failed" {
		t.Errorf("the save system's error: %q", kinds(l.Events))
	}
	m.End(now)
	if !m.State().LastSave.IsZero() {
		t.Error("a stop whose save failed counted as saved")
	}

	// The program's other console events are stops someone asked for too:
	// Ctrl+Break, its console closed, the user signing out.
	for _, why := range []string{"Close", "Logoff", "Ctrl_Break"} {
		m = Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
		m.Line("[I 00:12:22,566] [app] Trigger gameflow shutdown, exit: "+why, now)
		if !m.State().Stopping {
			t.Errorf("exit: %s isn't a stop", why)
		}
	}
	// "Shutdown" and "SaveDataIssue" are reasons the server gives players
	// it drops, not names of console events; a reason the program has no
	// name for is no stop someone asked for either.
	for _, why := range []string{"Shutdown", "SaveDataIssue", "[6]"} {
		m = Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
		m.Line("[I 00:12:22,566] [app] Trigger gameflow shutdown, exit: "+why, now)
		if m.State().Stopping {
			t.Errorf("exit: %s counted as a stop someone asked for", why)
		}
	}
}

// After a takeover the log is read back all at once: each line counts at
// its own time, the process's start plus the time the line gives.
func TestMonitorReplayTimes(t *testing.T) {
	started := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	m, lines, evs := feed(t, "compabot-2026-11-07-join-leave.log", game.Record{Started: started}, nil)
	if want := started.Add(1*time.Minute + 38*time.Second + 732*time.Millisecond); !evs[1].Time.Equal(want) {
		t.Errorf("the join counted at %v, want %v (the line's own time)", evs[1].Time, want)
	}
	if !lines[1].Time.Equal(started.Add(1156 * time.Millisecond)) {
		t.Errorf("the online line's time is %v", lines[1].Time)
	}
	_ = m
}

// The query port's count wins when it says more players are on than the
// log named, and above 0 it holds the computer awake; a 0 takes nobody the
// log named off the list.
func TestMonitorProbe(t *testing.T) {
	m := Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	m.Line("[I 00:00:01,284] [Session] 'HostOnline' (up)!", now)
	m.Line("[I 00:01:38,732] [server] Player 'Alex' logged in with Permissions:", now)
	evs := m.Probed(game.ProbeResult{Up: true, Count: 2}, now)
	if len(evs) != 1 || evs[0].Kind != "count" || evs[0].Count != 2 || !evs[0].Hold {
		t.Errorf("a count of 2: %+v", evs)
	}
	if st := m.State(); st.Count != 2 || len(st.Players) != 1 {
		t.Errorf("state after the probe: count %d, players %d", st.Count, len(st.Players))
	}
	if evs := m.Probed(game.ProbeResult{Up: true, Count: 0}, now.Add(time.Hour)); len(evs) != 0 {
		t.Errorf("a count of 0: %+v", evs)
	}
	if st := m.State(); st.Count != 1 || len(st.Players) != 1 {
		t.Errorf("a 0 from the probe took Alex off: %+v", st)
	}
	if evs := m.Probed(game.ProbeResult{Up: false, Count: -1}, now); evs != nil {
		t.Errorf("a failed probe gave %+v", evs)
	}
}

// When the log never says the server is online (the app can't read it, or
// a new version words it otherwise), a query port that has answered for a
// minute means it is: Steam lists it, so friends can join.
func TestMonitorOnlineFromProbe(t *testing.T) {
	m := Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	if evs := m.Tick(now.Add(time.Hour)); evs != nil {
		t.Errorf("online without any answer: %+v", evs)
	}
	m.Probed(game.ProbeResult{Up: true, Count: 0}, now)
	if evs := m.Tick(now.Add(30 * time.Second)); evs != nil {
		t.Errorf("online too soon: %+v", evs)
	}
	evs := m.Tick(now.Add(probeFallback))
	if len(evs) != 1 || evs[0].Kind != "online" || !m.State().Ready {
		t.Errorf("after a minute of answers: %+v", evs)
	}
	if evs := m.Tick(now.Add(2 * probeFallback)); evs != nil {
		t.Errorf("online twice: %+v", evs)
	}
}

// The warnings the server can give, and a session that closes.
func TestMonitorWarnings(t *testing.T) {
	cases := []struct{ line, kind string }{
		{"[I 00:20:00,000] [online] Session rejected with peer because it is full (76561198000000001)", "server_full"},
		{"[I 00:00:00,451] [online] Couldn't bind dedicated server socket. error='address in use'", "port_in_use"},
		{"[I 00:00:01,035] [online] Server failed to connected to Steam 3", "no_steam"},
		{"[I 02:00:00,000] [online] Server lost connection to Steam", "no_steam"},
		{"Internal Error: There are more reserved slots than overall slots", "bad_config"},
		{"[E 00:00:02,000] [server] Failed to load Server Save Game", "load_failed"},
		{"[E 00:30:00,000] [server] Failed to Save. Retry 3", "save_failed"},
	}
	for _, c := range cases {
		m := Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
		l := m.Line(c.line, time.Now())
		if len(l.Events) != 1 || l.Events[0].Kind != c.kind || l.Events[0].Text == "" {
			t.Errorf("%q gave %+v, want one %s event with a text", c.line, l.Events, c.kind)
		}
	}

	m := Game{}.New(testEnv(t)).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	m.Line("[I 00:00:01,284] [Session] 'HostOnline' (up)!", now)
	m.Line("[I 00:01:38,732] [server] Player 'Alex' logged in with Permissions:", now)
	evs := m.Line("[I 00:52:00,674] [Session] 'HostOnline' (down)!", now).Events
	if len(evs) != 1 || evs[0].Kind != "leave" || evs[0].Name != "Alex" || m.State().Ready {
		t.Errorf("the session closing: %+v, ready %v", evs, m.State().Ready)
	}
}

// The handle line before a join isn't a player, a player seen twice isn't
// two, and 2024's servers logged every save. The save lines are from a
// player's post of a 2024 server log (Steam discussions, 2024-01-31),
// with the brackets the forum turned into parentheses put back.
func TestMonitorDetails(t *testing.T) {
	m := Game{}.New(testEnv(t)).Watch(game.Record{}, func(name string) string { return "acct-" + name }).(*monitor)
	now := time.Now()
	if evs := m.Line("[I 00:01:38,730] [server] Machine '1': Player '0(0)' logged in", now).Events; len(evs) != 0 {
		t.Errorf("the handle line gave %+v", evs)
	}
	evs := m.Line("[I 00:01:38,732] [server] Player 'Tom's Kid' logged in with Permissions:", now).Events
	if len(evs) != 1 || evs[0].Name != "Tom's Kid" || evs[0].Account != "acct-Tom's Kid" {
		t.Errorf("a name with an apostrophe, and no connection line: %+v", evs)
	}
	if evs := m.Line("[I 00:01:40,000] [server] Player 'Tom's Kid' logged in with Permissions:", now).Events; len(evs) != 0 {
		t.Errorf("a player seen twice: %+v", evs)
	}
	if evs := m.Line("[I 00:02:13,732] [server] Remove Player 'Tom's Kid'", now).Events; len(evs) != 1 || evs[0].Kind != "leave" {
		t.Errorf("the leave: %+v", evs)
	}
	l := m.Line("[I 02:45:04,770] [server] Saved", now)
	if len(l.Events) != 1 || l.Events[0].Kind != "saved" || m.State().LastSave.IsZero() {
		t.Errorf("a 2024 save line: %+v", l.Events)
	}
	if l := m.Line("[I 02:45:04,770] [server] Save Serialization took 202.55 ms over 15 Ticks", now); len(l.Events) != 0 {
		t.Errorf("not a save line: %+v", l.Events)
	}
	// Lines printed before the log opens have no level and time.
	if l := m.Line("[app] Trigger gameflow shutdown, exit: Ctrl_C", now); len(l.Events) != 1 || l.Events[0].Kind != "stopping" {
		t.Errorf("a line without the prefix: %+v", l.Events)
	}
	// Once stopping, the session line doesn't make it online again.
	if evs := m.Line("[I 00:00:01,284] [Session] 'HostOnline' (up)!", now).Events; len(evs) != 0 {
		t.Errorf("online while stopping: %+v", evs)
	}
}
