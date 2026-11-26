// Package terraria is the adapter for Terraria's vanilla dedicated server
// (spec 5.3): everything the app knows about that server, behind the game
// contract (internal/game).
//
// Terraria is a console game. Its server saves only when "save" or "exit"
// is typed on its console, every 10 minutes while someone plays, and when
// the last player leaves; Ctrl+C ends it without saving. So the app keeps
// a pipe to its console (Plan.Console), stops it with "exit", and types
// "save" before the app itself exits. After an app restart the console is
// gone, and the stop falls back to Ctrl+C (spec 2.11).
//
// Every fact the code relies on was checked against the real server and is
// written down, with its source, in FACTS.md beside this file.
package terraria

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
	"valheim-easy-server/internal/worldstore"
)

// ID is Terraria's game id: its folder, its instance id and its slash
// command (/terraria).
const ID = "terraria"

// defaultPort is the port Terraria's own server uses unless told otherwise.
const defaultPort = 7777

func init() { game.Register(Game{}) }

// Game is Terraria, as the registry knows it.
type Game struct{}

// Info describes Terraria for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Terraria", Short: "Terraria", Letter: "T", Color: "#2c7fb8", Command: ID,
		// The catalog lists the games in the spec's order, ten apart:
		// section 5.n is (n-1)*10, so Valheim (5.1) is 0, Minecraft (5.2)
		// is 10 and Terraria (5.3) is 20. The gaps leave room for a game
		// placed between two others later without renumbering.
		Order: 20,
		// Preview until its real-server cells are green on Windows and the
		// Intel Mac (spec 8.2).
		Stage: game.Preview,
		// The Mac server is an x86_64 program only (FACTS.md), so it needs
		// an Intel Mac. Linux is not a system the app is offered on.
		OS:        map[string]game.Level{"windows": game.Runs, "darwin/amd64": game.Runs, "darwin/arm64": game.CantRun},
		NeedsText: map[string]string{"darwin/arm64": "Needs an Intel Mac", "linux": "Needs Windows or an Intel Mac"},
		// The official zip is 57 MB and holds the servers for all three
		// systems. Unpacking it needs room for all of them for a moment,
		// then only this system's server is kept (about 30 MB on Windows,
		// 75 MB on a Mac), plus the worlds.
		DownloadMB: 57, DiskMB: 300, MemoryMB: 1536, MaxPlayers: 8,
		FriendsNeed: "Terraria on PC or Mac, the same version as the server",
		// Joins and leaves come from the console only, so the manager
		// treats presence as unknown until this install has seen a join
		// (spec 2.6). The server prints no line when a player dies.
		Presence: game.ByName, Deaths: false,
		Ports: []game.PortRole{
			// The server listens on TCP and, for crossplay since 1.4.6, on
			// UDP of the same port (FACTS.md), so both are opened.
			{Role: "game", Label: "Game port", Protos: []string{"tcp", "udp"}, Default: defaultPort, Public: true},
		},
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password."},
		Import: true,
	}
}

// New makes the adapter for one instance. The world store is made here,
// once, because it remembers uploads between calls (worldstore.New).
func (Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store, log: env.Log, http: env.HTTP}
	if s.store == nil {
		s.store = &memStore{}
	}
	if s.log == nil {
		s.log = log.New(os.Stderr, "terraria: ", log.LstdFlags)
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: 30 * time.Second}
	}
	s.worlds = worldStore{newWorldStore(env.Dirs)}
	return s
}

// server is one Terraria instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	http   *http.Client
	worlds worldStore

	mu      sync.Mutex
	version string // the installed version, once known: Join says it without reading a file
}

func (s *server) Store() game.Store { return s.store }

// Program is the file that runs and listens. The real server is launched
// directly, never through the shell scripts the zip has, so the app's one
// process is the server (spec 2.4).
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, laid out
// the same on every system.
func (s *server) Program() string {
	if s.env.Fake != nil {
		return fakeProgram(s.env.Dirs.Server, runtime.GOOS)
	}
	return programFor(s.env.Dirs.Server, runtime.GOOS)
}

// osFolders are the zip's folder for each system, which the install keeps
// as server/<folder>.
var osFolders = map[string]string{"windows": "Windows", "darwin": "Mac", "linux": "Linux"}

// osFolder is the zip's folder for goos.
func osFolder(goos string) string {
	if f, ok := osFolders[goos]; ok {
		return f
	}
	return "Linux"
}

// programRel is the server's program inside its system's folder.
func programRel(goos string) string {
	switch goos {
	case "windows":
		return "TerrariaServer.exe"
	case "darwin":
		// The MonoKickstart program inside the app bundle. The bundle's
		// own entry point and the TerrariaServer script only cd here and
		// set DYLD_LIBRARY_PATH, which the Plan does itself.
		return filepath.Join("Terraria Server.app", "Contents", "MacOS", "TerrariaServer.bin.osx")
	}
	return "TerrariaServer.bin.x86_64"
}

// programFor is where the real server's program is on goos.
func programFor(serverDir, goos string) string {
	return filepath.Join(serverDir, osFolder(goos), programRel(goos))
}

// fakeProgram is where the stand-in goes: the same on every system, with
// ".exe" only on Windows, where nothing else will start.
func fakeProgram(serverDir, goos string) string {
	name := "TerrariaServer"
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(serverDir, name)
}

// Build is what's installed: the version noted at the install, as long as
// the program is still there.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.ReadBuild(s.env.Dirs.Server)
	if !ok {
		return game.Build{}, false
	}
	if _, err := os.Stat(s.Program()); err != nil {
		return game.Build{}, false
	}
	s.noteVersion(b.Version)
	return b, true
}

func (s *server) noteVersion(v string) {
	if v == "" || v == "fake" {
		return
	}
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
}

func (s *server) knownVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// Stop types "exit", which saves the world and ends the server. After a
// takeover the app has no console to type it on, so the chain goes on to
// Ctrl+C, which ends the server at once without saving.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "console", Line: "exit", Wait: 3 * time.Minute},
		{Kind: "interrupt", Wait: time.Minute,
			Say: "Terraria saves every 10 minutes and when the last player leaves; anything since then is lost."},
	}
}

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		if t := cleanLine(l); t != "" {
			lines = append(lines, t)
		}
	}
	has := func(match func(string) bool) bool { return slices.ContainsFunc(lines, match) }
	switch {
	case has(func(l string) bool {
		low := strings.ToLower(l)
		return strings.Contains(low, "already in use") || strings.Contains(low, "only one usage of each socket address") ||
			strings.Contains(l, "Tried to run two servers on the same PC")
	}), portSilentlyTaken(lines):
		port := ""
		for _, l := range lines {
			if m := reListening.FindStringSubmatch(l); m != nil {
				port = " " + m[1]
			}
		}
		return fmt.Sprintf("Another program is using port%s, so the Terraria server couldn't open it. Close that program, or choose new ports for Terraria.", port)
	case has(func(l string) bool {
		// Only XNA itself missing: a stack trace of any other crash can
		// also name Microsoft.Xna.Framework.
		return strings.Contains(l, "Microsoft.Xna.Framework") &&
			(strings.Contains(l, "Could not load") || strings.Contains(l, "FileNotFoundException") || strings.Contains(l, "FileLoadException"))
	}):
		return "Terraria's Windows server needs Microsoft XNA Framework 4.0, which isn't working on this computer. Click Repair to install it again."
	case has(func(l string) bool {
		return strings.HasPrefix(l, "Load failed!") || strings.HasPrefix(l, "Unable to load world")
	}):
		return "The world couldn't be loaded: its file may be damaged. Restore a backup on the Worlds page."
	case has(func(l string) bool {
		return strings.Contains(l, "DllNotFoundException") || strings.Contains(l, "BadImageFormatException")
	}):
		return "A file the Terraria server needs is missing or damaged. Click Repair to install the server again."
	}
	return ""
}

// portSilentlyTaken: when another program holds its TCP port, the server
// says which port it is listening on and then quits without a word
// (checked with the real 1.4.6.1 server, FACTS.md). So a log whose last
// line is the help hint, after "Listening on port", means the port was
// taken.
func portSilentlyTaken(lines []string) bool {
	listening := -1
	for i, l := range lines {
		switch {
		case reListening.MatchString(l):
			listening = i
		case l == "Server started":
			listening = -1
		}
	}
	return listening >= 0 && lines[len(lines)-1] == "Type 'help' for a list of commands."
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

// Make sure the world store's type is what Worlds returns.
var _ game.Worlds = worldStore{}

// newWorldStore is Terraria's world library (see worlds.go).
func newWorldStore(d game.Dirs) game.Worlds {
	return worldstore.New(worldFormat(d.Saves), d.Backups, d.Staging)
}
