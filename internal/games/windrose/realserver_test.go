//go:build realserver

// The real-server check (spec 5.11 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 90m ./internal/games/windrose/
//
// It downloads the real server with SteamCMD (Steam app 4129620, about
// 0.9 GB downloaded, 3.1 GB installed, plus Microsoft's Visual C++ runtime
// when the runner lacks it) and plays it through gametest, without the
// manager. Lines starting with "FACTS:" are for FACTS.md.
//
// CI has no Windrose game client, so no player ever joins: the presence
// decision needs a real session's log from the owner's PC (FACTS.md, "Who
// is playing").

package windrose

import (
	"debug/pe"
	"encoding/json"
	"fmt"
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
	if runtime.GOOS != "windows" {
		t.Skip("the Windrose server runs on Windows only")
	}
	t.Logf("FACTS: Visual C++ runtime before the install: %s", vcRuntimeVersion())

	// Everything every adapter must do, against the real server: install,
	// the first start that makes the settings file and the first world,
	// online, Join, Ctrl+C, a second start on the same world, a takeover
	// whose stop still works, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, Game{}, gametest.Options{Real: true, World: "RealCheck"})
	})

	t.Run("program, settings file, stop and a second world", func(t *testing.T) {
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Second sea ✓",
			Change: func(sv *game.Saved) {
				sv.Server["serverName"] = "Real check ✓"
				sv.Server["password"] = "parrot & co"
			}})
		ready := s.Plan().ReadyWait + 5*time.Minute
		start := time.Now()
		s.WaitEvent("online", "", ready)
		t.Logf("FACTS: the first start's world (made by the plan's first run) was ready %v after the real start", time.Since(start).Round(time.Second))
		srv := s.Server().(*server)
		build, _ := srv.Build()
		st := s.State()
		t.Logf("FACTS: installed Steam build %s (%s), Windrose %s, invite code %q", build.Build, build.Date.Format("2006-01-02"), st.Version, st.Code)
		t.Logf("FACTS: Visual C++ runtime after the install: %s", vcRuntimeVersion())
		programFacts(t, srv.Program())
		if p, err := platform.OpenProc(s.Record().PID); err == nil {
			if _, mem, err := p.Usage(); err == nil {
				t.Logf("FACTS: the server uses %d MB of memory with nobody on", mem>>20)
			}
			p.Close()
		}
		if st.Code == "" || len(st.Code) < 6 {
			t.Errorf("the server didn't say an invite code (%q)", st.Code)
		}
		d, err := readDescription(srv.descriptionPath())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := d.bytes()
		t.Logf("FACTS: ServerDescription.json while it runs (password hidden): %s",
			strings.Join(strings.Fields(strings.ReplaceAll(string(b), "parrot & co", "(hidden)")), " "))
		if d.str(keyServerName) != "Real check ✓" || !d.boolean(keyProtected) {
			t.Errorf("the server changed the app's settings: name %q, protected %v", d.str(keyServerName), d.boolean(keyProtected))
		}
		logFacts(t, srv.logPath(), "the first real start")

		// Ctrl+C saves the world.
		start = time.Now()
		saved := s.Stop()
		t.Logf("FACTS: Ctrl+C ended the server after %v; the Monitor saw it save at %v", time.Since(start).Round(100*time.Millisecond), saved)
		text := logText(t, srv.logPath())
		for _, want := range []string{"ConsoleCtrl", "Save backup has finished successfully"} {
			if !strings.Contains(text, want) {
				t.Errorf("after Ctrl+C the log has no %q", want)
			}
		}
		lastLines(t, text, 25, "the end of the log after Ctrl+C")

		// A second new world: the app gives it an id; record whether the
		// server makes it under that id, under its own, or not at all.
		srv.store.Save(func(sv *game.Saved) error { sv.World = "Third sea"; return nil })
		plan, err := srv.Plan(t.Context(), game.PlanRequest{World: "Third sea", New: true, Saved: srv.store.Get()})
		if err != nil {
			t.Fatal(err)
		}
		asked := extraString(plan.Record, "worldId")
		if err := runReal(t, plan); err != nil {
			t.Errorf("starting a second new world: %v", err)
			lastLines(t, logText(t, srv.logPath()), 40, "the log of the second new world's start")
		}
		d, _ = readDescription(srv.descriptionPath())
		worlds, _ := srv.worlds.scan()
		_, madeAsked := worlds[strings.ToUpper(asked)]
		t.Logf("FACTS: a new world asked for by id %s: made under that id %v; the file now names %s; worlds on disk %d; the app lists %q",
			asked, madeAsked, d.str(keyWorld), len(worlds), worldList(t, srv))
		if !srv.Worlds().Exists("Third sea") || !srv.Worlds().Exists("Second sea ✓") {
			t.Errorf("the worlds aren't both listed: %q", worldList(t, srv))
		}
		dirFacts(t, srv.savesDir())

		// The first world again, then a takeover whose Ctrl+C still works.
		srv.store.Save(func(sv *game.Saved) error { sv.World = "Second sea ✓"; return nil })
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		s.Takeover()
		s.Stop()
	})
}

// runReal starts a plan's program the way the app does, waits for its
// ready line and stops it.
func runReal(t *testing.T, plan game.Plan) error {
	p, err := startServer(platform.StartOptions{Exe: plan.Exe, Args: plan.Args, Dir: plan.Dir, Env: os.Environ()})
	if err != nil {
		return err
	}
	defer p.Close()
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	deadline := time.Now().Add(plan.ReadyWait + 5*time.Minute)
	for {
		b, _ := os.ReadFile(plan.LogPath)
		if reReady.Match(b) || reWaiting.Match(b) {
			break
		}
		select {
		case <-done:
			return fmt.Errorf("the server stopped before it was ready")
		case <-time.After(time.Second):
		}
		if time.Now().After(deadline) {
			p.Kill()
			<-done
			return fmt.Errorf("not ready within %v", plan.ReadyWait+5*time.Minute)
		}
	}
	if err := askToStop(t.Context(), p); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(stopWait):
		p.Kill()
		<-done
		return fmt.Errorf("it didn't stop within %v of Ctrl+C", stopWait)
	}
}

// programFacts records what kind of program the server is: a windowed one
// (Windows' subsystem 2), whose console is the one -log opens.
func programFacts(t *testing.T, program string) {
	t.Helper()
	for _, p := range []string{program, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(program)))), "WindroseServer.exe")} {
		f, err := pe.Open(p)
		if err != nil {
			t.Errorf("reading %s: %v", p, err)
			continue
		}
		if oh, ok := f.OptionalHeader.(*pe.OptionalHeader64); ok {
			t.Logf("FACTS: %s: subsystem %d (2 is a windowed program, 3 a console one)", filepath.Base(p), oh.Subsystem)
		}
		f.Close()
	}
	bat, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(program)))), "StartServerForeground.bat"))
	if err != nil {
		t.Errorf("reading StartServerForeground.bat: %v", err)
		return
	}
	for _, l := range strings.Split(string(bat), "\n") {
		if strings.Contains(strings.ToLower(l), "shipping") {
			t.Logf("FACTS: StartServerForeground.bat starts the server with: %s", strings.TrimSpace(l))
		}
	}
	if !strings.Contains(string(bat), `R5\Binaries\Win64\WindroseServer-Win64-Shipping.exe -log`) {
		t.Errorf("StartServerForeground.bat no longer starts the shipping program with -log: Program() and serverArgs copy it, so check what it does now")
	}
}

// logFacts records the lines the Monitor relies on, as this build writes
// them.
func logFacts(t *testing.T, path, when string) {
	t.Helper()
	text := logText(t, path)
	for _, re := range []string{"Host server is ready for owner to connect", "=> WaitingForFirstAccount", "> InviteCode", "Server registration finished", "\"DeploymentId\"", "Server initialized. CurrentIslandId", "Save backup has finished successfully", "Connected Accounts"} {
		found := ""
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, re) {
				found = strings.TrimSpace(rePasswords.ReplaceAllString(l, "${1}(hidden)"))
				break
			}
		}
		t.Logf("FACTS: %s: %q: %q", when, re, found)
	}
}

func lastLines(t *testing.T, text string, n int, what string) {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	t.Logf("FACTS: %s:\n%s", what, strings.Join(lines, "\n"))
}

// dirFacts records the saves folder's layout (folders and file names only).
func dirFacts(t *testing.T, dir string) {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if info.IsDir() || strings.HasSuffix(strings.ToLower(p), ".zip") || strings.HasSuffix(p, worldFile) {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(out) > 60 {
		out = out[:60]
	}
	b, _ := json.Marshal(out)
	t.Logf("FACTS: the saves folder: %s", b)
}

func logText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
