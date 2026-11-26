package manager

import (
	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/steamcmd"
)

// legacyStore is the manager's side of the legacy game's files
// (games/valheim.LegacyStore): settings.json, its folders, and the parts
// the manager is given when it is made. The adapter reads it at each call,
// so tests that replace m.opt.Installer or m.opt.ModsURL after New, and
// folder changes in the settings, take effect at once.
type legacyStore struct{ m *Manager }

// Settings is a copy of the latest snapshot, which saveSettingsLocked
// swaps: it never takes m.mu, so a Monitor or Join can read it while the
// manager holds the lock.
func (l legacyStore) Settings() config.Settings { return l.m.settingsSnap.Load().Clone() }

// Paths are the folders of the latest settings, swapped the same way.
func (l legacyStore) Paths() config.Paths { return *l.m.pathsSnap.Load() }

// Update changes settings.json under m.mu, so it never loses a change
// made at the same time. It is only called without m.mu held.
func (l legacyStore) Update(change func(*config.Settings) error) error {
	l.m.mu.Lock()
	defer l.m.mu.Unlock()
	next := l.m.settings.Clone()
	if err := change(&next); err != nil {
		return err
	}
	return l.m.saveSettingsLocked(next)
}

// Installer is m.opt.Installer: the real SteamCMD, --dev-fake-server's
// stand-in, or a test's steamcmd.Fake, set before the app starts.
func (l legacyStore) Installer() steamcmd.Installer { return l.m.opt.Installer }

// ModsURL is where mods are downloaded from.
func (l legacyStore) ModsURL() string { return l.m.opt.ModsURL }

// Note adds an entry to the activity feed.
func (l legacyStore) Note(kind, text string) { l.m.addActivity(kind, "", text) }
