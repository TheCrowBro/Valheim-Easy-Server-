package minecraft

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/rcon"
)

// The logs in testdata are real: 26.3's were captured from Mojang's
// server.jar run on Linux with Temurin 25 (players were driven by a small
// offline-mode client, so the server ran with online-mode=false); the
// online-mode lines come from published logs. FACTS.md says which is which.

// replay feeds a log to a fresh Monitor, as the app does after a start or a
// takeover, and returns the events and the lines.
type replayed struct {
	m      *monitor
	events []game.Event
	lines  []game.Line
	turnOn chan []string // what turnOn was asked to put on the allow list
}

// turned collects what turnOn was asked, waiting a moment for its
// goroutine when want calls are expected.
func (r *replayed) turned(want int) [][]string {
	var out [][]string
	timeout := time.After(2 * time.Second)
	for len(out) < want {
		select {
		case p := <-r.turnOn:
			out = append(out, p)
		case <-timeout:
			return out
		}
	}
	for {
		select {
		case p := <-r.turnOn:
			out = append(out, p)
		case <-time.After(20 * time.Millisecond):
			return out
		}
	}
}

func replay(t *testing.T, file string, rec game.Record) *replayed {
	t.Helper()
	return replayLines(t, strings.Split(strings.TrimRight(readFile(t, filepath.Join("testdata", file)), "\n"), "\n"), rec)
}

func replayLines(t *testing.T, lines []string, rec game.Record) *replayed {
	t.Helper()
	s := Game{}.New(testEnv(t)).(*server)
	r := &replayed{turnOn: make(chan []string, 8)}
	known := map[string]string{"Steve": "8667ba71-b85a-4004-af54-457a9734eed7"}
	r.m = s.Watch(rec, func(n string) string { return known[n] }).(*monitor)
	r.m.turnOn = func(_ game.Record, playing []string) { r.turnOn <- playing }
	now := time.Date(2026, 11, 26, 23, 0, 0, 0, time.Local)
	for _, l := range lines {
		line := r.m.Line(l, now)
		r.lines = append(r.lines, line)
		r.events = append(r.events, line.Events...)
	}
	return r
}

func testEnv(t *testing.T) game.Env {
	root := t.TempDir()
	return game.Env{Instance: ID, Data: root, Dirs: game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"),
	}}
}

// kinds lists the events as "kind:name".
func (r *replayed) kinds() []string {
	var out []string
	for _, e := range r.events {
		s := e.Kind
		if e.Name != "" {
			s += ":" + e.Name
		}
		out = append(out, s)
	}
	return out
}

func (r *replayed) find(kind string) []game.Event {
	var out []game.Event
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// planned is a run record as Plan writes it, with the allow list's state.
func planned(allow, mode string) game.Record {
	a, _ := json.Marshal(allow)
	m, _ := json.Marshal(mode)
	return game.Record{Ports: map[string]int{roleGame: 25565, roleRCON: 25575}, Secrets: map[string]string{roleRCON: "x"},
		Extra: map[string]json.RawMessage{extraAllowList: a, extraAllowMode: m}}
}

// TestMonitorSession: a whole real session on a new world: ready, a join,
// a death, a chat line worded like a death, an advancement, a save, two
// players turned away by the allow list, the leave, and the stop.
func TestMonitorSession(t *testing.T) {
	r := replay(t, "session-26.3.log", planned(allowOff, allowAuto))
	want := []string{
		"saved", "saved", "saved", // the save after the world was made, logged after "Done"
		"join:Alex", "death:Alex", "saved",
		"turned_away:Bob", "turned_away:Steve",
		"leave:Alex", "stopping", "saved", "saved", "saved",
	}
	got := slices.DeleteFunc(r.kinds(), func(k string) bool { return k == "online" })
	if !slices.Equal(got, want) {
		t.Errorf("events (without online):\n got %q\nwant %q", got, want)
	}
	if on := r.find("online"); len(on) != 1 {
		t.Errorf("%d online events, want 1", len(on))
	} else if on[0].Time.Format("15:04:05") != "19:40:08" {
		t.Errorf("online at %v, want the Done line's 19:40:08", on[0].Time)
	}
	st := r.m.State()
	if st.Version != "26.3" || !st.Ready || !st.Stopping || len(st.Players) != 0 || st.Count != 0 {
		t.Errorf("state at the end = %+v", st)
	}
	if d := r.find("death"); len(d) != 1 || d[0].Text != "Alex was slain by Zombie" {
		t.Errorf("deaths = %+v, want only Alex slain by a zombie (the chat line isn't one)", d)
	}
	if a := r.find("turned_away"); len(a) == 2 && a[1].Account != "8667ba71-b85a-4004-af54-457a9734eed7" {
		t.Errorf("Steve was turned away with account %q; the known one should be used", a[1].Account)
	}
	// Nothing in this session came from the internet.
	if turned := r.turned(0); len(turned) != 0 || len(r.find("allow_list_on")) != 0 {
		t.Errorf("the allow list was turned on by a login from this computer: %v", turned)
	}
	// The app's own RCON connections and the JVM's warnings are hidden; the
	// game's own lines aren't, and they lose their stamp and "System chat".
	shown := map[string]bool{}
	for _, l := range r.lines {
		if l.Hide {
			if strings.Contains(l.Text, "joined") || strings.Contains(l.Text, "Done") {
				t.Errorf("hid %q", l.Text)
			}
			continue
		}
		shown[l.Text] = true
		if strings.Contains(l.Text, "RCON Client") || strings.HasPrefix(l.Text, "WARNING: ") || strings.HasPrefix(l.Text, "Disconnecting ") {
			t.Errorf("showed noise %q", l.Text)
		}
	}
	for _, s := range []string{"Alex joined the game", "Alex was slain by Zombie", "[Not Secure] <Alex> Alex fell from a high place",
		"Warning: **** SERVER IS RUNNING IN OFFLINE/INSECURE MODE!", "Bob (/127.0.0.1:52238) lost connection: You are not white-listed on this server!"} {
		if !shown[s] {
			t.Errorf("the Log page doesn't show %q", s)
		}
	}
}

// TestMonitorDeaths: each kind of death in a real log becomes a death of
// the player who is on; a chat line and an advancement don't.
func TestMonitorDeaths(t *testing.T) {
	r := replay(t, "deaths-26.3.log", planned(allowOff, allowOff))
	var texts []string
	for _, e := range r.find("death") {
		if e.Name != "Alex" {
			t.Errorf("death of %q", e.Name)
		}
		texts = append(texts, e.Text)
	}
	want := []string{"Alex hit the ground too hard", "Alex was slain by Zombie", "Alex was killed", "Alex drowned",
		"Alex blew up", "Alex fell out of the world", "Alex died"}
	if !slices.Equal(texts, want) {
		t.Errorf("deaths:\n got %q\nwant %q", texts, want)
	}
}

// TestDeathMessages: every death message of 26.3 is recognised for a
// player who is on, and none for someone who isn't (a mob or a pet named
// like a player); player chat never is.
func TestDeathMessages(t *testing.T) {
	var msgs map[string]string
	if err := json.Unmarshal(deathsJSON, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 106 {
		t.Errorf("deaths.json has %d messages; 26.3's language file has 106", len(msgs))
	}
	on := func(n string) bool { return n == "Alex" }
	for key, msg := range msgs {
		if key == "death.attack.badRespawnPoint.link" {
			continue // a piece of badRespawnPoint.message, never said alone
		}
		line := strings.NewReplacer("%1$s", "Alex", "%2$s", "Zombie", "%3$s", "[Diamond Sword]", "%s", "Alex was slain by Zombie").Replace(msg)
		if got := deathOf(line, on); got != "Alex" {
			t.Errorf("%s: %q read as the death of %q", key, line, got)
		}
		other := strings.ReplaceAll(line, "Alex", "Bob")
		if got := deathOf(other, on); got != "" {
			t.Errorf("%s: %q read as the death of %q, who isn't playing", key, other, got)
		}
	}
	for _, chat := range []string{"<Alex> Alex died", "[Not Secure] <Alex> Alex drowned", "Alex has made the advancement [Stone Age]", "Alex joined the game"} {
		if got := deathOf(chat, on); got != "" {
			t.Errorf("%q read as the death of %q", chat, got)
		}
	}
	// A killer named like the end of the message still finds the player.
	if got := deathOf("Alex was slain by Bob was slain by Zombie", on); got != "Alex" {
		t.Errorf("named killer: %q", got)
	}
	// The cut-short copy Minecraft sends for a message that's too long.
	if got := deathOf("Actually, the message was too long to deliver fully. Sorry! Here's a stripped version: Alex was slain by a very long na", on); got != "Alex" {
		t.Errorf("message_too_long: %q", got)
	}
}

// TestMonitorRenamed: a player who changed their Minecraft name joins
// under the new one; the old one is only in the feed text.
func TestMonitorRenamed(t *testing.T) {
	r := replay(t, "renamed-26.3.log", planned(allowOn, allowOn))
	if got := r.kinds(); !slices.Equal(got, []string{"join:Alex", "leave:Alex"}) {
		t.Errorf("events %q", got)
	}
	if j := r.find("join"); len(j) == 1 && !strings.Contains(j[0].Text, "OldAlex") {
		t.Errorf("join text %q doesn't name the old name", j[0].Text)
	}
}

// TestMonitorStopWithPlayerOn: "stop" with a player on logs no leave; the
// run's end makes them leave.
func TestMonitorStopWithPlayerOn(t *testing.T) {
	r := replay(t, "stop-player-on-26.3.log", planned(allowOn, allowOn))
	if got := r.kinds(); !slices.Equal(got, []string{"join:Alex", "stopping", "saved", "saved", "saved"}) {
		t.Errorf("events %q", got)
	}
	end := r.m.End(time.Now())
	if len(end) != 1 || end[0].Kind != "leave" || end[0].Name != "Alex" {
		t.Errorf("End = %+v, want Alex leaving", end)
	}
	if st := r.m.State(); st.Ready || len(st.Players) != 0 {
		t.Errorf("after End: %+v", st)
	}
}

// TestMonitorOnlineMode: in online mode the login thread names each
// player's account first (a 26.1.2 log, from Paper, which keeps the vanilla
// login lines); the join carries it. A 1.8 log has the same lines, a
// renamed join, and a login from a public address: with the allow list on
// "auto" and off, that turns it on.
func TestMonitorOnlineMode(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(readFile(t, filepath.Join("testdata", "online-paper-26.1.2.log"))), "\n")
	lines = append(lines, "[02:11:52] [Server thread/INFO]: System chat: PerilousBear719 joined the game") // 26.3's join line
	r := replayLines(t, lines, planned(allowOff, allowAuto))
	if j := r.find("join"); len(j) != 1 || j[0].Account != "a3e3d240-fa59-46a7-82bb-f1d2b9f20efd" {
		t.Errorf("join = %+v, want the account from the UUID line", j)
	}
	if r.m.State().Version != "26.1.2" {
		t.Errorf("version %q", r.m.State().Version)
	}
	if len(r.turned(0)) != 0 {
		t.Errorf("a hidden address (**.**.**.**) turned the allow list on")
	}

	r = replay(t, "online-1.8-mc78020.log", planned(allowOff, allowAuto))
	if got := r.kinds(); !slices.Equal(got, []string{"allow_list_on:Andr3wSp1tsLava", "join:Andr3wSp1tsLava", "leave:Andr3wSp1tsLava"}) {
		t.Errorf("events %q", got)
	}
	if turned := r.turned(1); len(turned) != 1 || !slices.Equal(turned[0], []string{"Andr3wSp1tsLava"}) {
		t.Errorf("turnOn asked for %q, want the player who came in", turned)
	}
	if st := r.m.State(); st.Extra[extraAllowList] != allowOn {
		t.Errorf("after turning on, State().Extra = %v", st.Extra)
	}
	if j := r.find("join"); len(j) == 1 && j[0].Account != "45c5d60b-8d2f-4aa5-b2c3-97103ad8328b" {
		t.Errorf("join account %q", j[0].Account)
	}

	// The same login with the list already on, or set to "off", changes nothing.
	for _, rec := range []game.Record{planned(allowOn, allowAuto), planned(allowOff, allowOff), {}} {
		r = replay(t, "online-1.8-mc78020.log", rec)
		if len(r.find("allow_list_on")) != 0 {
			t.Errorf("record %v: the allow list was turned on", rec.Extra)
		}
	}
}

// TestMonitorTakeoverReplay: a copy of the app that takes over a running
// server reads its whole log again, through a Monitor made from the run
// record, which still says the allow list was off at the start. When the
// list was turned on earlier in the run (State has allowListOn), the old
// login from the internet must send nothing: the owner may have taken that
// player off the list since. When it never went on (the earlier copy
// couldn't reach RCON), it goes on once the log has been read, with
// whoever is playing then, not whoever was playing back then.
func TestMonitorTakeoverReplay(t *testing.T) {
	lines := strings.Split(strings.TrimRight(readFile(t, filepath.Join("testdata", "online-1.8-mc78020.log")), "\n"), "\n")
	for _, c := range []struct {
		name   string
		state  game.Values
		replay bool             // the manager says it is reading back (Replaying)
		end    func(m *monitor) // how the Monitor learns the log has been read
		want   []string         // what reaches RCON
	}{
		{"turned on earlier", game.Values{stateAllowListOn: "2026-11-26T05:40:43Z"}, true,
			func(m *monitor) {
				m.Replaying(false)
				m.Line("[06:30:00] [Server thread/INFO]: Stopping server", time.Now())
			}, nil},
		{"turned on earlier, without the hook", game.Values{stateAllowListOn: "2026-11-26T05:40:43Z"}, false,
			func(m *monitor) {}, nil},
		{"never turned on", game.Values{}, true,
			func(m *monitor) { m.Tick(time.Now()) }, []string{"whitelist on"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			srv, err := rcon.NewTestServer("127.0.0.1:0", "pw", func(cmd string) string {
				mu.Lock()
				got = append(got, cmd)
				mu.Unlock()
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { srv.Close() })
			_, port, _ := net.SplitHostPort(srv.Addr)
			rec := planned(allowOff, allowAuto)
			rec.Ports[roleRCON], _ = strconv.Atoi(port)
			rec.Secrets[roleRCON] = "pw"

			s := Game{}.New(testEnv(t)).(*server)
			s.store.Save(func(sv *game.Saved) error { sv.State = c.state.Clone(); return nil })
			m := s.Watch(rec, nil).(*monitor)
			calls := make(chan []string, 4)
			turnOn := m.turnOn // the real one, over RCON
			m.turnOn = func(r game.Record, playing []string) { turnOn(r, playing); calls <- playing }

			now := time.Date(2026, 11, 26, 23, 0, 0, 0, time.Local)
			for _, l := range lines {
				if c.replay {
					m.Replaying(true) // as the manager does before each line
				}
				m.Line(l, now)
			}
			if c.replay {
				select {
				case p := <-calls:
					t.Errorf("the allow list was turned on (%q) while the old log was read", p)
				case <-time.After(50 * time.Millisecond):
				}
			}
			if st := m.State(); st.Extra[extraAllowList] != allowOn {
				t.Errorf("after the login from the internet, State().Extra = %v, want the list on", st.Extra)
			}
			c.end(m)
			select {
			case p := <-calls:
				if c.replay && len(p) != 0 {
					t.Errorf("turnOn asked for %q; that player left long ago", p)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the allow list was never turned on")
			}
			m.Tick(now) // once is enough
			select {
			case p := <-calls:
				t.Errorf("turned on a second time (%q)", p)
			case <-time.After(50 * time.Millisecond):
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, c.want) {
				t.Errorf("RCON got %q, want %q", got, c.want)
			}
			if s.store.Get().State[stateAllowListOn] == "" {
				t.Errorf("State[allowListOn] isn't set afterwards")
			}
		})
	}
}

// TestMonitorRCONPortInUse: another program on the RCON port leaves the
// server running without RCON, which the owner is told.
func TestMonitorRCONPortInUse(t *testing.T) {
	r := replay(t, "rcon-port-in-use-26.3.log", planned(allowOn, allowOn))
	if got := r.kinds(); !slices.Equal(got, []string{"online", "rcon_failed"}) {
		t.Errorf("events %q", got)
	}
}

// TestExitHints: real last lines of a server that couldn't start.
func TestExitHints(t *testing.T) {
	s := Game{}.New(testEnv(t))
	for file, want := range map[string]string{
		"port-in-use-26.3.log":    "Another program is using Minecraft's game port",
		"eula-26.3.log":           "agree to its EULA",
		"java21-26.3.log":         "Click Repair",
		"damaged-level-26.3.log":  "Restore a backup",
		"session-26.3.log":        "",
		"stop-player-on-26.3.log": "",
	} {
		tail := strings.Split(readFile(t, filepath.Join("testdata", file)), "\n")
		if got := s.ExitHint(tail); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: ExitHint = %q, want it to say %q", file, got, want)
		}
	}
}

// TestProbedReconciles: RCON's list wins over the log, and an answer means
// the server is up.
func TestProbedReconciles(t *testing.T) {
	r := replayLines(t, []string{"[10:00:00] [Server thread/INFO]: System chat: Alex joined the game"}, planned(allowOn, allowOn))
	res, err := parseList("There are 1 of a max of 10 players online: Bob")
	if err != nil {
		t.Fatal(err)
	}
	evs := r.m.Probed(res, time.Now())
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+":"+e.Name)
	}
	if !slices.Equal(got, []string{"online:", "join:Bob", "leave:Alex", "count:"}) {
		t.Errorf("events %q", got)
	}
	if evs[len(evs)-1].Count != 1 || !evs[len(evs)-1].Hold {
		t.Errorf("count event %+v, want 1 with Hold", evs[len(evs)-1])
	}
	if res, _ := parseList("There are 0 of a max of 10 players online: "); res.Count != 0 || res.Names == nil || len(res.Names) != 0 {
		t.Errorf("empty list = %+v", res)
	}
	if _, err := parseList("Unknown or incomplete command"); err == nil {
		t.Errorf("a strange answer was taken for a list")
	}
}

// TestTestdataIsSmall keeps the recorded files small, as the tree should be.
func TestTestdataIsSmall(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Size() > 64<<10 {
			t.Errorf("testdata/%s is %d bytes", e.Name(), info.Size())
		}
	}
}
