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
		t.Logf("FACTS: after %s the world has a -wal: %v", saveLine, exists(world+"-wal"))

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
