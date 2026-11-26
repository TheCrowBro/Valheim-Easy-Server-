// Package necesse is the adapter for the Necesse dedicated server (spec
// 5.4): everything the app knows about that server, behind the game
// contract (internal/game).
//
// The server is Steam's free "Necesse Dedicated Server" (app 1169370,
// downloaded anonymously), a Java program (Server.jar) that comes with its
// own Java. The app starts that Java directly, never through the shipped
// start scripts, so the one process it watches is the server (spec 2.4).
//
// Necesse is a console game. It saves when "save" or "stop" is typed on its
// console and by itself about once a minute while people play; Ctrl+C ends
// it at once without saving. So the app keeps a pipe to its console
// (Plan.Console), stops it with "stop", and types "save" before the app
// itself exits. After an app restart the console is gone and the stop falls
// back to Ctrl+C, which loses what was played since the last autosave
// (spec 2.11).
//
// Every fact the code relies on was checked against the real 1.3.3 server
// and is written down, with its source, in FACTS.md beside this file.
package necesse

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

// ID is Necesse's game id: its folder, its instance id and its slash
// command (/necesse).
const ID = "necesse"

// appID is the dedicated server's Steam app (anonymous download).
const appID = "1169370"

// defaultPort is the UDP port Necesse's own server and game use unless told
// otherwise.
const defaultPort = 14159

func init() { game.Register(Game{}) }

// Game is Necesse, as the registry knows it.
type Game struct{}

// Info describes Necesse for the panel, the manager and the checks.
func (Game) Info() game.Info {
	return game.Info{
		ID: ID, Name: "Necesse", Short: "Necesse", Letter: "N", Color: "#8e44ad", Command: ID,
		// Its section in the spec is 5.4; Valheim is 0.
		Order: 4,
		// Preview until its real-server cells are green on Windows and the
		// Intel Mac and a real join and leave are in testdata (spec 8.2).
		Stage: game.Preview,
		// The Mac server's Java is an x86_64 program only (FACTS.md), so it
		// needs an Intel Mac. Linux is not a system the app is offered on.
		OS:        map[string]game.Level{"windows": game.Runs, "darwin/amd64": game.Runs, "darwin/arm64": game.CantRun},
		NeedsText: map[string]string{"darwin/arm64": "Needs an Intel Mac", "linux": "Needs Windows or an Intel Mac"},
		// Steam's download is about 100 MB on Windows (the server and
		// Steam's own libraries) and 67 MB on a Mac; installed it is about
		// 250 MB and 160 MB (FACTS.md). The disk figure leaves room for
		// worlds and their backups.
		DownloadMB: 100, DiskMB: 600, MemoryMB: 2048, MaxPlayers: 10,
		FriendsNeed: "Necesse on PC or Mac, the same version as the server",
		// Joins and leaves come from the console only, so the manager
		// treats presence as unknown until this install has seen a join
		// (spec 2.6). Deaths stay off until a real log of this version
		// shows a player's death line (FACTS.md).
		Presence: game.ByName, Deaths: false,
		Ports: []game.PortRole{
			// The server listens on UDP only (FACTS.md).
			{Role: "game", Label: "Game port", Protos: []string{"udp"}, Default: defaultPort, Public: true},
		},
		Password: game.PasswordRule{Use: "optional", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password."},
		Import: true,
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
		s.log = log.New(os.Stderr, "necesse: ", log.LstdFlags)
	}
	s.worlds = newWorldStore(env.Dirs)
	return s
}

// server is one Necesse instance.
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

// programFor is where the server's Java is on goos (Steam's launch
// settings for the app, FACTS.md). On a Mac the Java is a bundle, with the
// program under Contents/Home.
func programFor(serverDir, goos string) string {
	switch goos {
	case "windows":
		// java.exe, the console program: Ctrl+C reaches it. Steam's other
		// launch setting uses javaw.exe, a windowed program that has no
		// console, so Ctrl+C could never stop it (spec 2.7).
		return filepath.Join(serverDir, "jre", "bin", "java.exe")
	case "darwin":
		return filepath.Join(serverDir, "jre", "Contents", "Home", "bin", "java")
	}
	return filepath.Join(serverDir, "jre", "bin", "java")
}

// jarName is the server's program for Java, in the server folder.
const jarName = "Server.jar"

// Installer downloads the server with the shared SteamCMD, or puts the
// stand-in in its place in fake mode.
func (s *server) Installer() game.Installer {
	if s.env.Fake != nil {
		return install.Fake(s.env.Fake, s.Program(), []string{
			filepath.Join(s.env.Dirs.Server, jarName),
			filepath.Join(s.env.Dirs.Server, "steamapps", "appmanifest_"+appID+".acf"),
		})
	}
	return install.SteamCMD(steamApp, s.env.Dirs, func(line string) { s.log.Print(line) })
}

// steamApp is the dedicated server on Steam. Installed, it takes about
// 250 MB on Windows (FACTS.md).
var steamApp = install.SteamApp{AppID: appID, Name: "Necesse", SizeGB: 0.3}

// Build is what's installed: Steam's build, as long as the server's Java
// and Server.jar are still there. Necesse's own version number isn't in
// any file Steam writes; the Monitor reads it from the console.
func (s *server) Build() (game.Build, bool) {
	b, ok := install.SteamBuild(s.env.Dirs.Server, appID)
	if !ok {
		return game.Build{}, false
	}
	for _, p := range []string{s.Program(), filepath.Join(s.env.Dirs.Server, jarName)} {
		if _, err := os.Stat(p); err != nil {
			return game.Build{}, false
		}
	}
	return b, true
}

func (s *server) Worlds() game.Worlds { return s.worlds }

func (s *server) Texts() game.Texts { return game.Texts{Players: "players", Player: "player"} }

// Stop types "stop", which saves the world and ends the server. After a
// takeover the app has no console to type it on, so the chain goes on to
// Ctrl+C, which ends the server at once without saving.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "console", Line: "stop", Wait: 3 * time.Minute},
		{Kind: "interrupt", Wait: time.Minute, Resend: 20 * time.Second,
			Say: "Necesse doesn't save when it's stopped this way: anything since its last autosave (about once a minute while people play) is lost."},
	}
}

// ExitHint explains a server that ended by itself, from the end of its log.
func (s *server) ExitHint(tail []string) string {
	lines := make([]string, 0, len(tail))
	for _, l := range tail {
		if t, _ := splitLine(l); t != "" {
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
	case has("java.net.BindException", "Address already in use", "with code: NETWORK_ERROR"):
		port := ""
		for _, l := range lines {
			if m := reStarted.FindStringSubmatch(l); m != nil {
				port = " " + m[1]
			}
		}
		return "Another program is using UDP port" + port + ", so the Necesse server couldn't open it. Close that program, or choose new ports for Necesse."
	case has("Unable to access jarfile", "Could not find or load main class", "Invalid or corrupt jarfile"):
		return "Some of the Necesse server's files are missing or damaged. Click Repair to download them again."
	case has("Could not reserve enough space", "Invalid maximum heap size", "Too small maximum heap"):
		return "Java couldn't have the memory Necesse's settings give it. Lower \"Java memory\" in Necesse's settings, or close other programs."
	case has("java.lang.OutOfMemoryError"):
		return "The Necesse server ran out of memory. Raise \"Java memory\" in Necesse's settings."
	case has("Stuck in crash loop", "with code: SERVER_ERROR"):
		return "The Necesse server crashed. Start it again; if it keeps crashing, restore a backup of the world on the Worlds page. Its crash report is latest-crash.log in the server folder."
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
