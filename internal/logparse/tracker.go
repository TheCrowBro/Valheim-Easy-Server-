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

	unsure bool // which connection is theirs was a guess, so their account isn't known
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
	unsure         bool // a guess for an earlier character may have been this one
}

// countGap is how far apart, by this computer's clock, two low player counts
// must come before anyone is taken off the list. A player can write made-up
// count lines into the log, as many as they like at once. But Valheim logs
// its own count every 10 minutes, so one of those always comes between two
// low counts this far apart, and starts the wait again if it's high enough.
const countGap = 15 * time.Minute

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
	lowSince   time.Time // by this computer's clock, when the first of a run of low player counts came

	// KnownPlatformID, if set, gives the account a character's name last
	// played from. It tells apart friends who connect at the same time.
	// Apply calls it, so it runs under any lock Apply's caller holds.
	KnownPlatformID func(name string) string

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
// activity feed. now is used when the line had no timestamp, and to time
// player counts: a player can write a line with any timestamp they like,
// but can't change this computer's clock.
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
		// A new code for the server's own session is taken: Valheim gets one
		// when it registers its session again, and the app posts it. When
		// crossplay couldn't start, no code will come at all. The line's
		// player count isn't used: a player can write a line with the
		// server's name, and the server's "Connections" lines give it.
		if t.CrossplayFailed {
			ev.Code, ev.Addr = "", ""
		}
		if ev.Code != "" {
			t.JoinCode = ev.Code
		}
		if ev.Addr != "" {
			t.PublicAddr = ev.Addr
		}
		// Valheim opens its crossplay session once the world is loaded, so
		// this also means it's ready if the line saying so was missed.
		return ready()
	case PeerCount:
		return t.reconcile(ev.Count, at, now)
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
		if c, sure, ok := t.claim(ev.Name); ok {
			p.ID = c.id
			if sure {
				p.PlatformID = c.platformID
			} else {
				p.unsure = true // no account, so nobody bans or makes an admin of the wrong one
			}
		}
		t.players = append(t.players, p)
		t.lowSince = time.Time{}
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
func (t *Tracker) reconcile(count int, at, now time.Time) []Activity {
	if count < 0 {
		return nil
	}
	if count >= len(t.players) {
		t.lowSince = time.Time{}
		if count == 0 {
			t.pending = nil
		}
		return nil
	}
	// Ask for a low count twice, at least countGap apart with no count high
	// enough in between, so a join or leave racing the count line doesn't
	// remove anyone, and neither do count lines a player made up.
	if t.lowSince.IsZero() || now.Before(t.lowSince) {
		t.lowSince = now
		return nil
	}
	if now.Sub(t.lowSince) < countGap {
		return nil
	}
	t.lowSince = time.Time{}
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
			if platformID != "" && !p.unsure {
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

// claim takes the connection the character that just arrived came in on.
// Valheim doesn't say which it is, and characters arrive in the order the
// players' games finish loading the world, not the order they connected.
// sure is false when it's a guess, as when two friends connect at the same
// time and one of them hasn't played here before.
func (t *Tracker) claim(name string) (c pending, sure, ok bool) {
	if len(t.pending) == 0 {
		return pending{}, false, false
	}
	i := 0
	sure = len(t.pending) == 1 && !t.pending[0].unsure
	if !sure && t.KnownPlatformID != nil {
		if known := t.KnownPlatformID(name); known != "" {
			for j, p := range t.pending {
				if p.platformID == known {
					i, sure = j, true
					break
				}
			}
		}
	}
	c = t.pending[i]
	t.pending = append(t.pending[:i], t.pending[i+1:]...)
	if !sure {
		// Any of the others may be the one this guess should have taken.
		for j := range t.pending {
			t.pending[j].unsure = true
		}
	}
	return c, sure, true
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
