//go:build realserver

// The real-server check (spec 5.9 and 7.4). The CI matrix runs it on
// Windows from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 90m ./internal/games/palworld/
//
// It downloads the real server with SteamCMD (Steam app 2394010, about
// 4.7 GB, 5.75 GB installed) and plays it through gametest, without the
// manager: install, a new world, online, the REST API's probe, Join, the
// stop through the REST API (saved), a second start on the same world, a
// takeover whose stop still reaches the API, and what Ctrl+C and a taken
// game port do. Lines starting with "FACTS:" settle the facts FACTS.md
// still marks as to be confirmed; copy them there.
//
// With EGS_CI_ADMIN=1 (set only where the runner may install software
// system-wide) it also runs a Repair, which runs Microsoft's Visual C++
// runtime installer for real.

package palworld

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
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
		t.Skip("the app offers Palworld on Windows only, so its real check runs there")
	}
	facts := func(format string, args ...any) { t.Logf("FACTS: "+format, args...) }

	facts("Visual C++ runtime before the install: %s (new enough: %v)", vcRuntimeVersion(), vcRuntimeOK())
	installStart := time.Now()
	s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "RealCheck"})
	srv := s.Server().(*server)
	build, _ := srv.Build()
	facts("installed and started in %v: Steam build %s (%s)", time.Since(installStart).Round(time.Second), build.Build, build.Date.Format("2006-01-02"))
	for _, p := range []string{srv.Program(), filepath.Join(srv.env.Dirs.Server, "PalServer.exe"), srv.defaultSettingsPath(),
		filepath.Join(filepath.Dir(srv.Program()), "steam_appid.txt")} {
		_, err := os.Stat(p)
		facts("%s there: %v", relTo(srv, p), err == nil)
	}
	planned := s.Env().Store.Get().State[statePlanned]
	ready := s.Plan().ReadyWait + 5*time.Minute

	// Online: by the log line or by the REST API, whichever came first.
	started := time.Now()
	s.WaitEvent("online", "", ready)
	st := s.State()
	facts("online %v after the start, first seen by %q; version %q", time.Since(started).Round(time.Second), st.Extra["readyBy"], st.Version)
	facts("the run's log so far (is the console captured, and promptly?):\n%s", head(s.Plan().LogPath, 20))

	// The process the app started is the server itself.
	rec := s.Record()
	facts("process %d is %q", rec.PID, platform.ProcName(rec.PID))

	// The new world appears in the folder the app chose.
	level := filepath.Join(srv.savesDir(), planned, levelFile)
	appeared := time.Now()
	for {
		if _, err := os.Stat(level); err == nil {
			facts("the new world's Level.sav appeared %v after online, in the planned folder %s", time.Since(appeared).Round(time.Second), planned)
			break
		}
		if time.Since(appeared) > 4*time.Minute {
			entries, _ := os.ReadDir(srv.savesDir())
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("no Level.sav in the planned folder %s after 4 minutes; SaveGames/0 holds %q; DedicatedServerName is now %q",
				planned, names, srv.currentWorldChoice())
			break
		}
		time.Sleep(2 * time.Second)
	}
	facts("world folder: %s", listing(filepath.Join(srv.savesDir(), planned)))

	// The REST API.
	c, err := restFor(rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	in, err := c.info(ctx)
	cancel()
	if err != nil {
		t.Errorf("REST info: %v", err)
	}
	facts("REST info = %+v (worldguid is the planned id: %v)", in, strings.EqualFold(in.WorldGUID, planned))
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	res, err := srv.Probe(ctx, rec)
	cancel()
	if err != nil || !res.Up || res.Count != 0 || res.Names == nil {
		t.Errorf("Probe = %+v, %v; want up with nobody on", res, err)
	}
	for _, p := range []struct {
		proto string
		port  int
	}{{"udp", rec.Ports["game"]}, {"tcp", rec.Ports["rest"]}, {"udp", 27015}, {"tcp", rec.Ports["game"]}} {
		inUse, pid := platform.PortInUse(p.proto, p.port)
		facts("PortInUse(%q, %d) = %v, pid %d (the server is %d)", p.proto, p.port, inUse, pid, rec.PID)
	}

	j := srv.Join(game.JoinInput{Rec: &rec, State: &st, Saved: s.Env().Store.Get(), PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}})
	if j.Address != net.JoinHostPort("203.0.113.7", strconv.Itoa(rec.Port)) || len(j.Steps) == 0 {
		t.Errorf("Join = %+v", j)
	}

	// The stop through the REST API saves.
	before := modTime(level)
	time.Sleep(1100 * time.Millisecond)
	stopStart := time.Now()
	s.Stop()
	facts("the REST stop took %v; Level.sav saved by it: %v", time.Since(stopStart).Round(time.Second), modTime(level).After(before))
	facts("the end of the run's log:\n%s", tail(s.Plan().LogPath, 12))
	if !modTime(level).After(before) {
		t.Errorf("the stop didn't save Level.sav")
	}

	// A second start loads the same world.
	folders := worldFolders(srv)
	s.StartAgain()
	s.WaitEvent("online", "", ready)
	if got := srv.currentWorldChoice(); !strings.EqualFold(got, planned) {
		t.Errorf("the second start's DedicatedServerName is %q, not %q", got, planned)
	}
	if after := worldFolders(srv); !slices.Equal(after, folders) {
		t.Errorf("the second start made a new world folder: %q, before %q", after, folders)
	}

	// After a takeover the stop still reaches the API.
	s.Takeover()
	waitUntil(t, time.Minute, func() bool { return s.State().Ready })
	s.Stop()

	// Ctrl+C alone: does it stop the server, and does it save?
	s.StartAgain()
	s.WaitEvent("online", "", ready)
	time.Sleep(45 * time.Second) // past one autosave (every 30 s)
	before = modTime(level)
	rec = s.Record()
	proc, err := platform.OpenProc(rec.PID)
	if err != nil {
		t.Fatal(err)
	}
	signal.Notify(make(chan os.Signal, 1), os.Interrupt) // Windows sends it to this test's console too
	sent := time.Now()
	if err := proc.Interrupt(); err != nil {
		facts("Ctrl+C couldn't be sent: %v", err)
	}
	for !proc.Exited() && time.Since(sent) < interruptWait {
		time.Sleep(500 * time.Millisecond)
	}
	if proc.Exited() {
		code, _ := proc.Wait()
		facts("Ctrl+C stopped the server in %v (exit code %d); Level.sav saved on the way out: %v",
			time.Since(sent).Round(time.Second), code, modTime(level).After(before))
	} else {
		facts("Ctrl+C didn't stop the server within %v; it was killed", interruptWait)
		proc.Kill()
	}
	proc.Close()
	facts("the end of the Ctrl+C run's log:\n%s", tail(s.Plan().LogPath, 12))
	time.Sleep(2 * time.Second) // let gametest see the end

	// The world's backup and restore.
	wl := srv.Worlds()
	b, err := wl.Backup("RealCheck", "real")
	if err != nil {
		t.Fatal(err)
	}
	if err := wl.Restore("RealCheck", b.ID); err != nil {
		t.Errorf("restore: %v", err)
	}

	// A game port another program holds.
	portTaken(t, srv, facts)

	// Repair runs the Visual C++ runtime installer for real.
	if os.Getenv("EGS_CI_ADMIN") == "1" {
		repairStart := time.Now()
		err := srv.Installer().Install(context.Background(), true, func(p game.Progress) {})
		facts("Repair (SteamCMD validate and the Visual C++ runtime installer) took %v: %v; runtime now %s (new enough: %v)",
			time.Since(repairStart).Round(time.Second), err, vcRuntimeVersion(), vcRuntimeOK())
		if err != nil {
			t.Errorf("Repair: %v", err)
		}
	} else {
		t.Log("EGS_CI_ADMIN isn't 1: the Repair, which installs the Visual C++ runtime, wasn't run")
	}
}

// portTaken starts the server by hand while another program holds its game
// port, to see what it says and whether it stops.
func portTaken(t *testing.T, srv *server, facts func(string, ...any)) {
	sv := srv.store.Get()
	plan, err := srv.Plan(context.Background(), game.PlanRequest{World: sv.World, Saved: sv})
	if err != nil {
		t.Errorf("plan for the taken port: %v", err)
		return
	}
	hold, err := net.ListenPacket("udp", net.JoinHostPort("", strconv.Itoa(plan.Record.Port)))
	if err != nil {
		t.Errorf("holding the game port: %v", err)
		return
	}
	defer hold.Close()
	out := filepath.Join(t.TempDir(), "port-taken.log")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(plan.Exe, plan.Args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = plan.Dir, f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		t.Errorf("starting with the port taken: %v", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		facts("with UDP %d taken the server ended by itself: %v", plan.Record.Port, err)
	case <-time.After(3 * time.Minute):
		facts("with UDP %d taken the server was still running after 3 minutes; it was killed", plan.Record.Port)
		cmd.Process.Kill()
		<-done
	}
	f.Close()
	facts("its log:\n%s", tail(out, 15))
	facts("ExitHint for it: %q", srv.ExitHint(strings.Split(tail(out, 30), "\n")))
}

func relTo(s *server, p string) string {
	r, err := filepath.Rel(s.env.Dirs.Server, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func worldFolders(s *server) []string {
	entries, _ := os.ReadDir(s.savesDir())
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func listing(dir string) string {
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if info, err := d.Info(); err == nil && !d.IsDir() {
			out = append(out, filepath.ToSlash(rel)+" ("+strconv.FormatInt(info.Size(), 10)+" bytes)")
		}
		return nil
	})
	if len(out) > 40 {
		out = append(out[:40], "…")
	}
	return strings.Join(out, ", ")
}

func head(path string, n int) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return "  " + strings.Join(lines, "\n  ")
}

func tail(path string, n int) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func waitUntil(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Errorf("not ready within %v after the takeover", within)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
