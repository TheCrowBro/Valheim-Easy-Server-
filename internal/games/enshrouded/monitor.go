package enshrouded

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's log (logDirectory/enshrouded_server.log), as real servers
// wrote it in 2024 and in 1.0 (FACTS.md and testdata/real). Every line
// starts with a level letter and the time since the server started:
//
//	[I 00:01:38,732] [server] Player 'Eydis' logged in with Permissions:
//
// The strings below are in the 1.0 server's program (FACTS.md, "Log
// lines"), and lines printed before the log opens have no prefix.
var (
	rePrefix = regexp.MustCompile(`^\[([A-Z]) (\d+):(\d\d):(\d\d),(\d{3})\] ?`)

	// The game's build, the number Keen's patch notes give.
	reVersion = regexp.MustCompile(`^Game Version \(SVN\): (\S+)`)
	// Ready: Keen's FAQ names both lines as the sign the server is up.
	reOnline     = regexp.MustCompile(`^\[Session\] 'HostOnline' \(up\)!`)
	reOnlineDone = regexp.MustCompile(`^\[Session\] finished transition from '[^']*' to 'Host_Online'`)
	// The session closed: nobody can be on after this.
	reOffline = regexp.MustCompile(`^\[Session\] 'HostOnline' \(down\)!`)
	// A player is in, by the name they play as (their Steam name). The
	// line before it, "[server] Machine '1': Player '0(0)' logged in",
	// names a handle, not a player.
	reJoin = regexp.MustCompile(`^\[server\] Player '(.+)' logged in with Permissions:`)
	// A player has gone: "Remove Entity for Player 'X'" then "Remove
	// Player 'X'".
	reLeave = regexp.MustCompile(`^\[server\] Remove (?:Entity for )?Player '(.+)'$`)
	// The connection before a join, with the player's Steam account: 1.0
	// writes "Added peer 0(1) (steamid:76561198000000202)"; 2024's servers
	// wrote "Session accepted with peer ( id 7656… )." then "Added Peer #0.".
	rePeerAdded    = regexp.MustCompile(`^\[online\] Added peer (\S+) \((?:steamid:)?(\d{5,20})\)`)
	rePeerAccepted = regexp.MustCompile(`^\[online\] Session accepted with peer \(\s*(?:id )?(\d{5,20})\s*\)`)
	rePeerRemoved  = regexp.MustCompile(`(?i)^\[online\] Removed peer (\S+?)\.?$`)
	reFull         = regexp.MustCompile(`^\[online\] Session rejected with peer because it is full`)
	// A stop, and why. The reasons are the names the program gives the
	// console events it handles (see requested).
	reShutdown = regexp.MustCompile(`^\[app\] Trigger gameflow shutdown, exit: (\S+)`)
	// The server's world thread has begun shutting down, which saves the
	// world on the way out.
	reShutdownState = regexp.MustCompile(`^\[game_server\] Switching state from Run to Shutdown`)
	reSaveSystemBad = regexp.MustCompile(`^(Shutting down Server Save System with pending changes|Error while shutting down Server Save System)`)
	// 2024's servers logged every autosave; 1.0 logs none (FACTS.md).
	reSaved      = regexp.MustCompile(`^\[server\] Saved\b`)
	reSaveFailed = regexp.MustCompile(`^\[server\] (Failed to save .*|Failed to Save\. Retry .*)$`)
	// A save failed and the server tries again: what it writes on its way
	// to giving up ("On dedicated servers, the server will shut down
	// automatically in case many consecutive attempts to save were
	// unsuccessful", Keen's Hotfix 6).
	reSaveRetry = regexp.MustCompile(`^\[server\] Failed to Save\. Retry `)
	// The server telling each player it is shutting down because it can't
	// save. The program sends players a reason when it drops them, and
	// "SaveDataIssue" is one of those reasons, not a reason the server
	// logs for its own exit (FACTS.md, "Saving and stopping"). With nobody
	// on, there is no such line, only the retries before it.
	reSaveQuit   = regexp.MustCompile(`^\[server\] Send Save Data Error Shutdown Message to Player '`)
	reLoadFailed = regexp.MustCompile(`^\[server\] Failed to load Server Save Game|^\[ecs.\] Could not deserialize savegame`)
	reBadSocket  = regexp.MustCompile(`^\[online\] Couldn't (?:create|bind) dedicated server socket`)
	reNoSteam    = regexp.MustCompile(`^\[online\] Server failed to connected? to Steam`)
	reLostSteam  = regexp.MustCompile(`^\[online\] Server lost connection to Steam`)
	// The server's own checks of its settings file (its program's
	// strings: "Internal Error: user groups passwords must be unique").
	reConfigError = regexp.MustCompile(`^(?:Internal Error|Config Error): (.+)$`)
)

// requested are the shutdown reasons that mean someone asked the server to
// stop, so its end isn't a crash. They are the program's names for the
// console events it handles: Ctrl_C is the app's (or anyone's) Ctrl+C,
// Ctrl_Break is Ctrl+Break, Close is its console being closed and Logoff is
// the user signing out of Windows. Its strings hold these four names and,
// right after them, "[%u]", which most likely prints any other event as a
// number. A number, or any other word, is not taken as a stop someone
// asked for: a server that stopped by itself must look like one, so the
// manager can say so (and start it again, if the owner wants that). The
// cost is small: a stop for a reason nobody has seen is reported as a
// crash.
var requested = []string{"Ctrl_C", "Ctrl_Break", "Close", "Logoff"}

// noise is what the Log page hides: start-up details, the half-minute
// session dumps and the engine's busy work, which say nothing to the owner.
// Matched at the start of the line (after its level and time).
var noise = []string{
	"-------------- Session", "---------------------------------------", "Machines:",
	"CPUs:", "CPU Types:", " Idx ", "  Performance:", "  Efficient:",
	"[task]", "[os] Started thread", "[app] start creation step", "[app] start destruction step",
	"[resource]", "[ecs]", "[ecss]", "[guid_registry]", "[savexxx]", "[TerraformingEfficiencyRegistry]",
	"[OnlineProviderSteam]", "[SessionPlayer]", "[savedata] Start '", "[savedata] Updating '", "[savedata] Finished '",
	"Added a '", "Received new Character save game", "[session] Remote player added",
}

// reNoiseRow is a row of the start-up CPU table ("   3   259    0 …") or
// of the session dump ("  m#0(128): up 0 (0), …").
var reNoiseRow = regexp.MustCompile(`^\s*(\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s*|m#\d+\(\d+\): .*)$`)

// stripPrefix takes the level and time off a line.
func stripPrefix(line string) string {
	line = strings.TrimPrefix(strings.TrimRight(line, "\r\n"), "\ufeff")
	if m := rePrefix.FindStringIndex(line); m != nil {
		return line[m[1]:]
	}
	return line
}

// probeFallback is how long the server's Steam query port must have
// answered, with no "online" line in its log, before the Monitor says it
// is online anyway: Steam lists the server once that port answers, so
// friends can join, and a log the app can't read (or a reworded line)
// mustn't leave the game "starting" for good.
const probeFallback = time.Minute

// monitor follows one run of the server. Every method is called with the
// manager's lock held, so it calls nothing outside itself but known, which
// never locks.
type monitor struct {
	known   func(name string) string
	started time.Time // the process's start: the log's times count from it
	state   game.RunState

	peers      []peer    // connections not yet matched to a player
	probeUp    time.Time // when the query port first answered; zero before
	probeCount int       // the last count it gave; -1 before
	stopping   bool      // a stop someone asked for
	shutdown   bool      // the world thread began shutting down (it saves)
	saveBad    bool      // the save system said it couldn't save
	saveQuit   bool      // the server told players it's quitting over saves
}

type peer struct{ handle, account string }

// Watch follows a run. known gives the account a name last played from,
// for a player whose join the log can't tie to their Steam account.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, started: rec.Started, probeCount: -1,
		state: game.RunState{Count: 0, Players: []game.Player{}}}
}

// lineTime is a line's own time: the process's start plus the time the
// line gives, or zero when either isn't known.
func (m *monitor) lineTime(raw string) time.Time {
	p := rePrefix.FindStringSubmatch(raw)
	if p == nil || m.started.IsZero() {
		return time.Time{}
	}
	h, _ := strconv.Atoi(p[2])
	mi, _ := strconv.Atoi(p[3])
	sec, _ := strconv.Atoi(p[4])
	ms, _ := strconv.Atoi(p[5])
	return m.started.Add(time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute +
		time.Duration(sec)*time.Second + time.Duration(ms)*time.Millisecond)
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	raw = strings.TrimPrefix(strings.TrimRight(raw, "\r\n"), "\ufeff")
	text := stripPrefix(raw)
	out := game.Line{Text: text, Time: m.lineTime(raw)}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		out.Hide = true
		return out
	}
	for _, n := range noise {
		if strings.HasPrefix(text, n) {
			out.Hide = true
			break
		}
	}
	if reNoiseRow.MatchString(text) {
		out.Hide = true
	}
	// A line read back after a takeover happened at its own time, which
	// is earlier than now.
	at := now
	if !out.Time.IsZero() && out.Time.Before(now) {
		at = out.Time
	}
	add := func(e game.Event) {
		e.Time = at
		out.Events = append(out.Events, e)
	}

	switch {
	case reVersion.MatchString(text):
		m.state.Version = reVersion.FindStringSubmatch(text)[1]

	case reOnline.MatchString(text), reOnlineDone.MatchString(text):
		out.Events = append(out.Events, m.online(at)...)

	case reOffline.MatchString(text):
		out.Events = append(out.Events, m.leaveAll(at)...)
		m.state.Ready = false

	case reJoin.MatchString(text):
		name := reJoin.FindStringSubmatch(text)[1]
		if m.find(name) >= 0 {
			break // seen again without a leave: the same player
		}
		account := ""
		if len(m.peers) == 1 {
			// One connection waiting for its player: this is them.
			account = m.peers[0].account
			m.peers = nil
		}
		if account == "" {
			account = m.known(name)
		}
		m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: at})
		m.count()
		add(game.Event{Kind: "join", Name: name, Account: account, Text: name + " joined the game."})

	case reLeave.MatchString(text):
		name := reLeave.FindStringSubmatch(text)[1]
		if i := m.find(name); i >= 0 {
			p := m.state.Players[i]
			m.state.Players = slices.Delete(m.state.Players, i, i+1)
			m.count()
			add(game.Event{Kind: "leave", Name: p.Name, Account: p.Account, Text: p.Name + " left the game."})
		}

	case rePeerAdded.MatchString(text):
		g := rePeerAdded.FindStringSubmatch(text)
		m.addPeer(g[1], g[2])

	case rePeerAccepted.MatchString(text):
		m.addPeer("", rePeerAccepted.FindStringSubmatch(text)[1])

	case rePeerRemoved.MatchString(text):
		h := rePeerRemoved.FindStringSubmatch(text)[1]
		m.peers = slices.DeleteFunc(m.peers, func(p peer) bool { return p.handle != "" && p.handle == h })

	case reFull.MatchString(text):
		add(game.Event{Kind: "server_full",
			Text: "A player couldn't join: the server is full. Raise \"Most players at once\" in Enshrouded's settings to let more in."})

	case reShutdown.MatchString(text):
		if why := reShutdown.FindStringSubmatch(text)[1]; slices.Contains(requested, why) && !m.stopping {
			m.stopping = true
			m.state.Stopping = true
			m.state.StoppingAt = at
			add(game.Event{Kind: "stopping"})
		}

	case reSaveQuit.MatchString(text):
		// One line per player: say it once.
		m.saveBad = true
		if !m.saveQuit {
			m.saveQuit = true
			add(game.Event{Kind: "save_failed", Text: "The server is shutting down because it couldn't save the world."})
		}

	case reShutdownState.MatchString(text):
		m.shutdown = true

	case reSaveSystemBad.MatchString(text):
		m.saveBad = true
		add(game.Event{Kind: "save_failed", Text: "The server may not have saved the world's last changes as it stopped."})

	case reSaved.MatchString(text):
		m.state.LastSave = at
		add(game.Event{Kind: "saved"})

	case reSaveFailed.MatchString(text):
		add(game.Event{Kind: "save_failed", Text: "The server couldn't save the world (" + reSaveFailed.FindStringSubmatch(text)[1] + ")."})

	case reLoadFailed.MatchString(text):
		add(game.Event{Kind: "load_failed", Text: "The server couldn't load the world."})

	case reBadSocket.MatchString(text):
		add(game.Event{Kind: "port_in_use", Text: "The server couldn't open its port: another program may be using it."})

	case reNoSteam.MatchString(text):
		add(game.Event{Kind: "no_steam", Text: "The server couldn't connect to Steam, so friends can't find it."})

	case reLostSteam.MatchString(text):
		add(game.Event{Kind: "no_steam", Text: "The server lost its connection to Steam."})

	case reConfigError.MatchString(text):
		add(game.Event{Kind: "bad_config", Text: "The server refused its settings file: " + reConfigError.FindStringSubmatch(text)[1]})
	}
	return out
}

// addPeer notes a connection waiting for its player's login, by Steam
// account; a connection already noted only gains its handle.
func (m *monitor) addPeer(handle, account string) {
	if i := slices.IndexFunc(m.peers, func(p peer) bool { return p.account == account }); i >= 0 {
		if handle != "" {
			m.peers[i].handle = handle
		}
		return
	}
	m.peers = append(m.peers, peer{handle: handle, account: account})
}

// find is the listed player with this name.
func (m *monitor) find(name string) int {
	return slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
}

// count is who's on: the log's names, or the query port's count when it
// says more (a join line the app didn't recognise).
func (m *monitor) count() {
	m.state.Count = max(len(m.state.Players), m.probeCount)
}

// online says the server is ready for players, once.
func (m *monitor) online(at time.Time) []game.Event {
	if m.state.Ready || m.stopping {
		return nil
	}
	m.state.Ready = true
	return []game.Event{{Kind: "online", Time: at, Text: "The server is online and ready for players."}}
}

// leaveAll: whoever is listed has left.
func (m *monitor) leaveAll(at time.Time) []game.Event {
	var evs []game.Event
	for _, p := range m.state.Players {
		evs = append(evs, game.Event{Kind: "leave", Time: at, Name: p.Name, Account: p.Account})
	}
	m.state.Players = []game.Player{}
	m.peers = nil
	m.count()
	return evs
}

// Tick says the server is online once its query port has answered for a
// while with no "online" line in the log (probeFallback).
func (m *monitor) Tick(now time.Time) []game.Event {
	if m.probeUp.IsZero() || m.state.Ready || m.stopping || now.Sub(m.probeUp) < probeFallback {
		return nil
	}
	return m.online(now)
}

// Probed folds in the query port's answer (spec 2.6). Its count is the
// server's own, which a player can't fake: above 0 it holds the computer
// awake, and it wins when it says more players are on than the log named.
// It never names players, so names come from the log, and a 0 takes
// nobody off the list: a server can answer 0 while people play, and a
// game stopped or a computer put to sleep under its players is worse than
// one left on a while longer.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	if !p.Up {
		return nil
	}
	if m.probeUp.IsZero() {
		m.probeUp = now
	}
	if p.Count < 0 {
		return nil
	}
	m.probeCount = p.Count
	m.count()
	if p.Count > 0 {
		return []game.Event{{Kind: "count", Time: now, Count: p.Count, Hold: true}}
	}
	return nil
}

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = append([]game.Player{}, m.state.Players...)
	return st
}

// End: the run is over, so whoever is listed has left. A server that went
// on to shut its world down after a stop request saved the world on the
// way out, unless it said it couldn't (FACTS.md: 1.0 logs no line when it
// has saved), so the end of the process is when it was saved.
func (m *monitor) End(now time.Time) []game.Event {
	evs := m.leaveAll(now)
	m.probeCount = -1
	m.state.Count = 0
	m.state.Ready = false
	if m.stopping && m.shutdown && !m.saveBad {
		m.state.LastSave = now
	}
	return evs
}
