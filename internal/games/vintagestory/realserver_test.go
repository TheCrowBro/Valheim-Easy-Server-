//go:build realserver

// The real-server check (spec 5.5 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 60m ./internal/games/vintagestory/
//
// It downloads the real server from Vintage Story's site (61 MB) and .NET
// from Microsoft's (37 MB) and plays it through gametest, without the
// manager. It also runs on Linux, where the server has its own download
// (Linux isn't a system the app is offered on, but it is where this
// adapter was written and first checked). Lines starting with "FACTS:" are
// for FACTS.md.

package vintagestory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

func TestRealServer(t *testing.T) {
	// The server must keep everything in the instance's saves folder
	// (--dataPath) and never touch the VintagestoryData folder the owner's
	// own game uses.
	home, _ := os.UserHomeDir()
	own := localWorlds(home)[0]
	before := listing(own)
	t.Cleanup(func() {
		if after := listing(own); after != before {
			t.Errorf("the server changed %s, the owner's own game's saves:\nbefore: %s\nafter: %s", own, before, after)
		} else {
			t.Logf("FACTS: %s (the game's own saves) was not touched", own)
		}
	})

	// Everything every adapter must do, against the real server: install,
	// a new world, online, the save line, Join, the stop by "/stop", a
	// second start on the same world, a takeover whose stop has no console
	// and goes to Ctrl+C, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, Game{}, gametest.Options{Real: true, World: "RealCheck", Change: withPassword})
	})

	// The parts particular to Vintage Story.
	t.Run("settings, saves, takeover and Ctrl+C", func(t *testing.T) {
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Takeover", Change: func(sv *game.Saved) {
			withPassword(sv)
			sv.Server["serverName"] = "Real check"
			sv.Server["maxPlayers"] = "5"
		}, NewWorld: game.Values{"playStyle": "exploration", "seed": "424242"}})
		ready := s.Plan().ReadyWait + 5*time.Minute
		s.WaitEvent("online", "", ready)
		rec := s.Record()
		build, _ := s.Server().Build()
		t.Logf("FACTS: installed Vintage Story %s (the server says %s), program %s", build.Version, s.State().Version, s.Server().Program())
		for _, proto := range []string{"tcp", "udp"} {
			inUse, pid := platform.PortInUse(proto, rec.Port)
			t.Logf("FACTS: PortInUse(%q, %d) = %v, pid %d (the server is %d)", proto, rec.Port, inUse, pid, rec.PID)
			if !inUse {
				t.Errorf("the server doesn't hold %s port %d", proto, rec.Port)
			}
		}
		logPath := s.Plan().LogPath
		if l := logLine(t, logPath, "invite-only (whitelisted)"); l != "" {
			t.Errorf("the server still says it's invite-only: %q", l)
		}
		if l := logLine(t, logPath, "Create new save game data"); !strings.Contains(l, "Playstyle: exploration") {
			t.Errorf("the new world isn't an exploration world: %q", l)
		}
		if l := logLine(t, logPath, "Using world seed"); !strings.Contains(l, "424242") {
			t.Errorf("the new world didn't get its seed: %q", l)
		}

		// The server wrote its settings back once it started; the app's
		// keys must have survived that.
		var cfg map[string]any
		b, err := os.ReadFile(filepath.Join(s.Env().Dirs.Saves, configName))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]any{"ServerName": "Real check", "MaxClients": 5.0, "Password": "secret-word", "WhitelistMode": 1.0, "AdvertiseServer": false, "Port": float64(rec.Port)} {
			if cfg[k] != want {
				t.Errorf("serverconfig.json after the start: %s = %v, want %v", k, cfg[k], want)
			}
		}
		t.Logf("FACTS: serverconfig.json after the start has ConfigVersion %v", cfg["ConfigVersion"])

		world := s.Server().(*server).worldFile("Takeover")
		first := stat(t, world)

		// The app's save line saves without stopping.
		if err := s.Console(saveLine); err != nil {
			t.Fatal(err)
		}
		s.WaitEvent("saved", "", time.Minute)
		t.Logf("FACTS: after %q the world has a -wal: %v", saveLine, exists(world+"-wal"))

		// "/stop" saves and ends it, folding the -wal into the world.
		s.Stop()
		stopped := stat(t, world)
		if !stopped.ModTime().After(first.ModTime()) {
			t.Errorf("\"/stop\" didn't write the world (%v, before %v)", stopped.ModTime(), first.ModTime())
		}
		if exists(world + "-wal") {
			t.Error("the world's -wal is still there after the stop")
		}

		// The same world loads again, with its own style.
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		if l := logLine(t, s.Plan().LogPath, "Loaded existing save game data"); !strings.Contains(l, "exploration") {
			t.Errorf("the second start didn't load the saved world: %q", l)
		}

		// A takeover: the app that started it is gone with its console, so
		// the stop goes to Ctrl+C. The server handles that as a stop
		// someone asked for, and saves.
		time.Sleep(1100 * time.Millisecond)
		s.Takeover()
		start := time.Now()
		last := s.Stop()
		t.Logf("FACTS: the Ctrl+C stop after a takeover took %v", time.Since(start).Round(100*time.Millisecond))
		after := stat(t, world)
		saved := !last.Before(start.Truncate(time.Second)) && !exists(world+"-wal") && after.ModTime().After(stopped.ModTime())
		if saved {
			t.Logf("FACTS: Ctrl+C on %s saved the world (\"World saved!\" at %v, the -wal folded in, the file changed at %v)", runtime.GOOS, last, after.ModTime())
		} else {
			t.Errorf("Ctrl+C on %s didn't save the world (last save %v, -wal left: %v, file %v): the stop's Say text and FACTS.md must say what is lost",
				runtime.GOOS, last, exists(world+"-wal"), after.ModTime())
		}
		if l := logLine(t, s.Plan().LogPath, "Server termination event"); l != "" {
			t.Logf("FACTS: %s", l)
		}
	})

	// How the server reads its console and spells its output, on each
	// system (FACTS.md, "The console"). Its console reader runs what one
	// read gives only when that ends with the system's line ending, .NET's
	// Environment.NewLine ("\r\n" on Windows, "\n" elsewhere), and stops
	// reading for good after a read that doesn't. So the order matters:
	// first lines typed as the app types them (ending "\r\n"), then one
	// ending in "\n" alone, which on Windows should be lost and leave the
	// console deaf, and last Ctrl+C, the one stop that needs no console.
	t.Run("console", func(t *testing.T) {
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Console", Change: withPassword})
		s.WaitEvent("online", "", s.Plan().ReadyWait+5*time.Minute)
		logPath := s.Plan().LogPath
		const handling = "Handling Console Command "

		// 1. A command the server doesn't have, with a letter outside
		// ASCII: it only answers that it's handling it (run with the Linux
		// server), and that answer shows how its output spells "ü".
		probe := "/egsprobe-Jürgen"
		if err := s.Console(probe + endLine); err != nil {
			t.Fatal(err)
		}
		if !waitCount(t, logPath, handling+"/egsprobe-", 1, time.Minute) {
			t.Fatalf("%q typed as the app types it (ending \"\\r\\n\") didn't run within a minute: the console doesn't read as FACTS.md says", probe)
		}
		raw := logLine(t, logPath, handling+"/egsprobe-")
		spelled, how := spelling(raw)
		t.Logf("FACTS: on %s %q typed ending \"\\r\\n\" ran; the server's answer spells \"ü\" as % x (%s); this computer's OEM code page is %d", runtime.GOOS, probe, spelled, how, oemCodePage())
		text := (&server{}).Watch(game.Record{}, nil).Line(raw, time.Now()).Text
		switch {
		case strings.Contains(text, "Jürgen"):
			t.Logf("FACTS: the Monitor reads that line as %q", text)
		case how == "lost":
			t.Logf("FACTS: the console's code page has no \"ü\", so no reading can bring it back (the Monitor reads %q)", text)
		default:
			t.Errorf("the Monitor reads the server's line as %q, want \"Jürgen\" in it (consoleText)", text)
		}

		// 2. The save line, as the app types it before it exits.
		if err := s.Console(saveLine); err != nil {
			t.Fatal(err)
		}
		if !waitCount(t, logPath, handling+"/autosavenow", 1, time.Minute) {
			t.Fatalf("the save line %q didn't run within a minute", saveLine)
		}
		saved := s.WaitEvent("saved", "", 2*time.Minute)
		t.Logf("FACTS: on %s the save line %q ran and the server said %q at %v", runtime.GOOS, saveLine, logLine(t, logPath, "World saved!"), saved.Time)

		// 3. The same command ending in "\n" alone, as the app typed it
		// before, and then the save line again.
		if err := s.Console("/autosavenow"); err != nil {
			t.Fatal(err)
		}
		bare := waitCount(t, logPath, handling+"/autosavenow", 2, 20*time.Second)
		if runtime.GOOS != "windows" {
			t.Logf("FACTS: on %s \"/autosavenow\" ending \"\\n\" alone ran: %v", runtime.GOOS, bare)
			if !bare {
				t.Errorf("\"/autosavenow\" ending \"\\n\" didn't run, but \"\\n\" is the line ending on %s", runtime.GOOS)
			}
		} else {
			if err := s.Console(saveLine); err != nil {
				t.Fatal(err)
			}
			after := waitCount(t, logPath, handling, 3, 20*time.Second)
			t.Logf("FACTS: on Windows \"/autosavenow\" ending \"\\n\" alone ran: %v; the save line typed after it ran: %v", bare, after)
			if bare || after {
				t.Errorf("the Windows server ran a line ending in \"\\n\" alone (%v), or read its console after one (%v): FACTS.md says it doesn't, so fix it and endLine's comment", bare, after)
			}
		}

		// 4. Ctrl+C, as after a takeover: it needs no console.
		s.Takeover()
		start := time.Now()
		s.Stop()
		t.Logf("FACTS: on %s the Ctrl+C stop took %v; %q", runtime.GOOS, time.Since(start).Round(100*time.Millisecond), logLine(t, logPath, "Server termination event"))
	})
}

// spelling finds how the server's line spells the "ü" of "Jürgen": its
// bytes, and what they are in the usual console code pages.
func spelling(line string) ([]byte, string) {
	_, rest, ok := strings.Cut(line, "/egsprobe-J")
	end := strings.Index(rest, "rgen")
	if !ok || end < 0 {
		return nil, "not found in the line"
	}
	b := []byte(rest[:end])
	switch string(b) {
	case "ü":
		return b, "UTF-8"
	case "\x81":
		return b, "code page 437 or 850, the OEM code pages of English and western European Windows"
	case "\xfc":
		return b, "code page 1252, Windows' ANSI code page"
	case "?":
		return b, "lost"
	}
	return b, "another code page"
}

// waitCount waits until the run's log holds sub at least n times, and says
// whether it did within the wait.
func waitCount(t *testing.T, path, sub string, n int, within time.Duration) bool {
	t.Helper()
	for end := time.Now().Add(within); ; time.Sleep(200 * time.Millisecond) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), sub) >= n {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// listing is a folder's files with their sizes and times, to tell whether
// anything there changed ("" when it doesn't exist).
func listing(dir string) string {
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				b.WriteString(p + " " + info.ModTime().String() + "\n")
			}
		}
		return nil
	})
	return b.String()
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// logLine is the last line of the run's log that has sub in it.
func logLine(t *testing.T, path, sub string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for l := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(l, sub) {
			found = strings.TrimSpace(l)
		}
	}
	return found
}
