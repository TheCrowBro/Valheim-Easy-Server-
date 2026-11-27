// Package vintagestory is the adapter for the plain Vintage Story dedicated
// server (spec 5.5): everything the app knows about that server, behind the
// game contract (internal/game).
//
// The server is Anego Studios' own download from api.vintagestory.at: a .NET
// 10 program (VintagestoryServer.dll) that the app runs with a private copy
// of Microsoft's .NET runtime, started directly as "dotnet
// VintagestoryServer.dll", never through a script, so the one process the
// app watches is the server (spec 2.4). Friends need their own copy of the
// game (they log in with their Vintage Story account).
//
// Vintage Story is a console game: it saves when "/autosavenow" or "/stop"
// is typed on its console, every 5 minutes by itself, and, on Linux at
// least, when it gets Ctrl+C. So the app keeps a pipe to its console
// (Plan.Console), stops it with "/stop", types "/autosavenow" before the app
// itself exits, and falls back to Ctrl+C after an app restart (spec 2.11).
// Each line it types ends with "\r\n", because on Windows the server's
// console reader takes nothing else (endLine, in plan.go).
//
// The server runs on Windows only as far as the app is concerned: Anego
// ships a Mac server only inside the 600 MB game, so on a Mac the game is
// "Needs Windows" (decisions.md). Linux is used for tests.
//
// Every fact the code relies on was checked against the real 1.22.7 server
// and is written down, with its source, in FACTS.md beside this file.
package vintagestory

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// ID is Vintage Story's game id: its folder, its instance id and its slash
// command (/vintagestory).
const ID = "vintagestory"

// defaultPort is the server's own default: it listens on it with both TCP
// and UDP (FACTS.md).
const defaultPort = 42420

func init() { game.Register(Game{}) }

// Game is Vintage Story, as the registry knows it.
type Game struct{}

// Info describes Vintage Story for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Vintage Story", Short: "Vintage Story", Letter: "VS", Color: "#8a6d3b", Command: ID,
		// Section 5.n of the spec gives (n-1)*10, as Valheim's 0 and
		// Minecraft's 10 do (stream notes).
		Order: 40,
		// Preview until its real-server cell is green on Windows and a
		// real join and leave are in testdata (spec 8.2).
		Stage: game.Preview,
		// Only Windows. The Mac server comes only inside the game itself
		// (decisions.md); Linux is not a system the app is offered on.
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// The server zip is 61 MB and .NET's 37 MB; unpacked they take
		// about 330 MB and 80 MB (FACTS.md). The disk figure leaves room
		// for worlds, which grow as friends explore, and their backups.
		DownloadMB: 100, DiskMB: 2000, MemoryMB: 3072, MaxPlayers: 16,
		FriendsNeed: "Vintage Story on PC or Mac (each friend needs their own account), the same version as the server",
		// Joins and leaves come from the console only, so the manager
		// treats presence as unknown until this install has seen a join
		// (spec 2.6). The server has death messages, but no real log shows
		// one on the console, so deaths stay off (FACTS.md).
		Presence: game.ByName, Deaths: false,
		Ports: []game.PortRole{
			// The server opens the port with TCP and UDP (FACTS.md).
			{Role: "game", Label: "Game port", Protos: []string{"tcp", "udp"}, Default: defaultPort, Public: true},
		},
		// Since 1.20 the server lets in only players on its whitelist.
		// This round has no screen for that list, so the app turns the
		// whitelist off and the password is what keeps strangers out.
		Password: game.PasswordRule{Use: "required", Min: 1, Max: maxPassword,
			Help: "Friends type it when they join. It's what keeps strangers out, so it can't be empty."},
		Import: true,
	}
}

// New makes the adapter for one instance. The world store is made here,
// once, because it remembers uploads between calls (worldstore.New). New
// reads nothing from disk: the manager makes every instance before the
// panel answers (spec I5).
func (Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store, log: env.Log}
	if s.store == nil {
		s.store = &memStore{}
	}
	if s.log == nil {
		s.log = log.New(os.Stderr, "vintagestory: ", log.LstdFlags)
	}
	if s.env.HTTP == nil {
		s.env.HTTP = defaultClient
	}
	s.worlds = newWorldStore(env.Dirs)
	return s
}

// server is one Vintage Story instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds game.Worlds

	// version is the installed server's version as Build last found it,
	// kept so Join can name it without reading a file: the manager calls
	// Join with its lock held (spec 4.8).
	mu      sync.Mutex
	version string
}

func (s *server) Store() game.Store { return s.store }

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// exe is a program's file name on this system.
func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Program is the private .NET's dotnet, the program that runs the server
// and listens on its port (so the firewall rule names it). install.DotNet
// keeps each runtime version in a folder of its own, so the path changes
// only when the app installs another .NET (see installDotNet).
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, at the same
// place on every system, with ".exe" only on Windows.
func (s *server) Program() string {
	if s.env.Fake == nil {
		if dotnet, ok := install.FindDotNet(s.env.Dirs.Runtime); ok {
			return dotnet
		}
	}
	return filepath.Join(s.env.Dirs.Runtime, "dotnet", exe("dotnet"))
}

// serverDLL is the server's program for .NET, in its version's folder.
const serverDLL = "VintagestoryServer.dll"

// versionDir is where one version of the server is unpacked: a fresh folder
// for every version, because the server refuses to run with files left
// from an older one ("Your Server installation still contains old files
// from a previous game version", FACTS.md).
func (s *server) versionDir(version string) string {
	return filepath.Join(s.env.Dirs.Server, version)
}

// Build is what's installed: the version the last install wrote down, as
// long as its server files and the .NET runtime are still there.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.ReadBuild(s.env.Dirs.Server)
	if !ok || b.Version == "" || !isFile(filepath.Join(s.versionDir(b.Version), serverDLL)) || !isFile(s.Program()) {
		s.setVersion("")
		return game.Build{}, false
	}
	s.setVersion(b.Version)
	return b, true
}

func (s *server) setVersion(v string) {
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
}

// installedVersion is the version Build last found, without reading a file.
func (s *server) installedVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Stop types "/stop", which saves the world and ends the server. After a
// takeover the app has no console to type it on, so the chain goes on to
// Ctrl+C, which the server handles the same way: it saves and stops
// ("Server termination event SIGINT received", FACTS.md).
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "console", Line: stopLine, Wait: 3 * time.Minute},
		{Kind: "interrupt", Wait: 2 * time.Minute, Resend: time.Minute,
			Say: "Asking Vintage Story to save and shut down…"},
	}
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
