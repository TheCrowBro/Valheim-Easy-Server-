package manager

// The per-game public methods (spec 4.1 and 6.2) that the new HTTP routes
// call, and the instance helpers the R3 streams share. Every action takes
// an instance id; the old method names (StartServer, Worlds…) stay as the
// legacy game's, for the old routes, the tray and main.go.
//
// Phase 0' made these with their signatures so the API stream could be
// written against them at once; stream M3 owns this file. Where the legacy
// game already has the same method, the …Game form calls it for the legacy
// instance, so it behaves exactly as on the old routes; every other
// instance goes through its adapter and its servers.json entry
// (gamemethods.go has the settings, worlds, players, lists and log). A
// foreign instance (srv nil) answers why this version can't use it.
// Nothing here changes what the legacy methods do.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// errNotYet answers a request for something R3 hasn't built yet.
var errNotYet = errors.New("This part of the app isn't finished yet.")

// errNoGame answers a request for an instance this computer doesn't have.
func errNoGame(id string) error {
	return fmt.Errorf("There's no game %q on this computer.", id)
}

// instanceByID finds an instance, foreign ones too (inst.srv is nil for
// them). It may be called with or without m.mu.
func (m *Manager) instanceByID(id string) (*instance, error) {
	if inst := m.insts.Load().byID[id]; inst != nil {
		return inst, nil
	}
	return nil, errNoGame(id)
}

// runningInstance is instanceByID for an action that needs the game's
// server: a foreign instance says why this version can't do it.
func (m *Manager) runningInstance(id string) (*instance, error) {
	inst, err := m.instanceByID(id)
	if err == nil && inst.srv == nil {
		err = foreignError(inst)
	}
	return inst, err
}

// The reasons a servers.json entry is foreign (instance.foreign).
const (
	foreignUnknown = "This version of the app doesn't know this game: a newer version added it."
	foreignHidden  = "This game is switched off in this version of the app."
	foreignSecond  = "This version of the app runs only one server of this game."
)

func foreignError(inst *instance) error {
	return fmt.Errorf("%s can't be used here. %s", inst.short(), inst.foreign)
}

// newGameInstance makes the instance for a servers.json entry of a
// non-legacy game: its folders under games/<id>, its servers.json-backed
// game.Store, and its game.Env (Note, and Fake under EGS_DEV_FAKE_SERVERS).
// An entry whose game this build doesn't run gets a foreign instance (srv
// nil, foreign saying why): a game it doesn't know, one whose stage is
// hidden in this build (unless EGS_DEV_ALL_STAGES=1), or a second server of
// the legacy game, whose files can only be the legacy ones. It does no work
// on disk, so New stays quick (I5). A's AddGame and M3's New use it; the
// caller adds it with addInstanceLocked.
func (m *Manager) newGameInstance(in servers.Instance) (*instance, error) {
	if !servers.ValidID(in.ID) {
		return nil, fmt.Errorf("the instance id %q can't be a folder name", in.ID)
	}
	if in.ID == m.legacyID {
		return nil, fmt.Errorf("%q is the main game's own instance", in.ID)
	}
	inst := &instance{id: in.ID, dirs: m.gameDirs(in.ID), known: map[string]*KnownPlayer{},
		uploads: map[string]*worlds.Upload{}, logs: newRing[LogLine](3000),
		recs: recordsData{Runs: map[string][]records.Event{}}}
	g, ok := m.gameByID(in.Game)
	if !ok {
		short := in.Game
		if short == "" {
			short = in.ID
		}
		inst.info = game.Info{ID: in.Game, Name: short, Short: short, Letter: firstLetter(short), Color: "#6b7280"}
		inst.foreign = foreignUnknown
		return inst, nil
	}
	inst.g, inst.info = g, g.Info()
	switch {
	case inst.info.Legacy || in.Legacy:
		inst.foreign = foreignSecond
		return inst, nil
	case inst.info.Stage == game.Hidden && !m.allStages:
		inst.foreign = foreignHidden
		return inst, nil
	case inst.info.Stage == game.Hidden:
		inst.info.Stage = game.Preview // EGS_DEV_ALL_STAGES: shown as a preview
	}
	inst.srv = g.New(game.Env{Instance: in.ID, Data: m.opt.DataDir, Dirs: inst.dirs, Log: m.log, HTTP: gameClient,
		Store: serversStore{m: m, id: in.ID}, Fake: m.fake,
		Note: func(kind, text string) { m.instActivity(inst, kind, "", text) }})
	return inst, nil
}

// addInstanceLocked puts a new instance in m.instances and m.order (Add a
// game); removeInstanceLocked takes one out (removing a game; never the
// legacy one, which is only hidden). Both make new lists, so code reading
// the old ones without m.mu is never disturbed.
func (m *Manager) addInstanceLocked(inst *instance) {
	if _, ok := m.instances[inst.id]; ok {
		return
	}
	byID := make(map[string]*instance, len(m.instances)+1)
	for id, i := range m.instances {
		byID[id] = i
	}
	byID[inst.id] = inst
	m.setInstancesLocked(byID, append(slices.Clone(m.order), inst.id))
}

func (m *Manager) removeInstanceLocked(id string) {
	gone, ok := m.instances[id]
	if !ok || id == m.legacyID {
		return
	}
	delete(m.disc.recsRead, gone) // D's note of its records file; a game added again is read again
	byID := make(map[string]*instance, len(m.instances))
	for k, i := range m.instances {
		if k != id {
			byID[k] = i
		}
	}
	m.setInstancesLocked(byID, slices.DeleteFunc(slices.Clone(m.order), func(k string) bool { return k == id }))
}

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
	inst, err := m.runningInstance(id)
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
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	return m.stopGame(inst)
}

// RestartGame stops an instance's server and starts it again.
func (m *Manager) RestartGame(id string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	return m.restartGame(inst)
}

// ForceStopGame ends an instance's server process at once, without saving.
func (m *Manager) ForceStopGame(id string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	return m.forceStopGame(inst)
}

// InstallGame installs, updates or (repair) re-checks an instance's server.
func (m *Manager) InstallGame(id string, repair bool) error {
	inst, err := m.runningInstance(id)
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
// /api/power/stop-all). reason goes to the log. While the app's own job
// runs (going to sleep, which stops every game itself) it is refused with
// that job's BusyError, so the caller waits for it (main's stopAndWait).
// Otherwise it writes one activity entry and returns the first refusal,
// after asking every game.
func (m *Manager) StopAll(reason string) error {
	if reason != "" {
		m.log.Printf("stopping every game: %s", reason)
	}
	var on []*instance
	m.mu.Lock()
	if running(m.appJob) {
		t := m.appJob.Title
		m.mu.Unlock()
		return &BusyError{Title: lowerFirst(t)}
	}
	m.eachInstance(func(inst *instance) {
		if inst.run != nil && inst.srv != nil && !inst.run.stopping {
			on = append(on, inst)
		}
	})
	m.mu.Unlock()
	if len(on) == 0 {
		return nil
	}
	names := make([]string, len(on))
	for i, inst := range on {
		names[i] = inst.short()
	}
	m.addActivity("stopped", "", "Saving and stopping every game that is running: "+strings.Join(names, ", ")+".")
	var errs []error
	for _, inst := range on {
		if err := m.stopGame(inst); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// --- stars, the sidebar, turning a game off ----------------------------------

// SetStar stars or unstars a game (POST /api/games/{id}/star): starred
// games are under Active and get their Discord command (6.4). A removed
// (hidden) legacy game is added back first, never starred where it is
// hidden.
func (m *Manager) SetStar(id string, on bool) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	hidden := m.isHiddenLocked(inst)
	m.mu.Unlock()
	if hidden && on {
		return hiddenError(inst)
	}
	changed := false
	err = m.updateServers(func(f *servers.File) error {
		has := slices.Contains(f.App.Stars, id)
		switch {
		case on && !has:
			f.App.Stars = append(f.App.Stars, id)
			changed = true
		case !on && has:
			f.App.Stars = slices.DeleteFunc(f.App.Stars, func(s string) bool { return s == id })
			changed = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		m.go_(m.starsChanged) // WK: the waker's commands follow the stars
	}
	return nil
}

// SetSidebar remembers which of the sidebar's lists are open
// (POST /api/app/sidebar).
func (m *Manager) SetSidebar(activeOpen, inactiveOpen bool) error {
	return m.updateServers(func(f *servers.File) error {
		f.App.ActiveOpen, f.App.InactiveOpen = &activeOpen, &inactiveOpen
		return nil
	})
}

// starsLocked are the starred instances, in the order they were starred.
// A hidden legacy game and ids no instance has are left out.
func (m *Manager) starsLocked() []string {
	out := []string{}
	for _, id := range m.servers.App.Stars {
		if inst := m.instances[id]; inst != nil && !m.isHiddenLocked(inst) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// isStarredLocked reports whether the instance is starred (never a hidden
// legacy game, I1).
func (m *Manager) isStarredLocked(inst *instance) bool {
	return slices.Contains(m.servers.App.Stars, inst.id) && !m.isHiddenLocked(inst)
}

// firstLetter is a name's first letter in capitals, for a letter tile.
func firstLetter(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// hiddenError answers anything that would start or show a hidden legacy
// game (I1).
func hiddenError(inst *instance) error {
	return fmt.Errorf("%s is removed from the panel. Add it again first.", inst.short())
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
// (game.Supporter). Fake mode ignores both, so fake games run on every
// system (7.1), and the legacy game always runs where the app does, as it
// always has.
func (m *Manager) canRunLocked(inst *instance) (game.Level, string) {
	if inst.legacy || inst.srv != nil && m.fake != nil {
		return game.Runs, ""
	}
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
// instance's always can; another's once the latest probe of its running
// server answered with a count, or once this install has seen its first
// join (servers.json presenceSeen). A's auto-stop and idle sleep and D's
// ask rule use it.
func (m *Manager) presenceProvenLocked(inst *instance) bool {
	switch {
	case inst.legacy:
		return true
	case inst.srv == nil:
		return false
	case inst.run != nil && inst.run.probed:
		return true
	}
	in := m.servers.Instance(inst.id)
	return in != nil && !in.PresenceSeen.IsZero()
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
// load, change, back up, write, then publish the new list. It runs
// without m.mu (it takes it to publish) and refuses in safe mode. change
// works on a fresh copy of the file: it may change it freely, and nothing
// changes when it returns an error. Changes are made one at a time, so the
// list in memory always follows the file.
func (m *Manager) updateServers(change func(*servers.File) error) error {
	m.mu.Lock()
	safe := m.safeMode
	m.mu.Unlock()
	if safe != "" {
		return errSafeMode
	}
	m.serversMu.Lock()
	defer m.serversMu.Unlock()
	var next servers.File
	err := servers.Update(filepath.Join(m.opt.DataDir, servers.FileName), func(f *servers.File) error {
		if err := change(f); err != nil {
			return err
		}
		next = *f // Update's own copy, which nothing changes after this
		return nil
	})
	if err != nil {
		return err
	}
	next.Schema = servers.Schema
	m.mu.Lock()
	m.publishServersLocked(next)
	m.mu.Unlock()
	m.notify()
	return nil
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

// --- uploads and the owner's own worlds ---------------------------------------

// NewUploadGame starts an upload of worlds for the game.
func (m *Manager) NewUploadGame(id string) (*worlds.Upload, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	return m.newUploadFor(inst)
}

// UploadGame is one of the game's uploads.
func (m *Manager) UploadGame(id, uploadID string) (*worlds.Upload, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	return m.uploadFor(inst, uploadID)
}

// FinishUploadGame unpacks an upload and lists the worlds in it.
func (m *Manager) FinishUploadGame(id, uploadID string) ([]worlds.Candidate, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	return m.finishUploadFor(inst, uploadID)
}

// DiscardUploadGame throws an upload away.
func (m *Manager) DiscardUploadGame(id, uploadID string) error {
	inst, err := m.runningInstance(id)
	if err != nil {
		return err
	}
	m.discardUploadFor(inst, uploadID)
	return nil
}

// ImportUploadGame imports the chosen worlds of an upload, as a job.
func (m *Manager) ImportUploadGame(id, uploadID string, choices []worlds.Choice) (int, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return 0, err
	}
	return m.importUploadFor(inst, uploadID, choices)
}

// LocalWorldsGame lists the worlds in the owner's own copy of the game on
// this computer.
func (m *Manager) LocalWorldsGame(id string) ([]worlds.Candidate, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return nil, err
	}
	return m.localWorldsFor(inst), nil
}

// ImportLocalGame imports the chosen worlds of the owner's own game.
func (m *Manager) ImportLocalGame(id string, choices []worlds.Choice) (int, error) {
	inst, err := m.runningInstance(id)
	if err != nil {
		return 0, err
	}
	return m.importLocalFor(inst, choices)
}

// --- players and lists ----------------------------------------------------------

// GameList is one of a game's player lists (GET /api/games/{id}/lists).
type GameList struct {
	Kind game.ListKind `json:"kind"`
	IDs  []string      `json:"ids"`
}

// --- the log ---------------------------------------------------------------
