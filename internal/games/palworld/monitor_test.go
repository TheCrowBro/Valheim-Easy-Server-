package palworld

import (
	"os"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// feed runs a testdata log through a new Monitor and returns its events
// and the lines it shows.
func feed(t *testing.T, path string) (*monitor, []game.Event, []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	var evs []game.Event
	var shown []string
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line := m.Line(l, now)
		evs = append(evs, line.Events...)
		if !line.Hide {
			shown = append(shown, line.Text)
		}
	}
	return m, evs, shown
}

func kinds(evs []game.Event) string {
	var k []string
	for _, e := range evs {
		s := e.Kind
		if e.Name != "" {
			s += ":" + e.Name
		}
		k = append(k, s)
	}
	return strings.Join(k, " ")
}

// A real 1.0.5 start, REST save and REST shutdown (FACTS.md): the version,
// online from "Running Palworld dedicated server", the save and the stop
// the app asked for, and the Steam library's chatter hidden.
func TestMonitorRealStartSaveShutdown(t *testing.T) {
	m, evs, shown := feed(t, "testdata/monitor/v1.0.5.102999-start-save-shutdown.log")
	if got := kinds(evs); got != "online saved stopping" {
		t.Errorf("events = %q", got)
	}
	st := m.State()
	if st.Version != "v1.0.5.102999" || !st.Ready || st.Extra["readyBy"] != "log" || st.LastSave.IsZero() || !st.Stopping {
		t.Errorf("state = %+v", st)
	}
	for _, l := range shown {
		if strings.Contains(l, "S_API") || strings.Contains(l, "breakpad") {
			t.Errorf("noise shown: %q", l)
		}
	}
	if len(shown) != 6 {
		t.Errorf("shown = %q", shown)
	}
}

// Real join and leave lines (0.2.0.6, the version that added them).
func TestMonitorRealJoinLeave(t *testing.T) {
	m, evs, _ := feed(t, "testdata/monitor/v0.2.0.6-join-leave.log")
	if got := kinds(evs); got != "join:HunterAP leave:HunterAP" {
		t.Errorf("events = %q", got)
	}
	if evs[0].Account != "steam_<redacted>" || evs[0].Time.Format("15:04:05") != "19:26:37" {
		t.Errorf("join = %+v", evs[0])
	}
	if len(m.State().Players) != 0 {
		t.Errorf("players = %+v", m.State().Players)
	}
}

// A real log from a server in a container whose wrapper writes lines of
// its own, with RCON commands echoed: only the server's join line is a
// join; the wrapper's "has joined" and the commands are not.
func TestMonitorRealContainerLog(t *testing.T) {
	m, evs, _ := feed(t, "testdata/monitor/v0.2-container-wrapper.log")
	if got := kinds(evs); got != "join:Tanaka Lyanne" {
		t.Errorf("events = %q", got)
	}
	if st := m.State(); len(st.Players) != 1 || st.Players[0].Account != "steam_76561198379551257" {
		t.Errorf("players = %+v", st.Players)
	}
}

// The 1.0 lines: "connected the server" while loading in, then "joined
// the server" with the player id too.
func TestMonitorV1Joined(t *testing.T) {
	m, evs, _ := feed(t, "testdata/monitor/v1.0.3.101283-connected-joined.log")
	if got := kinds(evs); got != "online join:Alex" {
		t.Errorf("events = %q", got)
	}
	if st := m.State(); st.Version != "v1.0.3.101283" || len(st.Players) != 1 || st.Players[0].Account != "steam_00000000000000000" {
		t.Errorf("state = %+v", st)
	}
	if evs[1].Time.Day() != 1 || evs[1].Time.Month() != 8 {
		t.Errorf("the join's own time = %v", evs[1].Time)
	}
}

// The REST API's answers win: names it lists join, names it doesn't list
// leave, its count holds the computer awake, and an answer is enough to
// say the server is up.
func TestMonitorProbed(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, func(name string) string { return "known:" + name }).(*monitor)
	now := time.Now()
	evs := m.Probed(game.ProbeResult{Up: true, Count: 3, Names: []string{"Alex", "Sam"}, Version: "v1.0.5.102999"}, now)
	if got := kinds(evs); got != "online join:Alex join:Sam count" {
		t.Errorf("first probe: %q", got)
	}
	if evs[1].Account != "known:Alex" || evs[3].Count != 3 || !evs[3].Hold {
		t.Errorf("events = %+v", evs)
	}
	if st := m.State(); st.Count != 3 || st.Version != "v1.0.5.102999" || st.Extra["readyBy"] != "rest" {
		t.Errorf("state = %+v", st)
	}
	evs = m.Probed(game.ProbeResult{Up: true, Count: 1, Names: []string{"Sam"}}, now.Add(30*time.Second))
	if got := kinds(evs); got != "leave:Alex count" {
		t.Errorf("second probe: %q", got)
	}
	// An answer that couldn't list players changes nobody.
	if evs := m.Probed(game.ProbeResult{Up: true, Count: -1}, now.Add(time.Minute)); len(evs) != 0 || len(m.State().Players) != 1 {
		t.Errorf("a probe without names: %q, players %+v", kinds(evs), m.State().Players)
	}
	if got := kinds(m.End(now.Add(2 * time.Minute))); got != "leave:Sam" || m.State().Ready {
		t.Errorf("End: %q, ready %v", got, m.State().Ready)
	}
}

// A join or leave line that the server held back and wrote after a newer
// probe saw otherwise isn't acted on, so a player who has already gone
// doesn't come back, and one who came back doesn't leave.
func TestMonitorStaleLines(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	base := time.Date(2026, 11, 26, 20, 0, 0, 0, time.Local)
	stamp := func(d time.Duration) string { return "[" + base.Add(d).Format("2006-01-02 15:04:05") + "] " }

	m.Probed(game.ProbeResult{Up: true, Count: 0, Names: []string{}}, base.Add(time.Minute))
	// Alex joined and left before the probe; the lines come late.
	if evs := m.Line(stamp(10*time.Second)+"[LOG] Alex joined the server. (User id: steam_1, Player id: 0A0B0C0D000000000000000000000000)", base.Add(70*time.Second)).Events; len(evs) != 0 {
		t.Errorf("a stale join counted: %q", kinds(evs))
	}
	// A join after the probe counts.
	if evs := m.Line(stamp(80*time.Second)+"[LOG] Sam joined the server. (User id: steam_2, Player id: 0A0B0C0E000000000000000000000000)", base.Add(80*time.Second)).Events; kinds(evs) != "join:Sam" {
		t.Errorf("a new join: %q", kinds(evs))
	}
	// Sam is still on at the next probe; a late leave line from before it
	// (an earlier visit) changes nothing.
	m.Probed(game.ProbeResult{Up: true, Count: 1, Names: []string{"Sam"}}, base.Add(2*time.Minute))
	if evs := m.Line(stamp(90*time.Second)+"[LOG] Sam left the server. (User id: steam_2)", base.Add(125*time.Second)).Events; len(evs) != 0 {
		t.Errorf("a stale leave counted: %q", kinds(evs))
	}
	if evs := m.Line(stamp(130*time.Second)+"[LOG] Sam left the server. (User id: steam_2)", base.Add(130*time.Second)).Events; kinds(evs) != "leave:Sam" {
		t.Errorf("a new leave: %q", kinds(evs))
	}
}

// The app's own probe calls are logged by the server every 30 seconds;
// they stay off the Log page. A crash's lines don't read as a stop anyone
// asked for, so the manager reports the crash and can start it again.
func TestMonitorProbeLinesAndCrash(t *testing.T) {
	m := (&server{}).Watch(game.Record{}, nil).(*monitor)
	now := time.Now()
	for _, l := range []string{
		"[2026-11-26 20:00:00] [LOG] REST accessed endpoint /v1/api/players OK",
		"[2026-11-26 20:00:00] [LOG] REST accessed endpoint /v1/api/info OK",
	} {
		if line := m.Line(l, now); !line.Hide || len(line.Events) != 0 {
			t.Errorf("%q: %+v", l, line)
		}
	}
	if line := m.Line("[2026-11-26 20:00:01] [LOG] REST accessed endpoint /v1/api/info Unauthorized", now); line.Hide {
		t.Error("a refused REST call must show")
	}
	for _, l := range []string{"LowLevelFatalError [File:Unknown] [Line: 0] Out of memory", "REST API stopped"} {
		m.Line(l, now)
	}
	if m.State().Stopping {
		t.Error("a crash was taken for a requested stop")
	}
}
