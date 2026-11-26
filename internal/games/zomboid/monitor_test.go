package zomboid

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// feed runs a log through a new Monitor, one line at a time, and returns
// every event and every line as the Log page gets it.
func feed(t *testing.T, log string) (*monitor, []game.Event, []game.Line) {
	t.Helper()
	s := Game{}.New(testEnv(t)).(*server)
	m := s.Watch(game.Record{}, nil).(*monitor)
	now := time.Date(2026, 11, 26, 20, 0, 0, 0, time.Local)
	var evs []game.Event
	var lines []game.Line
	for _, l := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		if l == "" {
			continue
		}
		out := m.Line(l, now)
		evs = append(evs, out.Events...)
		lines = append(lines, out)
		now = now.Add(time.Second)
	}
	return m, evs, lines
}

func kinds(evs []game.Event) []string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

// A real 42.20.4 server's console while commands were typed on it
// (testdata/real, FACTS.md): "players", "save", options, then "quit".
// Each whole save ends in "Saving finish"; the console's own "quit" is a
// stop someone asked for; the start-up and shut-down housekeeping is
// hidden; every header is taken off.
func TestMonitorRealConsoleSession(t *testing.T) {
	m, evs, lines := feed(t, string(readTestdata(t, "real/console-session-42.20.4.log")))
	if got := kinds(evs); !slices.Equal(got, []string{"saved", "stopping", "saved"}) {
		t.Errorf("events %q, want saved (the \"save\"), stopping (the \"quit\") and saved (on the way out)", got)
	}
	st := m.State()
	if !st.Stopping || st.LastSave.IsZero() || st.Ready {
		t.Errorf("state %+v: stopping and saved, never said it started (the excerpt begins later)", st)
	}
	var shown []string
	hidden := 0
	for _, l := range lines {
		if strings.Contains(l.Text, "f:0 st:") || strings.HasPrefix(l.Text, "LOG  :") {
			t.Errorf("a line kept its header: %q", l.Text)
		}
		if l.Hide {
			hidden++
		} else {
			shown = append(shown, l.Text)
		}
	}
	for _, want := range []string{`command entered via server console (System.in): "save"`, "World saved", "Saving finish", "Players connected (0): ", "* additem : Dar un objeto a un jugador. Si no se da un nombre de usuario, recibirás el objeto tú mismo. La cantidad es opcional. Usa /additem usuario module.item cantidad. Ejemplo: /additem rj Base.Axe 5"} {
		if !slices.Contains(shown, want) {
			t.Errorf("the Log page lacks %q", want)
		}
	}
	for _, l := range lines {
		if l.Hide && (strings.HasPrefix(l.Text, "Saving") || strings.Contains(l.Text, "command entered")) {
			t.Errorf("hidden though it matters: %q", l.Text)
		}
	}
	if hidden < 8 { // the ObjectID list, LightingFPS, the weather and a missing texture
		t.Errorf("only %d lines hidden", hidden)
	}
}

// A real 42.20.4 server's first start without -adminpassword
// (testdata/real, FACTS.md): it reads its version, hides the long list of
// log settings, and says it is waiting for an admin password.
func TestMonitorRealFirstStartAdminPrompt(t *testing.T) {
	m, evs, lines := feed(t, string(readTestdata(t, "real/first-start-admin-prompt-42.20.4.log")))
	if got := kinds(evs); !slices.Equal(got, []string{"admin_prompt"}) {
		t.Errorf("events %q", got)
	}
	if v := m.State().Version; v != "42.20.4" {
		t.Errorf("version %q", v)
	}
	shown := 0
	for _, l := range lines {
		if !l.Hide {
			shown++
		}
	}
	if shown > 20 {
		t.Errorf("%d of %d lines shown: the log settings list should be hidden", shown, len(lines))
	}
}

// The ready line, as real servers write it (FACTS.md), in the header forms
// seen in 42.20 and 42.22, and in Build 41's; and 42.22.2's version line.
func TestMonitorReadyLines(t *testing.T) {
	for _, c := range []struct{ line, version string }{
		{"LOG  : Network      f:0 st:34,907,676> *** SERVER STARTED ****", ""},
		{"LOG  : General      f:0, t:1760000000000, st:1,234> *** SERVER STARTED ****", ""},
		{"LOG  : General     , 1655308484498> 137,346,089> *** SERVER STARTED ****", ""},
	} {
		m, evs, lines := feed(t, "LOG  : General      f:0 st:6,734,248> version=42.22.2 3d39148846 integrity=pending demo=false\n"+c.line)
		if got := kinds(evs); !slices.Equal(got, []string{"online"}) || !m.State().Ready {
			t.Errorf("%q: events %q", c.line, got)
		}
		if v := m.State().Version; v != "42.22.2" {
			t.Errorf("version %q", v)
		}
		if lines[1].Text != "*** SERVER STARTED ****" {
			t.Errorf("shown as %q", lines[1].Text)
		}
	}
	// "t:" is a time of day (milliseconds since 1970); "st:" isn't.
	_, _, lines := feed(t, "LOG  : General      f:0, t:1760000000000, st:1,234> Saving finish\nLOG  : General      f:0 st:1,234> Saving finish")
	if !lines[0].Time.Equal(time.UnixMilli(1760000000000)) || !lines[1].Time.IsZero() {
		t.Errorf("times %v, %v", lines[0].Time, lines[1].Time)
	}
}

// Java's shutdown handling runs after a quit, Ctrl+C, a crash and a start
// that failed alike: on its own it is never a stop someone asked for, so a
// crash stays a crash for the manager (its report, its restart); and
// nothing after it means ready.
func TestMonitorShutdownIsNotAStopRequest(t *testing.T) {
	m, evs, _ := feed(t, strings.Join([]string{
		"LOG  : General      f:0 st:6,740,023> Exception in thread \"main\" LOG  : General      f:0 st:6,740,023> java.lang.NullPointerException",
		"LOG  : General      f:0 st:6,740,024> Shutdown handling started",
		"LOG  : Network      f:0 st:6,740,026> Server exited",
		"LOG  : Network      f:0 st:6,740,027> *** SERVER STARTED ****",
		"LOG  : General      f:0 st:6,740,028> Shutdown handling finished",
	}, "\n"))
	if st := m.State(); st.Stopping || st.Ready || len(evs) != 0 {
		t.Errorf("state %+v, events %q", st, kinds(evs))
	}
}

func TestMonitorRCONFailed(t *testing.T) {
	_, evs, _ := feed(t, "LOG  : General      f:0 st:1,000> RCON: error creating socket on port 27015")
	if len(evs) != 1 || evs[0].Kind != "rcon_failed" || !strings.Contains(evs[0].Text, "27015") {
		t.Errorf("events %+v", evs)
	}
}

// RCON's "players" is who is on: names it lists join, names it stops
// listing leave, and a count above zero holds the computer awake.
func TestMonitorProbed(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	m := s.Watch(game.Record{}, func(name string) string {
		if name == "Alex" {
			return "76561198000000001"
		}
		return ""
	}).(*monitor)
	now := time.Now()
	evs := m.Probed(game.ProbeResult{Up: true, Count: 0, Names: []string{}}, now)
	if !slices.Equal(kinds(evs), []string{"online"}) || !m.State().Ready {
		t.Errorf("first answer: %q", kinds(evs))
	}
	evs = m.Probed(game.ProbeResult{Up: true, Count: 2, Names: []string{"Alex", "Sam Lee"}}, now)
	if !slices.Equal(kinds(evs), []string{"join", "join", "count"}) || evs[0].Account != "76561198000000001" || !evs[2].Hold || evs[2].Count != 2 {
		t.Errorf("two on: %+v", evs)
	}
	evs = m.Probed(game.ProbeResult{Up: true, Count: 1, Names: []string{"Sam Lee"}}, now)
	if !slices.Equal(kinds(evs), []string{"leave", "count"}) || evs[0].Name != "Alex" {
		t.Errorf("Alex gone: %+v", evs)
	}
	if st := m.State(); st.Count != 1 || len(st.Players) != 1 {
		t.Errorf("state %+v", st)
	}
	if evs := m.End(now); !slices.Equal(kinds(evs), []string{"leave"}) || m.State().Count != 0 {
		t.Errorf("end: %+v", evs)
	}
}

func TestParsePlayers(t *testing.T) {
	for _, c := range []struct {
		reply string
		count int
		names []string
	}{
		{"Players connected (0): \n", 0, []string{}}, // a real 42.20.4 answer (FACTS.md)
		{"Players connected (2): \n-Alex\n-Sam Lee\n", 2, []string{"Alex", "Sam Lee"}},
		{"Players connected (1): \r\n-Bob\r\n", 1, []string{"Bob"}},
	} {
		res, err := parsePlayers(c.reply)
		if err != nil || !res.Up || res.Count != c.count || !slices.Equal(res.Names, c.names) {
			t.Errorf("%q: %+v, %v", c.reply, res, err)
		}
	}
	if res, err := parsePlayers("Unknown command players"); err == nil || res.Count != -1 {
		t.Errorf("a reply that isn't a list: %+v, %v", res, err)
	}
}

// The probe talks RCON to 127.0.0.1 on the run's port with the run's
// password, so it works after a takeover.
func TestProbeOverRCON(t *testing.T) {
	srv, err := rcon.NewTestServer("127.0.0.1:0", "pw", func(cmd string) string {
		if cmd == "players" {
			return "Players connected (1): \n-Alex\n"
		}
		return "Unknown command " + cmd
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	_, port, _ := strings.Cut(srv.Addr, "]:")
	if port == "" {
		_, port, _ = strings.Cut(srv.Addr, ":")
	}
	s := Game{}.New(testEnv(t)).(*server)
	rec := game.Record{Ports: map[string]int{"rcon": atoi(t, port)}, Secrets: map[string]string{"rcon": "pw"}}
	res, err := s.Probe(context.Background(), rec)
	if err != nil || res.Count != 1 || !slices.Equal(res.Names, []string{"Alex"}) {
		t.Errorf("probe: %+v, %v", res, err)
	}
	rec.Secrets["rcon"] = "wrong"
	if _, err := s.Probe(context.Background(), rec); err == nil {
		t.Errorf("a wrong password was taken")
	}
	if _, err := s.Probe(context.Background(), game.Record{}); err == nil {
		t.Errorf("a record without an RCON port")
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a port: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestExitHint(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	for _, c := range []struct{ line, want string }{
		{"java.net.ConnectException: Connection Startup Failed. Code: 5", "couldn't open its UDP ports"},
		{"LOG  : General      f:0 st:1> Failed to connect to Steam servers", "couldn't reach Steam"},
		{"Error occurred during initialization of VM", "Repair"},
		{"Invalid maximum heap size: -Xmx99999999m", "Java memory"},
		{"java.lang.OutOfMemoryError: Java heap space", "ran out of memory"},
		{"LOG  : General      f:0 st:1> Exiting due to errors loading C:\\x\\Server\\W_SandboxVars.lua", "sandbox settings"},
		{"LOG  : General      f:0 st:6,740,022> Workshop: item state DownloadPending -> Fail ID=3077900375", "Workshop"},
	} {
		if got := s.ExitHint([]string{"LOG  : General      f:0 st:1> something", c.line}); !strings.Contains(got, c.want) {
			t.Errorf("%q: hint %q, want one with %q", c.line, got, c.want)
		}
	}
	if got := s.ExitHint([]string{"LOG  : General      f:0 st:1> Shutdown handling finished"}); got != "" {
		t.Errorf("a normal end: %q", got)
	}
}

func TestJoin(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	sv := defaultSaved(t, s)
	sv.Server["password"] = "secret"
	stopped := s.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"game": "open", "direct": "open"}})
	if stopped.How != "address" || stopped.Address != "203.0.113.7" || stopped.Port != 16261 || stopped.Password != "secret" ||
		!slices.Equal(stopped.LAN, []string{"192.168.1.20:16261"}) || !stopped.Router || stopped.Public || stopped.Problem != "" || len(stopped.Steps) < 3 {
		t.Errorf("stopped: %+v", stopped)
	}
	rec := &game.Record{Port: 16300, Password: "old", Ports: map[string]int{"game": 16300, "direct": 16301}}
	st := &game.RunState{Version: "42.22.2"}
	running := s.Join(game.JoinInput{Rec: rec, State: st, Saved: sv, PublicIP: "203.0.113.7", Router: map[string]string{"game": "open", "direct": "failed"}})
	if running.Port != 16300 || running.Password != "old" || !strings.Contains(running.Problem, "16300 and 16301") ||
		!slices.ContainsFunc(running.Steps, func(s string) bool { return strings.Contains(s, "42.22.2") }) {
		t.Errorf("running: %+v", running)
	}
	if strings.Contains(strings.Join(running.Steps, " "), "old") {
		t.Errorf("the steps show the password itself: %q", running.Steps)
	}
}
