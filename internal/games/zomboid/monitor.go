package zomboid

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The console, as a real Build 42 server writes it (FACTS.md has real
// excerpts in testdata/real). The server's own log lines have a header:
// the level, the part of the game that wrote it (padded), the frame
// number, maybe a clock, and "> " before the message:
//
//	LOG  : Network      f:0 st:34,907,676> *** SERVER STARTED ****
//	WARN : Script       f:0 st:34,865,802 at ModelScript.check     > no such model …
//	LOG  : General      f:0, t:1760000000000, st:1,234> Saving finish
//
// "st:" counts milliseconds since the server's Java started, so only "t:"
// (milliseconds since 1970, when the server prints it) is a time of day.
// Other lines have no header: the rest of a reply that runs over several
// lines (the "-name" lines after "Players connected (2): "), Java's own
// notes ("Picked up _JAVA_OPTIONS") and stack traces.
//
// Who is on comes from RCON's "players" (Probed): the console has no join
// or leave lines in Build 42 (FACTS.md: the server writes them to its own
// Logs/<time>_user.txt file only).
var (
	reHeader = regexp.MustCompile(`^(LOG|WARN|ERROR|DEBUG|TRACE|FATAL)\s*:\s*(\S+)[^>]*?> ?(.*)$`)
	reClock  = regexp.MustCompile(`\bt:(\d{10,})`)
	// Build 41 had a second counter after the header ("…> 137,346,089> …").
	reOldCounter = regexp.MustCompile(`^[\d,]+> `)

	reVersion    = regexp.MustCompile(`^version=(\S+)`)
	reRCONFailed = regexp.MustCompile(`^RCON: error creating socket on port (\d+)`)
	// The console's own "quit", which only someone at the server's console
	// can type (the app uses RCON, which the console doesn't echo).
	reConsoleQuit = regexp.MustCompile(`^command entered via server console \(System\.in\): "\s*quit\s*"$`)
)

// noise is what the Log page hides: the server's start-up housekeeping,
// which is long and says nothing to the owner.
var noise = []*regexp.Regexp{
	regexp.MustCompile(`^SLF4J\(`),
	regexp.MustCompile(`^Logs configuration:$`),
	regexp.MustCompile(`^\s*[A-Za-z]+: (General|Warning|Error|Debug|Trace|Off)$`), // the "Logs configuration" list
	regexp.MustCompile(`^LightingFPS set to `),
	regexp.MustCompile(`^Reseeding weather period`),
	regexp.MustCompile(`^Missing texture: `),
	regexp.MustCompile(`^ObjectID type=`),
	regexp.MustCompile(`^Picked up (_JAVA_OPTIONS|JAVA_TOOL_OPTIONS|JDK_JAVA_OPTIONS)`),
	regexp.MustCompile(`^compressionElapsed=`),
}

// splitLine takes the header off a console line: the message as the Log
// page shows it, its level ("" for a line without a header) and the
// line's own time (zero when it has none).
func splitLine(raw string) (text, level string, at time.Time) {
	text = strings.TrimRight(raw, "\r\n")
	text = strings.TrimPrefix(text, "\ufeff")
	m := reHeader.FindStringSubmatch(text)
	if m == nil {
		return text, "", time.Time{}
	}
	if c := reClock.FindStringSubmatch(text[:len(text)-len(m[3])]); c != nil {
		if ms, err := strconv.ParseInt(c[1], 10, 64); err == nil {
			at = time.UnixMilli(ms)
		}
	}
	return reOldCounter.ReplaceAllString(m[3], ""), m[1], at
}

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing but known, which never locks.
type monitor struct {
	known func(name string) string
	state game.RunState
	// shutdown is set once the server's shutdown handling has begun:
	// nothing after that means it is ready.
	shutdown bool
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
	text, level, stamp := splitLine(raw)
	out := game.Line{Text: text, Time: stamp}
	when := at(stamp, now)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || level == "DEBUG" || level == "TRACE" {
		out.Hide = true
		return out
	}
	for _, n := range noise {
		if n.MatchString(text) {
			out.Hide = true
			return out
		}
	}
	add := func(e game.Event) {
		e.Time = when
		out.Events = append(out.Events, e)
	}
	switch {
	case reVersion.MatchString(trimmed):
		m.state.Version = reVersion.FindStringSubmatch(trimmed)[1]

	case strings.Contains(trimmed, "*** SERVER STARTED"):
		for _, e := range m.online() {
			add(e)
		}

	case strings.HasPrefix(trimmed, "Saving finish"):
		// The end of every whole save: RCON's "save", and the save on
		// the way out (quit, Ctrl+C).
		m.state.LastSave = when
		add(game.Event{Kind: "saved"})

	case strings.HasPrefix(trimmed, "Shutdown handling started"):
		// Java is ending: after a quit, Ctrl+C, a crash or a failed
		// start alike, so it says nothing about whether someone asked.
		m.shutdown = true

	case reConsoleQuit.MatchString(trimmed):
		m.stopAsked(when, add)

	case reRCONFailed.MatchString(trimmed):
		p := reRCONFailed.FindStringSubmatch(trimmed)[1]
		add(game.Event{Kind: "rcon_failed",
			Text: "Project Zomboid couldn't open its RCON port " + p + " (another program has it), so the app can't count players or stop it gently. Stop it and choose new ports on its page."})

	case strings.HasPrefix(trimmed, "Enter new administrator password"):
		// The app always gives the admin password, so this means it
		// didn't reach the server; it now waits on its console for good.
		add(game.Event{Kind: "admin_prompt",
			Text: "The Project Zomboid server is waiting for an admin password, which the app gives it on its command line. Stop it and start it again; if this happens again, click Repair."})
	}
	return out
}

// online notes that the server is up, once.
func (m *monitor) online() []game.Event {
	if m.state.Ready || m.shutdown {
		return nil
	}
	m.state.Ready = true
	return []game.Event{{Kind: "online", Text: "The server is online and ready for players."}}
}

// stopAsked notes a stop someone asked for at the server's console. The
// manager counts the exit that follows as expected (RunState.Stopping): no
// crash report and no automatic restart. The app's own stop goes through
// RCON, and the manager knows it asked for that one.
func (m *monitor) stopAsked(when time.Time, add func(game.Event)) {
	if m.state.Stopping {
		return
	}
	m.state.Stopping = true
	m.state.StoppingAt = when
	add(game.Event{Kind: "stopping"})
}

func (m *monitor) listed(name string) bool {
	return slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
}

func (m *monitor) join(name string, now time.Time) []game.Event {
	if m.listed(name) {
		return nil
	}
	account := m.known(name)
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: now})
	return []game.Event{{Kind: "join", Time: now, Name: name, Account: account, Text: name + " joined the game."}}
}

func (m *monitor) leave(name string, now time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	return []game.Event{{Kind: "leave", Time: now, Name: name, Account: p.Account, Text: name + " left the game."}}
}

// Tick has nothing to do: the server always says when it's ready, and the
// probe answers once it is.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed folds in RCON's "players", which is authoritative: names it lists
// that aren't on yet join, names it no longer lists leave. RCON opens only
// once the server has started (FACTS.md), so an answer also means online.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	var evs []game.Event
	if p.Up {
		evs = append(evs, m.online()...)
		for i := range evs {
			evs[i].Time = now
		}
	}
	if p.Names != nil {
		for _, n := range p.Names {
			evs = append(evs, m.join(n, now)...)
		}
		for _, pl := range slices.Clone(m.state.Players) {
			if !slices.Contains(p.Names, pl.Name) {
				evs = append(evs, m.leave(pl.Name, now)...)
			}
		}
	}
	if p.Count >= 0 {
		m.state.Count = p.Count
		if p.Count > 0 {
			// The server's own count: a player can't fake it.
			evs = append(evs, game.Event{Kind: "count", Time: now, Count: p.Count, Hold: true})
		}
	} else {
		m.state.Count = len(m.state.Players)
	}
	return evs
}

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = slices.Clone(m.state.Players)
	return st
}

// End: the run is over, so whoever is listed has left.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	m.state.Count = 0
	m.state.Ready = false
	return evs
}
