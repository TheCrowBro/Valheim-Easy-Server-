package valheim

import (
	"log"
	"strconv"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/logparse"
)

// Monitor follows one run of the Valheim server through its log. It is
// the log reader the manager always used (internal/logparse's Tracker),
// which it embeds, so what the app makes of a Valheim log is unchanged.
//
// Like every Monitor, its methods run with the manager's lock held: none
// of them blocks or calls the manager.
type Monitor struct {
	logparse.Tracker

	log     *log.Logger
	readyAt time.Time // this computer's time when it became ready

	// How lines are timed when the app reads a log back (see Replaying
	// and FromLog); by this computer's clock otherwise.
	replaying bool
	fromLog   bool
	logTime   time.Time // FromLog: the newest time a line gave
}

// NewMonitor makes the Monitor for a run with record rec. known gives the
// account a character's name last played from, which tells apart friends
// who connect at the same time.
func NewMonitor(rec game.Record, known func(name string) string) *Monitor {
	m := &Monitor{}
	m.NoJoinCode = NoCrossplay(rec)
	m.KnownPlatformID = known
	return m
}

// Watch starts following a run.
func (s *Server) Watch(rec game.Record, known func(name string) string) game.Monitor {
	m := NewMonitor(rec, known)
	m.log = s.log
	return m
}

// Replaying is turned on while the app catches up with the log of a
// server it took over: a line then counts at its own time when that is
// earlier than now, so the player counts in a log read back all at once
// are timed by its own lines. Off again once the app has caught up.
func (m *Monitor) Replaying(on bool) { m.replaying = on }

// FromLog makes the Monitor read a finished run's log for the records:
// each line counts at the newest time the log has given so far, and
// lines before the first one with a time are only read, not counted.
func (m *Monitor) FromLog() { m.fromLog = true }

// Line reads one line of the server's log.
func (m *Monitor) Line(line string, now time.Time) game.Line {
	if logparse.IsNoise(line) {
		return game.Line{Hide: true}
	}
	ev := logparse.Parse(line)
	out := game.Line{Text: ev.Text, Time: ev.Time} // without Valheim's own timestamp; Time carries it
	switch {
	case m.fromLog:
		if !ev.Time.IsZero() {
			m.logTime = ev.Time
		}
		if m.logTime.IsZero() {
			return out
		}
		now = m.logTime
	case m.replaying:
		if !ev.Time.IsZero() && ev.Time.Before(now) {
			now = ev.Time
		}
	}
	out.Events = m.apply(ev, now)
	return out
}

// apply updates the state with one parsed line and says what happened.
func (m *Monitor) apply(ev logparse.Event, now time.Time) []game.Event {
	wasReady := m.Ready
	var out []game.Event
	for _, a := range m.Apply(ev, now) {
		out = append(out, event(a))
	}
	if m.Ready && !wasReady {
		m.readyAt = time.Now()
	}
	// The server's own count of who's connected. Lines a player makes up
	// can wrongly empty the player list, but the server keeps logging its
	// true count about every 10 minutes, so a count above 0 holds the
	// computer awake while friends play. A made-up count above 0 can only
	// keep it awake.
	if ev.Count > 0 && (ev.Kind == logparse.PeerCount || (ev.Kind == logparse.JoinCode && ev.Addr != "")) {
		out = append(out, game.Event{Kind: "count", Time: now, Count: ev.Count, Hold: true})
	}
	return out
}

func event(a logparse.Activity) game.Event {
	return game.Event{Kind: a.Kind, Time: a.Time, Name: a.Name, Account: a.PlatformID, Text: a.Text}
}

// Tick counts a server as ready when it logged in to Steam long ago but
// never said it opened, so a Valheim update that words that line
// differently can't leave it "starting" forever.
func (m *Monitor) Tick(now time.Time) []game.Event {
	if m.replaying || m.fromLog || !m.Overdue(now, ReadyWait) {
		return nil
	}
	if m.log != nil {
		m.log.Printf("the server logged in to Steam %v ago but never said it opened; counting it as online", ReadyWait)
	}
	return m.apply(logparse.Event{Kind: logparse.ServerReady, Count: -1}, now)
}

// Probed: Valheim has no probe; its log says who is on.
func (m *Monitor) Probed(game.ProbeResult, time.Time) []game.Event { return nil }

// State is what the log has said so far. Extra holds Valheim's own:
// "publicAddr" (from the crossplay session line), "crossplayFailed" and
// "codeLate" ("true": no join code came within CodeWait of being ready).
func (m *Monitor) State() game.RunState {
	st := game.RunState{Ready: m.Ready, Count: m.Count(), Version: m.Version, Code: m.JoinCode,
		LastSave: m.LastSave, Stopping: m.Stopping, StoppingAt: m.StoppingAt, Players: []game.Player{}}
	for _, p := range m.Players() {
		st.Players = append(st.Players, game.Player{Name: p.Name, Account: p.PlatformID, Since: p.Since})
	}
	st.Extra = map[string]string{}
	if m.PublicAddr != "" {
		st.Extra["publicAddr"] = m.PublicAddr
	}
	if m.CrossplayFailed {
		st.Extra["crossplayFailed"] = "true"
	}
	if m.Ready && !m.NoJoinCode && m.JoinCode == "" && !m.readyAt.IsZero() && time.Since(m.readyAt) > CodeWait {
		st.Extra["codeLate"] = strconv.FormatBool(true)
	}
	return st
}

// End: the run ended, so whoever is still listed leaves.
func (m *Monitor) End(now time.Time) []game.Event {
	var out []game.Event
	for _, a := range m.Reset(now) {
		out = append(out, event(a))
	}
	return out
}
