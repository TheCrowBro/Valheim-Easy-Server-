//go:build realserver

// The real-server check (spec 5.6 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 120m ./internal/games/zomboid/
//
// It downloads the real server with SteamCMD (Steam app 380870, Build 42:
// about 1.9 GB over the network, 7.1 GB on disk) and plays it through
// gametest, without the manager. Each of its two parts downloads it once
// (gametest gives every session a folder of its own), so the cell needs
// about 8 GB free and most of an hour. It also runs on Linux (SteamCMD
// there needs the 32-bit C libraries), though Linux isn't a system the app
// is offered on and has no cell. Lines starting with "FACTS:" are for
// FACTS.md.

package zomboid

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
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

	// Everything every adapter must do, against the real server: install
	// (which also checks the launch files can be read), a new world, online,
	// its ports in use, RCON's "players", Join, the stop by RCON "quit"
	// (saved), a second start on the same world, a takeover whose stop
	// still goes through RCON, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, Game{}, gametest.Options{Real: true, World: "RealCheck"})
	})

	// The parts particular to Project Zomboid, and the facts FACTS.md
	// marks for this check.
	t.Run("facts", func(t *testing.T) { realFacts(t) })
}

func realFacts(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Facts", Change: func(sv *game.Saved) {
		sv.Server["serverName"] = "Real check — Ñandú"
		sv.Server["password"] = "two words"
		sv.Worlds["Facts"]["zombies"] = "high"
	}})
	ready := s.Plan().ReadyWait + 5*time.Minute
	start := time.Now()
	s.WaitEvent("online", "", ready)
	t.Logf("FACTS: a new world was online %v after launch", time.Since(start).Round(time.Second))
	srv := s.Server()
	saves := s.Env().Dirs.Saves
	serverDir := s.Env().Dirs.Server
	build, _ := srv.Build()
	t.Logf("FACTS: Steam build %s (%s), the server says version %s", build.Build, build.Date.Format("2006-01-02"), s.State().Version)
	launchFacts(t, serverDir)
	javaFacts(t, srv.Program())
	t.Logf("FACTS: the plan ran %s %s", srv.Program(), strings.Join(redact(s.Plan().Args), " "))

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
	for _, line := range []string{"Administrator account 'admin' created.", "admin password changed via -adminpassword option", "RCON: listening on port", "*** SERVER STARTED"} {
		t.Logf("FACTS: log line %q", logLine(t, s.Plan().LogPath, line))
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
	s.WaitEvent("online", "", ready)
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
		t.Logf("FACTS: read from %s: JVM %q, class path %q, game arguments %q", l.Source, l.JVM, l.ClassPath, l.Game)
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
	out := append([]string(nil), args...)
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
