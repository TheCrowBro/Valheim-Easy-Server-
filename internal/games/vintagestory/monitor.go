package vintagestory

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The console, as the real 1.22.7 server writes it (FACTS.md): the local
// time ("d.M.yyyy HH:mm:ss"), the kind of line in brackets, then the text.
//
//	26.11.2026 21:34:47 [Server Event] Dedicated Server now running on Port 42420 and all ips!
//
// The server's own server-main.log writes the same lines without "Server "
// in the brackets ("[Event]"); both are read, so the public logs in
// testdata/issues test the same code. Lines without a time are the rest of
// a line before them: a stack trace, or the answer to /help.
var (
	reLine = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{4}) (\d{1,2})[:.](\d{2})[:.](\d{2}) \[(?:Server )?([A-Za-z]+)\] ?(.*)$`)

	reGameVersion = regexp.MustCompile(`^Game Version: v(\S+)`)
	reReady       = regexp.MustCompile(`^Dedicated Server now running on Port (\d+)`)
	// A player's account is their Vintage Story player uid, named in the
	// line before they get in. Player names are letters, digits, "_" and
	// "-" (the server's own rule, FACTS.md), so they have no spaces.
	reIdent = regexp.MustCompile(`^Client (\d+) uid (\S+) attempting identification\. Name: (\S+)$`)
	// "Sigrun [::ffff:198.51.100.30]:64570 joins.": the name, then the
	// address the player came from.
	reJoin    = regexp.MustCompile(`^(\S+) (\S+) joins\.$`)
	reLeft    = regexp.MustCompile(`^Player (\S+) left\.$`)
	reRemoved = regexp.MustCompile(`^Player (\S+) got removed\. Reason: (.*)$`)
	reSaved   = regexp.MustCompile(`^World saved! Saved `)
	reStopAsk = regexp.MustCompile(`^Server stop requested, begin shutdown sequence\. Stop reason: (.*)$`)
	reBadPass = regexp.MustCompile(`^(\S+) fails to join \(invalid server password\)\.$`)
	reSaveCmd = regexp.MustCompile(`^Handling Console Command /autosavenow\b`)
	reCorrupt = regexp.MustCompile(`^Failed opening savegame, possibly corrupted`)
)

// What the Log page hides: the long loading report, the steps of each
// save, and the warning a busy computer prints every few seconds.
var (
	noisePrefix = []string{
		"Found ", // "Found 316 base assets in total"
		"Offthread save of",
		"Saved player world data...",
		"Saved map regions...",
		"Saved map chunks...",
		"Saved loaded chunks...",
		"Saved generating chunks...",
		"Saved savegamedata...",
		"Entering runphase ",
		"Defragmented listener lists",
		"UDP: Client ",
		"Placing ",
	}
	noiseContains = []string{
		"Server overloaded. A tick took",
		"% (", // "0% (49 in queue)" while the spawn area loads
	}
)

func noise(text string) bool {
	for _, n := range noisePrefix {
		if strings.HasPrefix(text, n) {
			return true
		}
	}
	for _, n := range noiseContains {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// splitLine takes the time and the kind off a console line: the message as
// the Log page shows it, its kind ("Event", "Notification", "Debug"…), and
// the line's own time (zero when it has none). The server prints its
// local time. On Windows its lines end with "\r\n" and a letter outside
// ASCII comes in the console's code page (consoleText).
func splitLine(raw string) (text, kind string, at time.Time) {
	text = consoleText(strings.TrimRight(raw, "\r\n"))
	text = strings.TrimPrefix(text, "\ufeff")
	m := reLine.FindStringSubmatch(text)
	if m == nil {
		return text, "", time.Time{}
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	at = time.Date(n(3), time.Month(n(2)), n(1), n(4), n(5), n(6), 0, time.Local)
	return m[8], m[7], at
}

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing but known, which never locks.
type monitor struct {
	known func(name string) string
	state game.RunState

	uids      map[string]string // player name -> account (Vintage Story's player uid), from the identification line
	saveAsked bool              // the next "World saved!" answers /autosavenow or a stop
}

func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, uids: map[string]string{}, state: game.RunState{Count: 0}}
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
	text, kind, stamp := splitLine(raw)
	out := game.Line{Text: text, Time: stamp}
	when := at(stamp, now)
	if strings.TrimSpace(text) == "" {
		out.Hide = true
		return out
	}
	if kind == "" {
		// The rest of an earlier line: a stack trace (a port in use comes
		// this way, inside the crash report, which ExitHint reads), or the
		// answer to a command. It never makes an event.
		return out
	}
	switch kind {
	case "Debug", "VerboseDebug", "Worldgen", "Build", "Audit":
		out.Hide = true
	}
	if strings.HasPrefix(text, " ") || noise(text) {
		// Indented: the list of the server's systems, at every start.
		out.Hide = true
	}
	add := func(e game.Event) {
		e.Time = when
		out.Events = append(out.Events, e)
	}
	if kind == "Chat" {
		// What players type never makes an event, whatever it says.
		return out
	}

	switch {
	case reGameVersion.MatchString(text):
		m.state.Version = reGameVersion.FindStringSubmatch(text)[1]

	case reReady.MatchString(text):
		if !m.state.Ready && !m.state.Stopping {
			m.state.Ready = true
			add(game.Event{Kind: "online", Text: "The server is online and ready for players."})
		}

	case reIdent.MatchString(text):
		g := reIdent.FindStringSubmatch(text)
		m.uids[g[3]] = g[2]

	case reJoin.MatchString(text):
		name := reJoin.FindStringSubmatch(text)[1]
		account := m.uids[name]
		if account == "" {
			account = m.known(name)
		}
		if i := m.find(name); i < 0 {
			m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: when})
			m.state.Count = len(m.state.Players)
			add(game.Event{Kind: "join", Name: name, Account: account, Text: name + " joined the game."})
		}

	case reLeft.MatchString(text):
		m.leave(reLeft.FindStringSubmatch(text)[1], "", add)

	case reRemoved.MatchString(text):
		g := reRemoved.FindStringSubmatch(text)
		m.leave(g[1], g[2], add)

	case reBadPass.MatchString(text):
		name := reBadPass.FindStringSubmatch(text)[1]
		add(game.Event{Kind: "wrong_password", Name: name, Text: name + " couldn't join: they typed the wrong password."})

	case reSaveCmd.MatchString(text):
		m.saveAsked = true

	case reSaved.MatchString(text):
		m.state.LastSave = when
		e := game.Event{Kind: "saved"}
		if m.saveAsked || m.state.Stopping {
			// The autosave comes every 5 minutes; only the saves someone
			// asked for go in the feed.
			e.Text = "World saved."
		}
		m.saveAsked = false
		add(e)

	case reStopAsk.MatchString(text):
		// A stop someone asked for: "/stop" on the console or by an admin
		// in the game ("Shutdown via server command"), or Ctrl+C or the
		// system closing it ("External close event SIGINT received"). Any
		// other reason is the server giving up by itself ("Failed opening
		// savegame", "serverconfig.json read error"), which the manager
		// must see as the crash it is: it says why (ExitHint) and starts
		// the server again when "Start it again if it crashes" is on.
		reason := reStopAsk.FindStringSubmatch(text)[1]
		if askedStop(reason) && !m.state.Stopping {
			m.state.Stopping = true
			m.state.StoppingAt = when
			add(game.Event{Kind: "stopping"})
		}

	case reCorrupt.MatchString(text):
		add(game.Event{Kind: "load_failed",
			Text: "The world couldn't be opened: its file may be damaged. Restore one of its backups on the Worlds page."})
	}
	return out
}

// askedStop says whether a stop reason is a stop someone asked for.
func askedStop(reason string) bool {
	return reason == "Shutdown via server command" || strings.HasPrefix(reason, "External close event ") ||
		reason == "Stop through standalone server exit request"
}

// leave takes a player off the list. reason is the server's, for a player
// it removed ("The Players client crashed", a kick).
func (m *monitor) leave(name, reason string, add func(game.Event)) {
	i := m.find(name)
	if i < 0 {
		return
	}
	p := m.state.Players[i]
	m.state.Players = append(m.state.Players[:i], m.state.Players[i+1:]...)
	m.state.Count = len(m.state.Players)
	text := p.Name + " left the game."
	if reason != "" {
		text = p.Name + " left the game (" + strings.TrimSuffix(reason, ".") + ")."
	}
	add(game.Event{Kind: "leave", Name: p.Name, Account: p.Account, Text: text})
}

func (m *monitor) find(name string) int {
	for i, p := range m.state.Players {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// Tick has nothing to do: the server always says when it's ready.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed is never called: the app has no query it can ask the server.
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
