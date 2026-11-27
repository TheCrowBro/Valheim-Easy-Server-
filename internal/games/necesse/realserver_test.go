//go:build realserver

// The real-server check (spec 5.4 and 7.4). The CI matrix runs it on
// Windows and an Intel Mac from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 60m ./internal/games/necesse/
//
// It downloads the real server with SteamCMD (Steam app 1169370, about
// 100 MB) and plays it through gametest, without the manager. It also runs
// on Linux (SteamCMD there needs the 32-bit C libraries), though Linux
// isn't a system the app is offered on. Lines starting with "FACTS:" are
// for FACTS.md.

package necesse

import (
	"bytes"
	"context"
	"debug/macho"
	"debug/pe"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

func TestRealServer(t *testing.T) {
	realServerChecks(t, Game{})
}

// realServerChecks plays g, which installs the real server.
func realServerChecks(t *testing.T, g game.Game) {
	// The server must keep everything in the instance's folder (-datadir)
	// and never touch the Necesse folder the owner's own game uses.
	ownFolder := defaultDataFolder()
	before := snapshot(ownFolder)
	t.Cleanup(func() {
		if after := snapshot(ownFolder); after != before {
			t.Errorf("the server changed %s, the owner's own Necesse folder:\nbefore:\n%s\nafter:\n%s", ownFolder, before, after)
		} else {
			t.Logf("FACTS: %s (the default data folder) was not touched", ownFolder)
		}
	})

	// Everything every adapter must do, against the real server: install,
	// a new world, online, the save line, Join, the stop by "stop", a
	// second start on the same world, a takeover whose stop has no console
	// and goes to Ctrl+C, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, g, gametest.Options{Real: true, World: "RealCheck"})
	})

	// The parts particular to Necesse.
	//
	// On the Intel Mac runner (run 45921304914, both attempts) every check
	// in this subtest passed, and then Go's own removal of the subtest's
	// temporary folder failed: "unlinkat …/TestRealServerconsole,…: directory
	// not empty", while the same removal after "conformance" worked
	// (FACTS.md, "The Mac cell's temporary folder"). So this subtest notes
	// which folder gametest made for it, and after it says what, if
	// anything, was left there and which programs still had it open.
	var sessionFolder string
	ok := t.Run("console, password, takeover and Ctrl+C", func(t *testing.T) {
		// The first start has no password, given as an empty value, which
		// must reach Java as one (an empty value lost on the way would make
		// the next option the password).
		s := gametest.Start(t, g, gametest.Options{Real: true, World: "Takeover", Change: func(sv *game.Saved) {
			sv.Server["serverName"] = "Real check"
		}})
		// gametest's data folder is <the subtest's folder>/001.
		sessionFolder = filepath.Dir(s.Env().Data)
		// This runs before gametest's own cleanup and the folder's removal
		// (cleanups run last-registered first). Once every server here has
		// stopped, no program should still have a file open in the folder.
		finished := false
		t.Cleanup(func() {
			if !finished {
				return // a server the test left running is gametest's to stop
			}
			if h := holders(sessionFolder); h != "" {
				t.Logf("FACTS: after the last stop, programs still had files open in the test's folder %s:\n%s", sessionFolder, h)
			}
		})
		ready := s.Plan().ReadyWait + 5*time.Minute
		s.WaitEvent("online", "", ready)
		rec := s.Record()
		build, _ := s.Server().Build()
		t.Logf("FACTS: installed Steam build %s (%s), Necesse %s", build.Build, build.Date.Format("2006-01-02"), s.State().Version)
		javaFacts(t, s.Server().Program())

		if started := logLine(t, s.Plan().LogPath, "Started server using port"); strings.Contains(started, "with password") {
			t.Errorf("the server has a password though none was set: %q", started)
		}
		for _, proto := range []string{"udp", "tcp"} {
			inUse, pid := platform.PortInUse(proto, rec.Port)
			t.Logf("FACTS: PortInUse(%q, %d) = %v, pid %d (the server is %d)", proto, rec.Port, inUse, pid, rec.PID)
		}
		t.Logf("FACTS: %s", logLine(t, s.Plan().LogPath, "Started lan socket"))

		zipPath := filepath.Join(worldsDir(s.Env().Dirs.Saves), "Takeover.zip")
		first := modTime(t, zipPath)

		// The app's save line saves without stopping.
		time.Sleep(1100 * time.Millisecond)
		if err := s.Console("save"); err != nil {
			t.Fatal(err)
		}
		s.WaitEvent("saved", "", time.Minute)
		saved := modTime(t, zipPath)
		if !saved.After(first) {
			t.Errorf("\"save\" didn't write the world (%v, before %v)", saved, first)
		}

		// "stop" saves and ends it.
		time.Sleep(1100 * time.Millisecond)
		s.Stop()
		stopped := modTime(t, zipPath)
		if !stopped.After(saved) {
			t.Errorf("\"stop\" didn't save the world (%v, before %v)", stopped, saved)
		}

		// The next start has a password with a space in it, to show the
		// command line reaches Java whole on this system.
		s.Env().Store.Save(func(sv *game.Saved) error {
			sv.Server["password"] = "two words"
			return nil
		})
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		if started := logLine(t, s.Plan().LogPath, "Started server using port"); !strings.Contains(started, `with password "two words"`) {
			t.Errorf("the server didn't take the password with a space: %q", started)
		}

		// A takeover: the app that started it is gone with its console,
		// so the stop goes straight to Ctrl+C, which ends Java at once and
		// saves nothing (FACTS.md).
		s.Takeover()
		start := time.Now()
		s.Stop()
		t.Logf("FACTS: the Ctrl+C stop after a takeover took %v", time.Since(start).Round(100*time.Millisecond))
		if after := modTime(t, zipPath); !after.Equal(stopped) {
			t.Logf("FACTS: Ctrl+C changed the world file (%v, before %v)", after, stopped)
		} else {
			t.Log("FACTS: Ctrl+C left the world file as it was (no save)")
		}

		// The world from the last save loads again.
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		if !strings.Contains(logLine(t, s.Plan().LogPath, "Loading existing world"), "Takeover.zip") {
			t.Errorf("the third start didn't load the saved world")
		}
		s.Stop()

		codePageFacts(t, s.Server().(*server), s.Env().Store.Get())
		finished = true
	})
	if !ok {
		leftBehind(t, sessionFolder)
	}
}

// leftBehind says, for a subtest that failed, what is still in the
// temporary folder Go should have removed when it ended, and which programs
// have files open there: the evidence for a "TempDir RemoveAll cleanup"
// failure, which by itself names only the folder. It leaves the folder as
// it is, for the CI step that collects the test's logs.
func leftBehind(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		return
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Logf("FACTS: the failed subtest's folder %s is gone (%v)", dir, err)
		return
	}
	t.Logf("FACTS: the failed subtest's folder %s is still there, holding (path, size, mode, last change):\n%s", dir, listTree(dir))
	if h := holders(dir); h != "" {
		t.Logf("FACTS: programs with files open in it:\n%s", h)
	} else {
		t.Logf("FACTS: no program has a file open in it (or lsof isn't available here)")
	}
}

// listTree lists everything under dir, hidden files included, one line
// each, with the time each last changed (to the nanosecond, so it can be
// set against the times in the test's log).
func listTree(dir string) string {
	var lines []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			lines = append(lines, fmt.Sprintf("  %s: %v", p, err))
			return nil
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if info, err := d.Info(); err == nil {
			lines = append(lines, fmt.Sprintf("  %s %d %v %s", rel, info.Size(), info.Mode(), info.ModTime().UTC().Format(time.RFC3339Nano)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s (%v)", rel, err))
		}
		if len(lines) >= 200 {
			return fs.SkipAll
		}
		return nil
	})
	if len(lines) == 0 {
		return "  (nothing)"
	}
	return strings.Join(lines, "\n")
}

// holders is what lsof says about the programs that have a file (or their
// working folder) under dir, or "" when none has or lsof isn't there
// (Windows). It gives up after 30 seconds.
func holders(dir string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// lsof exits 1 when it finds nothing; its output says all there is.
	// -w leaves out its warnings (about file systems it can't look at),
	// so any output names a program.
	out, _ := exec.CommandContext(ctx, lsof, "-w", "-n", "-P", "+D", dir).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// ownTempFolder makes a temporary folder for something the test starts
// itself, outside the folder gametest made (named Test… so CI's step that
// collects a failed test's logs looks in it too), and removes it when the
// test ends. If it can't be removed at once, the test's log says what was
// in it and which programs had it open, and it tries again for 10 seconds
// before failing.
func ownTempFolder(t *testing.T, pattern string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := os.RemoveAll(dir)
		if err == nil {
			return
		}
		t.Logf("FACTS: removing %s failed (%v); it held:\n%s", dir, err, listTree(dir))
		if h := holders(dir); h != "" {
			t.Logf("FACTS: programs with files open in it:\n%s", h)
		}
		first := time.Now()
		for time.Since(first) < 10*time.Second {
			time.Sleep(500 * time.Millisecond)
			if err = os.RemoveAll(dir); err == nil {
				t.Logf("FACTS: removing it worked %v later", time.Since(first).Round(100*time.Millisecond))
				return
			}
		}
		t.Errorf("couldn't remove %s: %v; it holds:\n%s", dir, err, listTree(dir))
	})
	return dir
}

// codePageFacts settles how a value with characters outside Windows' code
// page reaches the server's Java (charset.go, FACTS.md). Validate refuses
// such a value, so this starts the real server itself, with such a world
// name and a data folder of its own, and looks at the world file the
// server makes: a file under the name as given means the name got through
// whole.
//
// That data folder is its own temporary folder, not one inside the folder
// gametest made for the subtest (t.TempDir would put it there, as "002"
// beside gametest's "001"). On the Mac this server really runs (on Windows
// it ends at once on the mangled name), and the subtest's folder was the
// one Go couldn't remove there; with this run's folder apart, a folder it
// leaves behind is reported on its own (ownTempFolder), and gametest's
// folder holds only what gametest put in it, as in "conformance", whose
// folder was removed without trouble.
func codePageFacts(t *testing.T, srv *server, sv game.Saved) {
	t.Helper()
	const world = "Code page ✓ 🏡"
	own := *srv
	own.env.Dirs.Saves = ownTempFolder(t, "TestRealServer-codepage-")
	cmd := exec.Command(srv.Program(), own.args(sv, world)...)
	cmd.Dir = srv.env.Dirs.Server
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	// Whatever happens below, this Java has ended before its folder is
	// removed (cleanups run last-registered first).
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			cmd.Process.Kill()
			<-done
		}
	})
	// It makes the world, then says it's ready; or, given a name it can't
	// use, it may end or hang instead.
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "Type help for list of commands.") {
		select {
		case <-done:
			deadline = time.Now()
		case <-time.After(time.Second):
		}
	}
	io.WriteString(stdin, "stop\n")
	select {
	case <-done:
	case <-time.After(time.Minute):
		cmd.Process.Kill()
		<-done
	}
	entries, _ := os.ReadDir(worldsDir(own.env.Dirs.Saves))
	var files []string
	for _, e := range entries {
		files = append(files, e.Name())
	}
	whole := slices.Contains(files, world+worldExt)
	fits := len(argLacks(world)) == 0
	t.Logf("FACTS: %s: the world name %q, which the check says %s, made the world files %q (the name got through whole: %v)",
		codePage(), world, map[bool]string{true: "gets through", false: "doesn't get through"}[fits], files, whole)
	// What the server said about the name (with an ASCII locale on Linux:
	// "(ERR) Invalid world path: Code page ??? ????").
	for _, l := range strings.Split(out.String(), "\n") {
		if text, _ := splitLine(l); strings.Contains(text, "Invalid world path") || strings.Contains(text, "Creating new world at") {
			t.Logf("FACTS: %s", text)
		}
	}
	switch {
	case runtime.GOOS == "linux":
		// Not a system the app is offered on; there Java reads its
		// arguments in the locale's character set.
	case fits && !whole:
		t.Errorf("the check says %q reaches Java whole, but the server made %q:\n%s", world, files, out.String())
	case !fits && whole:
		t.Errorf("%q reached Java whole though %s lacks some of it: the code page rule in charset.go refuses names that work, and can go", world, codePage())
	}
}

// lockedBuffer collects a program's output while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// defaultDataFolder is where the server would keep its data without
// -datadir: the Necesse folder of the owner's own game.
func defaultDataFolder() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		appdata := platform.KnownFolder("appdata")
		if appdata == "" {
			appdata = os.Getenv("APPDATA")
		}
		return filepath.Join(appdata, "Necesse")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Necesse")
	}
	return filepath.Join(home, ".config", "Necesse")
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

// javaFacts records what the server's Java is: a console program on
// Windows (so Ctrl+C reaches it), and on a Mac its processor and the oldest
// macOS it runs on (spec 5.4: it decides darwin/arm64).
func javaFacts(t *testing.T, java string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		f, err := pe.Open(java)
		if err != nil {
			t.Errorf("reading %s: %v", java, err)
			return
		}
		defer f.Close()
		oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
		if !ok {
			t.Errorf("%s isn't a 64-bit Windows program", java)
			return
		}
		t.Logf("FACTS: %s: subsystem %d (3 is a console program)", java, oh.Subsystem)
		if oh.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
			t.Errorf("%s isn't a console program, so Ctrl+C can't reach it", java)
		}
	case "darwin":
		if out, err := exec.Command("file", java).CombinedOutput(); err == nil {
			t.Logf("FACTS: file: %s", strings.TrimSpace(string(out)))
		}
		f, err := macho.Open(java)
		if err != nil {
			t.Errorf("reading %s (a universal Java would need macho.OpenFat): %v", java, err)
			return
		}
		defer f.Close()
		t.Logf("FACTS: %s: CPU %v, minimum macOS %s", java, f.Cpu, minOS(f))
		if f.Cpu != macho.CpuAmd64 {
			t.Errorf("the Mac server's Java is %v now, not x86_64: Info.OS for darwin/arm64 can change", f.Cpu)
		}
	}
}

// minOS reads LC_BUILD_VERSION (or the older LC_VERSION_MIN_MACOSX).
func minOS(f *macho.File) string {
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) < 16 {
			continue
		}
		cmd := f.ByteOrder.Uint32(raw)
		var v uint32
		switch cmd {
		case 0x32: // LC_BUILD_VERSION: cmd, size, platform, minos
			v = f.ByteOrder.Uint32(raw[12:])
		case 0x24: // LC_VERSION_MIN_MACOSX: cmd, size, version
			v = f.ByteOrder.Uint32(raw[8:])
		default:
			continue
		}
		return fmt.Sprintf("%d.%d.%d", v>>16, (v>>8)&0xff, v&0xff)
	}
	return "not stated"
}

// logLine is the first line of the run's log that holds text.
func logLine(t *testing.T, path, text string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range bytes.Split(b, []byte("\n")) {
		if s, _ := splitLine(string(l)); strings.Contains(s, text) {
			return s
		}
	}
	t.Errorf("the log has no line with %q", text)
	return ""
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the world file: %v", err)
	}
	return st.ModTime()
}
