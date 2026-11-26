// Package valheim is Valheim's game adapter: everything the app knows about
// the Valheim dedicated server, behind the game contract (internal/game).
//
// Valheim is the legacy game. Installed copies of the app have run it for
// a long time, so it keeps every file and folder where older builds put
// them (settings.json, runtime.json, server/, saves/, backups/, logs/ ...),
// and it behaves exactly as before: the code here was moved out of the
// manager without changing what it does. The manager reaches it only
// through the contract, and gives it access to settings.json and its other
// injected parts through LegacyStore.
package valheim

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

// ID is Valheim's game id: its slash command (/valheim) and, for the
// panel's new routes, its instance id.
const ID = "valheim"

// ReadyWait is how long after logging in to Steam a server that never says
// it opened is counted as ready anyway. Making a big new world can take
// several minutes on an older computer. It is read at each use, so tests
// can shorten it.
var ReadyWait = 10 * time.Minute

// CodeWait is how long the server normally takes to get its join code once
// it's ready, with time to spare. It is read at each use.
var CodeWait = 3 * time.Minute

// codeWaitMods is the longest the Discord post waits for the mods code.
var codeWaitMods = 45 * time.Second

func init() { game.Register(Game{}) }

// LegacyStore is the manager's side of Valheim's legacy files: settings.json
// and the parts the manager is given when it is made (the installer, where
// mods come from). The adapter reads it at each call and never keeps what
// it returns, because the settings and folders can change at any time
// (spec 5.1).
//
// Settings and Paths are snapshots that never take the manager's lock, so
// they can be read while the manager holds it. Update, Installer, ModsURL
// and Note may only be called without the manager's lock held (from jobs
// and the panel's requests), never from a Monitor or from Join.
type LegacyStore interface {
	// Settings is the latest settings.json, as a copy the caller may change.
	Settings() config.Settings
	// Paths are the folders those settings give (config.ResolvePaths).
	Paths() config.Paths
	// Update changes settings.json atomically: change gets a copy of the
	// latest settings, and what it leaves is saved (and checked) unless it
	// returns an error.
	Update(change func(*config.Settings) error) error
	// Installer is the manager's SteamCMD installer: the real one, the
	// stand-in of --dev-fake-server, or a test's steamcmd.Fake.
	Installer() steamcmd.Installer
	// ModsURL is where mods are downloaded from ("" = Thunderstore).
	ModsURL() string
	// Note adds an entry to the app's activity feed (kind as the feed's:
	// "update", "warning", ...). A start uses it for what it installed and
	// for a check for updates that didn't work, as the manager did.
	Note(kind, text string)
}

// Game is Valheim, as the registry knows it.
type Game struct{}

// Info describes Valheim (spec 5.1).
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Valheim", Short: "Valheim", Letter: "V", Color: "#f2a33a", Command: "valheim",
		Order: 0, Stage: game.Ready,
		OS: map[string]game.Level{"windows": game.Runs, "darwin": game.Runs, "linux": game.Runs},
		// The real download is about 2 GB on each system (Steam's depots);
		// the old panel's "about a 1 GB download" sentence stays in Texts.
		DownloadMB: 2100, DiskMB: 2500, MemoryMB: 4096, MaxPlayers: 10,
		FriendsNeed: "Valheim on Steam or Xbox (crossplay), the same version as the server",
		Presence:    game.ByName, Deaths: true,
		Ports: []game.PortRole{
			{Role: "game", Label: "Game port", Protos: []string{"udp"}, Default: config.DefaultGamePort, Public: true},
			{Role: "query", Label: "Query port", Protos: []string{"udp"}, Default: config.DefaultGamePort + 1, Tie: "game+1", Public: true},
		},
		Password: game.PasswordRule{Use: "required", Min: 5, Max: 64,
			Help: "At least 5 characters, without spaces or quotes. It can't be part of the server name or the world name."},
		Legacy: true, Import: true, Lists: true, Mods: true,
	}
}

// New makes the one Valheim instance. env.Legacy must be the manager's
// LegacyStore: Valheim keeps its settings in settings.json, not in
// servers.json, so it has no game.Store of its own from the manager.
func (Game) New(env game.Env) game.Server {
	store, ok := env.Legacy.(LegacyStore)
	if !ok {
		panic("valheim: New needs Env.Legacy, the manager's LegacyStore")
	}
	logger := env.Log
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	return &Server{env: env, store: store, log: logger}
}

// Server is the Valheim instance.
type Server struct {
	env   game.Env
	store LegacyStore
	log   *log.Logger

	lib      atomic.Pointer[worlds.Library] // the worlds library for the current folders
	versions atomic.Pointer[modVersions]    // installed mods, for the current server folder
	codesMu  sync.Mutex                     // modscodes.json
}

var (
	_ game.Server       = (*Server)(nil)
	_ game.OnlineTexter = (*Server)(nil)
	_ game.Afterer      = (*Server)(nil)
	_ game.ModLister    = (*Server)(nil)
	_ game.Lister       = (*Server)(nil)
	_ game.Recoverer    = (*Server)(nil)
)

// Store is Valheim's settings in settings.json, as game values (spec 2.9).
func (s *Server) Store() game.Store { return legacyValues{s} }

// Program is the dedicated server program the app starts and looks for.
func (s *Server) Program() string { return s.store.Paths().ServerExe }

// Installer installs and updates the server with the manager's SteamCMD
// installer, read at each call: tests replace it after the manager is made,
// and it follows folder changes.
func (s *Server) Installer() game.Installer { return installer{s} }

type installer struct{ s *Server }

func (i installer) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	return i.s.store.Installer().Install(ctx, repair, func(p steamcmd.Progress) {
		if progress != nil {
			progress(game.Progress{Step: p.Step, Percent: p.Percent})
		}
	})
}

// Build is the installed Steam build, from SteamCMD's app manifest.
func (s *Server) Build() (game.Build, bool) {
	id, date, ok := steamcmd.InstalledBuild(s.store.Paths().Server)
	if !ok {
		return game.Build{}, false
	}
	return game.Build{Build: id, Date: date}, true
}

// Texts are Valheim's words: its players are vikings, and the panel's
// older sentences stay exactly as they were.
func (s *Server) Texts() game.Texts { return Texts() }

// Texts is Server.Texts without a server, for code that has none.
func Texts() game.Texts {
	return game.Texts{Players: "vikings", Player: "viking", Exact: map[string]string{
		"not_installed":   "The Valheim server isn't installed yet. Click Install first (it's free, about a 1 GB download).",
		"install_missing": "SteamCMD finished, but %s wasn't found. Try Repair.",
		"no_worlds":       "No Valheim worlds were found in that. Add the world's folder (Valheim 1.0), its .fwl and .db files (older versions), or a zip of either.",
	}}
}

// Recover is the run record for a Valheim server found running without
// runtime.json (started before this app, say): today's defaults.
func (s *Server) Recover(pid int, started time.Time) game.Record {
	st, p := s.store.Settings(), s.store.Paths()
	rec := game.Record{PID: pid, Started: started, Launched: started, World: st.World,
		LogPath: NewestLog(p.Logs), Exe: p.ServerExe}
	setNoCrossplay(&rec, !st.Crossplay)
	return rec
}

// NewestLog is the newest server log in dir, or "".
func NewestLog(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "server-*.log"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// modVersions are the installed mods' versions, for the server folder they
// were read from: a folder change reads them again.
type modVersions struct {
	dir string
	v   map[string]string
}

// installedMods returns the versions of the mods installed in the current
// server folder. They are read from the folder the first time and after
// the folder changes, as the manager did when the settings moved it.
func (s *Server) installedMods() map[string]string {
	dir := s.store.Paths().Server
	if cur := s.versions.Load(); cur != nil && cur.dir == dir {
		return cur.v
	}
	v := (&mods.Installer{Dir: dir}).Installed()
	s.versions.Store(&modVersions{dir: dir, v: v})
	return v
}

// setInstalledMods keeps what a start just installed.
func (s *Server) setInstalledMods(dir string, v map[string]string) {
	s.versions.Store(&modVersions{dir: dir, v: v})
}
