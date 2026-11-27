package logparse

import (
	"strings"
	"testing"
	"time"
)

func TestParseLines(t *testing.T) {
	tests := []struct {
		line string
		want Event
	}{
		{"10/22/2026 20:01:11: Game server connected", Event{Kind: SteamConnected}},
		{"11/01/2026 07:30:06: Opened PlayFab server", Event{Kind: ServerReady}},
		{"11/01/2026 07:30:06: Opened Steam server", Event{Kind: ServerReady}},
		{`10/22/2026 20:01:15: Session "Viking Friends" with join code 604510 and IP 203.0.113.4:2456 is active with 2 player(s)`,
			Event{Kind: JoinCode, Code: "604510", Addr: "203.0.113.4:2456", Count: 2}},
		{`10/22/2026 20:01:12: Session "Viking Friends" registered with join code 604510`, Event{Kind: JoinCode, Code: "604510", Count: -1}},
		{`10/22/2026 20:01:09: New session server "Viking Friends" that has join code , now 0 player(s)`, Event{Kind: JoinCode, Count: 0}},
		{"10/22/2026 20:11:00: Connections 3 ZDOS:130616  sent:0 recv:0", Event{Kind: PeerCount, Count: 3}},
		{"10/22/2026 20:02:00: Got connection SteamID 76561198000000001", Event{Kind: Connecting, ID: "76561198000000001", PlatformID: "Steam_76561198000000001"}},
		{"10/22/2026 20:02:01: Got handshake from client 76561198000000001", Event{Kind: Handshake, ID: "76561198000000001", PlatformID: "Steam_76561198000000001"}},
		{"10/22/2026 20:02:01: PlayFab socket with remote ID playfab/EFEFD7F454DC3473 received local Platform ID Steam_76561198000000207",
			Event{Kind: PlatformID, ID: "playfab/EFEFD7F454DC3473", PlatformID: "Steam_76561198000000207"}},
		{"10/22/2026 20:02:10: Got character ZDOID from Ragnar Lothbrok : -1234567:1", Event{Kind: Character, Name: "Ragnar Lothbrok"}},
		{"10/22/2026 20:30:00: Got character ZDOID from Ragnar : 0:0", Event{Kind: Character, Name: "Ragnar", Dead: true}},
		{"10/22/2026 21:00:00: Closing socket 76561198000000001", Event{Kind: Disconnect, ID: "76561198000000001"}},
		{"10/22/2026 21:00:00: Peer 76561198000000009 has wrong password", Event{Kind: WrongPassword, ID: "76561198000000009"}},
		{"10/22/2026 21:00:00: Peer 76561198000000009 has incompatible version, mine:1.0.12 (network version 35)   remote 1.0.7 (network version 34)",
			Event{Kind: BadVersion, ID: "76561198000000009", Mine: "1.0.12", Version: "1.0.7"}},
		{"02/12/2021 20:01:13: VERSION check their:0.147.3  mine:0.147.4", Event{Kind: BadVersion, Version: "0.147.3", Mine: "0.147.4"}},
		{"02/12/2021 20:01:13: VERSION check their:0.147.3  mine:0.147.3", Event{Kind: Unknown}},
		{"10/22/2026 19:59:00: Valheim version: l-1.0.12 (network version 35)", Event{Kind: Version, Version: "1.0.12"}},
		{"10/22/2026 19:59:00: Valheim version:0.147.3", Event{Kind: Version, Version: "0.147.3"}},
		{"10/22/2026 20:15:00: World saved ( 1247.836ms )", Event{Kind: WorldSaved, Millis: 1247.836}},
		{"10/22/2026 20:15:00: World save (5/5) done", Event{Kind: WorldSaved}},
		{"10/22/2026 20:15:00: World save (2/5) done", Event{Kind: Unknown}},
		{"10/22/2026 20:15:00: World load failed mid-file. Exiting without save. Check backups!", Event{Kind: WorldLoadFailed}},
		{"10/22/2026 20:15:00: Error bad password: password is part of world name", Event{Kind: BadPasswordConfig}},
		{"10/22/2026 22:00:00: OnApplicationQuit", Event{Kind: Shutdown}},
		{"[Info   : Unity Log] 11/01/2026 07:30:56: Opened PlayFab server", Event{Kind: ServerReady}},
		{"Game server connected", Event{Kind: SteamConnected}},
		// Crossplay can't start: Unity's exception, and Valheim's own words.
		{"DllNotFoundException: Party assembly:<unknown assembly> type:<unknown type> member:(null)", Event{Kind: CrossplayFailed}},
		{"[Error  :   Unity Log] DllNotFoundException: Party", Event{Kind: CrossplayFailed}},
		{"10/31/2026 00:18:35: DLL Not Found: This error usually occurs when you do not have the correct dependencies installed, and will prevent crossplay from working. The dependencies are different depending on which platform you play on.", Event{Kind: CrossplayFailed}},
		{" For windows: You need VC++ Redistributables. https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist?view=msvc-170", Event{Kind: Unknown}},
	}
	for _, tc := range tests {
		got := Parse(tc.line)
		if tc.want.Count == 0 && tc.want.Kind != JoinCode && tc.want.Kind != PeerCount {
			tc.want.Count = -1
		}
		if got.Kind != tc.want.Kind || got.ID != tc.want.ID || got.PlatformID != tc.want.PlatformID ||
			got.Name != tc.want.Name || got.Dead != tc.want.Dead || got.Count != tc.want.Count ||
			got.Code != tc.want.Code || got.Addr != tc.want.Addr || got.Version != tc.want.Version ||
			got.Mine != tc.want.Mine || got.Millis != tc.want.Millis {
			t.Errorf("Parse(%q)\n got  %+v\n want %+v", tc.line, got, tc.want)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	ev := Parse("10/22/2026 20:01:11: Game server connected")
	want := time.Date(2026, 10, 22, 20, 1, 11, 0, time.Local)
	if !ev.Time.Equal(want) {
		t.Fatalf("time = %v, want %v", ev.Time, want)
	}
	if ev.Text != "Game server connected" {
		t.Fatalf("text = %q", ev.Text)
	}
}

func TestIsNoise(t *testing.T) {
	for _, l := range []string{"", "  ", "(Filename: ./Runtime/Export/Debug/Debug.bindings.h Line: 35)"} {
		if !IsNoise(l) {
			t.Errorf("IsNoise(%q) = false", l)
		}
	}
	if IsNoise("Game server connected") {
		t.Error("real message reported as noise")
	}
}

var clock = time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)

func feed(t *testing.T, tr *Tracker, log string) []Activity {
	t.Helper()
	return feedAt(t, tr, clock, log)
}

// feedAt reads the lines like the app does, all at the time now on this
// computer's clock.
func feedAt(t *testing.T, tr *Tracker, now time.Time, log string) []Activity {
	t.Helper()
	var acts []Activity
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if IsNoise(line) {
			continue
		}
		acts = append(acts, tr.Apply(Parse(strings.TrimSpace(line)), now)...)
	}
	return acts
}

// known stands in for the players the app has seen before.
func known(ids map[string]string) func(string) string {
	return func(name string) string { return ids[name] }
}

func kinds(acts []Activity) string {
	var k []string
	for _, a := range acts {
		k = append(k, a.Kind+":"+a.Name)
	}
	return strings.Join(k, ",")
}

func TestTrackerSteamSession(t *testing.T) {
	// Friends who played here before, connecting at the same time.
	tr := Tracker{KnownPlatformID: known(map[string]string{
		"Astrid": "Steam_76561198000000001",
		"Bjorn":  "Steam_76561198000000002",
	})}
	acts := feed(t, &tr, `
10/22/2026 20:00:00: Valheim version: l-1.0.12 (network version 35)
10/22/2026 20:00:02: Game server connected
10/22/2026 20:00:30: Opened Steam server
10/22/2026 20:01:00: Got connection SteamID 76561198000000001
10/22/2026 20:01:01: Got handshake from client 76561198000000001
10/22/2026 20:01:02: Got connection SteamID 76561198000000002
10/22/2026 20:01:03: Got handshake from client 76561198000000002
10/22/2026 20:01:09: Got character ZDOID from Astrid : -111:1
10/22/2026 20:01:12: Got character ZDOID from Bjorn : -222:1
10/22/2026 20:20:00: Got character ZDOID from Bjorn : 0:0
10/22/2026 20:20:40: Got character ZDOID from Bjorn : -222:9
10/22/2026 20:30:00: World save (5/5) done
10/22/2026 21:01:09: Closing socket 76561198000000001
`)
	if got, want := kinds(acts), "online:,join:Astrid,join:Bjorn,death:Bjorn,saved:,leave:Astrid"; got != want {
		t.Fatalf("activities = %s, want %s", got, want)
	}
	if tr.Version != "1.0.12" || !tr.Ready {
		t.Fatalf("version=%q ready=%v", tr.Version, tr.Ready)
	}
	players := tr.Players()
	if len(players) != 1 || players[0].Name != "Bjorn" || players[0].PlatformID != "Steam_76561198000000002" {
		t.Fatalf("players = %+v", players)
	}
	if !strings.Contains(acts[len(acts)-1].Text, "after 1 h") {
		t.Fatalf("leave text = %q", acts[len(acts)-1].Text)
	}
	if tr.LastSave.IsZero() {
		t.Fatal("last save not recorded")
	}
}

// On a Mac the server logs in to Steam before it has even started loading
// the world. It's online once it opens, after making or loading the world.
func TestTrackerOnlineOnceOpen(t *testing.T) {
	var tr Tracker
	acts := feed(t, &tr, `
11/01/2026 07:28:27: Steam game server initialized
11/01/2026 07:28:27: Valheim version: 1.0.21 (network version 41)
11/01/2026 07:28:29: Game server connected
11/01/2026 07:28:37: Load world: Midgard (Midgard)
11/01/2026 07:28:54: Loading: Generating locations
11/01/2026 07:30:05: There are 38 that take a long time to generate (over 0.5 sec). Total slow location time is 49.3 seconds that could be saved on world gen!
`)
	if tr.Ready || len(acts) != 0 {
		t.Fatalf("online while still making the world: %+v", acts)
	}
	if want := time.Date(2026, 11, 1, 7, 28, 29, 0, time.Local); !tr.SteamAt.Equal(want) {
		t.Fatalf("logged in to Steam at %v, want %v", tr.SteamAt, want)
	}
	acts = feed(t, &tr, `
11/01/2026 07:30:06: Semaphore type: ManualResetEventSlim
11/01/2026 07:30:06: Opened PlayFab server
11/01/2026 07:30:06: New session server "Valheim Easy Server CI" that has join code , now 0 player(s)
11/01/2026 07:30:07: Session "Valheim Easy Server CI" registered with join code 037599
`)
	if got := kinds(acts); got != "online:" || !tr.Ready || tr.JoinCode != "037599" {
		t.Fatalf("activities %s, ready %v, code %q", got, tr.Ready, tr.JoinCode)
	}
	if want := time.Date(2026, 11, 1, 7, 30, 6, 0, time.Local); !acts[0].Time.Equal(want) {
		t.Fatalf("online at %v, want %v", acts[0].Time, want)
	}
}

// If a Valheim update words the line about opening differently, a server
// that logged in to Steam long ago still counts as online in the end.
func TestTrackerOverdue(t *testing.T) {
	var tr Tracker
	at := func(clock string) time.Time {
		v, err := time.ParseInLocation("15:04:05", clock, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(2026, 11, 1, v.Hour(), v.Minute(), v.Second(), 0, time.Local)
	}
	if tr.Overdue(at("09:00:00"), 10*time.Minute) {
		t.Fatal("overdue before logging in to Steam")
	}
	feed(t, &tr, `
11/01/2026 07:50:00: Game server connected
11/01/2026 07:55:00: Game server connected
`)
	if tr.Overdue(at("07:59:59"), 10*time.Minute) {
		t.Fatal("overdue too soon")
	}
	if !tr.Overdue(at("08:00:00"), 10*time.Minute) {
		t.Fatal("not overdue 10 minutes after the first time it logged in to Steam")
	}
	feed(t, &tr, "11/01/2026 08:00:01: Opened Steam server\n")
	if !tr.Ready || tr.Overdue(at("09:00:00"), 10*time.Minute) {
		t.Fatalf("ready %v, still overdue", tr.Ready)
	}
}

// Crossplay that can't start means no join code, unless the server was
// started without crossplay, where the line means nothing.
func TestTrackerCrossplayFailed(t *testing.T) {
	var tr Tracker
	feed(t, &tr, `
DllNotFoundException: Party assembly:<unknown assembly> type:<unknown type> member:(null)
10/31/2026 00:19:11: New session server "Valheim with Friends" that has join code , now 0 player(s)
`)
	if !tr.CrossplayFailed || tr.JoinCode != "" {
		t.Fatalf("failed %v, code %q", tr.CrossplayFailed, tr.JoinCode)
	}
	steam := Tracker{NoJoinCode: true}
	feed(t, &steam, "DllNotFoundException: Party\n")
	if steam.CrossplayFailed {
		t.Fatal("a server without crossplay counted as failing at it")
	}
}

func TestTrackerCrossplaySession(t *testing.T) {
	var tr Tracker
	acts := feed(t, &tr, `
10/22/2026 20:00:00: New session server "Viking Friends" that has join code , now 0 player(s)
10/22/2026 20:00:03: Session "Viking Friends" registered with join code 604510
10/22/2026 20:00:04: Session "Viking Friends" with join code 604510 and IP 203.0.113.4:2456 is active with 0 player(s)
10/22/2026 20:05:00: PlayFab socket with remote ID playfab/AAAA received local Platform ID Xbox_2535416401464117
10/22/2026 20:05:01: Got handshake from client playfab/AAAA
10/22/2026 20:05:09: Got character ZDOID from Freydis : 999:1
10/22/2026 20:10:00: Session "Viking Friends" with join code 604510 and IP 203.0.113.4:2456 is active with 1 player(s)
10/22/2026 20:40:00: Closing socket playfab/AAAA
`)
	if got, want := kinds(acts), "online:,join:Freydis,leave:Freydis"; got != want {
		t.Fatalf("activities = %s, want %s", got, want)
	}
	if tr.JoinCode != "604510" || tr.PublicAddr != "203.0.113.4:2456" {
		t.Fatalf("join code %q addr %q", tr.JoinCode, tr.PublicAddr)
	}
	if !tr.Ready {
		t.Fatal("session line should mark the server ready")
	}
	if acts[1].PlatformID != "Xbox_2535416401464117" { // acts[0] is "online"
		t.Fatalf("platform id = %q", acts[1].PlatformID)
	}
}

func TestTrackerReconcilesMissedLeaves(t *testing.T) {
	var tr Tracker
	feed(t, &tr, `
Got connection SteamID 1
Got character ZDOID from A : 1:1
Got connection SteamID 2
Got character ZDOID from B : 2:1
`)
	if tr.Count() != 2 {
		t.Fatalf("count = %d", tr.Count())
	}
	// One low reading is not enough...
	feedAt(t, &tr, clock, "Connections 1 ZDOS:10  sent:0 recv:0")
	if tr.Count() != 2 {
		t.Fatalf("removed after one reading: %d", tr.Count())
	}
	// ...nor is the server's next one, ten minutes later...
	feedAt(t, &tr, clock.Add(10*time.Minute), "Connections 1 ZDOS:10  sent:0 recv:0")
	if tr.Count() != 2 {
		t.Fatalf("removed after two readings 10 minutes apart: %d", tr.Count())
	}
	// ...but one at least countGap after the first is.
	acts := feedAt(t, &tr, clock.Add(20*time.Minute), "Connections 1 ZDOS:10  sent:0 recv:0")
	if tr.Count() != 1 || len(acts) != 1 || acts[0].Kind != "leave" {
		t.Fatalf("count = %d acts = %+v", tr.Count(), acts)
	}
	// The same for nobody at all, and a count high enough in between
	// starts the wait again.
	feedAt(t, &tr, clock.Add(30*time.Minute), "Connections 0 ZDOS:10  sent:0 recv:0")
	feedAt(t, &tr, clock.Add(40*time.Minute), "Connections 1 ZDOS:10  sent:0 recv:0")
	feedAt(t, &tr, clock.Add(50*time.Minute), "Connections 0 ZDOS:10  sent:0 recv:0")
	if tr.Count() != 1 {
		t.Fatalf("removed although a count in between said otherwise: %d", tr.Count())
	}
	acts = feedAt(t, &tr, clock.Add(65*time.Minute), "Connections 0 ZDOS:10  sent:0 recv:0")
	if tr.Count() != 0 || len(acts) != 1 {
		t.Fatalf("count = %d acts = %+v", tr.Count(), acts)
	}
}

func TestTrackerWrongPasswordAndVersion(t *testing.T) {
	var tr Tracker
	acts := feed(t, &tr, `
Got connection SteamID 5
Peer 5 has wrong password
Got connection SteamID 6
Peer 6 has incompatible version, mine:1.0.12 (network version 35)   remote 1.0.7 (network version 34)
Got connection SteamID 7
Got character ZDOID from Sigrid : 7:1
`)
	if got, want := kinds(acts), "wrong_password:,bad_version:,join:Sigrid"; got != want {
		t.Fatalf("activities = %s, want %s", got, want)
	}
	if p := tr.Players(); len(p) != 1 || p[0].ID != "7" {
		t.Fatalf("players = %+v (rejected connections must not be matched)", p)
	}
	if !strings.Contains(acts[1].Text, "1.0.7") || !strings.Contains(acts[1].Text, "1.0.12") {
		t.Fatalf("bad version text = %q", acts[1].Text)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:              "30 s",
		45 * time.Minute:              "45 min",
		2 * time.Hour:                 "2 h",
		2*time.Hour + 5*time.Minute:   "2 h 5 min",
		26*time.Hour + 59*time.Minute: "26 h 59 min",
	}
	for d, want := range cases {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Valheim logs a connecting player's version string before it checks the
// password, and the names of characters. Neither may pass for anything else.
func TestParsePlayerTextIsOnlyItself(t *testing.T) {
	for _, tc := range []struct {
		line string
		kind Kind
	}{
		{`10/22/2026 21:00:00: VERSION check their:x registered with join code 777777  mine:1.0.12`, Unknown},
		{`10/22/2026 21:00:00: VERSION check their:Got connection SteamID 76561198000000666  mine:1.0.12`, Unknown},
		{`10/22/2026 21:00:00: Peer 76561198000000009 has incompatible version, mine:1.0.12 (network version 35) remote x Session "Viking Friends" with join code 424242 and IP 6.6.6.6:2456 is active with 0 player(s)`, BadVersion},
		{`10/22/2026 21:00:00: Got character ZDOID from Session "Viking Friends" with join code 424242 and IP 6.6.6.6:2456 is active with 0 player : 456:1`, Character},
		{`10/22/2026 21:00:00: PlayFab socket with remote ID playfab/AAAA received local Platform ID Connections 0 ZDOS:1`, PlatformID},
		{`10/22/2026 21:00:00: Kicking x registered with join code 777777`, Unknown},
		{`10/22/2026 21:00:00: Kicking OnApplicationQuit`, Unknown},
		{`10/22/2026 21:00:00: Got character ZDOID from DllNotFoundException: Party : 456:1`, Character},
		{`10/22/2026 21:00:00: Kicking DllNotFoundException: Party`, Unknown},
	} {
		ev := Parse(tc.line)
		if ev.Kind != tc.kind || ev.Code != "" || ev.Addr != "" || ev.Count != -1 {
			t.Errorf("Parse(%q) = %+v, want only kind %d", tc.line, ev, tc.kind)
		}
	}
	// A name can't fake a death either.
	if ev := Parse("10/22/2026 21:00:00: Got character ZDOID from Bjorn : 0:0 : -222:1"); ev.Dead || ev.Name != "Bjorn : 0:0" {
		t.Errorf("fake death: %+v", ev)
	}
}

// Valheim writes a player's text as it is, line breaks included, so a
// player can add whole lines. These can't change what matters.
func TestTrackerIgnoresMadeUpLines(t *testing.T) {
	var tr Tracker
	now := time.Now()
	apply := func(line string) []Activity { return tr.Apply(Parse(line), now) }
	apply(`10/22/2026 20:00:00: Valheim version: l-1.0.12 (network version 35)`)
	apply(`10/22/2026 20:00:03: Session "Viking Friends" registered with join code 604510`)
	apply("10/22/2026 20:01:00: Got connection SteamID 76561198000000001")
	apply("10/22/2026 20:01:09: Got character ZDOID from Astrid : -111:1")

	apply(`10/22/2026 20:02:00: Session "Fake" with join code 999999 and IP 6.6.6.6:2456 is active with 0 player(s)`)
	apply(`10/22/2026 20:02:00: Valheim version: l-9.9.9 (network version 99)`)
	apply(`10/22/2026 20:02:01: Connections 0 ZDOS:1  sent:0 recv:0`)
	if tr.JoinCode != "604510" || tr.PublicAddr != "" || tr.Version != "1.0.12" || tr.Count() != 1 {
		t.Fatalf("made-up lines changed the state: code %q addr %q version %q players %d", tr.JoinCode, tr.PublicAddr, tr.Version, tr.Count())
	}
	// The server's own count, read again countGap later, still clears the list.
	now = now.Add(countGap)
	if acts := apply(`10/22/2026 20:17:01: Connections 0 ZDOS:1  sent:0 recv:0`); tr.Count() != 0 || len(acts) != 1 {
		t.Fatalf("players %d, activities %+v", tr.Count(), acts)
	}
	// A real new code for the same session is taken: Valheim gets one when
	// it registers its session again.
	apply(`10/22/2026 20:18:00: Session "Viking Friends" with join code 123456 and IP 203.0.113.4:2456 is active with 0 player(s)`)
	if tr.JoinCode != "123456" || tr.PublicAddr != "203.0.113.4:2456" {
		t.Fatalf("code %q addr %q", tr.JoinCode, tr.PublicAddr)
	}

	// Without crossplay a server has no join code at all.
	off := Tracker{NoJoinCode: true}
	off.Apply(Parse(`10/22/2026 20:00:03: Session "Viking Friends" registered with join code 604510`), now)
	if off.JoinCode != "" {
		t.Fatalf("a server without crossplay got join code %q", off.JoinCode)
	}

	// When crossplay couldn't start, no code comes, but the server's own
	// session line still says it's open.
	failed := Tracker{}
	failed.Apply(Parse("DllNotFoundException: Party"), now)
	failed.Apply(Parse(`10/22/2026 20:00:03: New session server "Viking Friends" that has join code , now 0 player(s)`), now)
	failed.Apply(Parse(`10/22/2026 20:05:00: Session "Viking Friends" with join code 999999 and IP 6.6.6.6:2456 is active with 0 player(s)`), now)
	if failed.JoinCode != "" || failed.PublicAddr != "" || !failed.Ready {
		t.Fatalf("crossplay failed: code %q addr %q ready %v", failed.JoinCode, failed.PublicAddr, failed.Ready)
	}
}

// A player's version string is written into the log before the password is
// checked, line breaks and all. The lines it adds, even after a made-up
// "(Filename:" line like the one Unity writes after each message, can't
// empty the list of who's online.
func TestTrackerIgnoresLinesInPlayerText(t *testing.T) {
	var tr Tracker
	feedAt(t, &tr, clock, `
11/02/2026 20:00:00: Opened PlayFab server
11/02/2026 20:00:00: New session server "Viking Friends" that has join code , now 0 player(s)
11/02/2026 20:00:01: Session "Viking Friends" registered with join code 604510
11/02/2026 20:00:02: Session "Viking Friends" with join code 604510 and IP 203.0.113.4:2456 is active with 0 player(s)
11/02/2026 20:01:00: Got connection SteamID 76561198000000001
11/02/2026 20:01:09: Got character ZDOID from Astrid : -111:1
11/02/2026 20:02:00: Got connection SteamID 76561198000000002
11/02/2026 20:02:09: Got character ZDOID from Bjorn : -222:1
`)
	peer := "11/02/2026 20:05:00: Peer 76561198000000666 has incompatible version, mine:1.0.12 (network version 35)   remote "
	version := "x\n" +
		`11/02/2026 20:05:00: Session "Viking Friends" with join code 604510 and IP 203.0.113.4:2456 is active with 0 player(s)` + "\n" +
		"(Filename: ./Runtime/Export/Debug/Debug.bindings.h Line: 35)\n\n" +
		"11/02/2026 20:05:00: Connections 0 ZDOS:1  sent:0 recv:0\n" +
		"11/02/2026 20:05:00: Connections 0 ZDOS:1  sent:0 recv:0\n" +
		"11/02/2026 20:35:00: Connections 0 ZDOS:1  sent:0 recv:0"
	acts := feedAt(t, &tr, clock.Add(5*time.Minute), peer+version+" (network version 35)")
	if tr.Count() != 2 || kinds(acts) != "bad_version:" {
		t.Errorf("made-up counts emptied the list: %d players, activities %s", tr.Count(), kinds(acts))
	}
	// The server's own count comes in between, so another made-up count
	// later can't finish what the first began.
	feedAt(t, &tr, clock.Add(10*time.Minute), "11/02/2026 20:10:00: Connections 2 ZDOS:1  sent:0 recv:0")
	feedAt(t, &tr, clock.Add(21*time.Minute), peer+"x\n11/02/2026 20:21:00: Connections 0 ZDOS:1  sent:0 recv:0")
	if tr.Count() != 2 {
		t.Errorf("made-up counts emptied the list: %d players", tr.Count())
	}
}

// Characters arrive in the order the players' games finish loading the
// world, not the order they connected, so a friend with a faster computer
// can arrive first. Nobody is given another's account, and friends who
// played here before are told apart by theirs.
func TestTrackerFriendsConnectingTogether(t *testing.T) {
	log := `
10/22/2026 20:01:00: Got connection SteamID 1
10/22/2026 20:01:01: Got handshake from client 1
10/22/2026 20:01:02: Got connection SteamID 2
10/22/2026 20:01:03: Got handshake from client 2
10/22/2026 20:01:30: Got character ZDOID from Bjorn : -222:1
10/22/2026 20:01:50: Got character ZDOID from Astrid : -111:1
`
	tr := Tracker{KnownPlatformID: known(map[string]string{"Astrid": "Steam_1", "Bjorn": "Steam_2"})}
	acts := feed(t, &tr, log)
	if got, want := kinds(acts), "join:Bjorn,join:Astrid"; got != want {
		t.Fatalf("activities = %s, want %s", got, want)
	}
	if acts[0].PlatformID != "Steam_2" || acts[1].PlatformID != "Steam_1" {
		t.Fatalf("Bjorn joined as %q, Astrid as %q", acts[0].PlatformID, acts[1].PlatformID)
	}
	if acts = feed(t, &tr, "10/22/2026 21:00:00: Closing socket 1"); kinds(acts) != "leave:Astrid" {
		t.Fatalf("Astrid left, but: %s", kinds(acts))
	}
	if p := tr.Players(); len(p) != 1 || p[0].Name != "Bjorn" || p[0].PlatformID != "Steam_2" {
		t.Fatalf("players = %+v", p)
	}

	// Their first time here, or only Astrid's: which connection is whose is
	// a guess, so neither is given an account, not even by a later line
	// about the connection.
	for _, ids := range []map[string]string{nil, {"Astrid": "Steam_1"}} {
		tr := Tracker{KnownPlatformID: known(ids)}
		acts := feed(t, &tr, log)
		feed(t, &tr, "10/22/2026 20:02:00: PlayFab socket with remote ID 1 received local Platform ID Steam_1")
		for _, a := range acts {
			if a.PlatformID != "" {
				t.Errorf("known %v: %s joined as %q", ids, a.Name, a.PlatformID)
			}
		}
		for _, p := range tr.Players() {
			if p.PlatformID != "" || p.ID == "" {
				t.Errorf("known %v: %+v", ids, p)
			}
		}
		// Someone who connects after them is no guess.
		acts = feed(t, &tr, `
10/22/2026 20:05:00: Got connection SteamID 3
10/22/2026 20:05:30: Got character ZDOID from Sigrid : -333:1
`)
		if len(acts) != 1 || acts[0].PlatformID != "Steam_3" {
			t.Errorf("known %v: Sigrid joined as %+v", ids, acts)
		}
	}
}
