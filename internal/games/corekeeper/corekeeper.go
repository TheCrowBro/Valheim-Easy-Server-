// Package corekeeper is the adapter for the Core Keeper dedicated server
// (spec 5.8): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Core Keeper Dedicated Server" (app 1963720,
// downloaded anonymously), a Unity program. Friends never type an address:
// they join through Steam with the server's Game ID, so the server needs no
// router setup and opens no port of its own that friends connect to.
//
// The app starts CoreKeeperServer.exe directly, never through the shipped
// Launch.bat (which starts PowerShell, which starts the server), so the one
// process the app watches is the server itself (spec 2.4). It gives the
// server -batchmode, as the launcher does, and never -nographics: part of
// making a world runs on the graphics card (the publisher's README).
//
// Each of the app's worlds is a data folder of its own (-datapath), holding
// the server's world 0 and its settings file. The app chooses the Game ID
// once and gives it on every start, so friends keep one Game ID whichever
// world is running.
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package corekeeper

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// ID is Core Keeper's game id: its folder, its instance id and its slash
// command (/corekeeper).
const ID = "corekeeper"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "1963720"

func init() { game.Register(Game{}) }

// Game is Core Keeper, as the registry knows it.
type Game struct{}

// Info describes Core Keeper for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Core Keeper", Short: "Core Keeper", Letter: "C", Color: "#c9a227", Command: ID,
		// Spec section 5.8: (8-1)*10, the scheme the other game streams
		// use (Valheim 0, Minecraft 10, Terraria 20).
		Order: 70,
		// Preview until its real-server cell is green on Windows and a
		// real join and leave are in testdata (spec 8.2).
		Stage: game.Preview,
		// Steam has the server for Windows and Linux only (FACTS.md), and
		// Linux is not a system the app is offered on.
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// Steam's Windows download: the server's depot (417 MB) and
		// Steam's own libraries (68 MB); compressed it is about 190 MB
		// (FACTS.md). The disk figure leaves room for worlds and backups.
		DownloadMB: 490, DiskMB: 1500, MemoryMB: 2048, MaxPlayers: 8,
		FriendsNeed: "Core Keeper on Steam, the same version as the server",
		// Joins and leaves come from the server's log only, so the
		// manager treats presence as unknown until this install has seen
		// a join (spec 2.6). The log has no death lines.
		Presence: game.ByName, Deaths: false,
		// No ports: friends reach the server through Steam's relay with
		// the Game ID. The firewall rule is program-wide (spec 2.12).
		Ports: nil,
		Password: game.PasswordRule{Use: "none",
			Help:     "Core Keeper has no password when friends join with the Game ID.",
			NoneText: "Friends join with the Game ID: share it only with them."},
		// Worlds from the owner's own game are single files named by slot
		// number, which the shared world store can't offer yet (FACTS.md,
		// "Importing worlds").
		Import: false,
	}
}

// New makes the adapter for one instance. The world store is made here,
// once, because it remembers uploads between calls (worldstore.New).
func (Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store, log: env.Log}
	if s.store == nil {
		s.store = &memStore{}
	}
	if s.log == nil {
		s.log = log.New(os.Stderr, "corekeeper: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(env.Dirs)
	return s
}

// server is one Core Keeper instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds worldStore
}

func (s *server) Store() game.Store { return s.store }

// Program is CoreKeeperServer.exe, the file that runs (Steam's launch
// setting is Launch.bat, which only starts it; FACTS.md).
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, at the
// same place on every system, with ".exe" only on Windows. Outside fake
// mode on Linux it is the Linux server, which only the real-server check
// runs.
func (s *server) Program() string { return programFor(s.env.Dirs.Server, runtime.GOOS) }

// programFor is where the server's program is on goos.
func programFor(serverDir, goos string) string {
	if goos == "windows" {
		return filepath.Join(serverDir, "CoreKeeperServer.exe")
	}
	return filepath.Join(serverDir, "CoreKeeperServer")
}

// Installer downloads the server with the shared SteamCMD, or puts the
// stand-in in its place in fake mode.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), []string{
			filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf"),
		})
	}
	return install.SteamCMD(steamApp, s.env.Dirs, func(line string) { s.log.Print(line) })
}

// steamApp is the dedicated server on Steam. Installed, it takes about
// 490 MB on Windows (FACTS.md).
var steamApp = install.SteamApp{AppID: appID, Name: "Core Keeper", SizeGB: 0.5}

// Build is what's installed: Steam's build, as long as the server's
// program is still there. Core Keeper's own version number isn't in any
// file Steam writes; the Monitor reads it from the log ("Game version:").
func (s *server) Build() (game.Build, bool) {
	b, ok := install.SteamBuild(s.env.Dirs.Server, appID)
	if !ok {
		return game.Build{}, false
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Build{}, false
	}
	return b, true
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	has := func(subs ...string) bool {
		return slices.ContainsFunc(lines, func(l string) bool {
			for _, sub := range subs {
				if strings.Contains(l, sub) {
					return true
				}
			}
			return false
		})
	}
	switch {
	case has("Checking system requirements: FAIL", "System requirements not met"):
		return "The Core Keeper server needs a working graphics driver on this computer: part of making a world runs on the graphics card. Update the graphics driver, then start it again."
	case has("Not initializing Steam network", "SteamApi_Init returned false", "SteamAPI_Init() failed"):
		return "The Core Keeper server couldn't start Steam's networking, so friends couldn't join. Click Repair to download its files again, then start it again."
	case has("Failed to start session", "Failed to create listen socket", "Failed to generate Game ID"):
		return "The Core Keeper server couldn't open its Steam session. Check that this computer is online, then start it again."
	case has("Skipping world save due to"):
		return "The Core Keeper server couldn't save the world. Restore a backup of the world on the Worlds page if it doesn't load."
	case has("Something failed during server startup"):
		return "The Core Keeper server stopped while starting. Click Repair to download its files again; if it keeps stopping, restore a backup of the world on the Worlds page."
	}
	return ""
}

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
