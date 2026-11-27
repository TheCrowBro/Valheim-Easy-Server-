// Package palworld is the adapter for the Palworld 1.0 dedicated server
// (spec 5.9): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Palworld Dedicated Server" (app 2394010,
// downloaded anonymously), an Unreal Engine program for Windows (and
// Linux, which the app doesn't offer). Steam starts it through
// PalServer.exe, a small launcher that starts the real server,
// Pal\Binaries\Win64\PalServer-Win64-Shipping-Cmd.exe; the app starts that
// program directly, so the one process it watches is the server (spec 2.4).
//
// The app talks to the server through its REST API on a private port
// (8212 by default), with a new admin password for every run: the probe
// asks it who is on, and the stop asks it to save and shut down. Friends
// join by this computer's address and the game port (UDP 8211), and their
// game must be exactly the server's version, so the server is updated at
// every start.
//
// Palworld keeps its worlds in the install folder, one folder per world
// named with a 32-character id; the app keeps the names it shows in the
// instance's State ("world.<id>" = name) and picks the id itself for a new
// world (worlds.go).
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package palworld

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
)

// ID is Palworld's game id: its folder, its instance id and its slash
// command (/palworld).
const ID = "palworld"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "2394010"

// Default ports (FACTS.md): the game on UDP, the REST API on TCP.
const (
	defaultGamePort = 8211
	defaultRESTPort = 8212
)

func init() { game.Register(Game{}) }

// Game is Palworld, as the registry knows it.
type Game struct{}

// Info describes Palworld for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Palworld", Short: "Palworld", Letter: "P", Color: "#d6408b", Command: ID,
		// Section 5.9 of the spec: (9-1)*10, the scheme Minecraft (10)
		// and Terraria (20) use.
		Order: 80,
		// Preview until its real-server cell is green on Windows (spec 8.2).
		Stage: game.Preview,
		// The server is built for Windows and Linux only (Steam's app
		// info, FACTS.md); the app is offered on Windows and macOS.
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// Steam's Windows download is 4.67 GB and it takes 5.75 GB once
		// installed (FACTS.md); the disk figure leaves room for the worlds,
		// the server's own world backups and the app's.
		DownloadMB: 4700, DiskMB: 9000,
		// The docs: 16 GB recommended, 8 GB starts but risks running out.
		MemoryMB: 10240, MaxPlayers: maxSlots,
		FriendsNeed: "Palworld on PC or Mac, exactly the same version as the server (Xbox and PlayStation can't join by address)",
		// Who is on comes from the REST API, which names them (spec 2.6).
		Presence: game.ByName, Deaths: false,
		Ports: []game.PortRole{
			{Role: "game", Label: "Game port", Protos: []string{"udp"}, Default: defaultGamePort, Public: true},
			// The REST API is for the app alone: no firewall rule and no
			// router mapping (spec 2.5). It has a new password every run.
			{Role: "rest", Label: "REST API port (only for the app)", Protos: []string{"tcp"}, Default: defaultRESTPort},
		},
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password."},
	}
}

// New makes the adapter for one instance. The world store is made here,
// once, because it remembers uploads and runs changes one at a time
// (worldstore.New).
func (Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store, log: env.Log}
	if s.store == nil {
		s.store = &memStore{}
	}
	if s.log == nil {
		s.log = log.New(os.Stderr, "palworld: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(s)
	return s
}

// The contract's parts a Palworld instance has: the REST API is its probe,
// and a server found running without its run file is given one.
var (
	_ game.Server    = (*server)(nil)
	_ game.Prober    = (*server)(nil)
	_ game.Recoverer = (*server)(nil)
)

// server is one Palworld instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds *worldStore
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
// launcher PalServer.exe only starts this one and passes its arguments on
// (FACTS.md).
func (s *server) Program() string {
	return programFor(s.env.Dirs.Server, s.layout(), runtime.GOOS)
}

func programFor(serverDir, layout, goos string) string {
	if layout == "windows" {
		name := "PalServer-Win64-Shipping-Cmd"
		if goos == "windows" {
			name += ".exe"
		}
		return filepath.Join(serverDir, "Pal", "Binaries", "Win64", name)
	}
	// The Linux server, which PalServer.sh starts. Linux isn't a system
	// the app offers Palworld on; this is here so the paths stay right
	// for a developer who runs it there.
	return filepath.Join(serverDir, "Pal", "Binaries", "Linux", "PalServer-Linux-Shipping")
}

// configDir is where the server reads its settings: WindowsServer on
// Windows, LinuxServer on Linux (the docs, FACTS.md).
func (s *server) configDir() string {
	sub := "WindowsServer"
	if s.layout() != "windows" {
		sub = "LinuxServer"
	}
	return filepath.Join(s.env.Dirs.Server, "Pal", "Saved", "Config", sub)
}

func (s *server) settingsPath() string { return filepath.Join(s.configDir(), "PalWorldSettings.ini") }
func (s *server) userSettingsPath() string {
	return filepath.Join(s.configDir(), "GameUserSettings.ini")
}

// defaultSettingsPath is the sample the download ships, which the docs say
// to copy to PalWorldSettings.ini.
func (s *server) defaultSettingsPath() string {
	return filepath.Join(s.env.Dirs.Server, "DefaultPalWorldSettings.ini")
}

// savesDir holds the worlds: Pal/Saved/SaveGames/0/<world id>/ (FACTS.md).
func (s *server) savesDir() string {
	return filepath.Join(s.env.Dirs.Server, "Pal", "Saved", "SaveGames", "0")
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
	case has("address already in use", "only one usage of each socket address", "failed to bind", "could not bind"):
		return "Another program is using one of Palworld's ports, so the server couldn't open it. Close that program, or choose new ports for Palworld."
	case has("out of memory", "outofmemory", "ran out of memory"):
		return "The Palworld server ran out of memory. Close other programs or games; Palworld needs about 10 GB."
	case has("fatal error", "lowlevelfatalerror", "unhandled exception", "assertion failed"):
		return "The Palworld server crashed. Start it again; if it keeps crashing, click Repair, or restore a backup of the world on the Worlds page."
	case !has("game version is", "running palworld dedicated server", "[log]"):
		// It ended before the game itself wrote a line. A server that
		// can't load a library it needs (the Visual C++ runtime) ends
		// like that, and Repair puts both the server's files and that
		// runtime back.
		return "The Palworld server stopped before it started up. If it happens again, click Repair: it checks the server's files and installs the Microsoft Visual C++ runtime Palworld needs."
	}
	return ""
}

// stopWait is how long the REST shutdown may take before Ctrl+C is tried:
// the server saves first, which takes a while for a big world.
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
