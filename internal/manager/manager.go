// Package manager runs the Valheim server: it starts and stops it, follows
// its log, keeps the world library, records usage statistics, and handles
// hibernation. The web control panel and the tray icon both drive it.
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/logparse"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
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
	NoPublicIP      bool // don't look up the public IP address
	SampleInterval  time.Duration
	RestartDelay    time.Duration // wait before restarting after a crash
}

// Manager is the heart of the app. All methods are safe to call from
// several goroutines.
type Manager struct {
	opt     Options
	log     *log.Logger
	libPtr  atomic.Pointer[worlds.Library] // replaced when the folders change; see library
	sys     *platform.SysSampler
	awake   *platform.KeepAwake
	machine platform.MachineInfo

	history   *stats.History
	byPlayers *stats.ByPlayers
	logs      *ring[LogLine]
	activity  *ring[Activity]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu             sync.Mutex
	settings       config.Settings
	paths          config.Paths
	run            *serverRun
	job            *Job
	jobCancel      context.CancelFunc
	jobSeq         int
	lastExit       *ExitInfo
	live           Live
	crashes        []time.Time
	known          map[string]*KnownPlayer
	uploads        map[string]*worlds.Upload
	localCands     []worlds.Candidate
	idleSince      time.Time
	keepAwakeUntil time.Time
	resumeOnWake   bool
	net            netInfo
	checks         checkCache
	subs           []func()
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

// serverRun is one running server process.
type serverRun struct {
	proc       *platform.Proc
	info       runInfo
	tracker    logparse.Tracker
	stopping   bool
	forced     bool // ended with Force stop
	ready      bool
	replaying  bool          // reading an existing log after adopting a running server
	done       chan struct{} // closed when the process has exited
	readyCh    chan struct{} // closed when the server says it's ready
	cancelTail context.CancelFunc
	cpuPrev    time.Duration
	cpuPrevAt  time.Time
}

// runInfo is saved to runtime.json so the app can find a server that is
// still running after the app itself was closed or restarted.
type runInfo struct {
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"` // process creation time, guards against PID reuse
	Launched time.Time `json:"launched"`
	LogPath  string    `json:"logPath"`
	World    string    `json:"world"`
	ArgsKey  string    `json:"argsKey"`
	Exe      string    `json:"exe"`
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
		known:    map[string]*KnownPlayer{},
		uploads:  map[string]*worlds.Upload{},
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
	if !existed {
		if err := config.Save(settingsPath, s); err != nil {
			return nil, fmt.Errorf("can't write to %s: %w", opt.DataDir, err)
		}
	}
	m.libPtr.Store(newLibrary(m.paths))
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
	if b, err := stats.Load(filepath.Join(opt.DataDir, "stats.json")); err == nil {
		m.byPlayers = b
	} else {
		m.byPlayers = stats.NewByPlayers()
	}
	m.loadHistory()
	m.loadKnown()
	m.machine = platform.Machine()
	m.idleSince = time.Now()
	return m, nil
}

// Start begins background work: finding an already-running server,
// sampling usage, and (if enabled) starting the server.
func (m *Manager) Start() {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.sys = platform.NewSysSampler()
	m.awake = platform.NewKeepAwake("Valheim server is running")
	m.library().CleanUploads(time.Hour)
	m.adoptRunning()
	m.go_(m.sampleLoop)
	m.go_(m.checksLoop)

	m.mu.Lock()
	autoStart := m.settings.StartWithApp && m.run == nil && m.installedLocked()
	m.mu.Unlock()
	if autoStart {
		m.addActivity("info", "", "Starting the server because \"Start the server when the app opens\" is on.")
		if err := m.StartServer(); err != nil {
			m.log.Printf("auto start: %v", err)
		}
	}
}

// Close stops background work and saves data. A running Valheim server
// keeps running; the app finds it again next time.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
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
	m.activity.add(func(seq int64) Activity {
		return Activity{Seq: seq, Time: at, Kind: kind, Name: name, Text: text}
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

func (m *Manager) installedLocked() bool {
	_, err := os.Stat(m.paths.ServerExe)
	return err == nil
}

// stateLocked works out the state shown to people.
func (m *Manager) stateLocked() State {
	switch {
	case m.run != nil && m.run.stopping:
		return StateStopping
	case m.run != nil && m.run.ready:
		return StateOnline
	case m.run != nil:
		return StateStarting
	case !m.installedLocked():
		return StateNotInstalled
	}
	return StateStopped
}

// Activity returns feed entries after seq (newest last).
func (m *Manager) Activity(after int64, limit int) ([]Activity, int64) {
	return m.activity.after(after, limit)
}

// Logs returns server log lines after seq (oldest first).
func (m *Manager) Logs(after int64, limit int) ([]LogLine, int64) {
	return m.logs.after(after, limit)
}

// CurrentLogPath is the log file of the running (or last) server.
func (m *Manager) CurrentLogPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run != nil {
		return m.run.info.LogPath
	}
	return newestLog(m.paths.Logs)
}

func newestLog(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "server-*.log"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// --- saved data ---------------------------------------------------------

func (m *Manager) dataFile(name string) string { return filepath.Join(m.opt.DataDir, name) }

func (m *Manager) loadKnown() {
	b, err := os.ReadFile(m.dataFile("players.json"))
	if err != nil {
		return
	}
	var list []*KnownPlayer
	if json.Unmarshal(b, &list) == nil {
		for _, p := range list {
			m.known[p.Name] = p
		}
	}
}

func (m *Manager) saveData() {
	m.mu.Lock()
	list := make([]*KnownPlayer, 0, len(m.known))
	for _, p := range m.known {
		cp := *p
		list = append(list, &cp)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		config.WriteFileAtomic(m.dataFile("players.json"), b, 0o644)
	}
	if b, err := json.Marshal(m.byPlayers); err == nil {
		config.WriteFileAtomic(m.dataFile("stats.json"), b, 0o644)
	}
	// Keep a lighter copy of the last day so the charts survive a restart.
	day := stats.Downsample(m.history.Since(time.Now().Add(-24*time.Hour)), 2880)
	if b, err := json.Marshal(day); err == nil {
		config.WriteFileAtomic(m.dataFile("history.json"), b, 0o644)
	}
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

// KnownPlayers lists everyone who has played, most recent first.
func (m *Manager) KnownPlayers() []KnownPlayer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KnownPlayer, 0, len(m.known))
	for _, p := range m.known {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// notePlayerLocked updates the known-player list from a feed activity.
func (m *Manager) notePlayerLocked(a logparse.Activity, since time.Time) {
	if a.Name == "" || (a.Kind != "join" && a.Kind != "leave") {
		return
	}
	p := m.known[a.Name]
	if p == nil {
		p = &KnownPlayer{Name: a.Name, FirstSeen: a.Time}
		m.known[a.Name] = p
	}
	if a.PlatformID != "" {
		p.PlatformID = a.PlatformID
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

func saveRuntime(path string, info runInfo) {
	if b, err := json.MarshalIndent(info, "", "  "); err == nil {
		config.WriteFileAtomic(path, b, 0o644)
	}
}

func loadRuntime(path string) (runInfo, error) {
	var info runInfo
	b, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if info.PID <= 0 {
		return info, errors.New("no pid")
	}
	return info, nil
}
