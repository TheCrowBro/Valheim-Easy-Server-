//go:build realserver

// The real-server check (spec 5.7 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 90m ./internal/games/enshrouded/
//
// It downloads the real server with SteamCMD once (Steam app 2278520,
// about 3.6 GB to download and 8.1 GB on disk) into a folder every part of
// the check shares, and plays it through gametest, without the manager.
// Lines starting with "FACTS:" are for FACTS.md: they settle what only the
// real server on Windows can (the "verify" items 5.7 left).
//
// The server runs only on Windows, so elsewhere the check is skipped.

package enshrouded

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/json"
	"fmt"
	"io/fs"
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
)

func TestRealServer(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Enshrouded server runs only on Windows")
	}
	shared := installOnce(t)
	var g game.Game = linked{shared}

	// The server should keep everything under the instance. Its own
	// default places are the server folder and, for the owner's own
	// game, Saved Games; the check records whether it writes there.
	own := ownGameFolder()
	before := snapshot(own)
	t.Cleanup(func() {
		if after := snapshot(own); after == before {
			t.Logf("FACTS: %s (the game's own folder) was not touched", own)
		} else {
			t.Logf("FACTS: the server changed %s:\nbefore:\n%s\nafter:\n%s", own, before, after)
		}
	})

	// Everything every adapter must do, against the real server: a new
	// world, online, the query port answering, Join, Ctrl+C saving, a
	// second start on the same world, a takeover whose Ctrl+C still
	// works, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, g, gametest.Options{Real: true, World: "RealCheck", Change: withPassword})
	})

	t.Run("facts", func(t *testing.T) { realFacts(t, g) })
	t.Run("stdout", func(t *testing.T) { stdoutFacts(t, g, shared) })
}

// realFacts starts a world with a name in no Windows code page and records
// what the real server does with it.
func realFacts(t *testing.T, g game.Game) {
	world := "Second world ✓"
	s := gametest.Start(t, g, gametest.Options{Real: true, World: world, Change: func(sv *game.Saved) {
		withPassword(sv)
		sv.Server["serverName"] = "Real check ✓"
		sv.Server["maxPlayers"] = "4"
		sv.Worlds[world] = game.Values{"difficulty": "hard"}
	}})
	ready := s.Plan().ReadyWait + 5*time.Minute
	start := time.Now()
	s.WaitEvent("online", "", ready)
	t.Logf("FACTS: a new world was online %v after the start", time.Since(start).Round(time.Second))
	srv := s.Server()
	rec := s.Record()
	build, _ := srv.Build()
	t.Logf("FACTS: installed Steam build %s (%s), Game Version (SVN) %s", build.Build, build.Date.Format("2006-01-02"), s.State().Version)
	programFacts(t, srv.Program())
	logText := readShared(t, rec.LogPath)
	if !strings.Contains(logText, "'HostOnline' (up)") {
		t.Errorf("the server's log doesn't say HostOnline: the online event came from the query port, so the app can't read or recognise its log")
	}
	if f, err := os.Open(rec.LogPath); err != nil {
		t.Logf("FACTS: the server's log can't be opened without sharing flags while it runs: %v", err)
	} else {
		f.Close()
		t.Logf("FACTS: the server's log opens plainly while it runs (it shares it for reading)")
	}
	portFacts(t, rec.PID, rec.Ports["query"])

	p := srv.(game.Prober)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	res, err := p.Probe(ctx, rec)
	cancel()
	t.Logf("FACTS: the query port's A2S answer: up %v, players %d, version %q, error %v", res.Up, res.Count, res.Version, err)
	if err != nil || !res.Up || res.Count != 0 {
		t.Errorf("the query port: %+v, %v", res, err)
	}
	if pr, err := platform.OpenProc(rec.PID); err == nil {
		if _, mem, err := pr.Usage(); err == nil {
			t.Logf("FACTS: the server uses %d MB of memory with nobody on", mem>>20)
		}
		pr.Close()
	}

	// Ctrl+C saves the world.
	start = time.Now()
	s.Stop()
	t.Logf("FACTS: Ctrl+C ended the server after %v", time.Since(start).Round(100*time.Millisecond))
	saves := filepath.Join(s.Env().Dirs.Saves, world)
	t.Logf("FACTS: the world's folder after the first stop:\n%s", snapshot(saves))
	first := newest(t, saves)
	tail := lastLines(readShared(t, rec.LogPath), 15)
	t.Logf("FACTS: the log's last lines after Ctrl+C:\n%s", tail)

	cfg, err := os.ReadFile(filepath.Join(s.Env().Dirs.Server, configName))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &doc); err != nil {
		t.Fatalf("the settings file after the stop isn't JSON: %v", err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("FACTS: the settings file's keys after the server stopped: %s", strings.Join(keys, ", "))
	t.Logf("FACTS: its userGroups after the stop: %s", compact(doc["userGroups"]))
	if !bytes.Contains(cfg, []byte("Real check ✓")) {
		t.Errorf("the server name didn't survive the server's rewrite whole (Real check ✓)")
	}
	if !bytes.Contains(doc["userGroups"], []byte(`"Friends"`)) {
		t.Errorf("the server replaced the app's user group")
	}
	logs, _ := filepath.Glob(filepath.Join(filepath.Dir(rec.LogPath), "*", "*"))
	t.Logf("FACTS: the game log folder's subfolders hold %q", logs)

	// The next start loads that world, and a takeover's Ctrl+C saves too.
	time.Sleep(1100 * time.Millisecond)
	s.StartAgain()
	s.WaitEvent("online", "", ready)
	time.Sleep(10 * time.Second) // the world loads after the session is up
	again := readShared(t, s.Record().LogPath)
	if !strings.Contains(again, "deserialization took") {
		t.Errorf("the second start didn't say it loaded the saved world")
	}
	for _, l := range strings.Split(again, "\n") {
		if strings.Contains(l, "deserialization took") || strings.Contains(l, "savedata2-migration") {
			t.Logf("FACTS: loading the world: %s", strings.TrimSpace(l))
		}
	}
	s.Takeover()
	s.Stop()
	if after := newest(t, saves); !after.After(first) {
		t.Errorf("the stop after a takeover didn't save the world (%v, before %v)", after, first)
	}
}

// stdoutFacts runs the server once with its standard output and error
// going to a file, as Output Capture would, to record whether its log
// lines come there too (the app follows its own log file instead).
func stdoutFacts(t *testing.T, g game.Game, shared string) {
	root := t.TempDir()
	env := game.Env{Instance: ID, Data: root, Dirs: game.Dirs{Root: root, Server: filepath.Join(root, "server"),
		Saves: filepath.Join(root, "saves"), Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"), SteamCMD: filepath.Join(root, "steamcmd")}}
	for _, d := range []string{env.Dirs.Saves, env.Dirs.Logs} {
		os.MkdirAll(d, 0o755)
	}
	srv := g.New(env)
	if err := srv.Installer().Install(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	sv := defaultSaved()
	sv.Ports["query"] = 27650
	plan, err := srv.Plan(t.Context(), game.PlanRequest{World: "Stdout", New: true, Saved: sv})
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(root, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	proc, err := platform.Start(platform.StartOptions{Exe: plan.Exe, Args: plan.Args, Dir: plan.Dir, Stdout: out, Stderr: out})
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan int, 1)
	go func() { code, _ := proc.Wait(); exited <- code }()
	defer func() {
		if !proc.Exited() {
			proc.Kill()
		}
	}()
	deadline := time.Now().Add(plan.ReadyWait)
	for !strings.Contains(readShared(t, plan.LogPath), "'HostOnline' (up)") {
		if time.Now().After(deadline) {
			t.Fatalf("no HostOnline in %s within %v", plan.LogPath, plan.ReadyWait)
		}
		time.Sleep(time.Second)
	}
	time.Sleep(5 * time.Second)
	signalNotifyCtrlC()
	if err := proc.Interrupt(); err != nil {
		t.Errorf("Ctrl+C: %v", err)
	}
	select {
	case code := <-exited:
		t.Logf("FACTS: after Ctrl+C the server exited with code %d", code)
	case <-time.After(4 * time.Minute):
		t.Errorf("the server didn't exit within 4 minutes of Ctrl+C")
	}
	b, _ := os.ReadFile(filepath.Join(root, "stdout.txt"))
	t.Logf("FACTS: with its standard output going to a file it wrote %d bytes there; the session line is there: %v; the first lines:\n%s",
		len(b), bytes.Contains(b, []byte("HostOnline")), firstLines(string(b), 15))
}

// signalNotifyCtrlC keeps the Ctrl+C that Windows sends through the
// server's console from also stopping this test, as the app's own
// signal.Notify does for it (gametest does the same for its stops).
func signalNotifyCtrlC() { signal.Notify(make(chan os.Signal, 1), os.Interrupt) }

// installOnce downloads the real server with the adapter's own installer
// (the shared SteamCMD) into a folder of its own, which every part of the
// check then links its server folder to, so the 3.6 GB come down once.
func installOnce(t *testing.T) string {
	root := t.TempDir()
	env := game.Env{Instance: ID, Data: root, Dirs: game.Dirs{Root: root, Server: filepath.Join(root, "server"),
		SteamCMD: filepath.Join(root, "steamcmd")}}
	srv := Game{}.New(env)
	start := time.Now()
	last := -10.0
	err := srv.Installer().Install(t.Context(), false, func(p game.Progress) {
		if p.Percent < 0 || p.Percent >= last+10 {
			t.Logf("install: %s %.0f%%", p.Step, p.Percent)
			last = p.Percent
		}
	})
	if err != nil {
		t.Fatalf("installing the real server: %v", err)
	}
	b, ok := srv.Build()
	if !ok {
		t.Fatal("after the install, Build() says nothing is installed")
	}
	t.Logf("FACTS: SteamCMD installed build %s in %v", b.Build, time.Since(start).Round(time.Second))
	var size int64
	filepath.WalkDir(env.Dirs.Server, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	t.Logf("FACTS: the install takes %d MB", size>>20)
	if readme, err := os.ReadFile(filepath.Join(env.Dirs.Server, "enshrouded_server_readme.txt")); err == nil {
		for _, l := range strings.Split(string(readme), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "Version:") {
				t.Logf("FACTS: the readme says %s", strings.TrimSpace(l))
			}
		}
	}
	return env.Dirs.Server
}

// linked is Enshrouded with an install that is already there: its
// installer links the instance's server folder to it.
type linked struct{ dir string }

func (l linked) Info() game.Info { return Game{}.Info() }

func (l linked) New(env game.Env) game.Server {
	return linkedServer{Game{}.New(env).(*server), l.dir}
}

type linkedServer struct {
	*server
	from string
}

func (l linkedServer) Installer() game.Installer { return linkInstaller{l.env.Dirs.Server, l.from} }

type linkInstaller struct{ to, from string }

// Install makes the instance's server folder a directory junction to the
// shared install (a junction needs no administrator rights, unlike a
// symbolic link).
func (i linkInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if st, err := os.Lstat(i.to); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil
	} else if err == nil && st.Mode().Type()&fs.ModeIrregular != 0 {
		return nil
	}
	if err := os.Remove(i.to); err != nil && !os.IsNotExist(err) {
		return err // not empty: leave it alone
	}
	os.MkdirAll(filepath.Dir(i.to), 0o755)
	out, err := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", i.to, i.from).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J: %v: %s", err, out)
	}
	return nil
}

// programFacts records what kind of program the server is: a console
// program can be stopped with Ctrl+C (spec 2.7).
func programFacts(t *testing.T, program string) {
	t.Helper()
	f, err := pe.Open(program)
	if err != nil {
		t.Errorf("reading %s: %v", program, err)
		return
	}
	defer f.Close()
	if oh, ok := f.OptionalHeader.(*pe.OptionalHeader64); ok {
		t.Logf("FACTS: %s: subsystem %d (3 is a console program, 2 a windowed one)", filepath.Base(program), oh.Subsystem)
		if oh.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
			t.Errorf("the server is no longer a console program: Ctrl+C can't reach it, so the stop chain (stop.go) needs another way")
		}
	}
}

// portFacts records every port the server holds (netstat), to settle
// whether it needs more than its one UDP port.
func portFacts(t *testing.T, pid, query int) {
	t.Helper()
	for _, proto := range []string{"UDP", "TCP"} {
		out, err := exec.Command("netstat", "-ano", "-p", proto).Output()
		if err != nil {
			t.Logf("netstat -p %s: %v", proto, err)
			continue
		}
		var mine []string
		for _, l := range strings.Split(string(out), "\n") {
			f := strings.Fields(l)
			if len(f) >= 4 && f[len(f)-1] == strconv.Itoa(pid) {
				mine = append(mine, strings.Join(f, " "))
			}
		}
		t.Logf("FACTS: the server (query port %d) holds these %s ports: %q", query, proto, mine)
	}
}

// readShared reads a file another program has open for writing.
func readShared(t *testing.T, path string) string {
	t.Helper()
	f, err := platform.OpenShared(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var b bytes.Buffer
	b.ReadFrom(f)
	return b.String()
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func firstLines(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, "\n")
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	json.Compact(&b, raw)
	return b.String()
}

// newest is the newest change to any file in dir.
func newest(t *testing.T, dir string) time.Time {
	t.Helper()
	var at time.Time
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && info.ModTime().After(at) {
				at = info.ModTime()
			}
		}
		return nil
	})
	if at.IsZero() {
		t.Fatalf("the world's folder %s has no files", dir)
	}
	return at
}

// ownGameFolder is where the owner's own Enshrouded keeps its saves.
func ownGameFolder() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Saved Games", "Enshrouded")
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
