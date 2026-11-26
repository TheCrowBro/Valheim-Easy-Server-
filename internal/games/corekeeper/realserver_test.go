//go:build realserver

// The real-server check (spec 5.8 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 60m ./internal/games/corekeeper/
//
// It downloads the real server with SteamCMD (Steam app 1963720, about
// 490 MB installed) and plays it through gametest, without the manager.
// Lines starting with "FACTS:" are for FACTS.md.
//
// It also runs on Linux, which isn't a system the app is offered on, for a
// developer: there the server needs a display, so the check starts Xvfb
// when DISPLAY isn't set, and SteamCMD's coloured output there isn't
// understood by internal/steamcmd yet (stream-notes, G-necesse), so
// EGS_CK_INSTALLED may name a folder where the Linux server is installed
// already, which the check then uses instead of downloading it.

package corekeeper

import (
	"context"
	"debug/pe"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

func TestRealServer(t *testing.T) {
	var g game.Game = Game{}
	if runtime.GOOS == "linux" {
		linuxDisplay(t)
		if dir := os.Getenv("EGS_CK_INSTALLED"); dir != "" {
			g = preinstalled{dir}
		}
	}

	// The server should keep everything in the world's data folder. The
	// owner's own Core Keeper keeps its worlds under the same Pugstorm
	// folder, so the check records whether the server touches it.
	own := ownGameFolder()
	before := snapshot(own)
	t.Cleanup(func() {
		after := snapshot(own)
		if after == before {
			t.Logf("FACTS: %s (the game's own folder) was not touched", own)
		} else {
			t.Logf("FACTS: the server changed %s, the game's own folder:\nbefore:\n%s\nafter:\n%s", own, before, after)
		}
	})

	// Everything every adapter must do, against the real server: install,
	// a new world, online, Join, the quit request (Windows: the window's
	// close, as Pugstorm's launcher does), a second start on the same
	// world, a takeover whose stop still works, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, g, gametest.Options{Real: true, World: "RealCheck"})
	})

	t.Run("program, launcher, Game ID and world", func(t *testing.T) {
		s := gametest.Start(t, g, gametest.Options{Real: true, World: "Second world ✓",
			NewWorld: game.Values{"mode": "hard", "seed": "egs"},
			Change:   func(sv *game.Saved) { sv.Server["serverName"] = "Real check ✓" }})
		ready := s.Plan().ReadyWait + 5*time.Minute
		start := time.Now()
		s.WaitEvent("online", "", ready)
		t.Logf("FACTS: a new world was ready %v after the start", time.Since(start).Round(time.Second))
		srv := s.Server()
		build, _ := srv.Build()
		t.Logf("FACTS: installed Steam build %s (%s), Core Keeper %s", build.Build, build.Date.Format("2006-01-02"), s.State().Version)
		programFacts(t, srv.Program())

		// The Game ID: the one the app gave, in the log and in GameInfo.txt.
		id := s.Env().Store.Get().State[gameIDKey]
		if s.State().Code != id {
			t.Errorf("the server says Game ID %q, the app gave %q", s.State().Code, id)
		}
		info := waitFile(t, filepath.Join(s.Env().Dirs.Server, gameInfoFile), 30*time.Second)
		t.Logf("FACTS: GameInfo.txt: %q", info)
		if !strings.Contains(info, "GameID: "+id) {
			t.Errorf("GameInfo.txt doesn't have the app's Game ID %s: %q", id, info)
		}
		if p, err := platform.OpenProc(s.Record().PID); err == nil {
			if _, mem, err := p.Usage(); err == nil {
				t.Logf("FACTS: the server uses %d MB of memory with nobody on", mem>>20)
			}
			p.Close()
		}

		// The quit request saves the world.
		start = time.Now()
		s.Stop()
		t.Logf("FACTS: the quit request ended the server after %v", time.Since(start).Round(100*time.Millisecond))
		data := filepath.Join(s.Env().Dirs.Saves, "Second world ✓")
		world := filepath.Join(data, "worlds", worldFile)
		first := modTime(t, world)
		cfg, _ := os.ReadFile(filepath.Join(data, "ServerConfig.json"))
		t.Logf("FACTS: ServerConfig.json after the first run: %s", strings.Join(strings.Fields(string(cfg)), " "))
		if !strings.Contains(string(cfg), `"worldSeed": "egs"`) || !strings.Contains(string(cfg), `"gameId": "`+id+`"`) {
			t.Errorf("ServerConfig.json doesn't have the new world's seed and the app's Game ID")
		}
		// The name and the folder reached the server whole (a "✓" is in
		// no Windows code page but UTF-8).
		if !strings.Contains(string(cfg), "Real check ✓") {
			t.Errorf("ServerConfig.json doesn't have the server name as given (Real check ✓)")
		}

		// The next start loads it, with the same Game ID.
		time.Sleep(1100 * time.Millisecond)
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		if !strings.Contains(logText(t, s.Plan().LogPath), "successfully loaded world file") {
			t.Errorf("the second start didn't load the saved world")
		}
		if s.State().Code != id {
			t.Errorf("the second start has Game ID %q, not %q", s.State().Code, id)
		}

		// A takeover: the new copy of the app's stop still saves.
		s.Takeover()
		s.Stop()
		if after := modTime(t, world); !after.After(first) {
			t.Errorf("the stop after a takeover didn't save the world (%v, before %v)", after, first)
		}
	})
}

// programFacts records what kind of program the server is: on Windows a
// windowed one (so Ctrl+C can't reach it, spec 2.7) and how Pugstorm's own
// launcher stops it, which the stop chain copies.
func programFacts(t *testing.T, program string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	f, err := pe.Open(program)
	if err != nil {
		t.Errorf("reading %s: %v", program, err)
		return
	}
	defer f.Close()
	if oh, ok := f.OptionalHeader.(*pe.OptionalHeader64); ok {
		t.Logf("FACTS: %s: subsystem %d (2 is a windowed program, 3 a console one)", program, oh.Subsystem)
	}
	ps1, err := os.ReadFile(filepath.Join(filepath.Dir(program), "Launch.ps1"))
	if err != nil {
		t.Errorf("reading Launch.ps1: %v", err)
		return
	}
	for _, l := range strings.Split(string(ps1), "\n") {
		if strings.Contains(strings.ToLower(l), "taskkill") {
			t.Logf("FACTS: Launch.ps1 stops the server with: %s", strings.TrimSpace(l))
		}
	}
	if !strings.Contains(strings.ToLower(string(ps1)), "taskkill /pid") {
		t.Errorf("Launch.ps1 no longer stops the server with taskkill /pid: the stop chain (stop.go) copies it, so check what it does now")
	}
}

// preinstalled is Core Keeper with an install that is already there (see
// the file's comment): its installer links the server folder to it.
type preinstalled struct{ dir string }

func (p preinstalled) Info() game.Info { return Game{}.Info() }

func (p preinstalled) New(env game.Env) game.Server {
	return linkedServer{Game{}.New(env).(*server), p.dir}
}

type linkedServer struct {
	*server
	from string
}

func (l linkedServer) Installer() game.Installer { return linkInstaller{l.env.Dirs.Server, l.from} }

type linkInstaller struct{ to, from string }

// Install makes the instance's server folder a link to the install.
func (i linkInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if st, err := os.Lstat(i.to); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err := os.Remove(i.to); err != nil && !os.IsNotExist(err) {
		return err // not empty: leave it alone
	}
	return os.Symlink(i.from, i.to)
}

// linuxDisplay starts Xvfb for the server when there is no display, as
// the shipped _launch.sh does.
func linuxDisplay(t *testing.T) {
	if os.Getenv("DISPLAY") != "" {
		return
	}
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("the Linux server needs a display: install Xvfb or set DISPLAY")
	}
	display := fmt.Sprintf(":%d", 90+os.Getpid()%9)
	cmd := exec.Command(xvfb, display, "-screen", "0", "1x1x24", "-nolisten", "tcp")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	time.Sleep(time.Second)
	t.Setenv("DISPLAY", display)
}

// ownGameFolder is where the owner's own Core Keeper (and, without
// -datapath, the dedicated server) keeps its data.
func ownGameFolder() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "LocalLow", "Pugstorm", "Core Keeper")
	}
	return filepath.Join(home, ".config", "unity3d", "Pugstorm", "Core Keeper")
}

// snapshot lists a folder's files with their sizes and times ("absent"
// when it isn't there).
func snapshot(dir string) string {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			lines = append(lines, fmt.Sprintf("%s %d %s", p, info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)))
		}
		return nil
	})
	if err != nil {
		return "absent"
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func waitFile(t *testing.T, path string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Errorf("%s didn't appear within %v: %v", path, within, err)
			return ""
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func logText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the world file: %v", err)
	}
	return st.ModTime()
}
