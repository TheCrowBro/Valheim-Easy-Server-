package manager

// The per-game public methods (spec 4.1 and 6.2) that the new HTTP routes
// call, and the instance helpers the R3 streams share. Every action takes
// an instance id; the old method names (StartServer, Worlds…) stay as the
// legacy game's, for the old routes, the tray and main.go.
//
// Phase 0' made these with their signatures so the API stream can be
// written against them at once (stream M3 owns this file from then on).
// Where the legacy game already has the same method, the …Game form calls
// it for the legacy instance; for any other instance, and where no legacy
// method exists yet, it answers errNotYet until M3 (or the stream named in
// the comment) fills it in. Nothing here changes what the legacy methods do.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// errNotYet answers a request for something R3 hasn't built yet.
var errNotYet = errors.New("This part of the app isn't finished yet.")

// errNoGame answers a request for an instance this computer doesn't have.
func errNoGame(id string) error {
	return fmt.Errorf("There's no game %q on this computer.", id)
}

// instanceByID finds an instance. In R2 and phase 0' the instances are made
// in New and never change, so it reads them without m.mu; M3 guards them
// once adding and removing a game change them.
func (m *Manager) instanceByID(id string) (*instance, error) {
	if inst := m.instances[id]; inst != nil {
		return inst, nil
	}
	return nil, errNoGame(id)
}

// newGameInstance makes the instance for a servers.json entry of a
// non-legacy game: its folders under games/<id>, its servers.json-backed
// game.Store, and its game.Env (Note, and Fake under EGS_DEV_FAKE_SERVERS).
// An entry whose game this build doesn't run gets a foreign instance (srv
// nil, foreign saying why). It does no work on disk, so New stays quick
// (I5). A's AddGame and M3's New use it.
func (m *Manager) newGameInstance(in servers.Instance) (*instance, error) {
	return nil, errNotYet
}

// addInstanceLocked puts a new instance in m.instances and m.order (Add a
// game); removeInstanceLocked takes one out (removing a game). M3 makes
// readers of the instances safe for this first.
func (m *Manager) addInstanceLocked(inst *instance) {}

func (m *Manager) removeInstanceLocked(id string) {}

// legacyOnly runs fn when id is the legacy instance, which already has the
// method; any other instance gets errNotYet until M3 builds the general one.
func (m *Manager) legacyOnly(id string, fn func() error) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	if !inst.legacy {
		return errNotYet
	}
	return fn()
}

// --- starting and stopping ----------------------------------------------------

// StartOptions say how a start was asked for (spec 4.1 and 4.3).
type StartOptions struct {
	// Memory is "" to check that the game fits in the free memory first,
	// "anyway" to skip that check, or "stop:<instance>" to stop that
	// instance first (inside the start job), then start.
	Memory string `json:"memory"`
	// Source is who asked: SourcePanel, SourceDiscord, SourceWake,
	// SourceApp, SourceRestart or SourceResume. A start from the panel
	// checks memory; one from Discord or a wake that doesn't fit is refused
	// with a reply (6.4); a restart never checks.
	Source string `json:"-"`
}

// Where a start comes from (StartOptions.Source).
const (
	SourcePanel   = "panel"
	SourceDiscord = "discord"
	SourceWake    = "wake"
	SourceApp     = "app"     // start with the app (2.9)
	SourceRestart = "restart" // a restart after a crash
	SourceResume  = "resume"  // the resume after waking (4.7)
)

// StartGame starts an instance's server (POST /api/games/{id}/start).
func (m *Manager) StartGame(id string, o StartOptions) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	return m.startGameWith(inst, o)
}

// startGameWith is the one path every start takes in R3: the panel,
// Discord, a wake, start-with-app, a crash restart and the resume (spec
// 4.3). It is the legacy start's checks (server.go's startChecks: steps 1
// and 2), then the memory step (3), which is stream A's in memory.go:
// memoryForStartLocked before the job (the check, unless o.Memory is
// "anyway" or o.Source is SourceRestart) and memoryInStartJob inside it
// (stopping the game "stop:<id>" names), then the start job (4). M3 adds
// the rest of step 1 to the checks (foreign, hidden, disabled, can't run
// on this computer, which never applies to the legacy instance: it has
// always run wherever the app does) and moves the other starts here.
// StartServer doesn't come this way, so the old routes and the tray start
// Valheim exactly as before.
func (m *Manager) startGameWith(inst *instance, o StartOptions) error {
	if err := m.startChecks(inst); err != nil {
		return err
	}
	m.mu.Lock()
	err := m.memoryForStartLocked(inst, o)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.runStartJob(inst, func(ctx context.Context, j *jobCtl) error {
		return m.memoryInStartJob(ctx, j, inst, o)
	})
}

// StopGame saves the world and stops an instance's server.
func (m *Manager) StopGame(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	return m.stopGame(inst)
}

// RestartGame stops an instance's server and starts it again.
func (m *Manager) RestartGame(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	return m.restartGame(inst)
}

// ForceStopGame ends an instance's server process at once, without saving.
func (m *Manager) ForceStopGame(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	return m.forceStopGame(inst)
}

// InstallGame installs, updates or (repair) re-checks an instance's server.
func (m *Manager) InstallGame(id string, repair bool) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	return m.installGame(inst, repair)
}

// CancelGameJob cancels the instance's job, if it can be cancelled.
func (m *Manager) CancelGameJob(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !running(inst.job) {
		return nil
	}
	if !inst.job.Cancellable {
		return errors.New("This can't be cancelled at this point.")
	}
	inst.jobCancel()
	return nil
}

// StopAll saves and stops every running game, each in its own job, so they
// stop at the same time (the tray's "Stop all games", POST
// /api/power/stop-all). reason goes to the log. It returns the first
// refusal, after asking every game. M3 makes it wait for the app job and
// write one activity entry (6.6).
func (m *Manager) StopAll(reason string) error {
	if reason != "" {
		m.log.Printf("stopping every game: %s", reason)
	}
	var errs []error
	for _, id := range m.order {
		inst := m.instances[id]
		m.mu.Lock()
		on := inst.run != nil
		m.mu.Unlock()
		if on {
			if err := m.stopGame(inst); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// --- stars, the sidebar, turning a game off ----------------------------------

// SetStar stars or unstars a game (POST /api/games/{id}/star): starred
// games are under Active and get their Discord command (6.4).
func (m *Manager) SetStar(id string, on bool) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet // M3: servers.json app.stars, then WK's starsChanged
}

// SetSidebar remembers which of the sidebar's lists are open
// (POST /api/app/sidebar).
func (m *Manager) SetSidebar(activeOpen, inactiveOpen bool) error {
	return errNotYet // M3: servers.json app.activeOpen/inactiveOpen
}

// starsLocked are the starred instances, in the order they were starred.
func (m *Manager) starsLocked() []string {
	return append([]string{}, m.servers.App.Stars...)
}

// isStarredLocked reports whether the instance is starred.
func (m *Manager) isStarredLocked(inst *instance) bool {
	return slices.Contains(m.servers.App.Stars, inst.id)
}

// isDisabledLocked reports whether the owner turned the game off
// (servers.json app.disabledGames, 8.3): it can't start, isn't resumed and
// gets no Discord command.
func (m *Manager) isDisabledLocked(inst *instance) bool {
	return slices.Contains(m.servers.App.DisabledGames, inst.id)
}

// isHiddenLocked reports whether the legacy game was removed from the panel
// (servers.json "hidden", I1): stopped, unstarred, never started by
// anything.
func (m *Manager) isHiddenLocked(inst *instance) bool {
	if !inst.legacy {
		return false
	}
	in := m.servers.Instance(inst.id)
	return in != nil && in.Hidden
}

// canRunLocked says whether the instance's server runs on this computer,
// and why not (spec 2.10): Info.OS, then the adapter's own run-time answer
// (game.Supporter). M3 adds fake mode, which ignores both.
func (m *Manager) canRunLocked(inst *instance) (game.Level, string) {
	l, why := inst.info.LevelOn(runtime.GOOS, runtime.GOARCH)
	if l == game.Runs && inst.srv != nil {
		if s, ok := inst.srv.(game.Supporter); ok {
			return s.Support()
		}
	}
	return l, why
}

// presenceProvenLocked reports whether the instance's player count can be
// trusted to stop it or let the computer sleep (spec 2.6): the legacy
// instance's always can; another's once its probe has answered with a
// count, or once this install has seen its first join (presenceSeen). M3
// adds the last two; A's auto-stop and idle sleep and D's ask rule use it.
func (m *Manager) presenceProvenLocked(inst *instance) bool {
	return inst.legacy
}

// portChosenLocked reports whether the owner chose the role's port, so the
// app never picks another (spec 2.5). The legacy game's ports are always
// the owner's: they come from its own settings.
func (m *Manager) portChosenLocked(inst *instance, role string) bool {
	if inst.legacy {
		return true
	}
	in := m.servers.Instance(inst.id)
	return in != nil && slices.Contains(in.PortsChosen, role)
}

// updateServers changes servers.json (spec 3.2) and the manager's copy:
// load, change, back up, write. It runs without m.mu and refuses in safe
// mode. M3 builds it; until then nothing writes servers.json, as in R2.
func (m *Manager) updateServers(change func(*servers.File) error) error {
	return errNotYet
}

// --- settings ---------------------------------------------------------------

// GameSettingsView is a game's Settings tab
// (GET /api/games/{id}/settings).
type GameSettingsView struct {
	Schema          game.Schema         `json:"schema"`
	Values          GameValues          `json:"values"`
	Ports           []PortSetting       `json:"ports"`
	AutoStopMinutes int                 `json:"autoStopMinutes"` // 0 = never
	StartWithApp    bool                `json:"startWithApp"`
	Disabled        bool                `json:"disabled"`
	Discord         GameDiscordSettings `json:"discord"` // discord.go (D)
}

// GameValues are an instance's settings by scope.
type GameValues struct {
	Server game.Values            `json:"server"`
	Worlds map[string]game.Values `json:"worlds"`
	World  string                 `json:"world"`
}

// PortSetting is one of a game's ports on its Settings tab.
type PortSetting struct {
	Role   string   `json:"role"`
	Protos []string `json:"protos"`
	Port   int      `json:"port"`
	Public bool     `json:"public"`
	Chosen bool     `json:"chosen"` // the owner typed it: never picked again
}

// GameSettingsChange is what POST /api/games/{id}/settings changes: only
// the parts that are set, merged on the server, so a page that is out of
// date never blanks a field.
type GameSettingsChange struct {
	Values          game.Values        `json:"values,omitempty"` // server scope, by key
	Ports           map[string]int     `json:"ports,omitempty"`  // by role: these become chosen (A's ports.go checks them)
	AutoStopMinutes *int               `json:"autoStopMinutes,omitempty"`
	StartWithApp    *bool              `json:"startWithApp,omitempty"`
	Disabled        *bool              `json:"disabled,omitempty"` // turning a running game off stops it first
	Discord         *GameDiscordChange `json:"discord,omitempty"`  // discord.go (D)
	// EULA ticks the game's licence box (game.Licensed) for an instance that
	// lost it, such as one rebuilt from its folders (3.4).
	EULA *bool `json:"eula,omitempty"`
}

// GameSettings is a game's Settings tab.
func (m *Manager) GameSettings(id string) (GameSettingsView, error) {
	if _, err := m.instanceByID(id); err != nil {
		return GameSettingsView{}, err
	}
	return GameSettingsView{}, errNotYet
}

// UpdateGameSettings saves what changed on a game's Settings tab. Field
// errors come back keyed "server.<key>" (2.9).
func (m *Manager) UpdateGameSettings(id string, ch GameSettingsChange) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet
}

// AppSettingsChange is what POST /api/app/settings changes: the app-wide
// settings, only the parts that are set. All but RouterPorts are in
// settings.json, with the meanings they have today; RouterPorts is
// servers.json app.routerPorts (N reacts through routerPortsChanged).
type AppSettingsChange struct {
	IdleHibernate  *bool `json:"idleHibernate,omitempty"`
	IdleMinutes    *int  `json:"idleMinutes,omitempty"`
	AutoUpdate     *bool `json:"autoUpdate,omitempty"`
	StartAfterWake *bool `json:"startAfterWake,omitempty"`
	RouterPorts    *bool `json:"routerPorts,omitempty"`
}

// UpdateAppSettings saves the App settings page.
func (m *Manager) UpdateAppSettings(ch AppSettingsChange) error {
	return errNotYet
}

// --- worlds -----------------------------------------------------------------

// GameWorldsView is a game's Worlds tab (GET /api/games/{id}/worlds).
type GameWorldsView struct {
	Worlds   []worlds.World                 `json:"worlds"`
	Orphans  []worlds.Orphan                `json:"orphans"`
	Selected string                         `json:"selected"`
	Running  string                         `json:"running"`
	Folder   string                         `json:"folder"`
	Schema   WorldSchemas                   `json:"schema"`
	Values   map[string]game.Values         `json:"values"` // each world's world-scope values
	Mods     []ModInfo                      `json:"mods,omitempty"`
	Options  map[string]config.WorldOptions `json:"options,omitempty"` // the legacy game's, as the old Worlds page has them
}

// WorldSchemas are the fields of a world and of a new one.
type WorldSchemas struct {
	World    game.Schema `json:"world"`
	NewWorld game.Schema `json:"newWorld"`
}

// WorldsGame lists a game's worlds and their backups.
func (m *Manager) WorldsGame(id string) (GameWorldsView, error) {
	if _, err := m.instanceByID(id); err != nil {
		return GameWorldsView{}, err
	}
	return GameWorldsView{}, errNotYet
}

// SelectWorldGame makes name the world the game's next start uses (and
// restarts it with that world when restart is set).
func (m *Manager) SelectWorldGame(id, name string, restart bool) error {
	return m.legacyOnly(id, func() error { return m.SelectWorld(name, restart) })
}

// CreateWorldGame makes a new world from its new-world values; the server
// makes it at its first start.
func (m *Manager) CreateWorldGame(id, name string, values game.Values, restart bool) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet
}

// SetWorldValuesGame saves a world's world-scope values.
func (m *Manager) SetWorldValuesGame(id, name string, values game.Values) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet
}

// DeleteWorldGame deletes a world (its backups stay).
func (m *Manager) DeleteWorldGame(id, name string) error {
	return m.legacyOnly(id, func() error { return m.DeleteWorld(name) })
}

// BackupWorldGame backs a world up now.
func (m *Manager) BackupWorldGame(id, name string) error {
	return m.legacyOnly(id, func() error { return m.BackupWorld(name) })
}

// RestoreWorldGame puts a world back as one of its backups has it.
func (m *Manager) RestoreWorldGame(id, name, backupID string) error {
	return m.legacyOnly(id, func() error { return m.RestoreWorld(name, backupID) })
}

// RenameWorldGame renames a world where the game allows it.
func (m *Manager) RenameWorldGame(id, name, newName string) error {
	return m.legacyOnly(id, func() error { return m.RenameWorld(name, newName) })
}

// PrepareDownloadGame makes a world's download.
func (m *Manager) PrepareDownloadGame(id, name string) (path, filename string, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		path, filename, e = m.PrepareDownload(name)
		return e
	})
	return path, filename, err
}

// ExportBackupGame writes one of a world's backups to w.
func (m *Manager) ExportBackupGame(id string, w io.Writer, name, backupID string) error {
	return m.legacyOnly(id, func() error { return m.ExportBackup(w, name, backupID) })
}

// --- uploads and the owner's own worlds ---------------------------------------

// NewUploadGame starts an upload of worlds for the game.
func (m *Manager) NewUploadGame(id string) (u *worlds.Upload, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		u, e = m.NewUpload()
		return e
	})
	return u, err
}

// UploadGame is one of the game's uploads.
func (m *Manager) UploadGame(id, uploadID string) (u *worlds.Upload, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		u, e = m.Upload(uploadID)
		return e
	})
	return u, err
}

// FinishUploadGame unpacks an upload and lists the worlds in it.
func (m *Manager) FinishUploadGame(id, uploadID string) (c []worlds.Candidate, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		c, e = m.FinishUpload(uploadID)
		return e
	})
	return c, err
}

// DiscardUploadGame throws an upload away.
func (m *Manager) DiscardUploadGame(id, uploadID string) error {
	return m.legacyOnly(id, func() error {
		m.DiscardUpload(uploadID)
		return nil
	})
}

// ImportUploadGame imports the chosen worlds of an upload, as a job.
func (m *Manager) ImportUploadGame(id, uploadID string, choices []worlds.Choice) (job int, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		job, e = m.ImportUpload(uploadID, choices)
		return e
	})
	return job, err
}

// LocalWorldsGame lists the worlds in the owner's own copy of the game on
// this computer.
func (m *Manager) LocalWorldsGame(id string) (c []worlds.Candidate, err error) {
	err = m.legacyOnly(id, func() error {
		c = m.LocalWorlds()
		return nil
	})
	return c, err
}

// ImportLocalGame imports the chosen worlds of the owner's own game.
func (m *Manager) ImportLocalGame(id string, choices []worlds.Choice) (job int, err error) {
	err = m.legacyOnly(id, func() error {
		var e error
		job, e = m.ImportLocal(choices)
		return e
	})
	return job, err
}

// --- players and lists ----------------------------------------------------------

// KnownPlayersGame lists everyone who has played the game, most recent
// first.
func (m *Manager) KnownPlayersGame(id string) (p []KnownPlayer, err error) {
	err = m.legacyOnly(id, func() error {
		p = m.KnownPlayers()
		return nil
	})
	return p, err
}

// GameList is one of a game's player lists (GET /api/games/{id}/lists).
type GameList struct {
	Kind game.ListKind `json:"kind"`
	IDs  []string      `json:"ids"`
}

// ListsGame returns a game's player lists; none for a game without them.
func (m *Manager) ListsGame(id string) ([]GameList, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return nil, err
	}
	if !inst.legacy {
		return nil, errNotYet
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

// SetListGame replaces one of a game's player lists (live when it can).
func (m *Manager) SetListGame(id, kind string, ids []string) error {
	return m.legacyOnly(id, func() error { return m.SetList(config.ListKind(kind), ids) })
}

// AllowPlayer adds a turned-away player to the game's allow list, with one
// click (POST /api/games/{id}/allow, spec 5.2).
func (m *Manager) AllowPlayer(id, name string) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet
}

// --- the log ---------------------------------------------------------------

// LogsGame returns the game's server log lines after seq (oldest first).
func (m *Manager) LogsGame(id string, after int64, limit int) (lines []LogLine, last int64, err error) {
	err = m.legacyOnly(id, func() error {
		lines, last = m.Logs(after, limit)
		return nil
	})
	return lines, last, err
}

// CurrentLogPathGame is the log file of the game's running (or last)
// server.
func (m *Manager) CurrentLogPathGame(id string) (path string, err error) {
	err = m.legacyOnly(id, func() error {
		path = m.CurrentLogPath()
		return nil
	})
	return path, err
}
