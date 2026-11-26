package manager

// Reminders for players: what changed since they last played (a newer
// version of the game, updated, new or dropped mods, a new password), shown
// on the Home page and in the Discord post. What they last played with is
// noted when someone joins, so a reminder stays up until somebody has
// caught up.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
)

// played is what players play with: the game's version, the mods and a
// hash of the password. It is game.Played under its old name, so
// lastplayed.json and the run record keep their shape.
type played = game.Played

// reminderSalt starts what's hashed to note the password. lastplayed.json
// keeps the hash an older copy of the app made, so a different salt would
// tell everyone to use a new password that hasn't changed
// (manager_identity_test.go checks it).
const reminderSalt = "valheim-easy-server:"

func hashPassword(pw string) string {
	sum := sha256.Sum256([]byte(reminderSalt + pw))
	return hex.EncodeToString(sum[:8])
}

// serverModsLocked are the mods the server loads for world, with their
// installed versions; players' own mods are up to them. A game without
// mods has none.
func serverMods(inst *instance, world string) map[string]string {
	out := map[string]string{}
	if ml, ok := inst.srv.(game.ModLister); ok {
		for _, md := range ml.PlayerMods(world, nil) {
			if md.Players != mods.PlayersOnly {
				out[md.ID] = md.Version
			}
		}
	}
	return out
}

// startedWith is what a server started with world and password is played
// with, apart from the game's version, which its output says.
func (m *Manager) startedWith(inst *instance, world, password string) played {
	return played{Mods: serverMods(inst, world), Password: hashPassword(password)}
}

// playingLocked is what the instance's running server is played with.
func (m *Manager) playingLocked(inst *instance, run *serverRun) played {
	p := run.rec.Played
	if p.Mods == nil { // started by an older version of the app
		p = played{Mods: serverMods(inst, run.rec.World), Password: hashPassword(inst.saved().Server["password"])}
	}
	p.Game = run.state().Version
	if last := m.lastPlayedLocked(inst); p.Game == "" && last != nil {
		p.Game = last.Game
	}
	return p
}

// lastPlayedLocked is what the instance's players last played with.
func (m *Manager) lastPlayedLocked(inst *instance) *played {
	if inst.legacy {
		return m.lastPlayed
	}
	return inst.lastPlayed
}

// reminderLocked lists what players need to do before joining the running
// server, or nothing when they're up to date or nobody has played yet.
func (m *Manager) reminderLocked(inst *instance, run *serverRun) []string {
	if run == nil || !run.ready || run.replaying {
		return nil
	}
	return remindFor(inst.short(), m.lastPlayedLocked(inst), m.playingLocked(inst, run))
}

// remindFor compares what players last played with to what they'll play
// with now. short is the game's name ("update Valheim to 1.0.21").
func remindFor(short string, last *played, now played) []string {
	if last == nil {
		return nil
	}
	var out []string
	if last.Game != "" && now.Game != "" && last.Game != now.Game {
		out = append(out, "update "+short+" to "+now.Game)
	}
	for _, md := range mods.Catalog {
		v, on := now.Mods[md.ID]
		was, had := last.Mods[md.ID]
		switch {
		case on && !had:
			out = append(out, "install "+md.Name)
		case !on && had:
			out = append(out, "turn off "+md.Name)
		case on && v != "" && was != "" && v != was:
			out = append(out, fmt.Sprintf("update %s to %s", md.Name, v))
		}
	}
	if last.Password != "" && now.Password != "" && last.Password != now.Password {
		out = append(out, "use the new password")
	}
	return out
}

// notePlayedLocked remembers what someone just joined with. It reports
// whether that changed, so the caller saves it.
func (m *Manager) notePlayedLocked(run *serverRun) bool {
	now := m.playingLocked(run.inst, run)
	if last := m.lastPlayedLocked(run.inst); last != nil && reflect.DeepEqual(*last, now) {
		return false
	}
	if run.inst.legacy {
		m.lastPlayed = &now
	} else {
		run.inst.lastPlayed = &now
	}
	return true
}

func (m *Manager) loadPlayed(inst *instance) {
	b, err := os.ReadFile(m.instFile(inst, "lastplayed.json"))
	if err != nil {
		return
	}
	var p played
	if json.Unmarshal(b, &p) == nil && p.Mods != nil {
		if inst.legacy {
			m.lastPlayed = &p
		} else {
			inst.lastPlayed = &p
		}
	}
}

// savePlayed saves what the legacy game's players last played with.
func (m *Manager) savePlayed() { m.savePlayedFor(m.legacy()) }

func (m *Manager) savePlayedFor(inst *instance) {
	m.mu.Lock()
	p := m.lastPlayedLocked(inst)
	m.mu.Unlock()
	if p == nil {
		return
	}
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		config.WriteFileAtomic(m.instFile(inst, "lastplayed.json"), b, 0o644)
	}
}
