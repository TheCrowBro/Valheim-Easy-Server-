// Package zomboid is the adapter for the Project Zomboid dedicated server
// (spec 5.6): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Project Zomboid Dedicated Server" (app
// 380870, downloaded anonymously), Build 42 from Steam's default branch,
// as the owner decided. It is a Java program that comes with its own Java.
// Steam starts it through StartServer64.bat, which starts that Java; the
// app reads the .bat and starts the Java itself, so the one process it
// watches is the server (spec 2.4).
//
// The app talks to the running server over RCON: "players" counts who is
// on (the console prints no join or leave lines, FACTS.md), "quit" saves
// the world and stops it. RCON works the same for a copy of the app that
// took the server over after a restart, so this is not a console game.
//
// Each world is a "server name" to Project Zomboid: its settings file
// Server/<world>.ini, its sandbox file Server/<world>_SandboxVars.lua, its
// map in Saves/Multiplayer/<world>/ and its accounts in db/<world>.db, all
// under the instance's saves folder (the server's -cachedir).
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package zomboid

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

// ID is Project Zomboid's game id: its folder, its instance id and its
// slash command (/zomboid).
const ID = "zomboid"

// appID is the dedicated server's Steam app (anonymous download). Build 42
// is its default ("public") branch, so no branch is named.
const appID = "380870"

// Port roles and the server's own defaults (FACTS.md).
const (
	roleGame   = "game"   // DefaultPort: friends connect here, and Steam's queries come here too
	roleDirect = "direct" // UDPPort: the direct connection, always the game port plus one
	roleRCON   = "rcon"   // RCONPort: only the app uses it, from this computer

	defaultGamePort = 16261
	defaultRCONPort = 27015
)

// mainClass is the server's Java program, as every launcher names it.
const mainClass = "zombie.network.GameServer"

// adminUser is the account the server makes for its administrator on the
// first start (FACTS.md: "admin" is the default and the one name starting
// with "admin" the server allows).
const adminUser = "admin"

func init() { game.Register(Game{}) }

// Game is Project Zomboid, as the registry knows it.
type Game struct{}

// Info describes Project Zomboid for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Project Zomboid", Short: "Project Zomboid", Letter: "PZ", Color: "#b03a2e", Command: ID,
		// Section 5.n of the spec gives (n-1)*10, as Minecraft (10) and
		// Terraria (20) do, so the games line up in the spec's order.
		Order: 50,
		// Preview until its real-server cell is green on Windows (spec 8.2).
		Stage: game.Preview,
		// The dedicated server runs on Windows and Linux; the app offers
		// it on Windows only (decisions.md: "Needs Windows" on a Mac).
		// Linux is not a system the app is offered on.
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// The download is given as the depots' size, about 7.1 GB (spec
		// 5.6), the way every game gives it, so Add a game compares like
		// with like; Steam sends it compressed, about 1.9 GB (FACTS.md).
		// The disk figure leaves room for worlds, the app's backups and
		// the server's own startup backups.
		DownloadMB: 7100, DiskMB: 10000,
		// The server's Java gets 6 GB by default (memoryMB), and Java needs
		// a good deal beside that for itself.
		MemoryMB: 8192, MaxPlayers: 16,
		FriendsNeed: "Project Zomboid (Build 42) on PC or Mac, the same version as the server",
		// Who is on comes from RCON's "players", which names them. Deaths
		// stay off: the console never says when a player dies (FACTS.md).
		Presence: game.ByName, Deaths: false,
		Ports: []game.PortRole{
			{Role: roleGame, Label: "Game port", Protos: []string{"udp"}, Default: defaultGamePort, Public: true},
			{Role: roleDirect, Label: "Direct connection port", Protos: []string{"udp"}, Default: defaultGamePort + 1, Tie: "game+1", Public: true},
			// Only the app uses RCON, and the server is told to listen
			// for it on 127.0.0.1 alone (FACTS.md), so it needs no
			// firewall rule or router mapping.
			{Role: roleRCON, Label: "RCON port (this computer only)", Protos: []string{"tcp"}, Default: defaultRCONPort, Public: false},
		},
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password."},
		// A single-player save isn't a server's world (FACTS.md).
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
		s.log = log.New(os.Stderr, "zomboid: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(env.Dirs)
	return s
}

// server is one Project Zomboid instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds worldStore
}

func (s *server) Store() game.Store { return s.store }

// Program is the server's own Java, the file that runs and listens.
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, at the
// same place on every system, with ".exe" only on Windows.
func (s *server) Program() string {
	goos := runtime.GOOS
	if s.env.Fake != nil && goos != "windows" {
		goos = "linux"
	}
	return programFor(s.env.Dirs.Server, goos)
}

// programFor is where the server's Java is on goos: jre64\bin\java.exe,
// the one StartServer64.bat runs. java.exe is a console program, so
// Ctrl+C reaches it (spec 2.7).
func programFor(serverDir, goos string) string {
	if goos == "windows" {
		return filepath.Join(serverDir, "jre64", "bin", "java.exe")
	}
	return filepath.Join(serverDir, "jre64", "bin", "java")
}

// launchOS is the system whose launch files the plan reads. In fake mode
// that is always Windows: the stand-in is installed with the real Windows
// launch files, so the plan is made exactly as on the owner's PC.
func (s *server) launchOS() string {
	if s.env.Fake != nil {
		return "windows"
	}
	return runtime.GOOS
}

// steamApp is the dedicated server on Steam. Installed, it takes about
// 7.1 GB on Windows (FACTS.md).
var steamApp = install.SteamApp{AppID: appID, Name: "Project Zomboid", SizeGB: 7.1}

// Installer downloads the server with the shared SteamCMD and checks that
// the app can read how to start it; in fake mode it puts the stand-in in
// its place, with the real launch files beside it.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return &fakeInstaller{inner: install.Fake(s.env.Fake, s.Program(), []string{
			filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf"),
			filepath.Join(s.env.Dirs.Server, "java", "projectzomboid.jar"),
		}), server: s.env.Dirs.Server}
	}
	return &checkedInstaller{inner: install.SteamCMD(steamApp, s.env.Dirs, func(line string) { s.log.Print(line) }),
		server: s.env.Dirs.Server, goos: s.launchOS(), log: s.log}
}

// Build is what's installed: Steam's build, as long as the server's Java
// and a launch file the app can read are still there. Project Zomboid's
// own version number is in no file Steam writes; the Monitor reads it
// from the server's first lines.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.SteamBuild(s.env.Dirs.Server, appID)
	if !ok {
		return game.Build{}, false
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Build{}, false
	}
	if !launchFileThere(s.env.Dirs.Server) {
		return game.Build{}, false
	}
	return b, true
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// Stop asks the server over RCON to quit, which saves the world and ends
// it (about 10 seconds on a small world, FACTS.md). The RCON port and
// password come from the run record, so a copy of the app that took the
// server over stops it the same way. If RCON can't be reached, Ctrl+C:
// Java then runs the server's shutdown handling, which also saves.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "rcon", Line: "quit", Wait: 4 * time.Minute},
		{Kind: "interrupt", Wait: 2 * time.Minute, Say: "Asking Project Zomboid to save and shut down (Ctrl+C)…"},
	}
}

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		if t, _, _ := splitLine(l); t != "" {
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
	case has("Connection Startup Failed"):
		return "Project Zomboid couldn't open its UDP ports: another program is most likely using one of them. Close that program, or choose new ports for Project Zomboid."
	case has("Failed to connect to Steam servers"):
		return "The Project Zomboid server couldn't reach Steam, which it needs to start. Check this computer's internet connection, then start it again."
	case has("Could not reserve enough space", "Invalid maximum heap size", "Too small maximum heap", "Initial heap size set to a larger value"):
		return "Java couldn't have the memory Project Zomboid's settings give it. Lower \"Java memory\" in Project Zomboid's settings, or close other programs."
	case has("java.lang.OutOfMemoryError"):
		return "The Project Zomboid server ran out of memory. Raise \"Java memory\" in Project Zomboid's settings."
	case has("Exiting due to errors loading"):
		return "The world's sandbox settings file (Server/<world>_SandboxVars.lua in Project Zomboid's saves folder) has a mistake in it. Restore a backup of the world on the Worlds page, or fix the file."
	case has("Could not find or load main class", "ClassNotFoundException: zombie", "UnsupportedClassVersionError", "Illegal termination of worker thread", "Error: could not open", "Error occurred during initialization of VM"):
		return "Some of the Project Zomboid server's files are missing or damaged. Click Repair to download them again."
	case has("Workshop: item state DownloadPending -> Fail"):
		return "The Project Zomboid server couldn't download a Steam Workshop mod its settings name, and stopped. Remove the mod from the world's settings file, or try again later."
	case has("Required Animation Asset not found"):
		// What a server that can't load its own character models says
		// (FACTS.md, the first real-server check).
		return "The Project Zomboid server couldn't load some of its own files (its character animations) and gave up. Click Repair to download them again, then start it again."
	case has("Server Terminated"):
		return "The Project Zomboid server hit an error and gave up. The Log page shows the error just above \"Server Terminated.\"; if it names one of the server's own files, click Repair."
	}
	return ""
}

// launchFileThere says whether a launch file the plan reads is in the
// server folder (without reading it: Build is called often).
func launchFileThere(serverDir string) bool {
	for _, name := range []string{batName, jsonName} {
		if _, err := os.Stat(filepath.Join(serverDir, name)); err == nil {
			return true
		}
	}
	return false
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
