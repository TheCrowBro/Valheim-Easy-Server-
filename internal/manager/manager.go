// Package manager runs the game servers: it starts and stops them, follows
// their output, keeps their worlds, records usage statistics, and handles
// sleep and waking. The web control panel and the tray icon both drive it.
//
// It knows no game itself: each game's adapter (internal/games/<id>, found
// through internal/game's registry) says everything game-specific. This
// release runs one game, the legacy one (Valheim), with its data where
// older builds keep it.
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/jetkvm"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/selfupdate"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/steamcmd"
)

// State is what the server is doing.
type State string

const (
	StateNotInstalled State = "not_installed"
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateOnline       State = "online"
	StateStopping     State = "stopping"
)

// Options configure a Manager.
type Options struct {
	DataDir string
	AppExe  string // this program, for firewall rules and Windows startup
	Logger  *log.Logger

	// Replaceable for development and tests.
	Installer       steamcmd.Installer
	NewInstaller    func(config.Paths) steamcmd.Installer // used when Installer is nil
	Hibernate       func() error
	HibernateStatus func() (bool, string)
	Sleep           func() error
	SleepStatus     func() (bool, string)
	NoPublicIP      bool                         // don't look up the public IP address
	ModsURL         string                       // where mods are downloaded from; Thunderstore when empty
	DiscordURL      string                       // where Discord webhook links are sent instead of Discord, for tests
	WakePort        int                          // where Wake-on-LAN signals are heard; 9 when 0
	DiscordAPI      string                       // Discord's API, where the waker's bot token is checked; for tests
	WakerBinary     func() ([]byte, error)       // the waker for the JetKVM; the one built in when nil
	JetKVMDirs      *jetkvm.Target               // for tests: the Base, Run and Arch of a stand-in JetKVM
	NetAdapters     func() []platform.NetAdapter // platform.NetAdapters when nil
	AppVersion      string                       // this program's version, as people see it
	AppBuild        int                          // its build number; 0 for development builds, which don't update themselves
	Updater         *selfupdate.Updater
	Relaunch        func() // closes the app and starts the program at AppExe, after an update
	SampleInterval  time.Duration
	RestartDelay    time.Duration // wait before restarting after a crash
}

// Manager is the heart of the app. All methods are safe to call from
// several goroutines.
type Manager struct {
	opt     Options
	log     *log.Logger
	sys     *platform.SysSampler
	awake   *platform.KeepAwake
	machine platform.MachineInfo

	// The instances, by id, and their order. Made in New and never
	// changed afterwards in this release, so they are read without m.mu.
	// (In R3, adding and removing a game change them: M3 decides how they
	// are guarded then.)
	instances map[string]*instance
	order     []string
	legacyID  string

	history   *stats.History   // the machine and the legacy instance (history.json)
	byPlayers *stats.ByPlayers // the legacy instance's usage by players (stats.json)
	logs      *ring[LogLine]   // the legacy instance's server output
	activity  *ring[Activity]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	exits  sync.WaitGroup // servers that have exited, being wrapped up

	discordQ       chan discordPost // messages for the Discord channel
	discordPending atomic.Int32     // queued or being posted
	keyMu          sync.Mutex       // the app's key for the JetKVM is made once
	runFileMu      sync.Mutex       // run records, written by saveRunInfo

	// The legacy game's settings and folders as immutable snapshots, swapped
	// by saveSettingsLocked, so its adapter can read them without m.mu
	// (games/valheim.LegacyStore).
	settingsSnap atomic.Pointer[config.Settings]
	pathsSnap    atomic.Pointer[config.Paths]

	mu           sync.Mutex
	settings     config.Settings // settings.json: the legacy game's and the app's
	paths        config.Paths    // the legacy game's folders
	appJob       *Job            // the app's job slot: going to sleep (spec 4.2)
	appJobCancel context.CancelFunc
	jobSeq       int
	live         Live
	foreign      []foreignRun // other games' servers a newer version started (foreign.go)
	// machineIdleSince is when someone last played or did something:
	// automatic sleep counts from it.
	machineIdleSince time.Time
	keepAwakeUntil   time.Time
	wakeHold         time.Time      // after waking up, Windows is kept awake until then
	wakeConn         net.PacketConn // hears Wake-on-LAN signals, with wake on
	wakeStart        time.Time      // when an untagged wake signal last started the server
	wakeTag          string         // the last request a tagged wake signal came with
	wk               wakerState     // the waker on the JetKVM
	resumeOnWake     bool
	closing          bool // Close was called: exits are no longer waited for
	net              netInfo
	checks           checkCache
	lastPlayed       *played   // what the legacy game's players last played with; nil until someone has
	app              appUpdate // what the last check for an app update found
	subs             []func()

	// R3 (spec 4.1), guarded by m.mu. Each stream keeps the rest of its own
	// state in the struct type named here, which lives in its own file, so it
	// never needs to edit this one. The comment names the stream.
	observers []observer        // hooks.go: called at fixed points (M3 owns the list)
	servers   servers.File      // servers.json as last read or written; zero in R2 (M3)
	safeMode  string            // why only the legacy game runs (8.3), "" normally (M3)
	lan       []string          // this computer's LAN IPv4 addresses, cached by the checks loop (4.8) (M3)
	lanAt     time.Time         // when lan was read
	resumeSet []string          // the instances to start again after waking (4.7) (A)
	life      lifeState         // power.go: the rest of auto-stop, the resume and idle sleep (A)
	disc      discordState      // discord.go: the per-channel queues and daily posts (6.3) (D)
	wakeAsked time.Time         // when a wake packet last asked for a game (4.7) (WK)
	wakeTags  map[string]string // the last request each game's wake packets came with (6.4) (WK)
	router    routerState       // router.go: discovery, mappings, reachability (6.5) (N)
}

// ExitInfo describes the last time the server stopped.
type ExitInfo struct {
	At       time.Time `json:"at"`
	Code     int       `json:"code"`
	Expected bool      `json:"expected"`
	World    string    `json:"world"`
	Uptime   float64   `json:"uptime"` // seconds
	LogTail  []string  `json:"logTail,omitempty"`
}

// KnownPlayer is someone who has played on this server.
type KnownPlayer struct {
	Name        string    `json:"name"`
	PlatformID  string    `json:"platformId,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	PlaySeconds float64   `json:"playSeconds"`
	Sessions    int       `json:"sessions"`
}

// New loads settings and saved data. Call Start to begin.
func New(opt Options) (*Manager, error) {
	if opt.Logger == nil {
		opt.Logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	if opt.SampleInterval == 0 {
		opt.SampleInterval = 2 * time.Second
	}
	if opt.RestartDelay == 0 {
		opt.RestartDelay = 15 * time.Second
	}
	if err := os.MkdirAll(opt.DataDir, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{
		opt:      opt,
		log:      opt.Logger,
		logs:     newRing[LogLine](3000),
		activity: newRing[Activity](300),
		history:  stats.NewHistory(int(24 * time.Hour / opt.SampleInterval)),
		discordQ: make(chan discordPost, 16),
		// Job numbers carry on from the clock, so they never repeat after the
		// app restarts: an open control panel tells jobs apart by number.
		jobSeq: int(time.Now().UnixMilli()),
	}
	settingsPath := filepath.Join(opt.DataDir, "settings.json")
	s, existed, err := config.Load(settingsPath)
	if err != nil {
		// Keep the damaged file for inspection and start from defaults.
		broken := settingsPath + ".damaged-" + time.Now().Format("20060102-150405")
		os.Rename(settingsPath, broken)
		m.log.Printf("settings were damaged (%v); saved as %s and using defaults", err, broken)
		existed = false
	}
	m.settings = s
	m.paths = config.ResolvePaths(opt.DataDir, s)
	m.snapshotLocked()
	if !existed {
		if err := config.Save(settingsPath, s); err != nil {
			return nil, fmt.Errorf("can't write to %s: %w", opt.DataDir, err)
		}
	}
	if opt.Installer == nil && opt.NewInstaller != nil {
		opt.Installer = opt.NewInstaller(m.paths)
		m.opt.Installer = opt.Installer
	}
	if opt.Installer == nil {
		opt.Installer = &steamcmd.SteamCMD{Dir: m.paths.SteamCMD, InstallDir: m.paths.Server, Output: func(line string) {
			m.log.Printf("steamcmd: %s", line)
		}}
		m.opt.Installer = opt.Installer
	}
	if opt.Hibernate == nil {
		m.opt.Hibernate = platform.Hibernate
	}
	if opt.HibernateStatus == nil {
		m.opt.HibernateStatus = platform.HibernateStatus
	}
	if opt.Sleep == nil {
		m.opt.Sleep = platform.Sleep
	}
	if opt.SleepStatus == nil {
		m.opt.SleepStatus = platform.SleepStatus
	}
	if opt.NetAdapters == nil {
		m.opt.NetAdapters = platform.NetAdapters
	}
	// The legacy game (Valheim) is found through the registry, which main
	// fills by importing internal/games/all.
	g := game.Legacy()
	if g == nil {
		return nil, errors.New("no legacy game is registered: the program must import internal/games/all")
	}
	inst := m.newLegacyInstance(g)
	m.instances = map[string]*instance{inst.id: inst}
	m.order = []string{inst.id}
	m.legacyID = inst.id
	m.observers = newObservers(m)
	if b, err := stats.Load(filepath.Join(opt.DataDir, "stats.json")); err == nil {
		m.byPlayers = b
	} else {
		m.byPlayers = stats.NewByPlayers()
	}
	m.loadHistory()
	m.loadActivity()
	m.loadKnown(inst)
	m.loadPlayed(inst)
	m.loadRecords(inst)
	m.loadWaker()
	m.machine = platform.Machine()
	m.machineIdleSince = time.Now()
	return m, nil
}

// Start begins background work: finding an already-running server,
// sampling usage, and (if enabled) starting the server.
func (m *Manager) Start() {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.sys = platform.NewSysSampler()
	inst := m.legacy()
	m.awake = platform.NewKeepAwake(inst.short() + " server is running")
	inst.srv.Worlds().CleanStaging(time.Hour)
	updated := m.canUpdate() && m.noteUpdate()
	m.eachInstance(m.adoptRunning)
	m.scanForeign()
	m.go_(m.sampleLoop)
	m.go_(m.checksLoop)
	m.go_(m.discordLoop)
	m.go_(m.updateLoop)
	m.go_(m.backfillRecords)
	m.go_(m.dailyLoop)
	m.go_(m.wakerLoop)
	m.syncWakeListener()

	m.mu.Lock()
	autoStart := m.settings.StartWithApp && inst.run == nil && m.installedLocked(inst)
	m.mu.Unlock()
	if autoStart && updated {
		// Nobody opened the app: it restarted to update. A server that was
		// stopped stays stopped (one that was running is taken over).
		m.log.Printf("not starting the server: the app restarted after an update")
		autoStart = false
	}
	if autoStart {
		m.addActivity("info", "", "Starting the server because \"Start the server when the app opens\" is on.")
		if err := m.StartServer(); err != nil {
			m.log.Printf("auto start: %v", err)
		}
	}
}

// Close stops background work and saves data. Running servers keep
// running; the app finds them again next time.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	m.exits.Wait()                  // a server that just stopped is wrapped up
	m.flushDiscord(5 * time.Second) // e.g. that the server stopped
	if m.cancel != nil {
		m.cancel()
	}
	m.syncWakeListener() // stops listening
	m.wg.Wait()
	// An online post that hasn't gone out yet (Discord may be down) is
	// left for the app that takes the server over.
	m.eachInstance(func(inst *instance) {
		m.mu.Lock()
		run := inst.run
		unposted := run != nil && run.rec.Announced != "" && run.rec.Posted.IsZero() && run.rec.PostError == ""
		if unposted {
			run.rec.Announced = ""
		}
		m.mu.Unlock()
		if unposted {
			m.saveRunInfo(run)
		}
	})
	m.saveData()
	if m.sys != nil {
		m.sys.Close()
	}
}

// go_ runs fn in a goroutine that is waited for on Close and never takes
// the whole app down if it panics.
func (m *Manager) go_(fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.recoverPanic()
		fn()
	}()
}

func (m *Manager) recoverPanic() {
	if r := recover(); r != nil {
		m.log.Printf("panic: %v\n%s", r, debug.Stack())
	}
}

// Subscribe registers fn to be called when the status changes.
func (m *Manager) Subscribe(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

func (m *Manager) notify() {
	m.mu.Lock()
	subs := append([]func(){}, m.subs...)
	m.mu.Unlock()
	for _, fn := range subs {
		go fn()
	}
}

func (m *Manager) addActivity(kind, name, text string) {
	m.addActivityAt(time.Now(), kind, name, text)
}

func (m *Manager) addActivityAt(at time.Time, kind, name, text string) {
	m.addGameActivityAt(nil, at, kind, name, text)
}

// addGameActivity adds a feed entry about one instance (its "game" in
// activity.json), so the new panel can show each game's own entries. Like
// addActivity it never takes m.mu, so it may be called with it held.
func (m *Manager) addGameActivity(inst *instance, kind, name, text string) {
	m.addGameActivityAt(inst, time.Now(), kind, name, text)
}

func (m *Manager) addGameActivityAt(inst *instance, at time.Time, kind, name, text string) {
	g := ""
	if inst != nil {
		g = inst.id
	}
	m.activity.add(func(seq int64) Activity {
		return Activity{Seq: seq, Time: at, Kind: kind, Name: name, Text: text, Game: g}
	})
	m.log.Printf("activity: %s", text)
}

// Settings returns a copy of the current settings.
func (m *Manager) Settings() config.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Clone()
}

// Paths returns the folders in use.
func (m *Manager) Paths() config.Paths {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paths
}

// Machine describes this computer.
func (m *Manager) Machine() platform.MachineInfo { return m.machine }

// stateLocked works out the legacy game's state shown to people.
func (m *Manager) stateLocked() State { return m.instStateLocked(m.legacy()) }

// Activity returns feed entries after seq (newest last).
func (m *Manager) Activity(after int64, limit int) ([]Activity, int64) {
	return m.activity.after(after, limit)
}

// Logs returns the legacy game's server log lines after seq (oldest first).
func (m *Manager) Logs(after int64, limit int) ([]LogLine, int64) {
	return m.legacy().logs.after(after, limit)
}

// CurrentLogPath is the log file of the legacy game's running (or last)
// server.
func (m *Manager) CurrentLogPath() string {
	inst := m.legacy()
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.run != nil {
		return inst.run.rec.LogPath
	}
	return newestLog(inst, inst.dirs.Logs)
}

// --- saved data ---------------------------------------------------------

func (m *Manager) dataFile(name string) string { return filepath.Join(m.opt.DataDir, name) }

// instFile is one of an instance's own files: at the data folder's top for
// the legacy instance (the legacy files, spec 1.3), else in games/<id>.
func (m *Manager) instFile(inst *instance, name string) string {
	if inst.legacy {
		return m.dataFile(name)
	}
	return filepath.Join(m.opt.DataDir, "games", inst.id, name)
}

func (m *Manager) loadKnown(inst *instance) {
	b, err := os.ReadFile(m.instFile(inst, "players.json"))
	if err != nil {
		return
	}
	var list []*KnownPlayer
	if json.Unmarshal(b, &list) == nil {
		for _, p := range list {
			inst.known[p.Name] = p
		}
	}
}

func (m *Manager) saveData() {
	inst := m.legacy()
	m.mu.Lock()
	list := make([]*KnownPlayer, 0, len(inst.known))
	for _, p := range inst.known {
		cp := *p
		list = append(list, &cp)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		config.WriteFileAtomic(m.instFile(inst, "players.json"), b, 0o644)
	}
	if b, err := json.Marshal(m.byPlayers); err == nil {
		config.WriteFileAtomic(m.dataFile("stats.json"), b, 0o644)
	}
	// Keep a lighter copy of the last day so the charts survive a restart.
	day := stats.Downsample(m.history.Since(time.Now().Add(-24*time.Hour)), 2880)
	if b, err := json.Marshal(day); err == nil {
		config.WriteFileAtomic(m.dataFile("history.json"), b, 0o644)
	}
	// And the activity feed, which a restart (say, to update the app)
	// would otherwise empty.
	acts, _ := m.activity.all()
	if b, err := json.Marshal(acts); err == nil {
		config.WriteFileAtomic(m.dataFile("activity.json"), b, 0o644)
	}
}

func (m *Manager) loadActivity() {
	b, err := os.ReadFile(m.dataFile("activity.json"))
	if err != nil {
		return
	}
	var saved []Activity
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	var acts []Activity
	var seqs []int64
	for _, a := range saved {
		if len(seqs) == 0 || a.Seq > seqs[len(seqs)-1] {
			acts, seqs = append(acts, a), append(seqs, a.Seq)
		}
	}
	m.activity.restore(acts, seqs)
}

func (m *Manager) loadHistory() {
	b, err := os.ReadFile(m.dataFile("history.json"))
	if err != nil {
		return
	}
	var saved []stats.Sample
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
	var keep []stats.Sample
	for _, s := range saved {
		if s.T > cutoff {
			keep = append(keep, s)
		}
	}
	m.history.Restore(keep)
}

// KnownPlayers lists everyone who has played the legacy game, most recent
// first.
func (m *Manager) KnownPlayers() []KnownPlayer {
	inst := m.legacy()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KnownPlayer, 0, len(inst.known))
	for _, p := range inst.known {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// notePlayerLocked updates the instance's known-player list from a join
// or a leave.
func (m *Manager) notePlayerLocked(inst *instance, a game.Event, since time.Time) {
	if a.Name == "" || (a.Kind != "join" && a.Kind != "leave") {
		return
	}
	p := inst.known[a.Name]
	if p == nil {
		p = &KnownPlayer{Name: a.Name, FirstSeen: a.Time}
		inst.known[a.Name] = p
	}
	if a.Account != "" {
		if a.Kind == "join" && p.PlatformID != "" && p.PlatformID != a.Account {
			m.addActivity("warning", a.Name, fmt.Sprintf("%s joined from a different account than before (%s, before %s). Anyone can pick any name, so check it's really them before making them an admin.", a.Name, a.Account, p.PlatformID))
		}
		p.PlatformID = a.Account
	}
	p.LastSeen = a.Time
	switch a.Kind {
	case "join":
		p.Sessions++
	case "leave":
		if !since.IsZero() && a.Time.After(since) {
			p.PlaySeconds += a.Time.Sub(since).Seconds()
		}
	}
}

// snapshotLocked publishes the settings and folders for the legacy game's
// adapter, which reads them without m.mu.
func (m *Manager) snapshotLocked() {
	s, p := m.settings.Clone(), m.paths
	m.settingsSnap.Store(&s)
	m.pathsSnap.Store(&p)
}
