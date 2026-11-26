// Package satisfactory is the adapter for the Satisfactory 1.x dedicated
// server (spec 5.10): everything the app knows about that server, behind
// the game contract (internal/game).
//
// The server is Steam's free "Satisfactory Dedicated Server" (app 1690800,
// downloaded anonymously), an Unreal Engine 5 program for Windows (and
// Linux, which the app doesn't offer). Steam starts it through
// FactoryServer.exe, a small launcher that starts the real server,
// Engine\Binaries\Win64\FactoryServer-Win64-Shipping-Cmd.exe; the app
// starts that program directly, so the one process it watches is the
// server (spec 2.4).
//
// A new Satisfactory server does nothing until someone "claims" it: in
// the game, a player normally opens the Server Manager, gives the server a
// name and an admin password, and creates a game. The app does all of that
// itself through the server's HTTPS API, on the game port (7777 by
// default), so the owner never has to: right after each start it logs in,
// claims the server the first time, applies the server name and the
// friends' password, and loads (or creates) the selected world (setup.go).
// The same API answers the probe (how many are on) and stops the server
// cleanly (save, then shut down).
//
// The API's certificate is one the server made for itself, which no
// authority vouches for, so the app checks it in its own way: it remembers
// the certificate's key the first time and then only talks to that key,
// unless the program holding the port is the very server the app started
// (api.go).
//
// The server keeps its worlds ("sessions", made of .sav files) in the
// Windows user's %LocalAppData%\FactoryGame\Saved\SaveGames\server, outside
// the app's data folder, whatever its install folder is (worlds.go).
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package satisfactory

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// ID is Satisfactory's game id: its folder, its instance id and its slash
// command (/satisfactory).
const ID = "satisfactory"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "1690800"

// Default ports (FACTS.md). The game port is UDP for the game and TCP for
// the HTTPS API; the reliable messaging port is TCP. Both must reach the
// server from outside for friends to join.
const (
	defaultGamePort     = 7777
	defaultReliablePort = 8888
)

func init() { game.Register(Game{}) }

// Game is Satisfactory, as the registry knows it.
type Game struct{}

// Info describes Satisfactory for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Satisfactory", Short: "Satisfactory", Letter: "S", Color: "#d35400", Command: ID,
		// Section 5.10 of the spec: (10-1)*10, the scheme Minecraft (10),
		// Terraria (20) and Palworld (80) use.
		Order: 90,
		// Preview until its real-server check is green on Windows (spec
		// 8.2); that check runs on demand only, because of the download.
		Stage: game.Preview,
		// The server is built for Windows and Linux only (Steam's app
		// info, FACTS.md); the app is offered on Windows and macOS.
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// Steam's Windows download is 3.6 GB, which unpacks to 15.5 GB
		// (FACTS.md); the disk figure leaves room for saves and backups.
		DownloadMB: 3700, DiskMB: 17000,
		// The wiki: 8 GB, 16 GB for big factories or more than 4 players.
		MemoryMB: 12288, MaxPlayers: 4,
		FriendsNeed: "Satisfactory on PC (Steam or Epic), the same version as the server",
		// How many are on comes from the HTTPS API, which can't be faked
		// by a player (spec 2.6). Names come from the log when it has them.
		Presence: game.ByCount, Deaths: false,
		Ports: []game.PortRole{
			{Role: "game", Label: "Game port (also the server's API)", Protos: []string{"udp", "tcp"}, Default: defaultGamePort, Public: true},
			// Not tied to the game port: the server takes it as its own
			// option (-ReliablePort), so it is picked on its own.
			{Role: "reliable", Label: "Reliable messaging port", Protos: []string{"tcp"}, Default: defaultReliablePort, Public: true},
		},
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password."},
		// A single-player save from the owner's own game can be imported.
		Import: true,
	}
}

// New makes the adapter for one instance. The world store is made here,
// once, because it remembers uploads and runs changes one at a time
// (worldstore.New).
func (Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store, log: env.Log, setups: map[string]*setupRun{}}
	if s.store == nil {
		s.store = &memStore{}
	}
	if s.log == nil {
		s.log = log.New(os.Stderr, "satisfactory: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(s)
	return s
}

// The contract's parts a Satisfactory instance has: the HTTPS API is its
// probe; its setup runs after each launch (Afterer); a server found
// running without its run file is given one (Recoverer); and removing the
// game lets a later copy claim the server afresh (Remover).
var (
	_ game.Server    = (*server)(nil)
	_ game.Prober    = (*server)(nil)
	_ game.Afterer   = (*server)(nil)
	_ game.Recoverer = (*server)(nil)
	_ game.Remover   = (*server)(nil)
)

// server is one Satisfactory instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds *worldStore

	// The setups this copy of the app has run, one per run of the
	// server (setup.go). Guarded by setupMu, never the manager's lock.
	setupMu sync.Mutex
	setups  map[string]*setupRun

	// tokenMu guards the API token the probe and the stop reuse, so they
	// don't log in again every 30 seconds.
	tokenMu  sync.Mutex
	token    string
	tokenFor string // the port and admin password the token was made with
}

func (s *server) Store() game.Store { return s.store }

// layout says which of the server's builds the paths are for. In fake mode
// (tests, EGS_DEV_FAKE_SERVERS) it is always the Windows layout, so the
// stand-in sits at the same place on every system, with ".exe" only on
// Windows.
func (s *server) layout() string {
	if s.env.Fake != nil || runtime.GOOS == "windows" {
		return "windows"
	}
	return "linux"
}

// Program is the real server, the file that runs and listens. Steam's
// launcher FactoryServer.exe only starts this one and passes its arguments
// on (FACTS.md).
func (s *server) Program() string {
	return programFor(s.env.Dirs.Server, s.layout(), runtime.GOOS)
}

func programFor(serverDir, layout, goos string) string {
	if layout == "windows" {
		name := "FactoryServer-Win64-Shipping-Cmd"
		if goos == "windows" {
			name += ".exe"
		}
		return filepath.Join(serverDir, "Engine", "Binaries", "Win64", name)
	}
	// The Linux server, which FactoryServer.sh starts. Linux isn't a
	// system the app offers Satisfactory on; this is here so the paths
	// stay right for a developer who runs it there.
	return filepath.Join(serverDir, "Engine", "Binaries", "Linux", "FactoryServer-Linux-Shipping")
}

// logPath is the log the server writes itself, in its install folder
// (FACTS.md). The app follows it (Output GameLog).
func (s *server) logPath() string {
	return filepath.Join(s.env.Dirs.Server, "FactoryGame", "Saved", "Logs", "FactoryGame.log")
}

// saveGamesDir is the server's SaveGames folder: the worlds are in its
// "server" folder, and the server's own settings (its claim, passwords,
// certificate) in ServerSettings.<port>.sav beside it (FACTS.md).
//
// The real server always uses the Windows user's Local AppData. The
// stand-in is pointed at the instance's own saves folder instead (plan.go
// sets LOCALAPPDATA for it), so tests and a developer's fake games never
// touch the owner's real Satisfactory saves.
func (s *server) saveGamesDir() string {
	if s.env.Fake != nil {
		return filepath.Join(s.env.Dirs.Saves, "FactoryGame", "Saved", "SaveGames")
	}
	if s.layout() == "windows" {
		return filepath.Join(localAppData(), "FactoryGame", "Saved", "SaveGames")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "Epic", "FactoryGame", "Saved", "SaveGames")
}

// localAppData is the Windows user's Local AppData folder: what Windows
// says it is, else its environment variable, else Go's own guess.
func localAppData() string {
	if p := platform.KnownFolder("localappdata"); p != "" {
		return p
	}
	if p := os.Getenv("LOCALAPPDATA"); p != "" {
		return p
	}
	if p, err := os.UserCacheDir(); err == nil {
		return p
	}
	return ""
}

// savesDir holds the sessions' .sav files.
func (s *server) savesDir() string { return filepath.Join(s.saveGamesDir(), "server") }

// settingsFile is the server's own settings for one game port: its claim,
// its passwords, its certificate and the session it loads at start.
func (s *server) settingsFile(port int) string {
	return filepath.Join(s.saveGamesDir(), "ServerSettings."+itoa(port)+".sav")
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	has := func(subs ...string) bool {
		for _, l := range tail {
			low := strings.ToLower(l)
			for _, sub := range subs {
				if strings.Contains(low, sub) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("address already in use", "only one usage of each socket address", "failed to bind", "could not bind", "unable to bind", "failed to listen"):
		return "Another program is using one of Satisfactory's ports, so the server couldn't open it. Close that program, or choose new ports for Satisfactory."
	case has("out of memory", "outofmemory", "ran out of memory"):
		return "The Satisfactory server ran out of memory. Close other programs or games; Satisfactory needs about 12 GB, more for a big factory."
	case has("fatal error", "critical error", "unhandled exception", "assertion failed"):
		return "The Satisfactory server crashed. Start it again; if it keeps crashing, click Repair, or restore a backup of the world on the Worlds page."
	case !has("loginit", "logserver", "lognet", "logconfig"):
		// It ended before the engine wrote a line. A program that can't
		// load a library it needs ends like that; Repair checks every
		// file of the server.
		return "The Satisfactory server stopped before it started up. If it happens again, click Repair: it checks the server's files. If that doesn't help, install Microsoft's Visual C++ runtime (x64) and try again."
	}
	return ""
}

// How long the stop chain waits: the API's save and shutdown first (a big
// factory takes a while to save), then Ctrl+C.
const (
	stopWait      = 3 * time.Minute
	interruptWait = 2 * time.Minute
)

// memStore keeps the settings in memory when the instance has no Store
// (never in the app, which always gives one; a safety net for tools).
type memStore struct {
	mu sync.Mutex
	s  game.Saved
}

func (m *memStore) Get() game.Saved {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s.Clone()
}

func (m *memStore) Save(change func(*game.Saved) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.s.Clone()
	if err := change(&c); err != nil {
		return err
	}
	m.s = c
	return nil
}
