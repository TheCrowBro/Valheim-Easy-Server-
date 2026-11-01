package logparse

import (
	"fmt"
	"time"
)

// Player is someone currently in the world.
type Player struct {
	Name       string    `json:"name"`
	ID         string    `json:"id,omitempty"`
	PlatformID string    `json:"platformId,omitempty"`
	Since      time.Time `json:"since"`
}

// Activity is something worth showing in the activity feed.
type Activity struct {
	Kind       string // join, leave, death, online, wrong_password, bad_version, saved, load_failed, bad_password_config
	Time       time.Time
	Name       string
	PlatformID string
	Text       string
}

type pending struct {
	id, platformID string
}

// Tracker turns a stream of log events into server and player state.
// Valheim doesn't log a single "X joined" line, so joins are pieced
// together from the connection lines and the character line that follows.
type Tracker struct {
	pending    []pending
	players    []*Player
	JoinCode   string
	PublicAddr string
	Version    string
	Ready      bool      // the world is loaded and the server is open for players
	SteamAt    time.Time // when it logged in to Steam, often before the world has loaded
	LastSave   time.Time
	Stopping   bool
	StoppingAt time.Time // when the server said it was shutting down
	lowStreak  int

	// NoJoinCode is set for a server started without crossplay, which has
	// no join code: a line claiming one was made up by a player.
	NoJoinCode bool
	// CrossplayFailed is set when crossplay couldn't start, so no join code
	// will come.
	CrossplayFailed bool
	session         string // the server's session name, from its first join code line
}

// Overdue reports a server that logged in to Steam at least wait ago but
// never said it opened, as if a Valheim update had worded that differently.
// The caller can then count it as ready after all.
func (t *Tracker) Overdue(now time.Time, wait time.Duration) bool {
	return !t.Ready && !t.SteamAt.IsZero() && now.Sub(t.SteamAt) >= wait
}

// Players returns a copy of everyone online, in join order.
func (t *Tracker) Players() []Player {
	out := make([]Player, 0, len(t.players))
	for _, p := range t.players {
		out = append(out, *p)
	}
	return out
}

// Count is the number of players online.
func (t *Tracker) Count() int { return len(t.players) }

// Apply updates the state with one event and returns what to show in the
// activity feed. now is used when the line had no timestamp.
func (t *Tracker) Apply(ev Event, now time.Time) []Activity {
	at := ev.Time
	if at.IsZero() {
		at = now
	}
	act := func(kind, name, pid, text string) Activity {
		return Activity{Kind: kind, Time: at, Name: name, PlatformID: pid, Text: text}
	}

	ready := func() []Activity {
		if t.Ready {
			return nil
		}
		t.Ready = true
		return []Activity{act("online", "", "", "The server is online and ready for players.")}
	}

	switch ev.Kind {
	case ServerReady:
		return ready()
	case SteamConnected:
		if t.SteamAt.IsZero() {
			t.SteamAt = at
		}
	case CrossplayFailed:
		t.CrossplayFailed = !t.NoJoinCode
	case JoinCode:
		// The server registers its session before anyone can connect, so
		// a later line about another session was made up by a player.
		if t.NoJoinCode {
			return nil
		}
		if t.session == "" {
			t.session = ev.Session
		}
		if ev.Session != t.session {
			return nil
		}
		if ev.Code != "" {
			t.JoinCode = ev.Code
		}
		if ev.Addr != "" {
			t.PublicAddr = ev.Addr
		}
		// Valheim opens its crossplay session once the world is loaded, so
		// this also means it's ready if the line saying so was missed.
		out := ready()
		if ev.Count >= 0 && ev.Addr != "" {
			out = append(out, t.reconcile(ev.Count, at)...)
		}
		return out
	case PeerCount:
		return t.reconcile(ev.Count, at)
	case Connecting, Handshake, PlatformID:
		t.addPending(ev.ID, ev.PlatformID)
	case Character:
		if p := t.byName(ev.Name); p != nil {
			if ev.Dead {
				return []Activity{act("death", p.Name, p.PlatformID, p.Name+" died.")}
			}
			return nil // respawn after death: same player, new body
		}
		if ev.Dead {
			return nil
		}
		p := &Player{Name: ev.Name, Since: at}
		if len(t.pending) > 0 {
			p.ID, p.PlatformID = t.pending[0].id, t.pending[0].platformID
			t.pending = t.pending[1:]
		}
		t.players = append(t.players, p)
		t.lowStreak = 0
		return []Activity{act("join", p.Name, p.PlatformID, p.Name+" joined the game.")}
	case Disconnect:
		if ev.ID == "" || ev.ID == "0" {
			return nil
		}
		for i, p := range t.players {
			if p.ID == ev.ID {
				t.players = append(t.players[:i], t.players[i+1:]...)
				return []Activity{t.leaveActivity(p, at)}
			}
		}
		t.dropPending(ev.ID)
	case WrongPassword:
		t.dropPending(ev.ID)
		return []Activity{act("wrong_password", "", "", "Someone tried to join with the wrong password.")}
	case BadVersion:
		t.dropPending(ev.ID)
		text := "Someone tried to join with a different version of Valheim. Everyone needs the latest update."
		if ev.Version != "" && ev.Mine != "" {
			text = fmt.Sprintf("Someone tried to join with Valheim %s, but the server runs %s. Everyone needs the same version (update the game or the server).", ev.Version, ev.Mine)
		}
		return []Activity{act("bad_version", "", "", text)}
	case Version:
		if t.Version == "" { // logged once at startup, before anyone can connect
			t.Version = ev.Version
		}
	case WorldSaved:
		// Valheim can log a save in several lines; report it once.
		repeat := !t.LastSave.IsZero() && at.Sub(t.LastSave) < 10*time.Second
		t.LastSave = at
		if repeat {
			return nil
		}
		text := "World saved."
		if ev.Millis > 0 {
			text = fmt.Sprintf("World saved (took %.1f s).", ev.Millis/1000)
		}
		return []Activity{act("saved", "", "", text)}
	case WorldLoadFailed:
		return []Activity{act("load_failed", "", "", "The world file couldn't be loaded. It may be damaged: restore a backup from the Worlds page.")}
	case BadPasswordConfig:
		return []Activity{act("bad_password_config", "", "", "Valheim rejected the server password. It can't be part of the server name, the world name or the seed. Change it in Settings.")}
	case Shutdown:
		if !t.Stopping {
			t.Stopping, t.StoppingAt = true, at
		}
	}
	return nil
}

func (t *Tracker) leaveActivity(p *Player, at time.Time) Activity {
	text := p.Name + " left the game."
	if d := at.Sub(p.Since); d >= time.Minute {
		text = fmt.Sprintf("%s left the game after %s.", p.Name, HumanDuration(d))
	}
	return Activity{Kind: "leave", Time: at, Name: p.Name, PlatformID: p.PlatformID, Text: text}
}

// reconcile trusts the server's own player count when it disagrees with
// what was pieced together, e.g. when a disconnect line was missed.
func (t *Tracker) reconcile(count int, at time.Time) []Activity {
	if count < 0 {
		return nil
	}
	if count >= len(t.players) {
		t.lowStreak = 0
		if count == 0 {
			t.pending = nil
		}
		return nil
	}
	// Ask for two readings in a row, so a join or leave racing the count
	// line (or a line a player made up) doesn't remove anyone.
	t.lowStreak++
	if t.lowStreak < 2 {
		return nil
	}
	t.lowStreak = 0
	if count == 0 {
		t.pending = nil
	}
	var out []Activity
	for len(t.players) > count {
		i := 0
		for j, p := range t.players {
			if p.ID == "" {
				i = j
				break
			}
		}
		p := t.players[i]
		t.players = append(t.players[:i], t.players[i+1:]...)
		out = append(out, t.leaveActivity(p, at))
	}
	return out
}

func (t *Tracker) removeAll(at time.Time) []Activity {
	var out []Activity
	for _, p := range t.players {
		out = append(out, t.leaveActivity(p, at))
	}
	t.players = nil
	return out
}

// Reset forgets everything, e.g. when the server process stops. It returns
// leave activities for anyone still listed.
func (t *Tracker) Reset(at time.Time) []Activity {
	out := t.removeAll(at)
	*t = Tracker{}
	return out
}

func (t *Tracker) addPending(id, platformID string) {
	if id == "" {
		return
	}
	for _, p := range t.players {
		if p.ID == id {
			if platformID != "" {
				p.PlatformID = platformID
			}
			return
		}
	}
	for i := range t.pending {
		if t.pending[i].id == id {
			if platformID != "" {
				t.pending[i].platformID = platformID
			}
			return
		}
	}
	t.pending = append(t.pending, pending{id: id, platformID: platformID})
	if len(t.pending) > 20 { // never grow without bound on odd logs
		t.pending = t.pending[len(t.pending)-20:]
	}
}

func (t *Tracker) dropPending(id string) {
	if id == "" {
		return
	}
	for i := range t.pending {
		if t.pending[i].id == id {
			t.pending = append(t.pending[:i], t.pending[i+1:]...)
			return
		}
	}
}

func (t *Tracker) byName(name string) *Player {
	for _, p := range t.players {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// HumanDuration formats a duration like "2 h 5 min" or "45 min".
func HumanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	default:
		return fmt.Sprintf("%d h %d min", h, m)
	}
}
