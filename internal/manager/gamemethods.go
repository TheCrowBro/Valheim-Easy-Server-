package manager

// The per-game methods of the new panel (spec 4.1, 6.2) for every
// instance: a game's settings, worlds, players, lists and log, by its
// instance id. Where the legacy game (Valheim) has an old method, its
// …Game form calls that method, so it behaves exactly as on the old
// routes; every other game goes through its adapter and its servers.json
// entry (serversStore).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// --- settings ---------------------------------------------------------------

// GameSettings is a game's Settings tab.
func (m *Manager) GameSettings(id string) (GameSettingsView, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return GameSettingsView{}, err
	}
	saved := inst.saved()
	v := GameSettingsView{Schema: inst.srv.Schema(),
		Values: GameValues{Server: orEmpty(saved.Server), Worlds: map[string]game.Values{}, World: saved.World}, Ports: []PortSetting{}}
	for name, w := range saved.Worlds {
		v.Values.Worlds[name] = w.Clone()
	}
	if v.Schema == nil {
		v.Schema = game.Schema{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range inst.info.Ports {
		v.Ports = append(v.Ports, PortSetting{Role: p.Role, Protos: p.Protos, Port: saved.Ports[p.Role], Public: p.Public,
			Chosen: m.portChosenLocked(inst, p.Role)})
	}
	v.AutoStopMinutes = m.autoStopMinutesLocked(inst) // A
	if inst.legacy {
		v.StartWithApp = m.settings.StartWithApp // settings.json, as today (2.9)
	} else if in := m.servers.Instance(inst.id); in != nil {
		v.StartWithApp = in.StartWithApp
	}
	v.Disabled = m.isDisabledLocked(inst)
	v.Discord = m.gameDiscordLocked(inst) // D
	return v, nil
}

func orEmpty(v game.Values) game.Values {
	if v == nil {
		return game.Values{}
	}
	return v.Clone()
}

// maxAutoStopMinutes is the longest auto-stop the Settings tab takes (a
// day); 0 turns it off.
const maxAutoStopMinutes = 24 * 60

// UpdateGameSettings saves what changed on a game's Settings tab. Field
// errors come back keyed "server.<key>" (2.9), and the ports' by
// "ports.<role>". Only what is set changes, so a page that is out of date
// never blanks a field. Turning a running game off stops it first.
func (m *Manager) UpdateGameSettings(id string, ch GameSettingsChange) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if ch.AutoStopMinutes != nil && (*ch.AutoStopMinutes < 0 || *ch.AutoStopMinutes > maxAutoStopMinutes) {
		return game.FieldErrors{"autoStopMinutes": fmt.Sprintf("Choose 0 (never) to %d minutes.", maxAutoStopMinutes)}
	}
	if len(ch.Ports) > 0 {
		if inst.legacy {
			return errors.New("This game's port is one of its own settings: change it there.")
		}
		m.mu.Lock()
		errs := m.checkChosenPortsLocked(inst, ch.Ports) // A
		m.mu.Unlock()
		if len(errs) > 0 {
			return prefixed("ports.", errs)
		}
	}
	if len(ch.Values) > 0 {
		if err := m.saveGameValues(inst, ch.Values); err != nil {
			return err
		}
	}
	eula := ch.EULA != nil && *ch.EULA && !inst.legacy
	if ch.Disabled != nil && *ch.Disabled {
		m.mu.Lock()
		on := inst.run != nil && !inst.run.stopping
		m.mu.Unlock()
		if on {
			if err := m.stopGame(inst); err != nil {
				return err
			}
		}
	}
	if len(ch.Ports) > 0 || eula || ch.AutoStopMinutes != nil || ch.StartWithApp != nil && !inst.legacy || ch.Disabled != nil {
		// One change to servers.json, so its backup is the list as it was
		// before this save.
		err := m.updateServers(func(f *servers.File) error {
			in := f.Instance(inst.id)
			if in == nil {
				return errors.New("This game isn't in the list of games.")
			}
			if len(ch.Ports) > 0 {
				if in.Ports == nil {
					in.Ports = map[string]int{}
				}
				for role, port := range ch.Ports {
					in.Ports[role] = port
					if !slices.Contains(in.PortsChosen, role) {
						in.PortsChosen = append(in.PortsChosen, role)
					}
				}
			}
			if eula {
				if in.State == nil {
					in.State = game.Values{}
				}
				in.State[game.StateEULA] = time.Now().UTC().Format(time.RFC3339)
			}
			if ch.AutoStopMinutes != nil {
				n := *ch.AutoStopMinutes
				in.AutoStopMinutes = &n
			}
			if ch.StartWithApp != nil && !inst.legacy {
				in.StartWithApp = *ch.StartWithApp
			}
			if ch.Disabled != nil {
				f.App.DisabledGames = slices.DeleteFunc(f.App.DisabledGames, func(s string) bool { return s == inst.id })
				if *ch.Disabled {
					f.App.DisabledGames = append(f.App.DisabledGames, inst.id)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if ch.Disabled != nil {
			m.starsChanged() // a turned-off game has no Discord command (6.4)
		}
	}
	if ch.StartWithApp != nil && inst.legacy {
		m.mu.Lock()
		next := m.settings.Clone()
		next.StartWithApp = *ch.StartWithApp
		err := m.saveSettingsLocked(next)
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if ch.Discord != nil {
		if err := m.setGameDiscord(inst, *ch.Discord); err != nil { // D
			return err
		}
	}
	return nil
}

// saveGameValues merges server-scope values into a game's settings, after
// checking them as the adapter does. Keys the schema doesn't have as
// server settings are refused, so a mistyped key never lands in the file.
func (m *Manager) saveGameValues(inst *instance, values game.Values) error {
	known := map[string]bool{}
	for _, f := range inst.srv.Schema() {
		if f.Scope == game.ServerScope {
			known[f.Key] = true
		}
	}
	errs := game.FieldErrors{}
	for k := range values {
		if !known[k] {
			errs[k] = "This game has no such setting."
		}
	}
	if len(errs) > 0 {
		return prefixed("server.", errs)
	}
	saved := inst.saved()
	if inst.legacy {
		// The legacy game's folders (settings.json serverDir and saveDir)
		// can't move under a running server, as on its old Settings page.
		m.mu.Lock()
		busy := inst.run != nil || m.busyJobLocked(inst) != nil
		m.mu.Unlock()
		for _, k := range []string{"serverDir", "saveDir"} {
			if v, ok := values[k]; ok && v != saved.Server[k] && busy {
				return errors.New("Stop the server before changing its folders.")
			}
		}
	}
	next := saved.Clone()
	if next.Server == nil {
		next.Server = game.Values{}
	}
	maps.Copy(next.Server, values)
	if errs := inst.srv.Validate(next, selectedWorld(inst, next)); len(errs) > 0 {
		// Only the fields this change touches: an old problem elsewhere
		// (shown on the page) mustn't block saving a fix to another field.
		mine := game.FieldErrors{}
		for k, msg := range errs {
			if _, ok := values[k]; ok {
				mine[k] = msg
			}
		}
		if len(mine) > 0 {
			return prefixed("server.", mine)
		}
	}
	var before config.Settings
	if inst.legacy {
		before = m.Settings()
	}
	if err := inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Server == nil {
			s.Server = game.Values{}
		}
		maps.Copy(s.Server, values)
		return nil
	}); err != nil {
		return err
	}
	if inst.legacy {
		m.afterLegacySettings(before)
	}
	return nil
}

// prefixed keys field errors by their scope ("server.serverName"), as the
// new routes answer them (2.9).
func prefixed(scope string, errs game.FieldErrors) game.FieldErrors {
	out := game.FieldErrors{}
	for k, v := range errs {
		out[scope+k] = v
	}
	return out
}

// afterLegacySettings does what a settings save of the legacy game does
// beyond saving it (UpdateSettings): the waker follows its name, port and
// channel, and a running server's online post goes to a channel that was
// turned on or moved.
func (m *Manager) afterLegacySettings(cur config.Settings) {
	inst := m.legacy()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.settings
	if cur.DiscordWake && (next.ServerName != cur.ServerName || next.Port != cur.Port ||
		next.Discord != cur.Discord || next.DiscordWebhook != cur.DiscordWebhook) {
		m.go_(m.upkeepWaker)
	}
	if run := inst.run; run != nil && next.Discord && (!cur.Discord || next.DiscordWebhook != cur.DiscordWebhook ||
		next.DiscordPassword && !cur.DiscordPassword) {
		run.rec.Announced = ""
		if post, ok := m.announceLocked(run); ok {
			m.queueDiscord(post)
			go m.saveRunInfo(run)
		}
	}
}

// UpdateAppSettings saves the App settings page: idle sleep, automatic
// updates and starting after a wake in settings.json, with the meanings
// they have today, and router ports in servers.json.
func (m *Manager) UpdateAppSettings(ch AppSettingsChange) error {
	if ch.IdleHibernate != nil || ch.IdleMinutes != nil || ch.AutoUpdate != nil || ch.StartAfterWake != nil {
		m.mu.Lock()
		cur := m.settings
		next := cur.Clone()
		if ch.IdleHibernate != nil {
			next.IdleHibernate = *ch.IdleHibernate
		}
		if ch.IdleMinutes != nil {
			next.IdleMinutes = *ch.IdleMinutes
		}
		if ch.AutoUpdate != nil {
			next.AutoUpdate = *ch.AutoUpdate
		}
		if ch.StartAfterWake != nil {
			next.StartAfterWake = *ch.StartAfterWake
		}
		next.Normalize()
		if errs := next.Validate(); errs != nil {
			m.mu.Unlock()
			return errs
		}
		if next.IdleHibernate && !cur.IdleHibernate {
			m.machineIdleSince = timeNow() // start counting from now
		}
		err := m.saveSettingsLocked(next)
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if ch.RouterPorts != nil {
		on := *ch.RouterPorts
		if err := m.updateServers(func(f *servers.File) error {
			f.App.RouterPorts = &on
			return nil
		}); err != nil {
			return err
		}
		m.go_(func() { m.routerPortsChanged(on) }) // N
	}
	return nil
}

// --- worlds -----------------------------------------------------------------

// WorldsGame lists a game's worlds and their backups, with the fields a
// world has and each world's values.
func (m *Manager) WorldsGame(id string) (GameWorldsView, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return GameWorldsView{}, err
	}
	var v GameWorldsView
	if inst.legacy {
		old, err := m.Worlds()
		if err != nil {
			return GameWorldsView{}, err
		}
		v = GameWorldsView{Worlds: old.Worlds, Orphans: old.Orphans, Selected: old.Selected, Running: old.Running,
			Folder: old.Folder, Mods: old.Mods, Options: old.Options}
	} else {
		ws, orphans, err := inst.srv.Worlds().List()
		if err != nil {
			return GameWorldsView{}, err
		}
		saved := inst.saved()
		v = GameWorldsView{Worlds: ws, Orphans: orphans, Selected: saved.World, Folder: inst.dirs.Saves}
		m.mu.Lock()
		if inst.run != nil {
			v.Running = inst.run.rec.World
		}
		m.mu.Unlock()
	}
	if v.Worlds == nil {
		v.Worlds = []worlds.World{}
	}
	if v.Orphans == nil {
		v.Orphans = []worlds.Orphan{}
	}
	v.Schema = WorldSchemas{World: game.Schema{}, NewWorld: game.Schema{}}
	for _, f := range inst.srv.Schema() {
		switch f.Scope {
		case game.WorldScope:
			v.Schema.World = append(v.Schema.World, f)
		case game.NewWorldScope:
			v.Schema.NewWorld = append(v.Schema.NewWorld, f)
		}
	}
	v.Values = map[string]game.Values{}
	for name, w := range inst.saved().Worlds {
		v.Values[name] = w.Clone()
	}
	return v, nil
}

// SelectWorldGame makes name the world the game's next start uses (and
// restarts it with that world when restart is set).
func (m *Manager) SelectWorldGame(id, name string, restart bool) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.SelectWorld(name, restart)
	}
	name = strings.TrimSpace(name)
	if msg := inst.srv.Worlds().NameProblem(name); msg != "" {
		return errors.New(msg)
	}
	if err := inst.srv.Store().Save(func(s *game.Saved) error { s.World = name; return nil }); err != nil {
		return err
	}
	m.mu.Lock()
	running := inst.run != nil && inst.run.rec.World != name
	m.mu.Unlock()
	m.instActivity(inst, "world", "", fmt.Sprintf("%q is now the selected world of %s.", name, inst.short()))
	if running && restart {
		return m.restartGame(inst)
	}
	return nil
}

// CreateWorldGame makes a new world from its new-world values; the server
// makes it at its first start. The values (world and new-world fields)
// are kept with the world, so a first start that fails keeps them.
func (m *Manager) CreateWorldGame(id, name string, values game.Values, restart bool) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	lib := inst.srv.Worlds()
	if msg := lib.NameProblem(name); msg != "" {
		return errors.New(msg)
	}
	if lib.Exists(name) {
		return fmt.Errorf("there's already a world called %q", name)
	}
	w, err := worldValues(inst, values, true)
	if err != nil {
		return err
	}
	// Set even when none were chosen: values kept under the name of a
	// deleted world mustn't become the new one's.
	if err := inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Worlds == nil {
			s.Worlds = map[string]game.Values{}
		}
		s.Worlds[name] = w
		return nil
	}); err != nil {
		return err
	}
	return m.SelectWorldGame(id, name, restart)
}

// worldValues checks values for a world: only world-scope keys (and, for a
// new world, new-world ones) of the game's schema.
func worldValues(inst *instance, values game.Values, isNew bool) (game.Values, error) {
	known := map[string]bool{}
	for _, f := range inst.srv.Schema() {
		if f.Scope == game.WorldScope || isNew && f.Scope == game.NewWorldScope {
			known[f.Key] = true
		}
	}
	errs := game.FieldErrors{}
	out := game.Values{}
	for k, v := range values {
		if !known[k] {
			errs[k] = "This world has no such setting."
			continue
		}
		out[k] = v
	}
	if len(errs) > 0 {
		if isNew {
			return nil, prefixed("newWorld.", errs)
		}
		return nil, prefixed("world.", errs)
	}
	return out, nil
}

// SetWorldValuesGame saves a world's world-scope values (they apply at the
// next start). Values a new world was asked for stay with it.
func (m *Manager) SetWorldValuesGame(id, name string, values game.Values) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	w, err := worldValues(inst, values, false)
	if err != nil {
		return err
	}
	saved := inst.saved()
	next := saved.Clone()
	if next.Worlds == nil {
		next.Worlds = map[string]game.Values{}
	}
	merged := next.Worlds[name].Clone()
	if merged == nil {
		merged = game.Values{}
	}
	maps.Copy(merged, w)
	next.Worlds[name] = merged
	if errs := inst.srv.Validate(next, name); len(errs) > 0 {
		mine := game.FieldErrors{}
		for k, msg := range errs {
			if _, ok := w[k]; ok {
				mine[k] = msg
			}
		}
		if len(mine) > 0 {
			return prefixed("world.", mine)
		}
	}
	return inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Worlds == nil {
			s.Worlds = map[string]game.Values{}
		}
		cur := s.Worlds[name].Clone()
		if cur == nil {
			cur = game.Values{}
		}
		maps.Copy(cur, w)
		s.Worlds[name] = cur
		return nil
	})
}

// DeleteWorldGame deletes a world (a backup copy is kept).
func (m *Manager) DeleteWorldGame(id, name string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.DeleteWorld(name)
	}
	if strings.EqualFold(inst.saved().World, name) {
		return errors.New("This is the selected world. Pick a different world to play before deleting it.")
	}
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	return m.runInstJob(inst, "delete", "Deleting "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Keeping a backup copy, then deleting…", -1)
		if err := inst.srv.Worlds().Delete(name); err != nil {
			return err
		}
		m.instActivity(inst, "delete", "", fmt.Sprintf("%q was deleted. A backup copy is kept on the Worlds page.", name))
		j.result("The world was deleted. You can bring it back from its backup.")
		return nil
	})
}

// BackupWorldGame backs a world up now (while it runs, too).
func (m *Manager) BackupWorldGame(id, name string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.BackupWorld(name)
	}
	return m.runInstJob(inst, "backup", "Backing up "+name, false, func(_ ctxT, j *jobCtl) error {
		j.step("Copying the world into a backup file…", -1)
		b, err := inst.srv.Worlds().Backup(name, "manual")
		if err != nil {
			return err
		}
		m.instActivity(inst, "backup", "", fmt.Sprintf("A backup of %q was made.", name))
		j.result(fmt.Sprintf("Backup saved (%s).", humanBytes(b.Size)))
		return nil
	})
}

// RestoreWorldGame puts a world back as one of its backups has it; the
// current world is backed up first. Not while the world runs.
func (m *Manager) RestoreWorldGame(id, name, backupID string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.RestoreWorld(name, backupID)
	}
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	return m.runInstJob(inst, "restore", "Restoring "+name, false, func(_ ctxT, j *jobCtl) error {
		if err := m.checkRunning(inst, name); err != nil {
			return err
		}
		j.step("Keeping a copy of the current world, then restoring the backup…", -1)
		if err := inst.srv.Worlds().Restore(name, backupID); err != nil {
			return err
		}
		m.instActivity(inst, "restore", "", fmt.Sprintf("%q was restored from a backup.", name))
		j.result("The backup was restored.")
		return nil
	})
}

// RenameWorldGame renames a world where the game allows it; its selection
// and values follow it.
func (m *Manager) RenameWorldGame(id, name, newName string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.RenameWorld(name, newName)
	}
	if err := m.checkWorldFree(inst, name); err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	if err := inst.srv.Worlds().Rename(name, newName); err != nil {
		if errors.Is(err, game.ErrCantRename) {
			return fmt.Errorf("%s worlds can't be renamed here.", inst.short())
		}
		return err
	}
	err = inst.srv.Store().Save(func(s *game.Saved) error {
		if s.World == name {
			s.World = newName
		}
		// Values kept under the new name belong to a deleted world.
		delete(s.Worlds, newName)
		if w, ok := s.Worlds[name]; ok {
			delete(s.Worlds, name)
			s.Worlds[newName] = w
		}
		return nil
	})
	m.instActivity(inst, "world", "", fmt.Sprintf("%q was renamed to %q.", name, newName))
	return err
}

// PrepareDownloadGame makes a world's download: a zip file the caller
// deletes, and its name for the browser.
func (m *Manager) PrepareDownloadGame(id, name string) (path, filename string, err error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return "", "", err
	}
	if inst.legacy {
		return m.PrepareDownload(name)
	}
	staging := inst.dirs.Staging
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", "", err
	}
	path, err = inst.srv.Worlds().ExportFile(name, staging)
	if err != nil {
		return "", "", fmt.Errorf("Couldn't pack %q: %v", name, err)
	}
	return path, fmt.Sprintf("%s %s %s.zip", inst.short(), name, timeNow().Format("2006-01-02")), nil
}

// ExportBackupGame writes one of a world's backups to w.
func (m *Manager) ExportBackupGame(id string, w io.Writer, name, backupID string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	return inst.srv.Worlds().ExportBackup(w, name, backupID)
}

// --- players and lists ----------------------------------------------------------

// KnownPlayersGame lists everyone who has played the game, most recent
// first.
func (m *Manager) KnownPlayersGame(id string) ([]KnownPlayer, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KnownPlayer, 0, len(inst.known))
	for _, p := range inst.known {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b KnownPlayer) int { return b.LastSeen.Compare(a.LastSeen) })
	return out, nil
}

// ListsGame returns a game's player lists; none for a game without them.
func (m *Manager) ListsGame(id string) ([]GameList, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	out := []GameList{}
	l, ok := inst.srv.(game.Lister)
	if !ok {
		return out, nil
	}
	for _, k := range l.Lists() {
		ids, err := l.ReadList(k.ID)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []string{}
		}
		out = append(out, GameList{Kind: k, IDs: ids})
	}
	return out, nil
}

// SetListGame replaces one of a game's player lists: live when the game
// can and it runs, else at its next start.
func (m *Manager) SetListGame(id, kind string, ids []string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.SetList(config.ListKind(kind), ids)
	}
	l, ok := inst.srv.(game.Lister)
	if !ok {
		return errors.New("This game has no player lists.")
	}
	i := slices.IndexFunc(l.Lists(), func(k game.ListKind) bool { return k.ID == kind })
	if i < 0 {
		return errors.New("unknown list")
	}
	lk := l.Lists()[i]
	rec, running := m.runRecord(inst)
	if err := l.WriteList(context.Background(), rec, kind, ids); err != nil {
		return err
	}
	when := " Changes apply the next time the server starts."
	if lk.Live && running {
		when = ""
	}
	m.instActivity(inst, "info", "", fmt.Sprintf("The %s list of %s was updated.%s", strings.ToLower(lk.Label), inst.short(), when))
	return nil
}

// runRecord is a copy of the running server's record, or nil.
func (m *Manager) runRecord(inst *instance) (*game.Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.run == nil {
		return nil, false
	}
	r := inst.run.rec
	return &r, true
}

// allowedList is the id of the list a turned-away player is added to.
const allowedList = "allowed"

// AllowPlayer adds a turned-away player to the game's allow list, with one
// click (POST /api/games/{id}/allow, spec 5.2): live when the game can.
func (m *Manager) AllowPlayer(id, name string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	l, ok := inst.srv.(game.Lister)
	if !ok || name == "" || !slices.ContainsFunc(l.Lists(), func(k game.ListKind) bool { return k.ID == allowedList }) {
		return errors.New("This game has no allow list.")
	}
	ids, err := l.ReadList(allowedList)
	if err != nil {
		return err
	}
	if slices.Contains(ids, name) {
		return nil
	}
	rec, _ := m.runRecord(inst)
	if err := l.WriteList(context.Background(), rec, allowedList, append(ids, name)); err != nil {
		return err
	}
	m.instActivity(inst, "info", name, fmt.Sprintf("%s was added to the allow list of %s.", name, inst.short()))
	return nil
}

// --- the log ---------------------------------------------------------------

// LogsGame returns the game's server log lines after seq (oldest first).
func (m *Manager) LogsGame(id string, after int64, limit int) ([]LogLine, int64, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, 0, err
	}
	lines, last := inst.logs.after(after, limit)
	return lines, last, nil
}

// CurrentLogPathGame is the log file of the game's running (or last)
// server: for a game that writes its own log, the app's copy of it in the
// game's logs folder.
func (m *Manager) CurrentLogPathGame(id string) (string, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return "", err
	}
	if inst.legacy {
		return m.CurrentLogPath(), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.run != nil {
		return inst.run.logFile(), nil
	}
	return newestLog(inst, inst.dirs.Logs), nil
}
