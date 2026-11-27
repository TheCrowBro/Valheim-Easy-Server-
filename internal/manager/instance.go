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
// The legacy instance (Valheim) keeps its data in the legacy files at the
// data folder's top (spec 1.3). Every other instance is an entry of
// servers.json with its files under games/<id> (spec 3.1). An instance
// this build doesn't run (its game is unknown to it, or switched off in
// this build) is "foreign": it has no game.Server, and like the run files
// of games no entry names, it is only watched (foreign.go).
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
	// presenceNoting: the first join this install saw of it is being
	// written into servers.json (presenceSeen, 2.6) (M3).
	presenceNoting bool
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
	stdin    *os.File // console games: the app's end of the game's stdin pipe; nil after a takeover (2.11) (M3)
	saveLine string   // console games: the line that saves without stopping (Plan.SaveLine), typed before the app exits (M3)
	// gameLog: the game writes its own log at rec.LogPath (Output GameLog),
	// so the tail follows that file through rotations and copies each line
	// into the run's own log, ownLog (M3). logSkip is what the tail skips of
	// it: the size the file had before this run started it, when it was the
	// same file (a game that appends to last run's log), or, after a
	// takeover, the lines the copy already has.
	gameLog bool
	ownLog  string
	logSkip logSkip
	probed  bool // the last probe answered with a count (presence counts as proven, 2.6) (M3)
	saves   int  // "saved" events seen while the app follows it live (the save line before the app exits waits for one) (M3)
	// autoStopped: this run is being stopped by auto-stop or the "still
	// playing?" rule, for the reason autoReason (stopIdle or stopAsked). Its
	// exit then doesn't reset machineIdleSince, and the stopped post and the
	// activity entry say why. autoMinutes is the auto-stop setting the stop
	// was decided with, so "nobody played for an hour" stays true if the
	// setting changes while the game stops. A's autoStopGame sets all three
	// (D's ask rule calls it with stopAsked).
	autoStopped bool
	autoReason  stopReason
	autoMinutes int
	memNow      uint64                // memory in use at the last sample, bytes (A)
	cpuNow      float64               // CPU at the last sample, percent of the whole computer (A)
	probeFails  int                   // probes that failed in a row once it was ready; 3 shows a warning (2.6) (M3)
	mappings    []routerports.Mapping // router ports mapped for this run (6.5); onExit gives N the ended run to unmap them (N)
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
func (m *Manager) legacy() *instance { return m.legacyInst }

// instList is the instances as one value, so code without m.mu can read a
// list that never changes under it (Manager.insts).
type instList struct {
	byID  map[string]*instance
	order []string
}

// setInstancesLocked replaces the instances. byID and order must be new
// values that nothing else changes afterwards. m.mu must be held (or, in
// New, nothing else can see the manager yet).
func (m *Manager) setInstancesLocked(byID map[string]*instance, order []string) {
	m.instances, m.order = byID, order
	m.insts.Store(&instList{byID: byID, order: order})
}

// eachInstance calls fn for every instance, in order: foreign ones too
// (inst.srv is nil for them). It may be called with or without m.mu.
func (m *Manager) eachInstance(fn func(*instance)) {
	l := m.insts.Load()
	for _, id := range l.order {
		fn(l.byID[id])
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
// (it is quick, and tests check it straight after an install). Every other
// instance's answer comes from its cache, so Status never touches its
// disk (spec 4.8); checkInstalled looks afresh.
func (m *Manager) installedLocked(inst *instance) bool {
	switch {
	case inst.srv == nil:
		return false
	case !inst.legacy:
		return inst.cache.installed
	}
	_, err := os.Stat(inst.srv.Program())
	return err == nil
}

// checkInstalled looks at the disk for whether the instance's server is
// installed, and notes it in the cache. A start and an install ask this,
// not the cache, so a server deleted by hand since the last check is
// noticed before its start fails. It runs without m.mu.
func (m *Manager) checkInstalled(inst *instance) bool {
	if inst.srv == nil {
		return false
	}
	_, err := os.Stat(inst.srv.Program())
	installed := err == nil
	if !inst.legacy {
		m.mu.Lock()
		inst.cache.installed = installed
		m.mu.Unlock()
	}
	return installed
}

// refreshCache looks again at what Status shows of a non-legacy instance
// without touching its disk (spec 4.8): whether its server is installed,
// which build, and the free disk space where its worlds are. The checks
// loop calls it every minute, the install job before it finishes, and
// Start before anything can start the game. Its firewall answer is stream
// A's (checkGameFirewalls) and is kept. It runs without m.mu.
func (m *Manager) refreshCache(inst *instance) {
	if inst.legacy || inst.srv == nil {
		return
	}
	dirs := m.dirsOf(inst)
	c := instCache{at: time.Now()}
	_, err := os.Stat(inst.srv.Program())
	c.installed = err == nil
	if c.installed {
		c.build, c.hasBuild = inst.srv.Build()
	}
	c.freeDisk, c.freeDiskOK = platform.FreeDisk(dirs.Saves) // its nearest folder that exists
	m.mu.Lock()
	c.firewall = inst.cache.firewall
	inst.cache = c
	m.mu.Unlock()
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

// serverWord names the instance's server in a sentence's first words: "The
// server" for the legacy game, whose texts stay as they are, and the
// game's name for the others ("Minecraft stopped."), so a feed with several
// games says which one (spec 6.6).
func (inst *instance) serverWord() string {
	if inst.legacy {
		return "The server"
	}
	return inst.short()
}

// serverObject is serverWord in the middle of a sentence: "the server", or
// the game's name.
func (inst *instance) serverObject() string {
	if inst.legacy {
		return "the server"
	}
	return inst.short()
}

// exact is the game's own wording of a message, or def.
func (inst *instance) exact(id, def string) string {
	if inst.srv == nil {
		return def
	}
	if s, ok := inst.srv.Texts().Exact[id]; ok && s != "" {
		return s
	}
	return def
}

// selectedWorld is the world argument for the adapter's Validate and
// Fingerprint when the manager means "the selected world": "" for the
// legacy game, as it always was, and the selected world's name for the
// others. The contract says "" means the selected world, but naming it
// can't be misread by an adapter that took "" as a world called "".
func selectedWorld(inst *instance, saved game.Saved) string {
	if inst.legacy {
		return ""
	}
	return saved.World
}

// saved is the instance's settings, as values. Its Store never takes m.mu,
// so this may be called with m.mu held. A foreign instance has none.
func (inst *instance) saved() game.Saved {
	if inst.srv == nil {
		return game.Saved{}
	}
	return inst.srv.Store().Get()
}

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
