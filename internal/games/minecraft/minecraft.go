// Package minecraft is the adapter for Minecraft: Java Edition's vanilla
// server, the server.jar Mojang publishes (spec 5.2). It runs on a private
// Java (Eclipse Temurin) that the app installs next to the server, so
// nothing on the owner's computer has to be set up by hand.
//
// How it fits the app, in short:
//   - Install: Mojang's version list names the newest release, its
//     server.jar with a SHA-1, and the Java version it needs (resolve.go,
//     install.go).
//   - Start: the plan writes server.properties (with the allow list decided
//     before the port opens), eula.txt and whitelist.json, then runs
//     "java -jar server.jar --nogui --universe <saves> --world <name>"
//     (plan.go, properties.go, allowlist.go).
//   - While it runs: the log tells who joins, leaves and dies (monitor.go,
//     deaths.go); RCON tells how many are on (probe.go) and stops it
//     (stop.go).
//   - Friends: the allow list, admins and bans are Minecraft's own files,
//     changed over RCON while it runs (lists.go); they join by address
//     (join.go).
//
// Every fact about the real server this code relies on is in FACTS.md,
// with where it was checked.
package minecraft

import (
	"path/filepath"
	"runtime"
	"sync/atomic"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// ID is the game's id, folder and slash command.
const ID = "minecraft"

// Game is Minecraft: Java Edition. Like every game, it registers itself
// when internal/games/all imports the package.
type Game struct{}

func init() { game.Register(Game{}) }

// The ports' roles, as the run record and servers.json name them.
const (
	roleGame = "game"
	roleRCON = "rcon"
)

// defaultPort is Minecraft's own port: friends don't type it, so the join
// address leaves it out when the server uses it.
const defaultPort = 25565

// Info describes the game for the panel, the manager and Discord.
func (Game) Info() game.Info {
	return game.Info{
		ID:      ID,
		Name:    "Minecraft: Java Edition",
		Short:   "Minecraft",
		Letter:  "M",
		Color:   "#5b8731",
		Command: ID,
		Order:   10,
		// Preview until the real-server cells are green on Windows and
		// the Intel Mac (spec 8.2).
		Stage: game.Preview,
		// The server is a Java program, and the app installs a Java for
		// it on both systems the owner uses. Temurin 25's macOS x64 Java
		// needs macOS 11, so the Mac Pro on macOS 12 can run it
		// (FACTS.md); if a later Java can't start there, the installer
		// says so (install.go).
		OS:          map[string]game.Level{"windows": game.Runs, "darwin": game.Runs},
		DownloadMB:  120, // server.jar 62 MB plus Java 42 MB (Mac) to 59 MB (Windows)
		DiskMB:      500, // the jar and what it unpacks (127 MB), Java (about 190 MB), a new world (16 MB)
		MemoryMB:    3072,
		MaxPlayers:  10,
		FriendsNeed: "Minecraft: Java Edition on PC or Mac, the same version as the server",
		Presence:    game.ByName,
		Deaths:      true,
		Ports: []game.PortRole{
			{Role: roleGame, Label: "Game port", Protos: []string{"tcp"}, Default: defaultPort, Public: true},
			// RCON is how the app counts players, manages the lists and
			// stops the server. Friends never use it, so it gets no
			// router mapping and no firewall opening (spec 2.5).
			{Role: roleRCON, Label: "RCON port (for the app)", Protos: []string{"tcp"}, Default: 25575},
		},
		Password: game.PasswordRule{
			Use:      "none",
			Help:     "Minecraft servers have no password.",
			NoneText: "Minecraft uses an allow list instead of a password: friends are added on the Players tab.",
		},
		Import: true,
		Lists:  true,
		Mods:   false, // a later round
	}
}

// New makes the adapter for one instance. The world store is made once
// here and kept, as worldstore asks (it remembers uploads between calls).
func (g Game) New(env game.Env) game.Server {
	s := &server{env: env, store: env.Store}
	if s.store == nil {
		s.store = &memStore{}
	}
	s.worlds = newWorlds(env.Dirs)
	return s
}

// server is one instance of the Minecraft server.
type server struct {
	env    game.Env
	store  game.Store
	worlds game.Worlds

	// installed is the version Build() found last ("" for none), for
	// Join: the manager calls Join with its lock held, also from Status,
	// so Join must not read files (spec 2.2 and 4.8). Build() runs from
	// the manager's checks and the install job, which keep this fresh.
	installed atomic.Pointer[string]
}

// Interfaces this adapter implements beyond game.Server.
var (
	_ game.Prober    = (*server)(nil)
	_ game.Lister    = (*server)(nil)
	_ game.Recoverer = (*server)(nil)
)

func (s *server) Store() game.Store   { return s.store }
func (s *server) Worlds() game.Worlds { return s.worlds }

// Texts: Minecraft's players are just players.
func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// exe adds ".exe" on Windows.
func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Program is the instance's own Java, which is the process that runs and
// listens (the jar is only its argument). Each instance has its own Java
// in Dirs.Runtime, so finding the process by its program finds this
// server and nothing else.
//
// After a real install it is wherever Temurin's archive put bin/java (on
// macOS inside Contents/Home), as install.FindJava reads from the note
// the install left. Before that, and always in fake mode, it is the fixed
// path runtime/java/bin/java, the same on every system (spec 2.2's Fake).
func (s *server) Program() string {
	if s.env.Fake == nil {
		if java, ok := install.FindJava(s.env.Dirs.Runtime); ok {
			return java
		}
	}
	return filepath.Join(s.env.Dirs.Runtime, "java", "bin", exe("java"))
}

// jarPath is the server.jar Mojang publishes.
func (s *server) jarPath() string { return filepath.Join(s.env.Dirs.Server, "server.jar") }

// Build is what's installed: the version the last install wrote, when the
// jar and Java are both still there.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.ReadBuild(s.env.Dirs.Server)
	if !ok || !isFile(s.jarPath()) || !isFile(s.Program()) {
		s.setInstalled("")
		return game.Build{}, false
	}
	s.setInstalled(b.Version)
	return b, true
}

func (s *server) setInstalled(version string) { s.installed.Store(&version) }

// installedVersion is the version Build() found last, without reading
// anything.
func (s *server) installedVersion() string {
	if v := s.installed.Load(); v != nil {
		return *v
	}
	return ""
}
