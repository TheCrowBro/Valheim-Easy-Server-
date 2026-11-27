package terraria

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// The lines below are the real 1.4.6.1 server's, in English (the Plan sets
// language=en-US, so the console never speaks the computer's language).
// testdata/real holds the logs they were read from; FACTS.md says how
// each was made.
var (
	// "Terraria Server v1.4.6.1": the banner, printed at start and by "version".
	reVersion = regexp.MustCompile(`^Terraria Server v(\S+)$`)
	// "Listening on port 7777", just before the server is ready.
	reListening = regexp.MustCompile(`^Listening on port (\d+)$`)
	// "Alex has joined." / "Big Bob has left.": names may hold spaces.
	reJoined = regexp.MustCompile(`^(.+) has joined\.$`)
	reLeft   = regexp.MustCompile(`^(.+) has left\.$`)
	// "127.0.0.1:58404 was booted: Incorrect password": a friend turned
	// away before joining, so the server knows only their address.
	reBooted = regexp.MustCompile(`^(\S+) was booted: (.+)$`)
	// Progress lines, printed many times a second while a world is made,
	// loaded or saved: "Saving world data: 19%", "Settling liquids 42%",
	// "0.4% - Adding sand - 33.0%".
	reProgress = regexp.MustCompile(`^(Resetting game objects|Loading world data:|Settling liquids|Saving world data:|Validating world save:) ?\d+%$`)
	reGenStep  = regexp.MustCompile(`^\d+(?:\.\d+)?% - (.+) - \d+(?:\.\d+)?%$`)
)

// cleanLine is a console line as it is shown: without the byte order marks
// the server writes before its first line, and without the ": " prompt it
// writes while it waits for a command, which the next line then follows
// (": Server started").
func cleanLine(line string) string {
	t := strings.ReplaceAll(line, "\uFEFF", "")
	t = strings.TrimRight(t, " \t\r")
	for strings.HasPrefix(t, ": ") {
		t = t[2:]
	}
	if t == ":" {
		return ""
	}
	return t
}

// monitor follows one run of the Terraria server through its console
// output. The manager calls it with its lock held, so it only reads its
// own fields and the known func.
type monitor struct {
	known func(name string) string
	state game.RunState

	saving   bool   // "Saving world data:" was seen and its save isn't counted yet
	phase    string // the progress phase last shown, so only its first line is
	exitSave bool   // "Saving before exit..." was seen: the save on the way out
}

func newMonitor(known func(string) string) *monitor {
	if known == nil {
		known = func(string) string { return "" }
	}
	return &monitor{known: known, state: game.RunState{Count: 0, Players: []game.Player{}}}
}

// Watch starts following a run.
func (s *server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	return newMonitor(known)
}

// Line reads one line of the console. Terraria's lines carry no time of
// their own, so events are timed now.
func (m *monitor) Line(line string, now time.Time) game.Line {
	text := cleanLine(line)
	l := game.Line{Text: text}
	if text == "" {
		l.Hide = true
		return l
	}
	if p := progressPhase(text); p != "" {
		// Show the first line of each step, so the Log page reads as a
		// list of what the server did rather than thousands of
		// percentages.
		l.Hide = p == m.phase
		m.phase = p
		if p == "Saving world data:" {
			m.saving = true
		}
		if p == "Validating world save:" {
			// The world file is written before it is checked, so the save
			// is done when the check starts.
			l.Events = m.saved(now)
		}
		return l
	}
	m.phase = ""

	switch {
	case reVersion.MatchString(text):
		m.state.Version = reVersion.FindStringSubmatch(text)[1]
	case text == "Server started":
		if !m.state.Ready {
			m.state.Ready = true
			l.Events = []game.Event{{Kind: "online", Time: now, Text: "The server is online and ready for players."}}
		}
	case text == "Backing up world file":
		// The last step of a save; the check before it may not have
		// printed anything.
		l.Events = m.saved(now)
	case text == "Saving before exit...":
		m.exitSave = true
		if !m.state.Stopping {
			m.state.Stopping, m.state.StoppingAt = true, now
			l.Events = []game.Event{{Kind: "stopping", Time: now}}
		}
	case strings.HasPrefix(text, "<"):
		// Chat: "<Alex> hello". A player can type anything, even "Bob has
		// left.", so chat is never read as the server's own news.
	case reJoined.MatchString(text):
		l.Events = m.join(reJoined.FindStringSubmatch(text)[1], now)
	case reLeft.MatchString(text):
		l.Events = m.leave(reLeft.FindStringSubmatch(text)[1], now)
	case reBooted.MatchString(text):
		l.Events = m.booted(reBooted.FindStringSubmatch(text)[2], now)
	case strings.HasPrefix(text, "Load failed!") || strings.HasPrefix(text, "Unable to load world"):
		l.Events = []game.Event{{Kind: "load_failed", Time: now,
			Text: "The world file couldn't be loaded. It may be damaged: restore a backup from the Worlds page."}}
	}
	return l
}

// progressPhase names the step a progress line belongs to, or "".
func progressPhase(text string) string {
	if m := reProgress.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if m := reGenStep.FindStringSubmatch(text); m != nil {
		return "gen:" + m[1]
	}
	return ""
}

// saved counts one save once.
func (m *monitor) saved(now time.Time) []game.Event {
	if !m.saving {
		return nil
	}
	m.saving = false
	m.state.LastSave = now
	return []game.Event{{Kind: "saved", Time: now, Text: "World saved."}}
}

// booted turns a friend turned away into a warning in the feed. The server
// gives only their address, never a name.
func (m *monitor) booted(why string, now time.Time) []game.Event {
	switch {
	case strings.HasPrefix(why, "Incorrect password"):
		return []game.Event{{Kind: "wrong_password", Time: now, Text: "Someone tried to join with the wrong password."}}
	case strings.HasPrefix(why, "You are not using the same version as this server"):
		text := "Someone tried to join with a different version of Terraria. Everyone needs the same version as the server."
		if v := m.state.Version; v != "" {
			text = fmt.Sprintf("Someone tried to join with a different version of Terraria. Everyone needs Terraria %s, the same version as the server.", v)
		}
		return []game.Event{{Kind: "bad_version", Time: now, Text: text}}
	case strings.HasPrefix(why, "This server is full"):
		return []game.Event{{Kind: "server_full", Time: now,
			Text: "Someone couldn't join because the server is full. You can raise \"Most players at once\" in Settings."}}
	}
	return nil
}

func (m *monitor) join(name string, now time.Time) []game.Event {
	if name == "" || slices.ContainsFunc(m.state.Players, func(p game.Player) bool { return p.Name == name }) {
		return nil
	}
	acct := m.known(name)
	m.state.Players = append(m.state.Players, game.Player{Name: name, Account: acct, Since: now})
	m.state.Count = len(m.state.Players)
	return []game.Event{{Kind: "join", Time: now, Name: name, Account: acct, Text: name + " joined the game."}}
}

func (m *monitor) leave(name string, now time.Time) []game.Event {
	i := slices.IndexFunc(m.state.Players, func(p game.Player) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	p := m.state.Players[i]
	m.state.Players = slices.Delete(m.state.Players, i, i+1)
	m.state.Count = len(m.state.Players)
	text := p.Name + " left the game."
	if d := now.Sub(p.Since); d >= time.Minute {
		text = fmt.Sprintf("%s left the game after %s.", p.Name, humanDuration(d))
	}
	return []game.Event{{Kind: "leave", Time: now, Name: name, Account: p.Account, Text: text}}
}

// Tick has nothing to do: the server always says when it is ready, and
// when it can't start it ends.
func (m *monitor) Tick(now time.Time) []game.Event { return nil }

// Probed: Terraria has no probe.
func (m *monitor) Probed(game.ProbeResult, time.Time) []game.Event { return nil }

func (m *monitor) State() game.RunState {
	st := m.state
	st.Players = slices.Clone(m.state.Players)
	if st.Players == nil {
		st.Players = []game.Player{}
	}
	return st
}

// End: the run is over, so whoever is listed leaves (the server prints no
// leave lines when it exits with players on). A run that ended after
// "Saving before exit..." saved its world on the way out: the server
// prints nothing after that line, so its end is when the save is known to
// be done.
func (m *monitor) End(now time.Time) []game.Event {
	var evs []game.Event
	for _, p := range slices.Clone(m.state.Players) {
		evs = append(evs, m.leave(p.Name, now)...)
	}
	if m.exitSave {
		m.exitSave = false
		m.state.LastSave = now
	}
	m.state.Ready = false
	return evs
}

// humanDuration says how long someone played, in plain words.
func humanDuration(d time.Duration) string {
	mins := int(d.Round(time.Minute) / time.Minute)
	h, mm := mins/60, mins%60
	plural := func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	switch {
	case h == 0:
		return plural(mm, "minute")
	case mm == 0:
		return plural(h, "hour")
	}
	return plural(h, "hour") + " " + plural(mm, "minute")
}
