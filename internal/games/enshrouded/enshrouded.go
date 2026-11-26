// Package enshrouded is the adapter for the Enshrouded dedicated server
// (spec 5.7): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Enshrouded Dedicated Server" (app 2278520,
// downloaded anonymously), a Windows console program. It has no command
// line options the app needs and no console commands: everything it is
// told comes from enshrouded_server.json beside its program, which the
// Plan writes before every start. Friends find it in Enshrouded's own list
// of dedicated servers (or search it by address) and join with a password,
// which picks the "user group" whose rights they get. The app makes one
// group, Friends, with the owner's password.
//
// The server writes its log to a file of its own (logDirectory) and almost
// nothing to standard output, so the app follows that file (Output
// GameLog). Who is on comes from that log, by name; the server's Steam
// query port (A2S) gives an authoritative count. It is stopped with Ctrl+C,
// which it answers by saving the world and quitting.
//
// Every fact the code relies on is written down, with its source, in
// FACTS.md beside this file.
package enshrouded

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// ID is Enshrouded's game id: its folder, its instance id and its slash
// command (/enshrouded).
const ID = "enshrouded"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "2278520"

// roleQuery is the server's one port: the config calls it queryPort, and
// it carries both Steam's server queries and the game (FACTS.md, "Ports").
const roleQuery = "query"

// defaultPort is the server's own default queryPort.
const defaultPort = 15637

func init() { game.Register(Game{}) }

// Game is Enshrouded, as the registry knows it.
type Game struct{}

// Info describes Enshrouded for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Enshrouded", Short: "Enshrouded", Letter: "E", Color: "#16a085", Command: ID,
		// Spec section 5.7: (7-1)*10, the scheme the other game streams
		// use (Valheim 0, Minecraft 10, Terraria 20).
		Order: 60,
		// Preview until its real-server cell is green on Windows (spec 8.2).
		Stage: game.Preview,
		// Keen Games makes the server for Windows only (FACTS.md).
		OS:        map[string]game.Level{"windows": game.Runs},
		NeedsText: map[string]string{"darwin": "Needs Windows", "linux": "Needs Windows"},
		// Steam's depot is 8.05 GB (3.6 GB to download); Keen says the
		// server's files are about 13 GB and to keep 30 GB free. The disk
		// figure leaves room for an update and the worlds. Keen: about
		// 4.4 GB of memory idle, never more than 6 GB (FACTS.md).
		DownloadMB: 8100, DiskMB: 13000, MemoryMB: 6144, MaxPlayers: 16,
		FriendsNeed: "Enshrouded on Steam, kept up to date: it must be the same version as the server",
		// Joins and leaves by name come from the server's log; its Steam
		// query port counts players too (Prober), and that count wins.
		Presence: game.ByName, Deaths: false,
		// One UDP port, the "queryPort": friends join on it and Steam's
		// server list asks it about the server.
		Ports: []game.PortRole{{Role: roleQuery, Label: "Port", Protos: []string{"udp"}, Default: defaultPort, Public: true}},
		Password: game.PasswordRule{Use: "required", Min: minPassword, Max: maxPassword,
			Help: "Friends type this when they join. It lets them build, open chests and place Flame Altars, but not kick or ban anyone."},
		// The owner's own Enshrouded keeps its worlds in its own format
		// and folders, which Keen doesn't document for moving to a
		// server (FACTS.md, "Worlds").
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
		s.log = log.New(os.Stderr, "enshrouded: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(env.Dirs)
	return s
}

// server is one Enshrouded instance.
type server struct {
	env    game.Env
	store  game.Store
	log    *log.Logger
	worlds worldStore
}

func (s *server) Store() game.Store { return s.store }

// programName is the server's program, without ".exe".
const programName = "enshrouded_server"

// Program is enshrouded_server.exe, the file that runs (it is also Steam's
// launch setting, with no arguments).
//
// In fake mode (tests, EGS_DEV_FAKE_SERVERS) it is the stand-in, at the
// same place on every system, with ".exe" only on Windows. Outside fake
// mode it is always the Windows program: there is no other.
func (s *server) Program() string {
	name := programName + ".exe"
	if s.env.Fake != nil && runtime.GOOS != "windows" {
		name = programName
	}
	return filepath.Join(s.env.Dirs.Server, name)
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
// 8.1 GB (FACTS.md).
var steamApp = install.SteamApp{AppID: appID, Name: "Enshrouded", SizeGB: 8.1}

// Build is what's installed: Steam's build, as long as the server's
// program is still there. The game's own build number is in the log
// ("Game Version (SVN): …"), which the Monitor reads.
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

// gameLogDir is where the server writes its own log (logDirectory): a
// folder of its own inside the instance's logs, so the app's log pruning
// (the newest 30 "enshrouded-*.log" in Dirs.Logs) never touches it.
func (s *server) gameLogDir() string { return filepath.Join(s.env.Dirs.Logs, "game") }

// gameLog is the server's log of the current run. It names it after its
// program and moves the one before into backup/ when it starts.
func (s *server) gameLog() string { return filepath.Join(s.gameLogDir(), programName+".log") }

// Recover makes a run record for a server found running without its run
// file: the log it is writing is its own file, not one of the app's.
func (s *server) Recover(pid int, started time.Time) game.Record {
	sv := s.store.Get()
	port := s.port(sv)
	return game.Record{PID: pid, Started: started, Launched: started, World: sv.World, Exe: s.Program(),
		LogPath: s.gameLog(), Server: s.value(sv, "serverName"), Port: port, Password: s.value(sv, "password"),
		Game: ID, Instance: s.env.Instance, Name: "Enshrouded", Ports: map[string]int{roleQuery: port}}
}

// port is the instance's port, or the server's default when none was
// picked yet.
func (s *server) port(sv game.Saved) int {
	if p := sv.Ports[roleQuery]; p > 0 {
		return p
	}
	return defaultPort
}

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	var lines []string
	for _, l := range tail {
		if t := strings.TrimSpace(stripPrefix(l)); t != "" {
			lines = append(lines, t)
		}
	}
	find := func(re *regexp.Regexp) []string {
		for i := len(lines) - 1; i >= 0; i-- {
			if m := re.FindStringSubmatch(lines[i]); m != nil {
				return m
			}
		}
		return nil
	}
	switch {
	case len(lines) == 0:
		return "The Enshrouded server stopped before it wrote its log. It does that when it refuses its settings file, enshrouded_server.json, or when its files are damaged: start it again, and if it stops again, click Repair."
	case find(reBadSocket) != nil:
		return "The Enshrouded server couldn't open its port: another program is using it. Pick new ports for Enshrouded (or close that program), then start it again."
	case find(reConfigError) != nil:
		return "The Enshrouded server refused its settings file (" + find(reConfigError)[1] + "). Check Enshrouded's settings, then start it again."
	case find(reNoSteam) != nil:
		return "The Enshrouded server couldn't connect to Steam. Check that this computer is online, then start it again."
	case find(reLoadFailed) != nil:
		return "The Enshrouded server couldn't load the world. Restore one of its backups on the Worlds page, then start it again."
	case find(reShutdown) != nil && find(reShutdown)[1] == "SaveDataIssue":
		return "The Enshrouded server stopped because it couldn't save the world. Check that the disk isn't full, then start it again."
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
