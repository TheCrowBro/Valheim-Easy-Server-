package palworld

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's console lines the Monitor reads (FACTS.md has each with its
// source). The game's own lines start "[LOG] ", most with the time before
// it, "[2024-04-19 19:26:37] [LOG] …":
//
//	Game version is v1.0.5.102999
//	REST API started on port 8212
//	Running Palworld dedicated server on :8211
//	[…] [LOG] Alex 203.0.113.9 connected the server. (User id: steam_…)
//	[…] [LOG] Alex joined the server. (User id: steam_…, Player id: <32 hex>)
//	[…] [LOG] Alex left the server. (User id: steam_…)
//	[…] [LOG] REST accessed endpoint /v1/api/save OK
//
// Who is on comes first from the REST API, which the probe asks every 30
// seconds: its answer wins (spec 2.6). The join and leave lines only make
// the change show sooner. The Windows server may hold its output back and
// write it in bursts, so a join line can arrive after the probe already
// saw that player leave; a line older than the latest probe that disagrees
// with it is not acted on.
var (
	reStamp     = regexp.MustCompile(`^\[(\d{4})-(\d{1,2})-(\d{1,2}) (\d{1,2}):(\d{2}):(\d{2})\]\s*`)
	reVersion   = regexp.MustCompile(`^Game version is (v?\d[\w.\-]*)`)
	reRunning   = regexp.MustCompile(`^Running Palworld dedicated server on (\S*?):(\d+)`)
	reJoined    = regexp.MustCompile(`^\[LOG\] (.+) joined the server\. \(User id: ([^,)]*)(?:, Player id: ([0-9A-Fa-f]+))?.*\)$`)
	reLeft      = regexp.MustCompile(`^\[LOG\] (.+) left the server\. \(User id: ([^,)]*).*\)$`)
	reConnected = regexp.MustCompile(`^\[LOG\] (.+) (\S+) connected the server\. \(User id: ([^,)]*).*\)$`)
	reREST      = regexp.MustCompile(`^\[LOG\] REST accessed endpoint (\S+) (\S.*)$`)
)

// staleGrace is how far a line's own time may be behind the latest probe
// and still count: the server's clock and the app's are the same, but a
// second can pass between the line and the probe's answer.
const staleGrace = 2 * time.Second

type monitor struct {
	known    func(name string) string
	state    game.RunState
	accounts map[string]string // name → user id, from join lines
	probed   time.Time         // when the latest probe answered
	probeOn  map[string]bool   // who it listed
}

// Watch follows one run. The run record isn't needed: everything the
// Monitor knows comes from the log and the probe.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, accounts: map[string]string{}, state: game.RunState{Count: -1, Extra: map[string]string{}}}
}

// splitLine takes the game's own time off a line.
func splitLine(line string) (string, time.Time) {
	line = strings.TrimRight(line, "\r")
	m := reStamp.FindStringSubmatch(line)
	if m == nil {
		return strings.TrimSpace(line), time.Time{}
	}
	n := make([]int, 6)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, time.Local)
	return strings.TrimSpace(line[len(m[0]):]), t
}

// noise are lines that tell the owner nothing: the Steam library's start
// up chatter, which every good start prints too (FACTS.md).
func noise(text string) bool {
	return strings.HasPrefix(text, "[S_API") || strings.HasPrefix(text, "Setting breakpad minidump AppID") ||
		strings.HasPrefix(text, "dlopen failed") || strings.Contains(text, "steamclient.so")
}

func (m *monitor) Line(raw string, now time.Time) game.Line {
	text, at := splitLine(raw)
	l := game.Line{Text: text, Time: at}
	when := at
	if when.IsZero() {
		when = now
	}
	switch {
	case text == "":
		l.Hide = true
	case noise(text):
		l.Hide = true
	case reVersion.MatchString(text):
		m.state.Version = reVersion.FindStringSubmatch(text)[1]
	case reRunning.MatchString(text):
		l.Events = append(l.Events, m.online(when, "log")...)
	case reJoined.MatchString(text):
		g := reJoined.FindStringSubmatch(text)
		name, account := strings.TrimSpace(g[1]), strings.TrimSpace(g[2])
		if account != "" {
			m.accounts[name] = account
		}
		if !m.contradicted(at, name, false) {
			l.Events = append(l.Events, m.join(name, when)...)
		}
	case reLeft.MatchString(text):
		name := strings.TrimSpace(reLeft.FindStringSubmatch(text)[1])
		if !m.contradicted(at, name, true) {
			l.Events = append(l.Events, m.leave(name, when)...)
		}
	case reConnected.MatchString(text):
		// The player is still loading in: the joined line follows.
	case reREST.MatchString(text):
		g := reREST.FindStringSubmatch(text)
		endpoint, result := g[1], g[2]
		ok := strings.EqualFold(strings.TrimSpace(result), "OK")
		switch {
		case ok && strings.HasSuffix(endpoint, "/save"):
			m.state.LastSave = when
			l.Events = append(l.Events, game.Event{Kind: "saved", Time: when})
		case ok && (strings.HasSuffix(endpoint, "/shutdown") || strings.HasSuffix(endpoint, "/stop")):
			l.Events = append(l.Events, m.stopping(when)...)
		case ok:
			// The app's own probe asks info and players every 30 seconds;
			// those lines would bury everything else on the Log page.
			l.Hide = true
		}
	}
	return l
}

// contradicted says whether a join (or leave) line is older than the
// latest probe, which saw the opposite.
func (m *monitor) contradicted(at time.Time, name string, leaving bool) bool {
	if at.IsZero() || m.probed.IsZero() || !at.Add(staleGrace).Before(m.probed) {
		return false
	}
	return m.probeOn[name] == leaving
}

func (m *monitor) online(now time.Time, by string) []game.Event {
	if m.state.Ready {
		return nil
	}
	m.state.Ready = true
	m.state.Extra["readyBy"] = by
	return []game.Event{{Kind: "online", Time: now, Text: "The Palworld server is online."}}
}

func (m *monitor) stopping(now time.Time) []game.Event {
	if m.state.Stopping {
		return nil
	}
	m.state.Stopping, m.state.StoppingAt = true, now
	return []game.Event{{Kind: "stopping", Time: now}}
}

func (m *monitor) listed(name string) bool {
	return slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
}

func (m *monitor) join(name string, now time.Time) []game.Event {
	if name == "" || m.listed(name) {
		return nil
	}
	account := m.accounts[name]
	if account == "" {
		account = m.known(name)
	}
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: account, Since: now})
	return []game.Event{{Kind: "join", Time: now, Name: name, Account: account}}
}

func (m *monitor) leave(name string, now time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	return []game.Event{{Kind: "leave", Time: now, Name: name, Account: p.Account}}
}

func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed folds in the REST API's answer, which is authoritative: an
// answer means the server is up, names it lists that the log missed join,
// names it doesn't list leave, and its count is who is on (counting
// players still loading in, who have no name yet).
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	var evs []game.Event
	if p.Up {
		evs = append(evs, m.online(now, "rest")...)
	}
	if p.Version != "" {
		m.state.Version = p.Version
	}
	if p.Names != nil {
		m.probed = now
		m.probeOn = map[string]bool{}
		for _, n := range p.Names {
			m.probeOn[n] = true
			evs = append(evs, m.join(n, now)...)
		}
		for _, pl := range slices.Clone(m.state.Players) {
			if !m.probeOn[pl.Name] {
				evs = append(evs, m.leave(pl.Name, now)...)
			}
		}
	}
	if p.Count >= 0 {
		m.state.Count = p.Count
		if p.Count > 0 {
			// The server's own count can't be faked by a player: it
			// holds the computer awake (spec 2.6).
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

// End: the run is over, so whoever is listed leaves.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	m.state.Ready = false
	m.state.Count = -1
	return evs
}
