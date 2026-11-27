package windrose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// feed runs a log through a fresh Monitor, one line at a time, with the
// app's clock a second further on for each line. It returns the events in
// order and the Monitor.
func feed(t *testing.T, rec game.Record, lines []string) ([]game.Event, *monitor, []game.Line) {
	t.Helper()
	s := Game{}.New(testEnv(t)).(*server)
	m := s.Watch(rec, func(string) string { return "" }).(*monitor)
	now := time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	var evs []game.Event
	var out []game.Line
	for _, l := range lines {
		now = now.Add(time.Second)
		ln := m.Line(l, now)
		out = append(out, ln)
		evs = append(evs, ln.Events...)
		evs = append(evs, m.Tick(now)...)
	}
	return evs, m, out
}

// logLines reads a sample log as the app's tail gives it: line by line,
// with Windows' line ends trimmed.
func logLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	return lines
}

// kinds lists the events as "kind name" for comparing.
func kinds(evs []game.Event, only ...string) []string {
	var out []string
	for _, e := range evs {
		if len(only) > 0 && !slices.Contains(only, e.Kind) {
			continue
		}
		s := e.Kind
		if e.Name != "" {
			s += " " + e.Name
		}
		out = append(out, s)
	}
	return out
}

func recWith(extra map[string]string) game.Record {
	rec := game.Record{Extra: map[string]json.RawMessage{}}
	for k, v := range extra {
		b, _ := json.Marshal(v)
		rec.Extra[k] = b
	}
	return rec
}

// A whole session, assembled from the real lines in testdata/real and the
// hosting tools' samples (FACTS.md): the start, a player in and a second
// one, the server's own save, a connection that drops and a player who
// goes to the lobby, and Ctrl+C. verify: against the owner's first real
// session (spec 5.11's presence decision).
func TestMonitorSession(t *testing.T) {
	evs, m, lines := feed(t, game.Record{}, logLines(t, "session-assembled.log"))
	want := []string{"code", "online", "join Bosun", "saved", "join Marlinspike", "leave Marlinspike", "leave Bosun", "stopping", "saved"}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
	st := m.State()
	if st.Code != "d6221bb7" || st.Version != "0.10.0.9.32" || !st.Ready || !st.Stopping || st.Count != 0 {
		t.Errorf("state: code %q version %q ready %v stopping %v count %d", st.Code, st.Version, st.Ready, st.Stopping, st.Count)
	}
	if st.Extra["worldId"] != "DB57768A8A7746899683D0EEE91F97BF" {
		t.Errorf("world id %q", st.Extra["worldId"])
	}
	if st.LastSave.IsZero() || st.LastSave.Format("15:04:05") != "21:00:00" {
		t.Errorf("LastSave %v: the exit backup's time", st.LastSave)
	}
	// The first player's account line came before their join line, so the
	// join has it; the second's came after, so only their leave has it
	// (the player listed in between had it too).
	for _, e := range evs {
		switch {
		case e.Kind == "join" && e.Name == "Bosun" && len(e.Account) != 32:
			t.Errorf("join of %s has account %q", e.Name, e.Account)
		case e.Kind == "leave" && (e.Text == "" || len(e.Account) != 32):
			t.Errorf("leave %+v", e)
		}
	}
	// The Log page: the server's password never shows, the padding and
	// the source don't, and the noisy parts are hidden.
	for _, l := range lines {
		if strings.Contains(l.Text, "parrot") {
			t.Errorf("the password shows on the Log page: %q", l.Text)
		}
		if strings.Contains(l.Text, `D:\Source`) {
			t.Errorf("a source reference shows: %q", l.Text)
		}
		if strings.Contains(l.Text, "R5LogBLDalAQ") && !l.Hide {
			t.Errorf("a database line isn't hidden: %q", l.Text)
		}
	}
	if !slices.ContainsFunc(lines, func(l game.Line) bool {
		return strings.Contains(l.Text, "> Password : (hidden)") || strings.Contains(l.Text, "> Password           : (hidden)") || strings.Contains(l.Text, "Password  : (hidden)")
	}) {
		t.Errorf("the password line isn't hidden on the Log page")
	}
	if l := lines[1]; l.Time.Format(time.RFC3339Nano) != "2026-11-26T20:00:00.105Z" {
		t.Errorf("a line's own time: %v", l.Time)
	}
	// The run ends: nobody is left to leave, and the state is cleared.
	if left := m.End(time.Now()); len(left) != 0 {
		t.Errorf("End: %v", kinds(left))
	}
}

// Real lines from a player whose connection was reset, as two servers
// logged it a month and a version apart (testdata/real, FACTS.md): one
// leave, though the server says it in four lines and an "R5Ensure" block.
func TestMonitorRealLeave(t *testing.T) {
	for _, f := range []string{"real/ib66-2026-05-06-0.10.0.5-leave.log", "real/ib76-2026-06-05-0.10.0.6-leave.log"} {
		t.Run(f, func(t *testing.T) {
			lines := logLines(t, f)
			// The account the excerpt's player played from.
			var account string
			for _, l := range lines {
				if g := reGone.FindStringSubmatch(l); g != nil {
					account = g[1]
					break
				}
			}
			if account == "" {
				t.Fatal("the excerpt has no leave line")
			}
			join := "[2026.05.06-20.00.00:000][  1]R5LogDataKeeper:              [000001] IR5DataKeeperStateLogger::LogSetState              ServerAccount. AccountName 'Saltbeard'. AccountId " + account + ". PlayerState BP_R5PlayerState_C_1"
			evs, m, out := feed(t, game.Record{}, append([]string{"[2026.05.06-20.00.00:000][  1]LogNet: Join succeeded: Saltbeard", join}, lines...))
			if got := kinds(evs, "join", "leave"); !slices.Equal(got, []string{"join Saltbeard", "leave Saltbeard"}) {
				t.Errorf("events %q", got)
			}
			if m.State().Count != 0 {
				t.Errorf("count %d", m.State().Count)
			}
			hidden := 0
			for _, l := range out {
				if l.Hide {
					hidden++
				}
			}
			if hidden == 0 {
				t.Errorf("none of the network's lines is hidden")
			}
		})
	}
}

// A real player connecting to a new world (and the crash right after,
// testdata/real/ib45…): the list of accounts below "Server is ready to
// continue initialization" names them, still loading in (Reserved), so
// they count as on; the crash ends the run.
func TestMonitorRealJoinThenCrash(t *testing.T) {
	lines := logLines(t, "real/ib45-2026-04-29-0.10.0.4-join-crash.log")
	evs, m, _ := feed(t, game.Record{}, lines)
	if got := kinds(evs, "join", "leave", "stopping"); !slices.Equal(got, []string{"join Marlinspike"}) {
		t.Errorf("events %q", got)
	}
	if p := m.State().Players; len(p) != 1 || len(p[0].Account) != 32 {
		t.Errorf("players %+v", p)
	}
	if m.State().Stopping {
		t.Errorf("a crash isn't a stop someone asked for")
	}
	if left := m.End(time.Now()); !slices.Equal(kinds(left), []string{"leave Marlinspike"}) {
		t.Errorf("End: %q", kinds(left))
	}
	s := Game{}.New(testEnv(t)).(*server)
	if h := s.ExitHint(lines[len(lines)-30:]); !strings.Contains(h, "crashed") {
		t.Errorf("exit hint %q", h)
	}
}

// A real list of accounts with only past sessions in it (testdata/real
// ib6…): a player the Monitor had listed has gone.
func TestMonitorRealAccountsList(t *testing.T) {
	lines := logLines(t, "real/ib6-2026-04-15-0.10.0-accounts.log")
	g := reDumpRow.FindStringSubmatch(lines[3])
	if g == nil {
		t.Fatalf("not a row: %q", lines[3])
	}
	pre := []string{
		"[2026.04.15-01.50.40:000][  1]LogNet: Join succeeded: Saltbeard",
		"[2026.04.15-01.50.41:000][  2]R5LogDataKeeper:              [000002] IR5DataKeeperStateLogger::LogSetState              ServerAccount. AccountName 'Saltbeard'. AccountId " + g[2] + ". PlayerState BP_R5PlayerState_C_1",
		"[2026.04.15-02.07.48:000][  3]R5LogDataKeeper:              [000003] ...rver::MoveAccountToListOfDisconnected          Something else first",
	}
	evs, m, _ := feed(t, game.Record{}, append(append(pre, lines...), ` [D:\Source\Build\work\gameRepoCheckoutDir\Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:830]`))
	if got := kinds(evs, "join", "leave"); !slices.Equal(got, []string{"join Saltbeard", "leave Saltbeard"}) {
		t.Errorf("events %q", got)
	}
	if m.State().Count != 0 {
		t.Errorf("count %d", m.State().Count)
	}
}

// The hosting tools' samples: the account line before the join line
// (crunchydude/windrose-bot, from its production server) and the join
// line before the account line (jasonrundell/nitrado-discord-bot, from a
// Nitrado server's R5.log). Either way one join, with the account.
func TestMonitorJoinLineOrders(t *testing.T) {
	evs, m, _ := feed(t, game.Record{}, logLines(t, "real/crunchydude-2026-05-10-join-leave.log"))
	if got := kinds(evs, "join", "leave"); !slices.Equal(got, []string{"join Bosun", "leave Bosun"}) {
		t.Errorf("account line first: %q", got)
	}
	if evs[0].Account != "8D6B9FB898AF9D0565EB2090F8C6B9AC" || m.State().Count != 0 {
		t.Errorf("join %+v, count %d", evs[0], m.State().Count)
	}

	lines := logLines(t, "real/nitrado-2026-05-02-join.log")
	lines = append(lines, "[2026.05.02-15.00.00:000][  9]R5LogCoopProxy:               [000009] UR5CoopProxyServer::OnAccountDisconnected          Account disconnected. Inform Cm. AccountId 4567660C8240322A77158DAE499D3F5C. BLPlayerSessionId x. DisconnectReason 'BL disconnected'. FarewellReason 'Go to lobby'")
	evs, _, _ = feed(t, game.Record{}, lines)
	if got := kinds(evs, "join", "leave"); !slices.Equal(got, []string{"join Quartermaster", "leave Quartermaster"}) {
		t.Errorf("join line first: %q", got)
	}
}

// A real first start whose settings file named a world that couldn't be
// loaded (testdata/real/ib41…): the version from the file the server
// printed, a load_failed warning, and the exit hint for it.
func TestMonitorRealStartFailure(t *testing.T) {
	lines := logLines(t, "real/ib41-2026-04-27-0.10.0.3-start.log")
	evs, m, _ := feed(t, game.Record{}, lines)
	if got := kinds(evs); !slices.Contains(got, "load_failed") || slices.Contains(got, "online") {
		t.Errorf("events %q", got)
	}
	if v := m.State().Version; v != "0.10.0.3.104" {
		t.Errorf("version %q", v)
	}
	s := Game{}.New(testEnv(t)).(*server)
	if h := s.ExitHint(lines); !strings.Contains(h, "couldn't load the world") {
		t.Errorf("exit hint %q", h)
	}
}

// The ready line may be worded differently by a later version: three
// minutes after the server registered with Windrose's online service the
// Monitor says it's online anyway.
func TestMonitorReadyFallback(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	m := s.Watch(game.Record{}, nil)
	start := time.Now()
	m.Line("[2026.11.26-20.00.21:350][ 40]R5LogCoopProxy:               [000040] UR5CoopProxyServer::OnServerRegistered             Server registration finished successfully. BLSessionId x", start)
	if evs := m.Tick(start.Add(2 * time.Minute)); len(evs) != 0 {
		t.Errorf("online too soon: %v", evs)
	}
	if evs := m.Tick(start.Add(registerWait)); len(evs) != 1 || evs[0].Kind != "online" {
		t.Errorf("no online after %v: %v", registerWait, evs)
	}
	if !m.State().Ready {
		t.Error("not ready")
	}
}

// The record's invite code and world id are known before the log says
// them, so Join has the code from the start of the run.
func TestMonitorFromRecord(t *testing.T) {
	s := Game{}.New(testEnv(t)).(*server)
	st := s.Watch(recWith(map[string]string{"inviteCode": "abc123ef", "worldId": "DB57768A8A7746899683D0EEE91F97BF"}), nil).State()
	if st.Code != "abc123ef" || st.Extra["worldId"] != "DB57768A8A7746899683D0EEE91F97BF" {
		t.Errorf("state %+v", st)
	}
}

// The warnings the owner sees: a wrong password (with the passwords the
// server writes hidden), a friend on another version, the online service
// unreachable (once), and a failed save.
func TestMonitorWarnings(t *testing.T) {
	lines := []string{
		"[2026.11.26-20.01.00:000][  1]R5LogDataKeeper: Warning:     [000001] ...ataKeeperForServerCoop::OnAccountBLConnected    Client sent wrong password. Disconnect it. ClientPassword guess. ServerPassword parrot [D:\\Source\\x.cpp:270]",
		"[2026.11.26-20.02.00:000][  2]R5LogDataKeeper: Warning:     [000002] UR5DataKeeperForServer::VerifyAccount              DeploymentId mismatch. AccountId 8D6B9FB898AF9D0565EB2090F8C6B9AC. ServerDeploymentId '0.10.0.9.32-22d39a16'. ClientDeploymentId '0.10.0.10.4-aa11bb22' [D:\\Source\\x.cpp:300]",
		"[2026.11.26-20.03.00:000][  3]R5LogCoopProxy: Error:        [000003] UR5CoopProxyServer::SetBrokenState                 GcStream is broken. ErrorMessage 'Login finished with error.' [D:\\Source\\x.cpp:1]",
		"[2026.11.26-20.03.10:000][  4]R5LogCoopProxy: Error:        [000004] UR5CoopProxyServer::SetBrokenState                 GcStream is broken. ErrorMessage 'again' [D:\\Source\\x.cpp:1]",
		"[2026.11.26-20.04.00:000][  5]R5LogCoopProxy: Error:        [000005] UR5CoopDocumentBackup::SaveBackup                  Worlds/X. Save backup finished with error. Error 'disk full' [D:\\Source\\x.cpp:1]",
	}
	evs, m, out := feed(t, game.Record{}, lines)
	if got := kinds(evs); !slices.Equal(got, []string{"wrong_password", "bad_version", "no_backend", "save_failed"}) {
		t.Errorf("events %q", got)
	}
	if strings.Contains(out[0].Text, "parrot") || strings.Contains(out[0].Text, "guess") {
		t.Errorf("passwords show: %q", out[0].Text)
	}
	if !strings.Contains(evs[1].Text, "0.10.0.10.4") || !strings.Contains(evs[1].Text, "0.10.0.9.32") {
		t.Errorf("bad_version text %q", evs[1].Text)
	}
	if m.State().Extra["backend"] != "failed" {
		t.Errorf("extra %v", m.State().Extra)
	}
}

// Joins and leaves the server only shows in its lists of accounts (with
// none of the account lines): the lists alone keep who is on right.
func TestMonitorListsAlone(t *testing.T) {
	row := func(n int, name, id, state string) string {
		return "    " + strings.Repeat(" ", 1) + string(rune('0'+n)) + ". Name '" + name + "'. AccountId '" + id + "'. State '" + state + "'. NetAddress 'R5:x'. UePortal ''. ReserveMoment 2026.11.26-20.00.00. Connected in +00:00:01.000. IsReady in +00:00:01.000. TimeInGame +00:00:01.000. TimeOnServer +00:00:01.000. FarewellReason"
	}
	a, b := "8D6B9FB898AF9D0565EB2090F8C6B9AC", "A2FB1103C1A1BDA50311CB8FE66192F9"
	head := "[2026.11.26-20.10.00:000][  1]R5LogDataKeeper:              [000001] ...KeeperForServer::TryToStartTerrainGeneration    Server is ready to continue initialization"
	foot := ` [D:\Source\Build\work\gameRepoCheckoutDir\Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:785]`
	lines := []string{
		head, "Connected Accounts", row(1, "Bosun", a, "ReadyToPlay"), "Reserved Accounts", row(1, "Marlinspike", b, "UePreloginVerified"), "Disconnected Accounts", foot,
		head, "Connected Accounts", row(1, "Marlinspike", b, "ReadyToPlay"), "Reserved Accounts", "Disconnected Accounts", row(1, "Bosun", a, "ReadyToPlay"), foot,
		// A list cut short (no Disconnected header) says nothing.
		head, "Connected Accounts", "Reserved Accounts",
		"[2026.11.26-20.20.00:000][  2]LogNet: something else",
	}
	evs, m, _ := feed(t, game.Record{}, lines)
	if got := kinds(evs, "join", "leave"); !slices.Equal(got, []string{"join Bosun", "join Marlinspike", "leave Bosun"}) {
		t.Errorf("events %q", got)
	}
	if p := m.State().Players; len(p) != 1 || p[0].Name != "Marlinspike" || p[0].Account != b {
		t.Errorf("players %+v", p)
	}
}
