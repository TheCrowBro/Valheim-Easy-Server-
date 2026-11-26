package corekeeper

import (
	"regexp"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's log (-logfile), as the real 1.3.0.7 server writes it and as
// players posted it from 1.0.0.14 (FACTS.md, testdata/). Lines are Unity's
// plain Debug.Log output: no time, no level. The strings below are the
// ones in the server's own code (Pug.Other.dll), so the same lines come on
// Windows and Linux.
var (
	// "Game version: 1.3.0.7-5554": the version friends need, then a
	// build tag.
	reVersion = regexp.MustCompile(`^Game version: (\S+)$`)
	// The Steam session is up, with the Game ID: 1.3 prints it after
	// "info: "; 1.0 printed "Started session with Game ID <id>". The world
	// is loaded or made after this line, not before.
	reSession = regexp.MustCompile(`^Started session with (?:info: |Game ID )(\S+)`)
	// The world is up: the server pauses its clock until someone joins
	// ("timescale = 0"), and starts it again when they do. In every real
	// log this comes right after the world is loaded or made.
	reTimescale = regexp.MustCompile(`^timescale = \d`)
	// A player is in: "[userid:<steam id>] player <name> connected
	// islocalplayer=False". The name is the character's.
	reJoin = regexp.MustCompile(`^\[userid:(\d+)\] player (.+) connected islocalplayer=\w+$`)
	// The same player, with the name they now use.
	reRename = regexp.MustCompile(`^\[userid:(\d+)\] is using new name (.+)$`)
	// A player has gone: "Disconnected from userid:<steam id> with reason
	// App_Min". It names only the account, so the Monitor keeps who each
	// account is from the join line.
	reLeave = regexp.MustCompile(`^Disconnected from userid: ?(\d+) with reason (.*)$`)
	reFull  = regexp.MustCompile(`^Rejecting player with userid:(\d+) since player limit has been reached$`)
	// A stop: the server's answer to Ctrl+C (Linux) or to its window being
	// closed (Windows, what Pugstorm's launcher does). It saves the world
	// before it quits, saying "Quit blocked" while the save runs, then
	// "Running quit handlers" once it goes on to quit.
	reQuit         = regexp.MustCompile(`^Got quit request$`)
	reQuitHandlers = regexp.MustCompile(`^Running quit handlers$`)
	reSaveFailed   = regexp.MustCompile(`^Skipping world save due to (.*)$`)
	reSysFail      = regexp.MustCompile(`^Checking system requirements: FAIL$`)
	reNoSteam      = regexp.MustCompile(`^Not initializing Steam network since no Steam API available$`)
	reRelay        = regexp.MustCompile(`^SteamNet Error: Unable to communicate with ANY of \d+ Steam Datagram routing cluster`)
)

// noise is what the Log page hides: Unity's start-up details and the
// server's busy work, which say nothing to the owner. Matched at the
// start of a line.
var noise = []string{
	"CommandLineArgs.args[",
	"[Physics::Module]",
	"[Subsystems]",
	"Mono path[", "Mono config path",
	`"memorysetup-`, "[UnityMemory]", // on standard output, should it ever be captured
	"JobSystem:",
	"Automatically adding content bundle",
	"Adding content bundle",
	"Adding marker for",
	"Spawning ",
	"Removing fake marker",
	"timescale = ",
	"SteamNet Warning:",
	"Skipping faction mod", "Skipping loot mod",
	"PugDatabase initialized",
	"Converting 1 root objects",
	"ECSManager:",
	"disable ",
	"Ignoring invalid [",
	"This attribute can only order systems",
	"Make sure that both systems are in the same system group",
	"or by manually adding both systems",
	"A system Interaction.",
	"Read cartography table map",
	"Running 0 electricity updates",
	"CreateBoundSocket: failed to create socket",
	"Allocating a new file write buffer",
	"Input System ",
	"PlatformManager was destroyed",
}

// byteOrderMark may start the first line of a log file.
const byteOrderMark = string(rune(0xFEFF))

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing outside itself.
type monitor struct {
	state game.RunState

	names        map[string]string // steam id → the name it joined as
	sessionAt    time.Time         // when the session line came; zero before
	worldUp      bool              // the world was loaded or made
	stopping     bool
	quitHandlers bool // the server went on to quit
	saveFailed   bool
	relayWarned  bool
}

// worldWait is how long after its session line the Monitor waits for the
// world before it says the server is online anyway, in case a later
// version words the world's line differently. A new world took about a
// second here (FACTS.md).
const worldWait = 2 * time.Minute

// Watch follows a run. The join line names each player's Steam account
// itself, so the Monitor needs no account the name last played from.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	return &monitor{state: game.RunState{Count: 0}, names: map[string]string{}}
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	text := strings.TrimSpace(strings.TrimPrefix(raw, byteOrderMark))
	out := game.Line{Text: text}
	if text == "" {
		out.Hide = true
		return out
	}
	for _, n := range noise {
		if strings.HasPrefix(text, n) {
			out.Hide = true
			break
		}
	}
	if strings.Count(text, "GL_") >= 3 {
		// The graphics driver's extension list, which Unity splits over
		// several lines mid-word.
		out.Hide = true
	}
	add := func(e game.Event) {
		e.Time = now
		out.Events = append(out.Events, e)
	}

	switch {
	case reVersion.MatchString(text):
		v := reVersion.FindStringSubmatch(text)[1]
		v, _, _ = strings.Cut(v, "-") // "1.3.0.7-5554": the build tag isn't part of what players see
		m.state.Version = v

	case reSession.MatchString(text):
		id := reSession.FindStringSubmatch(text)[1]
		if validGameID(id) {
			m.state.Code = id
			add(game.Event{Kind: "code", Text: ""})
		}
		if m.sessionAt.IsZero() {
			m.sessionAt = now
		}

	case reTimescale.MatchString(text):
		// Ready once the world is up after the session started: a stop
		// before that quits without saving anything (FACTS.md), and the
		// world friends would join isn't there yet.
		if !m.sessionAt.IsZero() && !m.worldUp {
			m.worldUp = true
			out.Events = append(out.Events, m.online(now)...)
		}

	case reJoin.MatchString(text):
		g := reJoin.FindStringSubmatch(text)
		account, name := g[1], g[2]
		m.names[account] = name
		if i := m.find(account); i >= 0 {
			// Seen again without a leave (a reconnect): the same player.
			m.state.Players[i].Name = name
			break
		}
		m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: now})
		m.state.Count = len(m.state.Players)
		add(game.Event{Kind: "join", Name: name, Account: account, Text: name + " joined the game."})

	case reRename.MatchString(text):
		g := reRename.FindStringSubmatch(text)
		account, name := g[1], g[2]
		m.names[account] = name
		if i := m.find(account); i >= 0 {
			m.state.Players[i].Name = name
		}

	case reLeave.MatchString(text):
		account := reLeave.FindStringSubmatch(text)[1]
		if i := m.find(account); i >= 0 {
			p := m.state.Players[i]
			m.state.Players = append(m.state.Players[:i], m.state.Players[i+1:]...)
			m.state.Count = len(m.state.Players)
			add(game.Event{Kind: "leave", Name: p.Name, Account: account, Text: p.Name + " left the game."})
		}

	case reFull.MatchString(text):
		account := reFull.FindStringSubmatch(text)[1]
		add(game.Event{Kind: "server_full", Name: m.names[account], Account: account,
			Text: "A player couldn't join: the server is full. Raise \"Most players at once\" in Core Keeper's settings to let more in."})

	case reQuit.MatchString(text):
		// Once the world is up, only a stop someone asked for makes the
		// server quit: it has no console, and nothing in the game stops
		// it. Before that it may be the server giving up by itself ("...
		// aborted setup, exiting...", "Something failed during server
		// startup"), which the manager must see as the failure it is; a
		// stop the app asked for then is one the manager knows about.
		if !m.stopping && m.worldUp {
			m.stopping = true
			m.state.Stopping = true
			m.state.StoppingAt = now
			add(game.Event{Kind: "stopping"})
		}

	case reQuitHandlers.MatchString(text):
		m.quitHandlers = true

	case reSaveFailed.MatchString(text):
		m.saveFailed = true
		add(game.Event{Kind: "save_failed", Text: "The server couldn't save the world (" + reSaveFailed.FindStringSubmatch(text)[1] + ")."})

	case reSysFail.MatchString(text):
		add(game.Event{Kind: "bad_graphics",
			Text: "Core Keeper says this computer's graphics don't meet its needs. The server may not make worlds properly: update the graphics driver."})

	case reNoSteam.MatchString(text):
		add(game.Event{Kind: "no_steam", Text: "The server couldn't start Steam's networking, so friends can't join it."})

	case reRelay.MatchString(text):
		out.Hide = m.relayWarned // it repeats every few seconds
		if !m.relayWarned {
			m.relayWarned = true
			add(game.Event{Kind: "relay_failed",
				Text: "The server can't reach Steam's relay network yet, so friends may not be able to join. Check this computer's internet connection."})
		}
	}
	return out
}

// find is the listed player with this Steam account.
func (m *monitor) find(account string) int {
	for i, p := range m.state.Players {
		if p.Account == account {
			return i
		}
	}
	return -1
}

// online says the server is ready for players, once.
func (m *monitor) online(now time.Time) []game.Event {
	if m.state.Ready || m.stopping {
		return nil
	}
	m.state.Ready = true
	return []game.Event{{Kind: "online", Time: now, Text: "The server is online and ready for players."}}
}

// Tick says the server is online worldWait after its session started,
// when no line said its world was up: the session is what friends join.
func (m *monitor) Tick(now time.Time) []game.Event {
	if m.sessionAt.IsZero() || m.state.Ready || now.Sub(m.sessionAt) < worldWait {
		return nil
	}
	return m.online(now)
}

// Probed is never called: Core Keeper has no query the app can ask.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event { return nil }

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = append([]game.Player(nil), m.state.Players...)
	return st
}

// End: the run is over, so whoever is listed has left. A server whose
// world was up and that went on to quit after a stop request saved the
// world on its way out (it writes the world in its quit handlers,
// FACTS.md), unless it said the save failed; the log has no line once the
// file is written, so the end of the process is when it was saved. Asked
// to quit before its world was up, it quits without saving anything.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range m.state.Players {
		evs = append(evs, game.Event{Kind: "leave", Time: now, Name: p.Name, Account: p.Account})
	}
	m.state.Players = nil
	m.state.Count = 0
	m.state.Ready = false
	if m.worldUp && m.quitHandlers && !m.saveFailed {
		m.state.LastSave = now
	}
	return evs
}
