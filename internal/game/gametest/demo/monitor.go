package demo

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// monitor reads Demo Game's log. The manager calls it with its lock held,
// so it only reads its own fields and the known func.
type monitor struct {
	known func(name string) string
	state game.RunState
}

// reStamp is the time every Demo Game line starts with.
var reStamp = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)\] (.*)$`)

func (m *monitor) Line(line string, now time.Time) game.Line {
	l := game.Line{Text: line}
	if s := reStamp.FindStringSubmatch(line); s != nil {
		l.Time, _ = time.ParseInLocation("2006-01-02 15:04:05", s[1], time.Local)
		l.Text = s[2]
	}
	at := l.Time
	if at.IsZero() {
		at = now
	}
	text := l.Text
	switch {
	case strings.HasPrefix(text, "Demo Server version "):
		m.state.Version = strings.TrimPrefix(text, "Demo Server version ")
	case strings.HasPrefix(text, "Server ready: "):
		if !m.state.Ready {
			m.state.Ready = true
			l.Events = []game.Event{{Kind: "online", Time: at, Text: "The server is online."}}
		}
	case strings.HasPrefix(text, "Player joined: "):
		l.Events = m.join(strings.TrimPrefix(text, "Player joined: "), at)
	case strings.HasPrefix(text, "Player left: "):
		l.Events = m.leave(strings.TrimPrefix(text, "Player left: "), at)
	case strings.HasPrefix(text, "Player died: "):
		name := strings.TrimPrefix(text, "Player died: ")
		l.Events = []game.Event{{Kind: "death", Time: at, Name: name, Account: m.known(name), Text: name + " died."}}
	case strings.HasPrefix(text, "Turned away "):
		name, why, _ := strings.Cut(strings.TrimPrefix(text, "Turned away "), ": ")
		l.Events = []game.Event{{Kind: "turned_away", Time: at, Name: name, Text: name + " was turned away: " + why + "."}}
	case text == "World saved":
		m.state.LastSave = at
		l.Events = []game.Event{{Kind: "saved", Time: at}}
	case text == "Stopping the server":
		if !m.state.Stopping {
			m.state.Stopping, m.state.StoppingAt = true, at
			l.Events = []game.Event{{Kind: "stopping", Time: at}}
		}
	case strings.HasPrefix(text, "Players online: "):
		num, _, _ := strings.Cut(strings.TrimPrefix(text, "Players online: "), " ")
		if n, err := strconv.Atoi(num); err == nil {
			l.Events = []game.Event{{Kind: "count", Time: at, Count: n}}
		}
	case strings.HasPrefix(text, "fakeserver: "):
		l.Hide = true // the stand-in's own notes, not the game's
	}
	return l
}

func (m *monitor) join(name string, at time.Time) []game.Event {
	if name == "" || slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name }) {
		return nil
	}
	acct := m.known(name)
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: acct, Since: at})
	m.state.Count = len(m.state.Players)
	return []game.Event{{Kind: "join", Time: at, Name: name, Account: acct, Text: name + " joined."}}
}

func (m *monitor) leave(name string, at time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	m.state.Count = len(m.state.Players)
	return []game.Event{{Kind: "leave", Time: at, Name: name, Account: p.Account, Text: name + " left."}}
}

// Tick has nothing to do: Demo Game always says when it's ready.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed folds in the API's answer, which is authoritative: names it lists
// that the log missed join, names it doesn't list leave.
func (m *monitor) Probed(p game.ProbeResult, now time.Time) []game.Event {
	var evs []game.Event
	if p.Up && !m.state.Ready {
		m.state.Ready = true
		evs = append(evs, game.Event{Kind: "online", Time: now, Text: "The server is online."})
	}
	if p.Version != "" {
		m.state.Version = p.Version
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
			// The server's own count can't be faked by a player: it
			// holds the computer awake.
			evs = append(evs, game.Event{Kind: "count", Time: now, Count: p.Count, Hold: true})
		}
	}
	return evs
}

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = slices.Clone(m.state.Players)
	return st
}

// End: whoever is listed leaves.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	m.state.Ready = false
	return evs
}
