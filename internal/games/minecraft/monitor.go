package minecraft

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The log, as Minecraft 26.3 writes it (FACTS.md has a real excerpt of
// every line used here). Each line is
//
//	[19:40:16] [Server thread/INFO]: System chat: Alex joined the game
//
// from any thread ("Server thread", "User Authenticator #1", "RCON Client
// /127.0.0.1 #2", whose name has a slash in it). Messages the server
// broadcasts to every player (joins, leaves, deaths, advancements, command
// echoes) start with "System chat: " in 26.3; older versions wrote them
// without it, so the prefix is optional here. Player chat never has it and
// never starts with a bare name ("<Alex> …", "[Not Secure] <Alex> …"),
// so a player can't fake a join or a death by typing one.
var (
	reLine = regexp.MustCompile(`^\[(\d\d):(\d\d):(\d\d)\] \[(.+?)/(INFO|WARN|ERROR|FATAL|DEBUG|TRACE)\]: (.*)$`)

	// A Minecraft account name: letters, digits and _ (FACTS.md).
	name = `([A-Za-z0-9_]{1,16})`

	reVersion    = regexp.MustCompile(`^Starting minecraft server version (.+)$`)
	reDone       = regexp.MustCompile(`^Done \([0-9.,]+s\)! For help, type "help"`)
	rePlayerUUID = regexp.MustCompile(`^UUID of player ` + name + ` is ([0-9a-fA-F-]{32,36})$`)
	reLoggedIn   = regexp.MustCompile(`^` + name + `\[/(.*):(\d+)\] logged in with entity id \d+ at \(`)
	reJoined     = regexp.MustCompile(`^` + name + ` joined the game$`)
	reRenamed    = regexp.MustCompile(`^` + name + ` \(formerly known as ` + name + `\) joined the game$`)
	reLeft       = regexp.MustCompile(`^` + name + ` left the game$`)
	reDenied     = regexp.MustCompile(`^` + name + ` \(/[^)]*\) lost connection: You are not white-listed on this server!$`)
	reDenying    = regexp.MustCompile(`^Disconnecting ` + name + ` \(/[^)]*\): You are not white-listed on this server!$`)
	reSaving     = regexp.MustCompile(`^Saving chunks for level '.*'/`)
	reRCONNote   = regexp.MustCompile(`^Thread RCON Client /\S+ (started|shutting down)$`)
)

const systemChat = "System chat: "

// monitor follows one run's log. The manager calls it with its lock held,
// so it only reads its own fields and the known func, and anything slow
// (turning the allow list on over RCON) goes to a goroutine.
type monitor struct {
	rec      game.Record
	known    func(name string) string
	turnOn   func(rec game.Record, playing []string) // the allow list, over RCON
	state    game.RunState
	accounts map[string]string // name -> account id, from "UUID of player" lines
	allowOn  bool              // the allow list is on now
	mode     string            // the owner's setting this run started with: auto, on or off
}

// Watch starts following a run. rec is the run record (also after a
// takeover); known gives the account a name last played from.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	m := &monitor{rec: rec, known: known, turnOn: s.turnOnAllowList, accounts: map[string]string{}}
	if known == nil {
		m.known = func(string) string { return "" }
	}
	// What the plan recorded; a record without it (a run the app didn't
	// plan) leaves the list alone.
	var allow string
	json.Unmarshal(rec.Extra[extraAllowList], &allow)
	json.Unmarshal(rec.Extra[extraAllowMode], &m.mode)
	m.allowOn = allow == allowOn
	m.state = game.RunState{Count: 0, Extra: map[string]string{extraAllowList: allowOff}}
	if m.allowOn {
		m.state.Extra[extraAllowList] = allowOn
	}
	return m
}

// lineTime is the line's own time: its clock time on today's date, or
// yesterday's when that would be in the future (a log read back after
// midnight).
func lineTime(h, mi, sec string, now time.Time) time.Time {
	atoi := func(s string) int { return int(s[0]-'0')*10 + int(s[1]-'0') }
	t := time.Date(now.Year(), now.Month(), now.Day(), atoi(h), atoi(mi), atoi(sec), 0, now.Location())
	if t.After(now.Add(time.Minute)) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

func (m *monitor) Line(line string, now time.Time) game.Line {
	l := game.Line{Text: line}
	parts := reLine.FindStringSubmatch(line)
	if parts == nil {
		// Lines without Minecraft's stamp: the jar unpacking itself, the
		// Java runtime's own warnings, and the lines of a stack trace.
		t := strings.TrimSpace(line)
		l.Hide = t == "" || strings.HasPrefix(line, "Unpacking ") || line == "Starting net.minecraft.server.Main" ||
			strings.HasPrefix(line, "WARNING: ") || strings.HasPrefix(line, "Picked up ") ||
			strings.HasPrefix(t, "at ") || (strings.HasPrefix(t, "... ") && strings.HasSuffix(t, " more"))
		return l
	}
	l.Time = lineTime(parts[1], parts[2], parts[3], now)
	thread, level, msg := parts[4], parts[5], parts[6]
	at := l.Time
	text, broadcast := strings.CutPrefix(msg, systemChat)
	l.Text = text
	switch level {
	case "WARN":
		l.Text = "Warning: " + text
	case "ERROR", "FATAL":
		l.Text = "Error: " + text
	}

	switch {
	case reRCONNote.MatchString(msg), strings.HasPrefix(thread, "RCON Listener") && strings.HasPrefix(msg, "Thread RCON Client"):
		l.Hide = true // the app's own RCON connections, every 30 s
	case msg == "handleDisconnection() called twice":
		l.Hide = true // a harmless note after a refused login
	case reDenying.MatchString(msg):
		l.Hide = true // the "lost connection" line just after says it
	case reVersion.MatchString(msg):
		m.state.Version = reVersion.FindStringSubmatch(msg)[1]
	case reDone.MatchString(msg):
		l.Events = m.online(at)
	case rePlayerUUID.MatchString(msg):
		p := rePlayerUUID.FindStringSubmatch(msg)
		if id, ok := dashUUID(p[2]); ok {
			m.accounts[p[1]] = id
		}
	case reLoggedIn.MatchString(msg):
		p := reLoggedIn.FindStringSubmatch(msg)
		l.Events = m.loggedIn(p[1], p[2], at)
	case reRenamed.MatchString(text):
		p := reRenamed.FindStringSubmatch(text)
		l.Events = m.join(p[1], at, p[1]+" (formerly "+p[2]+") joined.")
	case reJoined.MatchString(text):
		p := reJoined.FindStringSubmatch(text)
		l.Events = m.join(p[1], at, p[1]+" joined.")
	case reLeft.MatchString(text):
		l.Events = m.leave(reLeft.FindStringSubmatch(text)[1], at)
	case reDenied.MatchString(msg):
		who := reDenied.FindStringSubmatch(msg)[1]
		l.Events = []game.Event{{Kind: "turned_away", Time: at, Name: who, Account: m.account(who),
			Text: who + " was turned away: they're not on the allow list."}}
	case msg == "Stopping server":
		if !m.state.Stopping {
			m.state.Stopping, m.state.StoppingAt = true, at
			l.Events = []game.Event{{Kind: "stopping", Time: at}}
		}
	case reSaving.MatchString(msg), saved(text):
		m.state.LastSave = at
		l.Events = []game.Event{{Kind: "saved", Time: at}}
	case strings.HasPrefix(msg, "Unable to initialise RCON on "):
		l.Events = []game.Event{{Kind: "rcon_failed", Time: at,
			Text: "Minecraft couldn't open its RCON port (another program has it), so the app can't count players or stop it gently. Stop it and pick new ports on its page."}}
	case broadcast && thread == "Server thread":
		// Deaths need the "System chat: " prefix, unlike joins: a few
		// death messages don't start with the player ("Zombie showed Alex
		// that…"), and without the prefix a chat line could end that way.
		if who := deathOf(text, m.isOn); who != "" {
			l.Events = []game.Event{{Kind: "death", Time: at, Name: who, Account: m.account(who), Text: text}}
		}
	}
	return l
}

// saved says whether a broadcast tells of a save: "save-all" answers
// "Saved the game", and through RCON the server echoes it to admins as
// "[Rcon: Saved the game]".
func saved(text string) bool {
	return text == "Saved the game" || text == "[Rcon: Saved the game]"
}

func (m *monitor) online(at time.Time) []game.Event {
	if m.state.Ready {
		return nil
	}
	m.state.Ready = true
	return []game.Event{{Kind: "online", Time: at, Text: "The server is online."}}
}

func (m *monitor) isOn(name string) bool {
	return slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
}

// account is a player's account id: from this run's login lines, or where
// the name last played from.
func (m *monitor) account(name string) string {
	if a := m.accounts[name]; a != "" {
		return a
	}
	return m.known(name)
}

// loggedIn notes where a player came from. Someone coming in from the
// internet while the allow list is off means the internet can reach this
// server, so the list goes on now (the owner's decision).
func (m *monitor) loggedIn(who, ip string, at time.Time) []game.Event {
	// Only "auto" turns on by itself, and only once.
	if m.allowOn || m.mode != allowAuto || !publicAddr(ip) {
		return nil
	}
	m.allowOn = true
	m.state.Extra[extraAllowList] = allowOn
	playing := []string{who}
	for _, p := range m.state.Players {
		if p.Name != who {
			playing = append(playing, p.Name)
		}
	}
	if m.turnOn != nil {
		go m.turnOn(m.rec, playing)
	}
	return []game.Event{{Kind: "allow_list_on", Time: at, Name: who,
		Text: "Minecraft's allow list is now on: " + who + " came in from the internet. Everyone playing now was added to it; add other friends on the Players tab."}}
}

func (m *monitor) join(who string, at time.Time, text string) []game.Event {
	if m.isOn(who) {
		return nil
	}
	acct := m.account(who)
	m.state.Players = append(m.state.Players, game.Player{Name: who, Account: acct, Since: at})
	m.state.Count = len(m.state.Players)
	return []game.Event{{Kind: "join", Time: at, Name: who, Account: acct, Text: text}}
}

func (m *monitor) leave(who string, at time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == who })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	m.state.Count = len(m.state.Players)
	return []game.Event{{Kind: "leave", Time: at, Name: who, Account: p.Account, Text: who + " left."}}
}

// Tick has nothing to do: the server always writes "Done", and the probe
// is the fallback when a future version words it differently.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed folds in RCON's "list", which is authoritative: names it lists
// that the log missed join, names it doesn't list leave. RCON only opens
// once the server is ready, so an answer also means online.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	var evs []game.Event
	if p.Up {
		evs = append(evs, m.online(now)...)
	}
	if p.Version != "" {
		m.state.Version = p.Version
	}
	if p.Names != nil {
		for _, n := range p.Names {
			evs = append(evs, m.join(n, now, n+" joined.")...)
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
			// The server's own count can't be faked by a player.
			evs = append(evs, game.Event{Kind: "count", Time: now, Count: p.Count, Hold: true})
		}
	}
	return evs
}

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = slices.Clone(m.state.Players)
	st.Extra = map[string]string{}
	for k, v := range m.state.Extra {
		st.Extra[k] = v
	}
	return st
}

// End: the run ended, so whoever is listed leaves (a stop doesn't always
// log each player leaving).
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	m.state.Ready = false
	return evs
}
