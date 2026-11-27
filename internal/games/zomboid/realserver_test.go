//go:build realserver

// The real-server check (spec 5.6 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 60m ./internal/games/zomboid/
//
// It downloads the real server with SteamCMD once (Steam app 380870,
// Build 42: about 1.9 GB over the network and 7.1 GB on disk, about four
// minutes on GitHub's runner) into a folder every part of the check
// shares, and plays it through gametest, without the manager. The first
// real run downloaded it again for each part, which with a server that
// never started made 39 minutes (FACTS.md). Lines starting with "FACTS:"
// are for FACTS.md.
//
// In order:
//   - the install, and what it put on disk: the launch files, the bundled
//     Java, the native libraries, recorded before anything starts, so they
//     are known even when the server can't start;
//   - "facts": a new world started as the app starts it, watched for
//     "*** SERVER STARTED" and for the server giving up ("Server
//     Terminated."), then its ports, RCON, settings files, saves, the stop
//     by RCON and by Ctrl+C, and whether it starts from a folder with a
//     short "8.3" name. If that first start fails, the check shows the
//     server's whole log and makes the same start again four times, each
//     changing one thing (the .bat as Steam runs it; a plain environment;
//     Steam mode off; bare settings files), so one run says where the
//     trouble is, and keeps the logs in a Test* folder in TEMP for the
//     workflow to upload;
//   - "conformance": what every adapter must do, only once "facts" saw the
//     server online (otherwise it would wait a quarter of an hour for the
//     same failure).
//
// When everything works it takes about 15 minutes; when the server can't
// start, about 12. It also runs on Linux (SteamCMD there needs the 32-bit
// C libraries), though Linux isn't a system the app is offered on and has
// no cell; the .bat is tried on Windows only.

package zomboid

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/rcon"
)

func TestRealServer(t *testing.T) {
	// The server must keep everything in the instance's folder (-cachedir)
	// and never touch %USERPROFILE%\Zomboid, the folder the owner's own
	// game keeps its saves in.
	home, _ := os.UserHomeDir()
	ownFolder := filepath.Join(home, "Zomboid")
	before := snapshot(ownFolder)
	t.Cleanup(func() {
		if after := snapshot(ownFolder); after != before {
			t.Errorf("the server changed %s, the owner's own Project Zomboid folder:\nbefore:\n%s\nafter:\n%s", ownFolder, before, after)
		} else {
			t.Logf("FACTS: %s (the default data folder) was not touched", ownFolder)
		}
	})

	sh := installOnce(t)
	g := sharedGame{sh}

	// The parts particular to Project Zomboid, and the facts FACTS.md
	// marks for this check. It runs first because its first start is the
	// one that explains a server that can't start.
	online := false
	if !t.Run("facts", func(t *testing.T) { realFacts(t, g, sh, &online) }) && !online {
		t.Fatal("the server never came online in the facts part (its log and the other ways of starting it are above), so the conformance part, which would only wait 15 minutes for the same failure, is left out")
	}

	// Everything every adapter must do, against the real server: a new
	// world, online, its ports in use, RCON's "players", Join, the stop
	// by RCON "quit" (saved), a second start on the same world, a takeover
	// whose stop still goes through RCON, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, g, gametest.Options{Real: true, World: "RealCheck"})
	})
}

// shared is the one install every part of the check uses.
type shared struct {
	root     string // the check's own folder: the install, SteamCMD, the experiments' worlds
	server   string // the installed server
	steamcmd string // the shared SteamCMD
}

// sharedGame is Project Zomboid with its server folder and SteamCMD in the
// shared install, so each gametest session's install is SteamCMD finding
// nothing new to download (as the app's update before a start mostly is),
// not another 7 GB. Everything else (the saves, logs and run record) stays
// in the session's own folder.
type sharedGame struct{ sh shared }

func (g sharedGame) Info() game.Info { return Game{}.Info() }

func (g sharedGame) New(env game.Env) game.Server {
	env.Dirs.Server, env.Dirs.SteamCMD = g.sh.server, g.sh.steamcmd
	return Game{}.New(env)
}

// installOnce downloads the real server with the adapter's own installer
// into the check's folder and records what it installed. Then it runs the
// installer again, which is what every later part (and every start in the
// app, before it starts the server) does with nothing new to download.
func installOnce(t *testing.T) shared {
	root := t.TempDir()
	sh := shared{root: root, server: filepath.Join(root, "server"), steamcmd: filepath.Join(root, "steamcmd")}
	if err := os.MkdirAll(sh.server, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("FACTS: the check's folder is %s; spelt in full, %s", root, longPath(root))
	steam := &steamLog{t: t}
	defer steam.close()
	env := game.Env{Instance: ID, Data: root, Dirs: game.Dirs{Root: root, Server: sh.server, SteamCMD: sh.steamcmd,
		Saves: filepath.Join(root, "saves"), Logs: filepath.Join(root, "logs")}, Log: log.New(steam, "install: ", 0)}
	srv := Game{}.New(env)
	start := time.Now()
	if err := srv.Installer().Install(t.Context(), false, func(game.Progress) {}); err != nil {
		t.Fatalf("installing the real server: %v", err)
	}
	b, ok := srv.Build()
	if !ok {
		t.Fatal("after the install, Build() says nothing is installed")
	}
	size, _, files := treeInfo(sh.server)
	t.Logf("FACTS: SteamCMD installed Steam build %s (%s) in %v: %d MB in %d files", b.Build, b.Date.Format("2006-01-02"),
		time.Since(start).Round(time.Second), size>>20, files)
	start = time.Now()
	err := srv.Installer().Install(t.Context(), false, func(game.Progress) {})
	t.Logf("FACTS: SteamCMD again, with nothing new to download, took %v (%v)", time.Since(start).Round(time.Second), err)

	t.Logf("FACTS: the install's top folder holds %s", listDir(sh.server, 1))
	for _, sub := range []string{"natives", filepath.Join("natives", "win64"), "java"} {
		if names := listDir(filepath.Join(sh.server, sub), 1); names != "" {
			t.Logf("FACTS: %s/ holds %s", filepath.ToSlash(sub), names)
		}
	}
	for _, sub := range []string{"anims_X", "models_X", "lua", "maps"} {
		size, _, files := treeInfo(filepath.Join(sh.server, "media", sub))
		t.Logf("FACTS: media/%s has %d files, %d MB", sub, files, size>>20)
	}
	launchFacts(t, sh.server)
	javaFacts(t, srv.Program())
	envFacts(t, sh.server)
	return sh
}

// steamLog sends SteamCMD's output to the test's log, with one line for
// each step of its download rather than hundreds, and nothing after the
// install is over.
type steamLog struct {
	mu     sync.Mutex
	t      *testing.T
	last   string
	closed bool
}

var reSteamState = regexp.MustCompile(`Update state \((0x[0-9a-f]+)\)`)

func (l *steamLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := strings.TrimRight(string(p), "\r\n")
	if m := reSteamState.FindStringSubmatch(line); m != nil {
		if m[1] == l.last {
			return len(p), nil
		}
		l.last = m[1]
	}
	if !l.closed {
		l.t.Log(line)
	}
	return len(p), nil
}

func (l *steamLog) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}

// realFacts starts a new world and records what the real server does. It
// sets *online once the server has come online, which the conformance
// part needs.
func realFacts(t *testing.T, g sharedGame, sh shared, online *bool) {
	s := gametest.Start(t, g, gametest.Options{Real: true, World: "Facts", Change: func(sv *game.Saved) {
		sv.Server["serverName"] = "Real check — Ñandú"
		sv.Server["password"] = "two words"
		sv.Worlds["Facts"]["zombies"] = "high"
	}})
	plan := s.Plan()
	saves := s.Env().Dirs.Saves
	t.Logf("FACTS: the plan runs %s %s in %s, adding %q to the environment", plan.Exe, strings.Join(redact(plan.Args), " "), plan.Dir, plan.Env)
	// The logs, kept where the workflow uploads them from if this part
	// fails (gametest removes its own folder when the part ends; this
	// runs before that).
	t.Cleanup(func() {
		if t.Failed() {
			keepLogs(t, "facts", plan.LogPath, saves)
			if again := s.Plan().LogPath; again != plan.LogPath {
				keepLogs(t, "facts-again", again, "")
			}
		}
	})
	ready := plan.ReadyWait + 5*time.Minute
	start := time.Now()
	if !waitOnline(t, s, ready) {
		startFailed(t, s, sh)
		return
	}
	*online = true
	t.Logf("FACTS: a new world was online %v after launch", time.Since(start).Round(time.Second))
	srv := s.Server()
	build, _ := srv.Build()
	t.Logf("FACTS: Steam build %s, the server says version %s", build.Build, s.State().Version)

	rec := s.Record()
	for role, proto := range map[string]string{roleGame: "udp", roleDirect: "udp", roleRCON: "tcp"} {
		inUse, pid := platform.PortInUse(proto, rec.Ports[role])
		t.Logf("FACTS: PortInUse(%q, %d) for %s = %v, pid %d (the server is %d)", proto, rec.Ports[role], role, inUse, pid, rec.PID)
	}
	rconLoopbackOnly(t, rec.Ports[roleRCON])

	// RCON "players" with nobody on.
	res, err := srv.(game.Prober).Probe(context.Background(), rec)
	if err != nil || res.Count != 0 {
		t.Errorf("RCON players with nobody on: %+v, %v", res, err)
	}

	// The world's settings files after the server read and rewrote them:
	// the app's values are kept, and the partial .ini is completed.
	ini, err := os.ReadFile(iniFile(saves, "Facts"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"PublicName": "Real check — Ñandú", "Password": "two words", "UPnP": "false", "Public": "false",
		"Open": "true", "DefaultPort": strconv.Itoa(rec.Ports[roleGame]), "UDPPort": strconv.Itoa(rec.Ports[roleDirect]), "RCONPort": strconv.Itoa(rec.Ports[roleRCON])} {
		if got, _ := iniGet(string(ini), key); got != want {
			t.Errorf("after the start the .ini has %s=%q, want %q", key, got, want)
		}
	}
	keys := 0
	for l := range strings.SplitSeq(string(ini), "\n") {
		if line := strings.TrimSpace(l); line != "" && !strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
			keys++
		}
	}
	t.Logf("FACTS: the server completed the app's partial .ini to %d keys (CRLF: %v)", keys, strings.Contains(string(ini), "\r\n"))
	lua, _ := os.ReadFile(sandboxFile(saves, "Facts"))
	if !strings.Contains(string(lua), "Zombies = 3,") || !strings.Contains(string(lua), "PopulationMultiplier = 1.2,") {
		t.Errorf("after the start the SandboxVars file lost the app's values:\n%.2000s", lua)
	} else {
		t.Logf("FACTS: the server kept Zombies = 3 and PopulationMultiplier = 1.2 in its rewritten SandboxVars (%d bytes)", len(lua))
	}
	for _, line := range []string{"version=", "Administrator account 'admin' created.", "admin password changed via -adminpassword option",
		"Looking in these map folders", "RCON: listening on port", "*** SERVER STARTED"} {
		t.Logf("FACTS: log line %q", logLine(t, plan.LogPath, line))
	}
	t.Logf("FACTS: the save folder holds %s", listDir(saves, 2))

	// RCON "save" saves without stopping.
	mapDir := filepath.Join(multiplayerDir(saves), "Facts")
	first := newest(mapDir)
	addr, pw, _ := rconAddr(rec)
	reply, err := rcon.Exec(context.Background(), addr, pw, "save")
	t.Logf("FACTS: RCON save answered %q, %v", reply, err)
	s.WaitEvent("saved", "", 2*time.Minute)
	if saved := newest(mapDir); !saved.After(first) {
		t.Errorf("RCON save didn't write the world (%v, before %v)", saved, first)
	}

	// RCON quit saves and ends it.
	start = time.Now()
	s.Stop()
	t.Logf("FACTS: the stop by RCON quit took %v", time.Since(start).Round(100*time.Millisecond))
	stopped := newest(mapDir)

	// Ctrl+C: Java's shutdown handling. Does it save?
	s.StartAgain()
	start = time.Now()
	if !waitOnline(t, s, s.Plan().ReadyWait+5*time.Minute) {
		startFailed(t, s, sh)
		return
	}
	t.Logf("FACTS: the same world was online again %v after launch", time.Since(start).Round(time.Second))
	if !strings.Contains(logLine(t, s.Plan().LogPath, "admin password changed"), "admin password changed") {
		t.Errorf("the second start didn't take the admin password again")
	}
	rec = s.Record()
	proc, err := platform.OpenProc(rec.PID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Ctrl+C goes to every program on the server's console, and
		// gametest's own handler keeps it from ending this test too.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	}
	start = time.Now()
	if err := proc.Interrupt(); err != nil {
		t.Fatalf("Ctrl+C: %v", err)
	}
	for !proc.Exited() {
		if time.Since(start) > 2*time.Minute {
			proc.Kill()
			t.Fatalf("the server didn't end within 2 minutes of Ctrl+C")
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(time.Second) // the last lines reach the log
	b, _ := os.ReadFile(s.Plan().LogPath)
	tail := string(b)
	if i := strings.LastIndex(tail, "Shutdown handling started"); i >= 0 {
		tail = tail[i:]
	}
	t.Logf("FACTS: after Ctrl+C the server ended in %v; its shutdown handling ran: %v; it saved (\"Saving finish\"): %v; the world changed: %v",
		time.Since(start).Round(100*time.Millisecond), strings.Contains(tail, "Shutdown handling started"),
		strings.Contains(tail, "Saving finish"), newest(mapDir).After(stopped))

	// Whether the server starts from folders named with short "8.3"
	// names, as the first real check's were (TEMP on GitHub's runner is
	// C:\Users\RUNNER~1\…). The app now gives it full names (longPath),
	// so this only says whether that mattered: the app's start again, with
	// the app's settings files, from the same folders named short.
	//
	// The short name is compared with the name spelt in full, not with
	// sh.server: that is under TEMP, so on the runner it already holds
	// RUNNER~1, and when Windows makes no short name for the check's own
	// new folder the two would be equal and the start left out, though
	// the folders it uses still carry RUNNER~1.
	if runtime.GOOS == "windows" {
		if shortServer, longServer := shortPath(sh.server), longPath(sh.server); shortServer != longServer {
			cache := filepath.Join(sh.root, "short-names")
			os.MkdirAll(cache, 0o755)
			p := appWorld(t, s, cache, s.Record().World)
			result := tryStart(t, "the app's start from folders with short names", sh, p,
				experimentArgs(plan.Args, shortPath(cache), p), shortServer, append(os.Environ(), plan.Env...),
				programFor(shortServer, runtime.GOOS))
			t.Logf("FACTS: started from %s with -cachedir=%s (in full, %s), the server %s", shortServer, shortPath(cache), longServer, result)
		} else {
			t.Logf("FACTS: the check's server folder has no short name (%s), so a start from short names wasn't tried", longServer)
		}
	}
}

// waitOnline waits for the server to come online, as gametest's WaitEvent
// would, but gives up as soon as the log says the server gave up ("Server
// Terminated.", the admin prompt, a broken settings file, a taken port)
// or its program has ended, and says why in the test's log. The first
// real check's server wrote "Server Terminated." 30 seconds in and then
// kept running, so a plain wait sat out the whole quarter of an hour.
func waitOnline(t *testing.T, s *gametest.Session, within time.Duration) bool {
	t.Helper()
	logPath := s.Plan().LogPath
	proc, err := platform.OpenProc(s.Record().PID)
	if err != nil {
		t.Logf("opening the server's process: %v", err)
	}
	start := time.Now()
	for time.Since(start) < within {
		if s.State().Ready {
			s.WaitEvent("online", "", time.Second) // take the event, so a later wait waits for a new one
			return true
		}
		if line := gaveUp(logPath); line != "" {
			time.Sleep(5 * time.Second) // the rest of what it has to say reaches the log
			t.Errorf("the server gave up %v after launch: %q", time.Since(start).Round(time.Second), line)
			return false
		}
		if proc != nil && proc.Exited() {
			t.Errorf("the server's program ended %v after launch without coming online", time.Since(start).Round(time.Second))
			return false
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("the server didn't come online within %v", within)
	return false
}

// reGaveUp are the lines after which a start never comes online (the
// Monitor or ExitHint names each one, FACTS.md).
var reGaveUp = regexp.MustCompile(`Server Terminated|Enter new administrator password|Exiting due to errors loading|Connection Startup Failed`)

// gaveUp is the first line of the log at path saying the server gave up
// on its start, "" when there is none (yet).
func gaveUp(path string) string {
	b, _ := os.ReadFile(path)
	if loc := reGaveUp.FindIndex(b); loc != nil {
		start := bytes.LastIndexByte(b[:loc[0]], '\n') + 1
		end := bytes.IndexByte(b[loc[0]:], '\n')
		if end < 0 {
			end = len(b) - loc[0]
		}
		return strings.TrimSpace(string(b[start : loc[0]+end]))
	}
	return ""
}

// startFailed explains a start that never came online: the server's whole
// log (without its long list of log settings), what it left in its saves
// folder, and then, after stopping it, the same start made again four
// times, each changing one thing, so the run says which of them the
// trouble is in: the app's way of launching the server, this computer's
// environment, Steam mode, or the settings files the app writes.
//
// Apart from the one thing, each repeats the start that failed: the same
// install, a new world of the same name (as the first start's was) in a
// data folder of its own, with the settings files the app writes from
// the same settings (appWorld), on new free ports. Changing two things at
// once would leave a start that comes online pointing at either.
func startFailed(t *testing.T, s *gametest.Session, sh shared) {
	t.Helper()
	plan := s.Plan()
	saves := s.Env().Dirs.Saves
	world := s.Record().World
	showLog(t, "the server's log", plan.LogPath)
	t.Logf("the saves folder (-cachedir) holds %s", listDir(saves, 2))
	// The server's own copy of its console (FACTS.md); the part's cleanup
	// keeps it with the rest.
	if p := filepath.Join(saves, "server-console.txt"); sizeOf(p) != "unreadable" {
		t.Logf("the server also wrote %s (%s)", p, sizeOf(p))
	}
	// Stop the server that failed, so the others can run. (The part's
	// cleanup keeps its logs.)
	if proc, err := platform.OpenProc(s.Record().PID); err == nil && !proc.Exited() {
		proc.Kill()
		for i := 0; i < 75 && !proc.Exited(); i++ {
			time.Sleep(200 * time.Millisecond)
		}
	}
	// The environment the failed start had: gametest's, which is the
	// manager's (this computer's, with the plan's on top).
	appEnv := append(os.Environ(), plan.Env...)
	folder := func(name string) string {
		cache := filepath.Join(sh.root, name)
		os.MkdirAll(cache, 0o755)
		return cache
	}

	// 1. Steam's way of launching it instead of the app's:
	// StartServer64.bat itself through cmd.exe (a copy that passes on all
	// its arguments and asks for the app's memory rather than its 16 GB),
	// so the server gets the .bat's own Java options and this computer's
	// environment as it is, without the app's options or its emptied Java
	// option variables. The .bat is given by its full name, so its own
	// folder (%~dp0) has no short names either.
	if runtime.GOOS == "windows" {
		if bat, err := batCopy(sh.server); err != nil {
			t.Logf("FACTS: the .bat couldn't be tried: %v", err)
		} else {
			bat = longPath(bat)
			cache := folder("bat")
			p := appWorld(t, s, cache, world)
			args := []string{"/c", bat, "-cachedir=" + longPath(cache), "-servername", world, "-adminusername", adminUser, "-adminpassword", "egscheck1",
				"-port", strconv.Itoa(p), "-udpport", strconv.Itoa(p + 1)}
			result := tryStart(t, "StartServer64.bat, as Steam runs it", sh, p, args, longPath(sh.server), os.Environ(), "cmd.exe")
			t.Logf("FACTS: through the .bat (cmd.exe /c %s), with the app's settings files, the server %s", strings.Join(args[1:], " "), result)
			os.Remove(bat)
		}
	}

	// 2. A plain environment: only Windows' own folders on PATH and no
	// Java settings from the environment (GitHub's runner has many
	// programs and their libraries on PATH).
	cache := folder("plain")
	p := appWorld(t, s, cache, world)
	result := tryStart(t, "the app's start with a plain environment", sh, p,
		experimentArgs(plan.Args, longPath(cache), p), plan.Dir, append(plainEnv(), plan.Env...), "")
	t.Logf("FACTS: with a plain environment (otherwise the app's start) the server %s", result)

	// 3. Steam mode off. The app runs the server in Steam mode, as the .bat
	// does (Steam friends can join only then), but the one published
	// Build 42 first start whose log is in testdata ran without Steam
	// ("Loading ZNetNoSteam64...", FACTS.md).
	if args, ok := swapArg(plan.Args, steamOn, steamOff); !ok {
		t.Logf("FACTS: the app's command line has no %s, so a start without Steam wasn't tried", steamOn)
	} else {
		cache := folder("nosteam")
		p := appWorld(t, s, cache, world)
		result := tryStart(t, "the app's start without Steam", sh, p, experimentArgs(args, longPath(cache), p), plan.Dir, appEnv, "")
		t.Logf("FACTS: with %s (otherwise the app's start) the server %s", steamOff, result)
	}

	// 4. Bare settings files instead of the app's: an .ini with only the
	// keys that keep a start from hanging or reaching outside, and no
	// SandboxVars file, so the server uses its own defaults for the rest
	// (the second error in the first real check came from the world's
	// Lua).
	cache = folder("bare")
	p = bareWorld(t, cache, world)
	result = tryStart(t, "the app's start with bare settings files", sh, p,
		experimentArgs(plan.Args, longPath(cache), p), plan.Dir, appEnv, "")
	t.Logf("FACTS: with bare settings files (an .ini with UPnP, Public, the ports and RCON off; no SandboxVars; otherwise the app's start) the server %s", result)
	t.Errorf("the server didn't come online; its log, and how it did when started the other ways, are above")
}

// Steam mode as the .bat (and so the app) gives it, and the same option
// with Steam off, as StartServer64_nosteam.bat has it.
const (
	steamOn  = "-Dzomboid.steam=1"
	steamOff = "-Dzomboid.steam=0"
)

// batCopy writes a copy of StartServer64.bat beside it that passes on all
// its arguments (%*, where the .bat has "%1 %2") and gives Java 6 GB, as
// the app does, rather than the 16 GB the runner hasn't got to spare.
func batCopy(serverDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(serverDir, batName))
	if err != nil {
		return "", err
	}
	text := strings.Replace(string(b), "%1 %2", "%*", 1)
	text = regexp.MustCompile(`(?i)-Xms\S+ -Xmx\S+`).ReplaceAllString(text, "-Xmx"+strconv.Itoa(defMemoryMB)+"m")
	path := filepath.Join(serverDir, "egs-check-StartServer64.bat")
	return path, os.WriteFile(path, []byte(text), 0o644)
}

// appWorld writes, into the data folder cache, the settings files the app
// writes for a new world from the session's settings: the adapter's own
// iniSettings and writeSettings, as Plan uses them (with no seed, as the
// check gives none). Only the ports are new, free ones, and so is the
// RCON password, which is new at every start anyway. It returns the game
// port; the direct port is the one after it.
func appWorld(t *testing.T, s *gametest.Session, cache, world string) int {
	t.Helper()
	env := s.Env()
	env.Dirs.Saves = cache
	srv := Game{}.New(env).(*server)
	sv := env.Store.Get()
	p := freeUDPPair(t)
	sv.Ports = map[string]int{roleGame: p, roleDirect: p + 1, roleRCON: freeTCPPort(t)}
	rconPW, err := secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.writeSettings(world, srv.iniSettings(sv, rconPW), sv); err != nil {
		t.Fatal(err)
	}
	return p
}

// bareWorld writes, into the data folder cache, a world's settings file
// with only the app's keys that keep a start from hanging or reaching
// outside (UPnP off, not public), on two free UDP ports, with RCON off,
// and no SandboxVars file: the server gives every other setting its own
// default. It returns the game port; the direct port is the one after it.
func bareWorld(t *testing.T, cache, world string) int {
	p := freeUDPPair(t)
	ini := iniSet("", []setting{{"UPnP", "false"}, {"Public", "false"}, {"DefaultPort", strconv.Itoa(p)}, {"UDPPort", strconv.Itoa(p + 1)},
		{"RCONPassword", ""}}, newline())
	if err := writeFile(iniFile(cache, world), ini); err != nil {
		t.Fatal(err)
	}
	return p
}

// freeUDPPair is a UDP port that is free, with the one after it free too.
func freeUDPPair(t *testing.T) int {
	for range 200 {
		p := 20000 + rand.IntN(9000)
		a, err := net.ListenPacket("udp", ":"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		b, err := net.ListenPacket("udp", ":"+strconv.Itoa(p+1))
		a.Close()
		if err != nil {
			continue
		}
		b.Close()
		return p
	}
	t.Fatal("no two free UDP ports in a row")
	return 0
}

// freeTCPPort is a TCP port that is free, for RCON.
func freeTCPPort(t *testing.T) int {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// experimentArgs is the plan's command line args for another start of the
// same world: its data folder is cache, and its ports the ones that
// start's settings files were written with (port, and the one after it
// for -udpport). Everything else is left as it is.
func experimentArgs(args []string, cache string, port int) []string {
	out := slices.Clone(args)
	for i := 0; i < len(out); i++ {
		switch a := out[i]; {
		case strings.HasPrefix(a, "-cachedir="):
			out[i] = "-cachedir=" + cache
		case a == "-port" && i+1 < len(out):
			i++
			out[i] = strconv.Itoa(port)
		case a == "-udpport" && i+1 < len(out):
			i++
			out[i] = strconv.Itoa(port + 1)
		}
	}
	return out
}

// swapArg is args with the argument from changed to to, and whether args
// had it.
func swapArg(args []string, from, to string) ([]string, bool) {
	i := slices.Index(args, from)
	if i < 0 {
		return args, false
	}
	out := slices.Clone(args)
	out[i] = to
	return out, true
}

// plainEnv is this computer's environment with PATH cut down to Windows'
// own folders and without the variables Java reads its settings from.
func plainEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "PATH", "_JAVA_OPTIONS", "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_HOME", "CLASSPATH":
			continue
		}
		out = append(out, kv)
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		out = append(out, "PATH="+strings.Join([]string{filepath.Join(root, "System32"), root, filepath.Join(root, "System32", "Wbem")}, ";"))
	} else {
		out = append(out, "PATH=/usr/bin:/bin")
	}
	return out
}

// tryStart starts the server another way (exe "" is the server's own
// Java) and watches its output for up to five minutes: online, gave up,
// ended by itself, or no answer. It then ends it, shows its log when it
// didn't come online, keeps the log for the workflow, and says how it went.
func tryStart(t *testing.T, name string, sh shared, port int, args []string, dir string, env []string, exe string) string {
	t.Helper()
	if exe == "" {
		exe = programFor(sh.server, runtime.GOOS)
	}
	logPath := filepath.Join(sh.root, "try-"+strconv.Itoa(port)+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return "couldn't be started: " + err.Error()
	}
	defer f.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, f, f
	// A .bat's PAUSE, and the admin prompt if it comes, read this.
	cmd.Stdin = strings.NewReader("egscheck1\negscheck1\n\n")
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "couldn't be started: " + err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var result string
	ended := false
	for result == "" {
		select {
		case err := <-done:
			ended = true
			result = fmt.Sprintf("ended by itself %v after launch (%v)", time.Since(start).Round(time.Second), err)
		case <-time.After(2 * time.Second):
			b, _ := os.ReadFile(logPath)
			took := time.Since(start).Round(time.Second)
			switch {
			case bytes.Contains(b, []byte("*** SERVER STARTED")):
				result = fmt.Sprintf("came online %v after launch", took)
			case gaveUp(logPath) != "":
				time.Sleep(5 * time.Second)
				result = fmt.Sprintf("gave up %v after launch: %q", took, gaveUp(logPath))
			case time.Since(start) > 5*time.Minute:
				result = fmt.Sprintf("neither came online nor gave up within %v", took)
			}
		}
	}
	if !ended {
		killTree(cmd.Process.Pid)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Logf("%s: the server was still running 15 s after it was ended", name)
		}
	}
	if !strings.HasPrefix(result, "came online") {
		showLog(t, name+": the server's log", logPath)
	}
	keepLogs(t, strings.ReplaceAll(name, " ", "-"), logPath, "")
	return result
}

// showLog puts a server log in the test's log: every line the Log page
// would show (the Monitor hides the long list of log settings) up to the
// 150th, then every warning, error and milestone after that with the two
// lines that follow it, at most 500 lines in all.
func showLog(t *testing.T, title, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Logf("%s: %v", title, err)
		return
	}
	m := (&server{}).Watch(game.Record{}, nil)
	all := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var out []string
	shown, after := 0, 0
	for i, raw := range all {
		if len(out) >= 500 {
			out = append(out, fmt.Sprintf("… %d more lines", len(all)-i))
			break
		}
		l := m.Line(raw, time.Now())
		switch {
		case shown < 150 && !l.Hide:
			shown++
			out = append(out, raw)
		case shown >= 150 && reTrouble.MatchString(raw):
			after = 3
			fallthrough
		case shown >= 150 && after > 0:
			out = append(out, raw)
			after--
		}
	}
	t.Logf("%s (%s, %d lines):\n%s", title, path, len(all), strings.Join(out, "\n"))
}

// reTrouble are the lines worth showing from a long log.
var reTrouble = regexp.MustCompile(`^(ERROR|WARN|SEVERE)|Exception|Terminated|not found|Failed|failed|SERVER STARTED|map folders|LOADING ASSETS|RCON|admin`)

// keepLogs copies a part's logs, and the server's own logs in its data
// folder, into a Test* folder in TEMP that nothing removes, which the
// workflow uploads when a cell fails (it looks for Test*/ folders there).
func keepLogs(t *testing.T, part, logPath, cacheDir string) {
	dest := filepath.Join(os.TempDir(), "TestRealServer-zomboid-logs", time.Now().Format("150405")+"-"+part)
	copyTo := func(src, name string) {
		in, err := os.Open(src)
		if err != nil {
			return
		}
		defer in.Close()
		os.MkdirAll(filepath.Dir(filepath.Join(dest, name)), 0o755)
		out, err := os.Create(filepath.Join(dest, name))
		if err != nil {
			return
		}
		defer out.Close()
		io.Copy(out, in)
	}
	if logPath != "" {
		copyTo(logPath, filepath.Base(logPath))
	}
	if cacheDir != "" {
		copyTo(filepath.Join(cacheDir, "server-console.txt"), "server-console.txt")
		logs, _ := filepath.Glob(filepath.Join(cacheDir, "Logs", "*.txt"))
		for _, l := range logs {
			copyTo(l, filepath.Join("Logs", filepath.Base(l)))
		}
		for _, ini := range []string{"*.ini", "*_SandboxVars.lua"} {
			files, _ := filepath.Glob(filepath.Join(cacheDir, "Server", ini))
			for _, f := range files {
				copyTo(f, filepath.Join("Server", filepath.Base(f)+".txt"))
			}
		}
	}
	t.Logf("the logs are kept in %s", dest)
}

func sizeOf(path string) string {
	if st, err := os.Stat(path); err == nil {
		return strconv.FormatInt(st.Size(), 10) + " bytes"
	}
	return "unreadable"
}

// launchFacts records the launch files this build installed, so a change
// shows (testdata/launch holds the recorded ones).
func launchFacts(t *testing.T, serverDir string) {
	for _, name := range []string{batName, "StartServer64_nosteam.bat", jsonName} {
		b, err := os.ReadFile(filepath.Join(serverDir, name))
		if err != nil {
			t.Logf("FACTS: %s: %v", name, err)
			continue
		}
		sum := sha256.Sum256(b)
		t.Logf("FACTS: %s is %d bytes, SHA-256 %s:\n%s", name, len(b), hex.EncodeToString(sum[:]), b)
	}
	if l, err := readLaunch(serverDir, runtime.GOOS); err != nil {
		t.Errorf("reading the launch files: %v", err)
	} else {
		t.Logf("FACTS: read from %s: JVM %q, class path %q, game arguments %q, environment %q", l.Source, l.JVM, l.ClassPath, l.Game, l.Env)
	}
}

// javaFacts records the bundled Java's version and whether Windows sees it
// as a console program (Ctrl+C reaches only those, spec 2.7).
func javaFacts(t *testing.T, java string) {
	out, err := exec.Command(java, "-version").CombinedOutput()
	t.Logf("FACTS: %s -version: %s %v", java, strings.TrimSpace(string(out)), err)
	if runtime.GOOS != "windows" {
		return
	}
	f, err := pe.Open(java)
	if err != nil {
		t.Logf("FACTS: reading %s as a Windows program: %v", java, err)
		return
	}
	defer f.Close()
	var sub uint16
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		sub = h.Subsystem
	case *pe.OptionalHeader32:
		sub = h.Subsystem
	}
	// 3 is a console program, 2 a windowed one Ctrl+C can't reach.
	if sub != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		t.Errorf("%s has Windows subsystem %d, not a console program's (3): Ctrl+C can't reach it", java, sub)
	} else {
		t.Logf("FACTS: %s is a console program (subsystem 3)", java)
	}
}

// envFacts records what in this computer's environment could change how
// the server's Java starts: the variables Java reads its settings from,
// and folders on PATH holding a library with the name of one of the
// server's own (Windows looks for a library's own dependencies there).
func envFacts(t *testing.T, serverDir string) {
	for _, name := range []string{"_JAVA_OPTIONS", "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_HOME", "CLASSPATH", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Logf("FACTS: the environment has %s=%s", name, v)
		}
	}
	var libs []string
	for _, dir := range []string{serverDir, filepath.Join(serverDir, "natives"), filepath.Join(serverDir, "natives", "win64")} {
		for _, pat := range []string{"*.dll", "*.so"} {
			m, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, p := range m {
				libs = append(libs, strings.ToLower(filepath.Base(p)))
			}
		}
	}
	clash := map[string][]string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		for _, lib := range libs {
			if _, err := os.Stat(filepath.Join(dir, lib)); err == nil {
				clash[lib] = append(clash[lib], dir)
			}
		}
	}
	if len(clash) == 0 {
		t.Logf("FACTS: no folder on PATH has a library named like one of the server's %d", len(libs))
	}
	for lib, dirs := range clash {
		t.Logf("FACTS: PATH also has %s, in %s", lib, strings.Join(dirs, "; "))
	}
}

// rconLoopbackOnly checks that RCON can't be reached on this computer's
// other addresses (-Drconlo=true).
func rconLoopbackOnly(t *testing.T, port int) {
	addrs, _ := net.InterfaceAddrs()
	tried := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		tried++
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), strconv.Itoa(port)), 3*time.Second)
		if err == nil {
			c.Close()
			t.Errorf("RCON answers on %s: it should listen on 127.0.0.1 only (-Drconlo)", ipn.IP)
		} else {
			t.Logf("FACTS: RCON port %d on %s: %v (not reachable, as -Drconlo asks)", port, ipn.IP, err)
		}
	}
	if tried == 0 {
		t.Log("FACTS: no other IPv4 address to try RCON on")
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Errorf("RCON on 127.0.0.1: %v", err)
	} else {
		c.Close()
	}
}

// redact hides the admin password in a command line for the log.
func redact(args []string) []string {
	out := slices.Clone(args)
	for i := range out {
		if out[i] == "-adminpassword" && i+1 < len(out) {
			out[i+1] = "•••"
		}
	}
	return out
}

// logLine is the first line of the log at path holding sub.
func logLine(t *testing.T, path, sub string) string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.Contains(sc.Text(), sub) {
			return sc.Text()
		}
	}
	return ""
}

// newest is the newest change of any file under dir.
func newest(dir string) time.Time {
	_, mod, _ := treeInfo(dir)
	return mod
}

// listDir lists a folder's entries to depth levels, for the facts.
func listDir(root string, depth int) string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if strings.Count(rel, string(filepath.Separator)) >= depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// snapshot lists a folder's files with their sizes and times ("" when it
// doesn't exist), to tell whether anything changed in it.
func snapshot(dir string) string {
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %s\n", p, info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
		}
		return nil
	})
	return b.String()
}
