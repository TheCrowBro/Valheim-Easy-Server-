package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/worlds"
)

// UpdateSettings saves general settings. The selected world and world
// options have their own methods.
func (m *Manager) UpdateSettings(in config.Settings) error {
	inst := m.legacy()
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.settings
	next := in.Clone()
	next.World = cur.World
	next.WorldOptions = cur.Clone().WorldOptions // set on the Worlds page
	next.Mods = nil                              // each world has its own now
	// Set when the JetKVM waker is installed, not on the Settings page.
	next.DiscordBotToken, next.JetKVM, next.DiscordWake = cur.DiscordBotToken, cur.JetKVM, cur.DiscordWake
	next.Normalize()
	if errs := next.Validate(); errs != nil {
		return errs
	}
	if next.ServerDir != cur.ServerDir || next.SaveDir != cur.SaveDir {
		if inst.run != nil {
			return errors.New("Stop the server before changing its folders.")
		}
		if busy := m.busyJobLocked(inst); busy != nil {
			return &BusyError{Title: lowerFirst(busy.Title)}
		}
	}
	if next.IdleHibernate && !cur.IdleHibernate {
		m.machineIdleSince = timeNow() // start counting from now
	}
	if err := m.saveSettingsLocked(next); err != nil {
		return err
	}
	// The waker on the JetKVM names the server, asks whether it's up at its
	// port, and answers only in the join-code channel's Discord server.
	if cur.DiscordWake && (next.ServerName != cur.ServerName || next.Port != cur.Port ||
		next.Discord != cur.Discord || next.DiscordWebhook != cur.DiscordWebhook) {
		m.go_(m.upkeepWaker)
	}
	// Turned on, or moved to another channel: post the join code there now.
	// Likewise when the password is to be included, so friends get it.
	if run := inst.run; run != nil && next.Discord && (!cur.Discord || next.DiscordWebhook != cur.DiscordWebhook ||
		next.DiscordPassword && !cur.DiscordPassword) {
		run.rec.Announced = ""
		if post, ok := m.announceLocked(run); ok {
			m.queueDiscord(post)
			go m.saveRunInfo(run)
		}
	}
	return nil
}

// saveSettingsLocked writes settings and updates derived paths.
func (m *Manager) saveSettingsLocked(s config.Settings) error {
	if err := config.Save(m.paths.Settings, s); err != nil {
		return fmt.Errorf("couldn't save settings: %w", err)
	}
	m.settings = s
	m.paths = config.ResolvePaths(m.opt.DataDir, s)
	m.snapshotLocked()
	// The legacy game's folders follow the settings (its adapter reads them
	// from the snapshot at each call, so its world library and mods do too).
	m.legacy().dirs = legacyDirs(m.paths)
	if in, ok := m.opt.Installer.(interface{ SetInstallDir(string) }); ok {
		in.SetInstallDir(m.paths.Server)
	}
	go m.notify()
	return nil
}

// SetWorldOptions changes a world's difficulty settings, keeping its mods.
func (m *Manager) SetWorldOptions(name string, o config.WorldOptions) error {
	return m.changeWorldOptions(name, func(prev config.WorldOptions) config.WorldOptions {
		o = o.WithOff(prev)
		o.Mods = prev.Mods
		return o
	})
}

// SetWorldMods changes which mods a world of the legacy game is played
// with, keeping its difficulty settings.
func (m *Manager) SetWorldMods(name string, ids []string) error {
	ml, ok := m.legacy().srv.(game.ModLister)
	if !ok {
		return errors.New("This game has no mods.")
	}
	return ml.SetWorldMods(name, ids)
}

func (m *Manager) changeWorldOptions(name string, change func(config.WorldOptions) config.WorldOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.settings.Clone()
	if next.WorldOptions == nil {
		next.WorldOptions = map[string]config.WorldOptions{}
	}
	o := change(next.WorldOptions[name].Clone())
	if msg := o.Problem(); msg != "" {
		return errors.New(msg)
	}
	if o.IsZero() {
		delete(next.WorldOptions, name)
	} else {
		next.WorldOptions[name] = o.Clone()
	}
	return m.saveSettingsLocked(next)
}

// Lists returns the legacy game's player lists (admins, banned and
// permitted players).
func (m *Manager) Lists() (map[config.ListKind][]string, error) {
	out := map[config.ListKind][]string{}
	l, ok := m.legacy().srv.(game.Lister)
	if !ok {
		return out, nil
	}
	for _, k := range l.Lists() {
		ids, err := l.ReadList(k.ID)
		if err != nil {
			return nil, err
		}
		out[config.ListKind(k.ID)] = ids
	}
	return out, nil
}

// SetList replaces one of the legacy game's player lists.
func (m *Manager) SetList(kind config.ListKind, ids []string) error {
	inst := m.legacy()
	l, ok := inst.srv.(game.Lister)
	if !ok || !slices.ContainsFunc(l.Lists(), func(k game.ListKind) bool { return k.ID == string(kind) }) {
		return errors.New("unknown list")
	}
	m.mu.Lock()
	var rec *game.Record
	if inst.run != nil {
		r := inst.run.rec
		rec = &r
	}
	m.mu.Unlock()
	if err := l.WriteList(context.Background(), rec, string(kind), ids); err != nil {
		return err
	}
	m.addActivity("info", "", fmt.Sprintf("The %s list was updated. Changes apply the next time the server starts.", listLabel(kind)))
	return nil
}

func listLabel(k config.ListKind) string {
	switch k {
	case config.Admins:
		return "admin"
	case config.Banned:
		return "banned players"
	}
	return "allowed players"
}

// --- worlds -----------------------------------------------------------------

// WorldsView is the Worlds page.
type WorldsView struct {
	Worlds   []worlds.World                 `json:"worlds"`
	Orphans  []worlds.Orphan                `json:"orphans"`
	Selected string                         `json:"selected"`
	Running  string                         `json:"running"`
	Options  map[string]config.WorldOptions `json:"options"`
	Mods     []ModInfo                      `json:"mods"` // the mods a world can be played with
	Folder   string                         `json:"folder"`
}

// ModInfo is a mod the app offers, with the version installed, if any.
// It is mods.Info under its old name: the type moved so the game contract
// can use it, and an alias keeps every use and the JSON exactly as before.
type ModInfo = mods.Info

// Worlds lists the legacy game's worlds and their backups.
func (m *Manager) Worlds() (WorldsView, error) {
	inst := m.legacy()
	ws, orphans, err := inst.srv.Worlds().List()
	if err != nil {
		return WorldsView{}, err
	}
	var all []ModInfo
	if ml, ok := inst.srv.(game.ModLister); ok {
		all = ml.WorldMods()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := WorldsView{Worlds: ws, Orphans: orphans, Selected: m.settings.World, Options: m.settings.Clone().WorldOptions, Folder: m.paths.Worlds}
	v.Mods = all
	if v.Worlds == nil {
		v.Worlds = []worlds.World{}
	}
	if v.Orphans == nil {
		v.Orphans = []worlds.Orphan{}
	}
	if v.Options == nil {
		v.Options = map[string]config.WorldOptions{}
	}
	if inst.run != nil {
		v.Running = inst.run.rec.World
	}
	return v, nil
}

// worldBusyLocked reports whether the instance's running server uses a
// world.
func (m *Manager) worldBusyLocked(inst *instance, name string) bool {
	return inst.run != nil && strings.EqualFold(inst.run.rec.World, name)
}

func (m *Manager) checkWorldFree(inst *instance, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.worldBusyLocked(inst, name) {
		return fmt.Errorf("%q is running on the server. Stop the server first.", name)
	}
	if busy := m.busyJobLocked(inst); busy != nil {
		return &BusyError{Title: lowerFirst(busy.Title)}
	}
	return nil
}

// SelectWorld picks the world the server plays. With restart, a running
// server is restarted straight away to switch.
func (m *Manager) SelectWorld(name string, restart bool) error {
	inst := m.legacy()
	if msg := inst.srv.Worlds().NameProblem(name); msg != "" {
		return errors.New(msg)
	}
	m.mu.Lock()
	next := m.settings.Clone()
	next.World = name
	if errs := next.Validate(); errs != nil {
		m.mu.Unlock()
		return errs
	}
	if err := m.saveSettingsLocked(next); err != nil {
		m.mu.Unlock()
		return err
	}
	running := inst.run != nil && inst.run.rec.World != name
	m.mu.Unlock()
	m.addActivity("world", "", fmt.Sprintf("%q is now the selected world.", name))
	if running && restart {
		return m.RestartServer()
	}
	return nil
}

// CreateWorld selects a new world name; the server creates the world when
// it next starts.
func (m *Manager) CreateWorld(name string, o config.WorldOptions, restart bool) error {
	name = strings.TrimSpace(name)
	lib := m.legacy().srv.Worlds()
	if msg := lib.NameProblem(name); msg != "" {
		return errors.New(msg)
	}
	if lib.Exists(name) {
		return fmt.Errorf("there's already a world called %q", name)
	}
	// Set the options even when none were chosen: a deleted world keeps its
	// options (so restoring it brings them back), and a new world with its
	// name mustn't get its difficulty and mods.
	o.Off = nil // a new world has nothing to turn off
	if err := m.changeWorldOptions(name, func(config.WorldOptions) config.WorldOptions { return o }); err != nil {
		return err
	}
	return m.SelectWorld(name, restart)
}

// BackupWorld makes a zip backup of a world.
func (m *Manager) BackupWorld(name string) error {
	return m.runJob("backup", "Backing up "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Copying the world into a backup file…", -1)
		b, err := m.legacy().srv.Worlds().Backup(name, "manual")
		if err != nil {
			return err
		}
		m.addActivity("backup", "", fmt.Sprintf("A backup of %q was made.", name))
		j.result(fmt.Sprintf("Backup saved (%s).", humanBytes(b.Size)))
		return nil
	})
}

// RestoreWorld puts a backup back. The current world is backed up first.
func (m *Manager) RestoreWorld(name, backupID string) error {
	inst := m.legacy()
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	return m.runJob("restore", "Restoring "+name, false, func(_ ctxT, j *jobCtl) error {
		if err := m.checkRunning(inst, name); err != nil {
			return err
		}
		j.step("Keeping a copy of the current world, then restoring the backup…", -1)
		if err := inst.srv.Worlds().Restore(name, backupID); err != nil {
			return err
		}
		m.addActivity("restore", "", fmt.Sprintf("%q was restored from a backup.", name))
		j.result("The backup was restored.")
		return nil
	})
}

// DeleteWorld removes a world, keeping a backup copy.
func (m *Manager) DeleteWorld(name string) error {
	m.mu.Lock()
	selected := strings.EqualFold(m.settings.World, name)
	m.mu.Unlock()
	if selected {
		return errors.New("This is the selected world. Pick a different world to play before deleting it.")
	}
	inst := m.legacy()
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	return m.runJob("delete", "Deleting "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Keeping a backup copy, then deleting…", -1)
		if err := inst.srv.Worlds().Delete(name); err != nil {
			return err
		}
		m.addActivity("delete", "", fmt.Sprintf("%q was deleted. A backup copy is kept on the Worlds page.", name))
		j.result("The world was deleted. You can bring it back from its backup.")
		return nil
	})
}

// RenameWorld renames an old-format world.
func (m *Manager) RenameWorld(name, newName string) error {
	inst := m.legacy()
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	if err := inst.srv.Worlds().Rename(name, newName); err != nil {
		return err
	}
	m.mu.Lock()
	next := m.settings.Clone()
	changed := false
	if next.World == name {
		next.World, changed = newName, true
	}
	// Options already saved under the new name belong to a deleted world
	// (deleting keeps them so a restore brings them back). The renamed world
	// mustn't get that world's difficulty and mods, so drop them first.
	if _, ok := next.WorldOptions[newName]; ok {
		delete(next.WorldOptions, newName)
		changed = true
	}
	if o, ok := next.WorldOptions[name]; ok {
		delete(next.WorldOptions, name)
		next.WorldOptions[newName] = o
		changed = true
	}
	var err error
	if changed {
		err = m.saveSettingsLocked(next)
	}
	m.mu.Unlock()
	m.addActivity("world", "", fmt.Sprintf("%q was renamed to %q.", name, newName))
	return err
}

func (m *Manager) checkRunning(inst *instance, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.worldBusyLocked(inst, name) {
		return fmt.Errorf("%q is running on the server. Stop the server first.", name)
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
}
