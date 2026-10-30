package manager

// Reminders for players: what changed since they last played (a newer
// Valheim, updated, new or dropped mods, a new password), shown on the Home
// page and in the Discord post. What they last played with is noted when
// someone joins, so a reminder stays up until somebody has caught up.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/mods"
)

// played is what players play with.
type played struct {
	Game     string            `json:"game,omitempty"`     // Valheim version, such as 1.0.20
	Mods     map[string]string `json:"mods"`               // mod ID -> version
	Password string            `json:"password,omitempty"` // a hash, enough to notice a change
}

func hashPassword(pw string) string {
	sum := sha256.Sum256([]byte("valheim-easy-server:" + pw))
	return hex.EncodeToString(sum[:8])
}

// startedWith is what a server started with these settings is played with,
// apart from the Valheim version, which its log says.
func (m *Manager) startedWith(s config.Settings) played {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := played{Mods: map[string]string{}, Password: hashPassword(s.Password)}
	for _, id := range mods.ServerMods(s.WorldMods()) { // players' own mods are up to them
		if md, ok := mods.Find(id); ok {
			p.Mods[id] = m.modVersions[md.Package]
		}
	}
	return p
}

// playingLocked is what the running server is played with.
func (m *Manager) playingLocked(run *serverRun) played {
	p := run.info.Played
	if p.Mods == nil { // started by an older version of the app
		p = played{Mods: map[string]string{}, Password: hashPassword(m.settings.Password)}
		for _, id := range mods.ServerMods(m.settings.WorldOptions[run.info.World].Mods) {
			if md, ok := mods.Find(id); ok {
				p.Mods[id] = m.modVersions[md.Package]
			}
		}
	}
	p.Game = run.tracker.Version
	if p.Game == "" && m.lastPlayed != nil {
		p.Game = m.lastPlayed.Game
	}
	return p
}

// reminderLocked lists what players need to do before joining the running
// server, or nothing when they're up to date or nobody has played yet.
func (m *Manager) reminderLocked(run *serverRun) []string {
	if run == nil || !run.ready || run.replaying {
		return nil
	}
	return remind(m.lastPlayed, m.playingLocked(run))
}

// remind compares what players last played with to what they'll play with
// now.
func remind(last *played, now played) []string {
	if last == nil {
		return nil
	}
	var out []string
	if last.Game != "" && now.Game != "" && last.Game != now.Game {
		out = append(out, "update Valheim to "+now.Game)
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
	now := m.playingLocked(run)
	if m.lastPlayed != nil && reflect.DeepEqual(*m.lastPlayed, now) {
		return false
	}
	m.lastPlayed = &now
	return true
}

func (m *Manager) loadPlayed() {
	b, err := os.ReadFile(m.dataFile("lastplayed.json"))
	if err != nil {
		return
	}
	var p played
	if json.Unmarshal(b, &p) == nil && p.Mods != nil {
		m.lastPlayed = &p
	}
}

func (m *Manager) savePlayed() {
	m.mu.Lock()
	p := m.lastPlayed
	m.mu.Unlock()
	if p == nil {
		return
	}
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		config.WriteFileAtomic(m.dataFile("lastplayed.json"), b, 0o644)
	}
}
