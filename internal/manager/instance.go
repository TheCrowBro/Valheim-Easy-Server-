package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/routerports"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/worlds"
)

// An instance is one server of a game on this computer. The manager knows
// nothing about any game: it asks the instance's game.Server (the game's
// adapter, from the registry) for everything game-specific, so the code
// here is the same for every game.
//
// This release has one instance, the legacy one (Valheim), whose data is
// in the legacy files at the data folder's top (spec 1.3). Other games'
// servers that a newer version of the app started are "foreign" and only
// watched (foreign.go).
type instance struct {
	id     string
	g      game.Game
	info   game.Info
	srv    game.Server // nil for a foreign instance (R3)
	dirs   game.Dirs   // guarded by m.mu: a settings save replaces the legacy instance's (dirsOf copies them)
	legacy bool
	// foreign says why this build doesn't run the instance (its game is
	// unknown, hidden or turned off; R3), "" when it does.
	foreign string

	run        *serverRun
	job        *Job               // the instance's job slot: its current or last job
	jobCancel  context.CancelFunc // cancels job
	loadWait   *Job               // the job that started the server and waits for its world to load
	lastExit   *ExitInfo
	crashes    []time.Time
	known      map[string]*KnownPlayer // who has played, by name
	lastPlayed *played                 // what they last played with (the legacy instance uses Manager.lastPlayed)
	recs       recordsData             // past runs' joins, leaves and deaths
	logs       *ring[LogLine]          // the server's output, for the Log page
	uploads    map[string]*worlds.Upload
	localCands []worlds.Candidate

	// R3 (spec 4.1). Each is guarded by m.mu; the comment names the stream
	// that fills it in.
	history    *stats.History   // its CPU, memory and players (games/<id>/history.json); nil for the legacy instance, which uses Manager.history (A)
	byPlayers  *stats.ByPlayers // its usage by players (games/<id>/stats.json); nil for the legacy instance (A)
	emptySince time.Time        // the auto-stop clock (4.5): when it was last seen in use (A)
	ask        askState         // the "still playing?" rule (4.6, ask.go) (D)
	cache      instCache        // what Status shows without touching the disk (4.8) (M3)
}

// instCache is what the checks loop and the install job found about a
// non-legacy instance, so Status never touches its disk (spec 4.8). The
// legacy instance keeps today's quick look at the disk instead.
type instCache struct {
	at         time.Time  // when it was last refreshed; zero: never
	installed  bool       // Program() exists
	build      game.Build // what's installed
	hasBuild   bool
	firewall   bool   // its firewall rules are in place (A's firewall.go says)
	freeDisk   uint64 // free space on its drive
	freeDiskOK bool
}

// serverRun is one running server process.
type serverRun struct {
	inst       *instance
	proc       *platform.Proc
	rec        game.Record  // its run record (runtime.json for the legacy instance)
	mon        game.Monitor // the game's reader of its output
	stopping   bool
	forced     bool // ended with Force stop
	ready      bool
	readyAt    time.Time     // when it became ready
	replaying  bool          // reading an existing log after adopting a running server
	done       chan struct{} // closed when the process has exited
	readyCh    chan struct{} // closed when the server says it's ready
	cancelTail context.CancelFunc
	cpuPrev    time.Duration
	cpuPrevAt  time.Time
	discordAt  time.Time       // when the online post went out
	discordErr string          // why posting it didn't work
	events     []records.Event // joins, leaves and deaths, for the records
	// afterPending: the game's work after launch (Valheim's mods code) is
	// still running, so the online post waits for it.
	afterPending bool

	// R3 (spec 4.1), guarded by m.mu like the rest; the comment names the
	// stream that fills it in.
	stdin *os.File // console games: the app's end of the game's stdin pipe; nil after a takeover (2.11) (M3)
	// autoStopped: this run is being stopped by auto-stop or the "still
	// playing?" rule, for the reason autoReason (stopIdle or stopAsked). Its
	// exit then doesn't reset machineIdleSince, and the stopped post says
	// why (A sets it for auto-stop, D for the ask rule).
	autoStopped bool
	autoReason  stopReason
	memNow      uint64                // memory in use at the last sample, bytes (A)
	cpuNow      float64               // CPU at the last sample, percent of the whole computer (A)
	probeFails  int                   // probes that failed in a row; 3 shows a warning (2.6) (M3)
	mappings    []routerports.Mapping // router ports mapped for this run (6.5) (N)
}

// state is what the run's Monitor knows. A run without one (only made by
// tests) knows nothing yet.
func (r *serverRun) state() game.RunState {
	if r.mon == nil {
		return game.RunState{Players: []game.Player{}}
	}
	return r.mon.State()
}

// legacyDirs are the legacy instance's folders: exactly config.ResolvePaths'
// (invariant I1), so Valheim's files never move.
func legacyDirs(p config.Paths) game.Dirs {
	return game.Dirs{Root: p.Data, Server: p.Server, Saves: p.Save, Backups: p.Backups, Logs: p.Logs,
		Staging: p.Staging, SteamCMD: p.SteamCMD}
}

// newLegacyInstance makes the legacy game's instance. It does no work on
// disk: manager.New must stay quick (invariant I5).
func (m *Manager) newLegacyInstance(g game.Game) *instance {
	info := g.Info()
	inst := &instance{id: info.ID, g: g, info: info, legacy: true, dirs: legacyDirs(m.paths),
		known: map[string]*KnownPlayer{}, uploads: map[string]*worlds.Upload{}, logs: m.logs}
	inst.srv = g.New(game.Env{Instance: info.ID, Data: m.opt.DataDir, Dirs: inst.dirs, Log: m.log, Legacy: legacyStore{m},
		Note: func(kind, text string) { m.addGameActivity(inst, kind, "", text) }})
	return inst
}

// legacy is the legacy game's instance, which always exists.
func (m *Manager) legacy() *instance { return m.instances[m.legacyID] }

// eachInstance calls fn for every instance, in order.
func (m *Manager) eachInstance(fn func(*instance)) {
	for _, id := range m.order {
		fn(m.instances[id])
	}
}

// runFile is the instance's run record: runtime.json for the legacy
// instance (invariant I4), games/<id>/run.json for any other.
func (m *Manager) runFile(inst *instance) string {
	if inst.legacy {
		return m.dataFile("runtime.json")
	}
	return filepath.Join(m.opt.DataDir, "games", inst.id, "run.json")
}

// logPrefix starts the name of each of the instance's server logs:
// "server-" for the legacy instance, as older builds named them.
func logPrefix(inst *instance) string {
	if inst.legacy {
		return "server-"
	}
	return inst.id + "-"
}

// installedLocked says whether the instance's server is installed. For
// the legacy instance it looks at the disk each time, as it always did
// (it is quick, and tests check it straight after an install).
func (m *Manager) installedLocked(inst *instance) bool {
	_, err := os.Stat(inst.srv.Program())
	return err == nil
}

// instStateLocked works out the instance's state shown to people.
func (m *Manager) instStateLocked(inst *instance) State {
	switch run := inst.run; {
	case run != nil && run.stopping:
		return StateStopping
	case run != nil && run.ready:
		return StateOnline
	case run != nil:
		return StateStarting
	case !m.installedLocked(inst):
		return StateNotInstalled
	}
	return StateStopped
}

// short is the game's short name ("Valheim") for messages.
func (inst *instance) short() string { return inst.info.Short }

// exact is the game's own wording of a message, or def.
func (inst *instance) exact(id, def string) string {
	if s, ok := inst.srv.Texts().Exact[id]; ok && s != "" {
		return s
	}
	return def
}

// saved is the instance's settings, as values. Its Store never takes m.mu,
// so this may be called with m.mu held.
func (inst *instance) saved() game.Saved { return inst.srv.Store().Get() }

// dirsOf is a copy of the instance's folders, for code that runs without
// m.mu. A settings save replaces the legacy instance's folders under the
// lock (the owner can move the server and save folders), so reading them
// without it would be a data race, even when the save changed only the
// server's name.
func (m *Manager) dirsOf(inst *instance) game.Dirs {
	m.mu.Lock()
	defer m.mu.Unlock()
	return inst.dirs
}

// newestLog is the newest of the instance's server logs in the folder
// logs, or "". Callers read that folder from inst.dirs with m.mu held, or
// through dirsOf.
func newestLog(inst *instance, logs string) string {
	matches, _ := filepath.Glob(filepath.Join(logs, logPrefix(inst)+"*.log"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// saveRuntime writes a run record readable only by the user running the
// app, like settings.json, because it holds the server password the
// running server started with. Only this app reads it back.
func saveRuntime(path string, rec game.Record) {
	if b, err := json.MarshalIndent(rec, "", "  "); err == nil {
		os.MkdirAll(filepath.Dir(path), 0o755)
		config.WriteFileAtomic(path, b, 0o600)
	}
}

// loadRuntime reads a run record.
func loadRuntime(path string) (game.Record, error) {
	var rec game.Record
	b, err := readFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	if rec.PID <= 0 {
		return rec, errors.New("no pid")
	}
	return rec, nil
}

// readFile is os.ReadFile; the quickness tests count what it reads.
var readFile = os.ReadFile

// actionOf turns an adapter's error that suggests a next step for the
// panel (games/valheim's ActionError, say) into the manager's own, so jobs
// and the panel show it the same way. A game.ActionError already is the
// manager's own and passes through unchanged.
func actionOf(err error) error {
	var ae *ActionError
	if err == nil || errors.As(err, &ae) {
		return err
	}
	var s interface{ SuggestedAction() string }
	if errors.As(err, &s) {
		return &ActionError{Msg: err.Error(), Action: s.SuggestedAction()}
	}
	return err
}

// knownAccountLocked gives the account a name last played from on the
// instance, or "" for someone new. A Monitor calls it with m.mu held.
func (m *Manager) knownAccountLocked(inst *instance, name string) string {
	if p := inst.known[name]; p != nil {
		return p.PlatformID
	}
	return ""
}
