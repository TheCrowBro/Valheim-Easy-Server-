// Package windrose is the adapter for the Windrose dedicated server (spec
// 5.11): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Windrose Dedicated Server" (app 4129620,
// downloaded anonymously), an Unreal Engine program for Windows only.
// Friends never type an address: they paste the server's invite code in
// the game (Play, Connect to Server), and Windrose's own online service
// connects them through NAT punch-through, so nobody sets up the router.
// The server has no console, RCON or web API: the app writes its settings
// file (R5/ServerDescription.json) while it is stopped, follows its log
// (R5/Saved/Logs/R5.log) and stops it with Ctrl+C, which makes it save.
//
// The app starts R5/Binaries/Win64/WindroseServer-Win64-Shipping.exe with
// -log itself, as the shipped StartServerForeground.bat does, never the
// WindroseServer.exe launcher in the install's root, so the one process
// the app watches is the server (spec 2.4).
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package windrose

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// ID is Windrose's game id: its folder, its instance id and its slash
// command (/windrose).
const ID = "windrose"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "4129620"

func init() { game.Register(Game{}) }

// Game is Windrose, as the registry knows it.
type Game struct{}

// Info describes Windrose for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Windrose", Short: "Windrose", Letter: "W", Color: "#34495e", Command: ID,
		// Spec section 5.11: (11-1)*10, the scheme the other game streams
		// use (Valheim 0, Minecraft 10, ... Satisfactory 90).
		Order: 100,
		// Preview until its real-server cell is green on Windows and a
		// real session's join and leave are in testdata (spec 8.2).
		Stage: game.Preview,
		// The publisher ships the server for Windows only (FACTS.md).
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// Steam's Windows install is 3.1 GB (the server's depot and Steam's
		// own libraries; about 0.9 GB is downloaded, compressed). The
		// publisher asks for 35 GB of disk, which leaves room for worlds,
		// the server's own backups and updates (FACTS.md).
		DownloadMB: 3105, DiskMB: 35000,
		// The publisher: 8 GB for 2 players, 12 GB for 4, 16 GB for 10.
		MemoryMB: 12288, MaxPlayers: 4,
		FriendsNeed: "Windrose (on Steam, Epic Games Store or Stove), the same version as the server",
		// Joins and leaves come from the server's log only (its account
		// lines, FACTS.md "Who is playing"), so the manager treats presence
		// as unknown until this install has seen a join (spec 2.6).
		Presence: game.ByName, Deaths: false,
		// No ports: friends reach the server through Windrose's online
		// service (NAT punch-through), on ports the server picks. Its
		// firewall rule is program-wide (spec 2.12).
		Ports: nil,
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty so the invite code is all they need."},
		// Worlds from the owner's own game are RocksDB databases tied to
		// the server that made them (FACTS.md, "Worlds"); not this round.
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
		s.log = log.New(os.Stderr, "windrose: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(s)
	return s
}

// server is one Windrose instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds *worldStore
}

func (s *server) Store() game.Store { return s.store }

// Program is the server's shipping program, the file that runs: the
// launcher in the install's root (WindroseServer.exe, Steam's launch
// setting) only checks for the Visual C++ runtime and starts this one
// (FACTS.md).
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, at the
// same place on every system, with ".exe" only on Windows.
func (s *server) Program() string { return programFor(s.env.Dirs.Server, runtime.GOOS) }

// programFor is where the server's program is on goos.
func programFor(serverDir, goos string) string {
	name := "WindroseServer-Win64-Shipping"
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(serverDir, "R5", "Binaries", "Win64", name)
}

// Paths inside the install, as the server lays them out (FACTS.md).
func (s *server) descriptionPath() string {
	return filepath.Join(s.env.Dirs.Server, "R5", "ServerDescription.json")
}
func (s *server) logPath() string {
	return filepath.Join(s.env.Dirs.Server, "R5", "Saved", "Logs", "R5.log")
}
func (s *server) savesDir() string {
	return filepath.Join(s.env.Dirs.Server, "R5", "Saved", "SaveProfiles", "Default")
}

// Build is what's installed: Steam's build, as long as the server's
// program is still there. Windrose's own version ("0.10.0.9.32") is in
// ServerDescription.json's DeploymentId, which the Monitor reads from the
// log; Build never reads that file, so it stays cheap.
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

// Recover is the run record for a server found running without its run
// file (started by Steam, say). Its log is the server's own R5.log, which
// the manager would otherwise look for among the app's per-run logs.
func (s *server) Recover(pid int, started time.Time) game.Record {
	sv := s.store.Get()
	return game.Record{PID: pid, Started: started, Launched: started, World: sv.World,
		LogPath: s.logPath(), Exe: s.Program(), Server: s.value(sv, "serverName")}
}

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
	case len(lines) == 0:
		return "The Windrose server stopped before it wrote anything. Click Repair to download its files and Microsoft's Visual C++ runtime again, then start it again."
	case has("Another instance of server is running from the same folder"):
		return "Another copy of the Windrose server is already running from this folder. Stop it (in Task Manager it is WindroseServer-Win64-Shipping.exe), then start it again."
	case has("Cannot load island"):
		return "The Windrose server couldn't load the world. Restore one of its backups on the Worlds page, or choose another world, then start it again."
	case has("GcStream is broken", "GsStream is broken", "Server Authorization failed", "Server registration finished with error"):
		return "The Windrose server couldn't keep its connection to Windrose's online service, which friends need to find it by its invite code. Check this computer's internet connection and start it again; if it keeps happening, the service may be down for a while."
	case has("Save backup finished with error"):
		return "The Windrose server couldn't save the world. Check that the disk isn't full, then start it again; if the world doesn't load, restore one of its backups on the Worlds page."
	case has("=== Critical error: ===", "Crash Stack Trace", "appError called", "Fatal error", "Error: [Callstack]"):
		return "The Windrose server crashed. Start it again; if it keeps crashing, click Repair to download its files again."
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
