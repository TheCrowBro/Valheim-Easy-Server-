package satisfactory

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// feed runs every line of a testdata log through a fresh Monitor and
// returns it with the events it gave and the lines it hid.
func feed(t *testing.T, name string) (*monitor, []game.Event, []string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "monitor", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := testServer(t, true)
	m := s.Watch(game.Record{Ports: map[string]int{"game": 7777}}, nil).(*monitor)
	var evs []game.Event
	var hidden []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := m.Line(sc.Text(), time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC))
		evs = append(evs, l.Events...)
		if l.Hide {
			hidden = append(hidden, l.Text)
		}
	}
	return m, evs, hidden
}

// A real 1.0.0 server's start (testdata/README.md): the version, the
// certificate fingerprint and where the API listens are read; no event
// comes from it (online comes from the API's answer); the engine's
// start-up chatter is hidden, its errors are not.
func TestMonitorRealStart(t *testing.T) {
	m, evs, hidden := feed(t, "v1.0.0-CL-365306-linux-start.log")
	if len(evs) != 0 {
		t.Errorf("events from the start's lines: %+v", evs)
	}
	st := m.State()
	if st.Version != "1.0.0 (build 365306)" || st.Extra[extraBuild] != "365306" {
		t.Errorf("Version = %q, build %q", st.Version, st.Extra[extraBuild])
	}
	if got := st.Extra[extraFingerprint]; got != "SHA256:Re8Y8l7EB+699kPspYduzdfA9PewDxrATl2tZlL2kRQ=" {
		t.Errorf("fingerprint = %q", got)
	}
	if st.Extra[extraAPI] != "0.0.0.0:7777" {
		t.Errorf("api = %q", st.Extra[extraAPI])
	}
	if st.Ready || st.Count != -1 {
		t.Errorf("Ready %v, Count %d before any probe", st.Ready, st.Count)
	}
	joined := strings.Join(hidden, "\n")
	for _, want := range []string{"LogLoad: Loading WwiseSoundEngine", "LogHttpListener: Created new HttpListener", "LogStreaming: Warning: Failed to read file"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q isn't hidden", want)
		}
	}
	for _, keep := range []string{"LogOnlineSchema: Error", "LogServer: Display: Server API listening"} {
		if strings.Contains(joined, keep) {
			t.Errorf("%q is hidden", keep)
		}
	}
	// The line keeps the engine's time, in UTC, without the prefix.
	l := m.Line("[2024.09.12-11.27.38:063][  0]LogServer: Display: Server API listening on '0.0.0.0:7777' (Standalone)", time.Now())
	if l.Text != "LogServer: Display: Server API listening on '0.0.0.0:7777' (Standalone)" ||
		!l.Time.Equal(time.Date(2024, 9, 12, 11, 27, 38, 63e6, time.UTC)) {
		t.Errorf("Line = %+v", l)
	}
	// The API's certificate check reads the same fingerprint from the file.
	if got := loggedFingerprint(filepath.Join("testdata", "monitor", "v1.0.0-CL-365306-linux-start.log")); got != "SHA256:Re8Y8l7EB+699kPspYduzdfA9PewDxrATl2tZlL2kRQ=" {
		t.Errorf("loggedFingerprint = %q", got)
	}
}

// Joins and leaves in the engine's own wording (testdata/README.md: written
// from Unreal Engine's format strings and the tools that read them; to be
// confirmed with a real session's log). Names may have spaces; the leave
// line names the account, which the login line tied to the name.
func TestMonitorJoinLeave(t *testing.T) {
	m, evs, _ := feed(t, "engine-format-join-leave.log")
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+":"+e.Name+"/"+e.Account)
	}
	want := "join:Alex/Steam:76561198000000001 join:Sam Smith/Steam:76561198000000002 leave:Alex/Steam:76561198000000001"
	if strings.Join(got, " ") != want {
		t.Errorf("events = %q\nwant      %q", strings.Join(got, " "), want)
	}
	if st := m.State(); len(st.Players) != 1 || st.Players[0].Name != "Sam Smith" ||
		!st.Players[0].Since.Equal(time.Date(2026, 11, 26, 20, 18, 23, 204e6, time.UTC)) {
		t.Errorf("Players = %+v", st.Players)
	}
	// The API's count wins: 0 means nobody, whatever the log said.
	evs = m.Probed(game.ProbeResult{Up: true, Count: 0}, time.Now())
	if len(evs) != 2 || evs[0].Kind != "online" || evs[1].Kind != "leave" || evs[1].Name != "Sam Smith" {
		t.Errorf("Probed(up, 0) = %+v; want online, then Sam Smith leaves", evs)
	}
	if st := m.State(); !st.Ready || st.Count != 0 || len(st.Players) != 0 {
		t.Errorf("State after the probe = %+v", st)
	}
}

func TestMonitorProbeAndEnd(t *testing.T) {
	m := testServer(t, true).Watch(game.Record{Ports: map[string]int{"game": 7777}}, func(name string) string { return "acct-" + name }).(*monitor)
	if evs := m.Probed(game.ProbeResult{Up: false, Count: 0}, time.Now()); len(evs) != 0 || m.State().Ready {
		t.Errorf("a probe that isn't up: %+v, ready %v", evs, m.State().Ready)
	}
	evs := m.Probed(game.ProbeResult{Up: true, Count: 2}, time.Now())
	if len(evs) != 2 || evs[0].Kind != "online" || evs[1].Kind != "count" || evs[1].Count != 2 || !evs[1].Hold {
		t.Errorf("Probed(up, 2) = %+v; want online and a held count", evs)
	}
	if evs := m.Probed(game.ProbeResult{Up: true, Count: 2}, time.Now()); len(evs) != 1 || evs[0].Kind != "count" {
		t.Errorf("a second probe = %+v; want just the count", evs)
	}
	m.Line("[2026.11.26-20.15.04:388][838]LogNet: Join succeeded: Pat", time.Now())
	if st := m.State(); len(st.Players) != 1 || st.Players[0].Account != "acct-Pat" {
		t.Errorf("a join without its login line takes the known account: %+v", st.Players)
	}
	evs = m.End(time.Now())
	if len(evs) != 1 || evs[0].Kind != "leave" || evs[0].Name != "Pat" || m.State().Ready || m.State().Count != -1 {
		t.Errorf("End = %+v, state %+v", evs, m.State())
	}
}

func TestMonitorSaveStopAndPortMoved(t *testing.T) {
	m := testServer(t, true).Watch(game.Record{Ports: map[string]int{"game": 7777}}, nil).(*monitor)
	at := time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		line, kind string
	}{
		{"[2026.11.26-21.00.00:000][  1]LogGame: World Serialization (save): 0.42 seconds", "saved"},
		{"[2026.11.26-21.00.00:000][  1]LogNet: GameNetDriver IpNetDriver_2147482574 IpNetDriver listening on port 7777", ""},
		{"[2026.11.26-21.00.00:000][  1]LogNet: GameNetDriver IpNetDriver_2147482574 IpNetDriver listening on port 7778", "port_moved"},
		{"[2026.11.26-21.00.00:000][  1]LogCore: Warning: *** INTERRUPTED *** : SHUTTING DOWN", "stopping"},
		{"[2026.11.26-21.00.00:000][  1]LogExit: Preparing to exit.", ""}, // already stopping
	} {
		l := m.Line(tc.line, time.Now())
		switch {
		case tc.kind == "" && len(l.Events) != 0:
			t.Errorf("%q gave %+v", tc.line, l.Events)
		case tc.kind != "" && (len(l.Events) != 1 || l.Events[0].Kind != tc.kind):
			t.Errorf("%q gave %+v; want %s", tc.line, l.Events, tc.kind)
		}
	}
	if st := m.State(); !st.LastSave.Equal(at) || !st.Stopping {
		t.Errorf("State = %+v", st)
	}
}

// The stand-in's own lines read the same way (its save line, its leave
// line), so the conformance run checks the Monitor against what the
// stand-in prints.
func TestMonitorStandInLines(t *testing.T) {
	m := testServer(t, true).Watch(game.Record{}, nil).(*monitor)
	lines := []string{
		"[2026.11.26-21.00.00:000][  1]LogNet: Login request: ?EncryptionToken=0123456789ABCDEF?Name=Alex userId: Steam:76561198000000001 platform: Steam",
		"[2026.11.26-21.00.00:000][  2]LogNet: Join succeeded: Alex",
		"[2026.11.26-21.00.01:000][  3]LogNet: UNetConnection::Close: [UNetConnection] RemoteAddr: 127.0.0.1:52001, Name: IpConnection_2147481001, Driver: GameNetDriver IpNetDriver_2147482574, IsServer: YES, PC: BP_PlayerController_C_2147480001, Owner: BP_PlayerController_C_2147480001, UniqueId: Steam:76561198000000001, Channels: 73, Time: 2026.11.26-21.00.01",
	}
	var kinds []string
	for _, l := range lines {
		for _, e := range m.Line(l, time.Now()).Events {
			kinds = append(kinds, e.Kind)
		}
	}
	if strings.Join(kinds, ",") != "join,leave" {
		t.Errorf("events = %v", kinds)
	}
}
