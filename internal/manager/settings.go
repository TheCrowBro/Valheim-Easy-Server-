package manager

import (
	"errors"
	"fmt"
	"strings"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/worlds"
)

// UpdateSettings saves general settings. The selected world and world
// options have their own methods.
func (m *Manager) UpdateSettings(in config.Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.settings
	next := in.Clone()
	next.World = cur.World
	next.WorldOptions = cur.Clone().WorldOptions // set on the Worlds page
	next.Mods = nil                              // each world has its own now
	next.Normalize()
	if errs := next.Validate(); errs != nil {
		return errs
	}
	if next.ServerDir != cur.ServerDir || next.SaveDir != cur.SaveDir {
		if m.run != nil {
			return errors.New("Stop the server before changing its folders.")
		}
		if m.job != nil && m.job.Finished == nil {
			return &BusyError{Title: lowerFirst(m.job.Title)}
		}
	}
	if next.IdleHibernate && !cur.IdleHibernate {
		m.idleSince = timeNow() // start counting from now
	}
	if err := m.saveSettingsLocked(next); err != nil {
		return err
	}
	return nil
}

// library is the worlds library for the current folders. A Library's
// folders never change; when the settings move them, a new one replaces it,
// and anything still using the old one finishes in the old folders.
func (m *Manager) library() *worlds.Library { return m.libPtr.Load() }

func newLibrary(p config.Paths) *worlds.Library {
	return &worlds.Library{Worlds: p.Worlds, Backups: p.Backups, Staging: p.Staging}
}

// saveSettingsLocked writes settings and updates derived paths.
func (m *Manager) saveSettingsLocked(s config.Settings) error {
	if err := config.Save(m.paths.Settings, s); err != nil {
		return fmt.Errorf("couldn't save settings: %w", err)
	}
	m.settings = s
	oldServer := m.paths.Server
	m.paths = config.ResolvePaths(m.opt.DataDir, s)
	if m.paths.Server != oldServer {
		m.modVersions = (&mods.Installer{Dir: m.paths.Server}).Installed()
	}
	if lib := m.library(); lib.Worlds != m.paths.Worlds || lib.Backups != m.paths.Backups || lib.Staging != m.paths.Staging {
		m.libPtr.Store(newLibrary(m.paths))
	}
	if inst, ok := m.opt.Installer.(interface{ SetInstallDir(string) }); ok {
		inst.SetInstallDir(m.paths.Server)
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

// SetWorldMods changes which mods a world is played with, keeping its
// difficulty settings.
func (m *Manager) SetWorldMods(name string, ids []string) error {
	return m.changeWorldOptions(name, func(prev config.WorldOptions) config.WorldOptions {
		prev.Mods = ids
		return prev
	})
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

// Lists returns the admin, banned and permitted player lists.
func (m *Manager) Lists() (map[config.ListKind][]string, error) {
	m.mu.Lock()
	save := m.paths.Save
	m.mu.Unlock()
	out := map[config.ListKind][]string{}
	for _, k := range config.ListKinds {
		ids, err := config.ReadList(save, k)
		if err != nil {
			return nil, err
		}
		out[k] = ids
	}
	return out, nil
}

// SetList replaces one of the player lists.
func (m *Manager) SetList(kind config.ListKind, ids []string) error {
	if kind.FileName() == "" {
		return errors.New("unknown list")
	}
	m.mu.Lock()
	save := m.paths.Save
	m.mu.Unlock()
	if err := config.WriteList(save, kind, ids); err != nil {
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
type ModInfo struct {
	mods.Mod
	Version string `json:"version,omitempty"`
}

// Worlds lists the worlds and their backups.
func (m *Manager) Worlds() (WorldsView, error) {
	ws, orphans, err := m.library().List()
	if err != nil {
		return WorldsView{}, err
	}
	installed := (&mods.Installer{Dir: m.Paths().Server}).Installed()
	m.mu.Lock()
	defer m.mu.Unlock()
	v := WorldsView{Worlds: ws, Orphans: orphans, Selected: m.settings.World, Options: m.settings.Clone().WorldOptions, Folder: m.paths.Worlds}
	for _, md := range mods.Catalog {
		v.Mods = append(v.Mods, ModInfo{Mod: md, Version: installed[md.Package]})
	}
	if v.Worlds == nil {
		v.Worlds = []worlds.World{}
	}
	if v.Orphans == nil {
		v.Orphans = []worlds.Orphan{}
	}
	if v.Options == nil {
		v.Options = map[string]config.WorldOptions{}
	}
	if m.run != nil {
		v.Running = m.run.info.World
	}
	return v, nil
}

// worldBusyLocked reports whether the running server uses a world.
func (m *Manager) worldBusyLocked(name string) bool {
	return m.run != nil && strings.EqualFold(m.run.info.World, name)
}

func (m *Manager) checkWorldFree(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.worldBusyLocked(name) {
		return fmt.Errorf("%q is running on the server. Stop the server first.", name)
	}
	if m.job != nil && m.job.Finished == nil {
		return &BusyError{Title: lowerFirst(m.job.Title)}
	}
	return nil
}

// SelectWorld picks the world the server plays. With restart, a running
// server is restarted straight away to switch.
func (m *Manager) SelectWorld(name string, restart bool) error {
	if msg := config.WorldNameProblem(name); msg != "" {
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
	running := m.run != nil && m.run.info.World != name
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
	if msg := config.WorldNameProblem(name); msg != "" {
		return errors.New(msg)
	}
	if m.library().Exists(name) {
		return fmt.Errorf("there's already a world called %q", name)
	}
	if !o.IsZero() {
		o.Off = nil // a new world has nothing to turn off
		if err := m.changeWorldOptions(name, func(config.WorldOptions) config.WorldOptions { return o }); err != nil {
			return err
		}
	}
	return m.SelectWorld(name, restart)
}

// BackupWorld makes a zip backup of a world.
func (m *Manager) BackupWorld(name string) error {
	return m.runJob("backup", "Backing up "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Copying the world into a backup file…", -1)
		b, err := m.library().Backup(name, "manual")
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
	if err := m.checkWorldFree(name); err != nil {
		return err
	}
	return m.runJob("restore", "Restoring "+name, false, func(_ ctxT, j *jobCtl) error {
		if err := m.checkRunning(name); err != nil {
			return err
		}
		j.step("Keeping a copy of the current world, then restoring the backup…", -1)
		if err := m.library().Restore(name, backupID); err != nil {
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
	if err := m.checkWorldFree(name); err != nil {
		return err
	}
	return m.runJob("delete", "Deleting "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Keeping a backup copy, then deleting…", -1)
		if err := m.library().Delete(name); err != nil {
			return err
		}
		m.addActivity("delete", "", fmt.Sprintf("%q was deleted. A backup copy is kept on the Worlds page.", name))
		j.result("The world was deleted. You can bring it back from its backup.")
		return nil
	})
}

// RenameWorld renames an old-format world.
func (m *Manager) RenameWorld(name, newName string) error {
	if err := m.checkWorldFree(name); err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	if err := m.library().Rename(name, newName); err != nil {
		return err
	}
	m.mu.Lock()
	next := m.settings.Clone()
	changed := false
	if next.World == name {
		next.World, changed = newName, true
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

func (m *Manager) checkRunning(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.worldBusyLocked(name) {
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
