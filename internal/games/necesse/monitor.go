package necesse

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The console, as the real 1.3.3 server writes it (FACTS.md). Each line
// starts with a colour code, then the local time in brackets, then the
// message; debug lines and errors say so after the time:
//
//	ESC[39m[2026-11-26 20:17:45] Type help for list of commands.
//	ESC[34m[2026-11-26 20:17:36] (DEBUG) Started logging to: …
//	[2026-11-26 20:26:54] (ERR) java.net.BindException: Address already in use
//
// A few lines come before the server's own log starts, without a time
// ("First time launched"), and Java itself may print "Picked up …" lines.
var (
	reANSI  = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	reStamp = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)\] `)

	reLoading = regexp.MustCompile(`^Loading dedicated server on version (\S+?)\.?$`)
	// The password, when there is one, is printed in clear; the Monitor
	// masks it in the line the Log page shows.
	reStarted = regexp.MustCompile(`^Started server using port (\d+) with (\d+) slots on world "(.*?)"( with password "(.*)")?, game version (\S+?)\.?$`)
	reReady   = regexp.MustCompile(`^Type help for list of commands\.$`)
	// The server also repeats its command line, password and all.
	reLaunched = regexp.MustCompile(`^Launched game with arguments: `)
	reArgPass  = regexp.MustCompile(`(-password )("[^"]*"|[^\s"]+)`)
	// A player gets a slot once the server has let them in. A player new to
	// the world may be named by their account number here, before they
	// have a character (FACTS.md).
	reJoin = regexp.MustCompile(`^Client "(.+)" connected on slot (\d+)/(\d+)\.$`)
	// The account of the player who just got in.
	reLoaded = regexp.MustCompile(`^(?:Loaded player|Creating new player): (\d+)$`)
	reLeave  = regexp.MustCompile(`^Player (\d+) \("(.*)"\) disconnected with message: (.*)$`)
	reCount  = regexp.MustCompile(`^Players online: (\d+)/(\d+)$`)
	// Saves: a "save" typed on the console, the save before stopping, and
	// the autosave (about once a minute while the world runs), which says
	// only that it reloaded its files once it has written them. Servers
	// before 1.0 said "Saved all data." instead (the forum logs in
	// testdata/forum).
	reSaved = regexp.MustCompile(`^(?:Completed world save( before stopping server)?|(?:Auto save: )?Reloaded file system in .*|Saved all data\.)$`)
	// The stop, in the order the server says it.
	reStopCmd  = regexp.MustCompile(`^(?:> |.* issued command: /)(?:stop|exit|quit)\s*$`)
	reStopped  = regexp.MustCompile(`^Stopped server on .* with code: (\w+)$`)
	reStopEnd  = regexp.MustCompile(`^(?:Server has stopped|Exiting in \d+ seconds\.\.\.)$`)
	reBadVer   = regexp.MustCompile(`had wrong version \(`)
	reQuotedNm = regexp.MustCompile(`^Client "(.+?)"`)
	// Chat: "(name): message". Anything a player types arrives this way,
	// so a chat line never makes an event, whatever it says.
	reChat = regexp.MustCompile(`^\(.+?\): `)
)

// noise is what the Log page hides: the server's busy work, and Java's
// own notes.
var noise = []string{
	"Starting to load level presets region",
	"Suggesting garbage collection",
	"Unloaded level ",
	"Reloaded file system in ",
	"Picked up JAVA_TOOL_OPTIONS",
	"Picked up _JAVA_OPTIONS",
	"Picked up JDK_JAVA_OPTIONS",
}

// splitLine takes the colour codes and the time off a console line: the
// message as the Log page shows it, and the line's own time (zero when it
// has none). The server prints its local time.
func splitLine(raw string) (string, time.Time) {
	text := strings.TrimRight(raw, "\r\n")
	text = strings.TrimPrefix(text, "\ufeff")
	if strings.Contains(text, "\x1b") {
		text = reANSI.ReplaceAllString(text, "")
	}
	var at time.Time
	if m := reStamp.FindStringSubmatch(text); m != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local); err == nil {
			at = t
		}
		text = text[len(m[0]):]
	}
	return strings.TrimSpace(text), at
}

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing but known, which never locks.
type monitor struct {
	known func(name string) string
	state game.RunState

	bindFailed bool   // the port couldn't be opened: the server says it started, but it stops at once
	stopCode   string // "SERVER_STOPPED", "NETWORK_ERROR"…, once it said
	stopping   bool
	// lastJoin is the player who got in last and whose account isn't
	// known yet; the next "Loaded player" line names it.
	lastJoin string
}

func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, state: game.RunState{Count: 0}}
}

// at is when an event happened: the line's own time, unless it has none
// or reads later than now (a clock change).
func at(line, now time.Time) time.Time {
	if line.IsZero() || line.After(now) {
		return now
	}
	return line
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	text, stamp := splitLine(raw)
	out := game.Line{Text: text, Time: stamp}
	when := at(stamp, now)
	if text == "" {
		out.Hide = true
		return out
	}
	if strings.HasPrefix(text, "(DEBUG) ") {
		out.Hide = true
		return out
	}
	if rest, ok := strings.CutPrefix(text, "(ERR) "); ok {
		if strings.Contains(rest, "java.net.BindException") {
			m.bindFailed = true
		}
		return out
	}
	if reChat.MatchString(text) {
		return out
	}
	for _, n := range noise {
		if strings.Contains(text, n) {
			out.Hide = true
			break
		}
	}
	add := func(e game.Event) {
		e.Time = when
		out.Events = append(out.Events, e)
	}

	switch {
	case reLaunched.MatchString(text):
		out.Text = reArgPass.ReplaceAllString(text, "${1}•••")

	case reLoading.MatchString(text):
		m.state.Version = reLoading.FindStringSubmatch(text)[1]

	case reStarted.MatchString(text):
		g := reStarted.FindStringSubmatch(text)
		m.state.Version = g[6]
		if g[4] != "" {
			// Keep the password off the Log page.
			out.Text = strings.Replace(text, g[4], ` with password "•••"`, 1)
		}

	case reReady.MatchString(text):
		// With its port taken the server still says this, then stops
		// (FACTS.md), so it counts only when the port opened.
		if !m.state.Ready && !m.bindFailed && !m.stopping {
			m.state.Ready = true
			add(game.Event{Kind: "online", Text: "The server is online and ready for players."})
		}

	case reJoin.MatchString(text):
		name := reJoin.FindStringSubmatch(text)[1]
		account := m.known(name)
		if isAccount(name) {
			account = name
		}
		if !m.listed(name) {
			m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: when})
			m.state.Count = len(m.state.Players)
			add(game.Event{Kind: "join", Name: name, Account: account, Text: name + " joined the game."})
		}
		m.lastJoin = ""
		if account == "" {
			m.lastJoin = name
		}

	case reLoaded.MatchString(text):
		if m.lastJoin != "" {
			acc := reLoaded.FindStringSubmatch(text)[1]
			for i := range m.state.Players {
				if m.state.Players[i].Name == m.lastJoin && m.state.Players[i].Account == "" {
					m.state.Players[i].Account = acc
				}
			}
			m.lastJoin = ""
		}

	case reLeave.MatchString(text):
		g := reLeave.FindStringSubmatch(text)
		account, name := g[1], g[2]
		if i := m.find(name, account); i >= 0 {
			p := m.state.Players[i]
			m.state.Players = append(m.state.Players[:i], m.state.Players[i+1:]...)
			m.state.Count = len(m.state.Players)
			add(game.Event{Kind: "leave", Name: p.Name, Account: account, Text: p.Name + " left the game."})
		}

	case reCount.MatchString(text):
		n, _ := strconv.Atoi(reCount.FindStringSubmatch(text)[1])
		m.state.Count = n
		// The server's own count: a player can't fake this line (chat
		// lines start with the player's name in brackets).
		add(game.Event{Kind: "count", Count: n, Hold: n > 0})

	case reSaved.MatchString(text):
		m.state.LastSave = when
		e := game.Event{Kind: "saved"}
		if !strings.Contains(text, "Reloaded file system") {
			// The autosave comes every minute; only the saves someone
			// asked for go in the feed.
			e.Text = "World saved."
		}
		add(e)

	case reStopCmd.MatchString(text), reStopped.MatchString(text), reStopEnd.MatchString(text):
		if g := reStopped.FindStringSubmatch(text); g != nil {
			m.stopCode = g[1]
		}
		if !m.stopping {
			m.stopping = true
			m.state.Stopping = true
			m.state.StoppingAt = when
			add(game.Event{Kind: "stopping"})
		}

	case reBadVer.MatchString(text):
		name := ""
		if g := reQuotedNm.FindStringSubmatch(text); g != nil {
			name = g[1]
		}
		who := name
		if who == "" {
			who = "A player"
		}
		add(game.Event{Kind: "bad_version", Name: name,
			Text: who + " couldn't join: their Necesse is a different version from the server's."})
	}
	return out
}

// isAccount says whether a name is really an account number, as the
// server shows a player who has no character on this world yet.
func isAccount(name string) bool {
	if len(name) < 6 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (m *monitor) listed(name string) bool { return m.find(name, "") >= 0 }

// find is who a leave line means: the player by name, by account, or by
// the account number they joined under.
func (m *monitor) find(name, account string) int {
	for i, p := range m.state.Players {
		if p.Name == name {
			return i
		}
	}
	if account == "" {
		return -1
	}
	for i, p := range m.state.Players {
		if p.Account == account || p.Name == account {
			return i
		}
	}
	return -1
}

// Tick has nothing to do: the server always says when it's ready, and it
// starts in seconds.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed is never called: Necesse has no query the app can ask.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event { return nil }

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = append([]game.Player(nil), m.state.Players...)
	return st
}

// End: the run is over, so whoever is listed has left.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range m.state.Players {
		evs = append(evs, game.Event{Kind: "leave", Time: now, Name: p.Name, Account: p.Account})
	}
	m.state.Players = nil
	m.state.Count = 0
	m.state.Ready = false
	return evs
}
