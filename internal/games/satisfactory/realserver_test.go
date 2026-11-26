//go:build realserver

// The real-server check (spec 5.10 and 7.4). It runs on demand only (the
// download is 3.6 GB and unpacks to 15.5 GB), on Windows, from
// realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 120m ./internal/games/satisfactory/
//
// It downloads the real server with SteamCMD (Steam app 1690800) and plays
// it through gametest, without the manager: install, the app's setup
// through the HTTPS API (claim, friends' password, a new world, saved),
// online, the probe, Join, the stop through the API (saved), a second
// start that loads the same world by itself, a takeover whose stop still
// reaches the API, what Ctrl+C does, and what a taken game port does.
// Lines starting with "FACTS:" settle what FACTS.md still marks as to be
// confirmed; copy them there.
//
// The server keeps its saves in this Windows user's
// %LocalAppData%\FactoryGame\Saved\SaveGames; the check uses world names
// of its own and removes them, and its ServerSettings file, at the end.

package satisfactory

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
		t.Skip("the app offers Satisfactory on Windows only, so its real check runs there")
	}
	facts := func(format string, args ...any) { t.Logf("FACTS: "+format, args...) }
	facts("free disk space where the test installs: %s", freeSpace(os.TempDir()))

	const world = "EGS Real Check"
	installStart := time.Now()
	s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: world,
		NewWorld: game.Values{"startingLocation": "Grass Fields"},
		Change:   func(sv *game.Saved) { sv.Server["password"] = "egs-check" }})
	srv := s.Server().(*server)
	rec := s.Record()
	t.Cleanup(func() { cleanUpSaves(t, srv, rec) })
	build, _ := srv.Build()
	facts("installed and started in %v: Steam build %s (%s)", time.Since(installStart).Round(time.Second), build.Build, build.Date.Format("2006-01-02"))
	win := filepath.Dir(srv.Program())
	for _, p := range []string{srv.Program(), filepath.Join(srv.env.Dirs.Server, "FactoryServer.exe"),
		filepath.Join(win, "steamclient64.dll"), filepath.Join(win, "vcruntime140.dll"), filepath.Join(win, "msvcp140.dll"),
		filepath.Join(srv.env.Dirs.Server, "Engine", "Extras", "Redist", "en-us", "UEPrereqSetup_x64.exe"),
		filepath.Join(srv.env.Dirs.Server, "CommunityResources", "DedicatedServerAPIDocs.md")} {
		_, err := os.Stat(p)
		facts("%s there: %v", relTo(srv, p), err == nil)
	}

	// Online: the setup claimed the server, made the world and saved it.
	ready := s.Plan().ReadyWait + 5*time.Minute
	started := time.Now()
	s.WaitEvent("online", "", ready)
	st := s.State()
	state := s.Env().Store.Get().State
	facts("online %v after the start; version %q; build %q", time.Since(started).Round(time.Second), st.Version, st.Extra[extraBuild])
	facts("log fingerprint %q; pinned key %q; equal: %v", st.Extra[extraFingerprint], state[stateCert], st.Extra[extraFingerprint] == state[stateCert])
	facts("the log is where the plan follows it (%s): %v", relTo(srv, s.Plan().LogPath), fileThere(s.Plan().LogPath))
	facts("ServerSettings file %s there: %v", srv.settingsFile(rec.Ports["game"]), fileThere(srv.settingsFile(rec.Ports["game"])))
	facts("the saves folder %s holds: %s", srv.savesDir(), listing(srv.savesDir()))
	facts("process %d is %q", rec.PID, platform.ProcName(rec.PID))
	for _, p := range []struct {
		proto string
		port  int
	}{{"udp", rec.Ports["game"]}, {"tcp", rec.Ports["game"]}, {"tcp", rec.Ports["reliable"]}} {
		inUse, pid := platform.PortInUse(p.proto, p.port)
		facts("PortInUse(%q, %d) = %v, pid %d (the server is %d)", p.proto, p.port, inUse, pid, rec.PID)
	}
	if !srv.worlds.Exists(world) {
		t.Errorf("the new world isn't in the saves folder after the setup")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var gs serverGameState
	var options, sessions, command json.RawMessage
	err := srv.asAdmin(ctx, rec, func(c *apiClient) (e error) {
		if gs, e = c.queryState(ctx); e != nil {
			return e
		}
		options, _ = c.do(ctx, "GetServerOptions", nil)
		sessions, _ = c.do(ctx, "EnumerateSessions", nil)
		command, e = c.do(ctx, "RunCommand", map[string]string{"command": "server.GenerateAPIToken"})
		if e != nil {
			facts("RunCommand server.GenerateAPIToken: %v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the API as administrator: %v", err)
	}
	facts("QueryServerState = %+v (playerLimit 4 is the planned MaxPlayers: %v)", gs, gs.PlayerLimit == 4)
	facts("GetServerOptions = %s", options)
	facts("EnumerateSessions = %s", sessions)
	facts("RunCommand server.GenerateAPIToken answered %d bytes (shape: %s)", len(command), shape(command))
	if c, err := srv.client(ctx, rec); err == nil {
		_, err = c.passwordlessLogin(ctx, "Client")
		facts("passwordless login as Client with a friends' password set: %v", err)
	}
	if res, err := srv.Probe(ctx, rec); err != nil || !res.Up || res.Count != 0 {
		t.Errorf("Probe = %+v, %v; want up with nobody on", res, err)
	}
	j := srv.Join(game.JoinInput{Rec: &rec, State: &st, Saved: s.Env().Store.Get(), PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}})
	if j.Address != net.JoinHostPort("203.0.113.7", strconv.Itoa(rec.Port)) || len(j.Steps) == 0 {
		t.Errorf("Join = %+v", j)
	}

	// The stop through the API saves.
	save := filepath.Join(srv.savesDir(), world+".sav")
	before := modTime(save)
	time.Sleep(1100 * time.Millisecond)
	stopStart := time.Now()
	s.Stop()
	facts("the API stop took %v; %s.sav saved by it: %v", time.Since(stopStart).Round(time.Second), world, modTime(save).After(before))
	facts("the end of the run's log:\n%s", tail(s.Plan().LogPath, 25))
	if !modTime(save).After(before) {
		t.Errorf("the stop didn't save the world")
	}

	// A second start loads the same world by itself (it is the one to
	// load at start), and the setup has nothing to change.
	s.StartAgain()
	s.WaitEvent("online", "", ready)
	if err := srv.asAdmin(ctx, s.Record(), func(c *apiClient) (e error) { gs, e = c.queryState(ctx); return e }); err != nil || !sameSession(gs.ActiveSessionName, world) {
		t.Errorf("after the second start the server plays %q (%v)", gs.ActiveSessionName, err)
	}
	facts("the second start's log, first lines after the API started:\n%s", grep(s.Plan().LogPath, "LogServer", 15))

	// After a takeover the stop still reaches the API.
	s.Takeover()
	waitUntil(t, 2*time.Minute, func() bool { return s.State().Ready })
	s.Stop()

	// Ctrl+C: does the server save on the way out?
	s.StartAgain()
	s.WaitEvent("online", "", ready)
	time.Sleep(5 * time.Second)
	saves := savesTimes(srv)
	proc, err := platform.OpenProc(s.Record().PID)
	if err != nil {
		t.Fatal(err)
	}
	signal.Notify(make(chan os.Signal, 1), os.Interrupt) // the Ctrl+C must not stop the test too
	interrupted := time.Now()
	if err := proc.Interrupt(); err != nil {
		facts("Ctrl+C couldn't be sent: %v", err)
	}
	exited := waitExited(proc, 2*time.Minute)
	facts("after Ctrl+C the server exited: %v, in %v; saves changed: %v", exited, time.Since(interrupted).Round(time.Second), savesChanged(saves, savesTimes(srv)))
	facts("the log's end after Ctrl+C:\n%s", tail(s.Plan().LogPath, 15))
	if !exited {
		proc.Kill()
	}
	proc.Close()
	time.Sleep(2 * time.Second)

	// A game port another program holds: does the server move to the next
	// port (FACTS.md), and does the Monitor say so?
	takenTCP, err1 := net.Listen("tcp", ":"+strconv.Itoa(rec.Ports["game"]))
	takenUDP, err2 := net.ListenPacket("udp", ":"+strconv.Itoa(rec.Ports["game"]))
	if err1 == nil && err2 == nil {
		defer takenTCP.Close()
		defer takenUDP.Close()
		s.StartAgain()
		moved := waitFor(s, "port_moved", 3*time.Minute)
		facts("with the game port taken, a port_moved event: %v; the log's network lines:\n%s", moved, grep(s.Plan().LogPath, "LogNet", 10))
		if p, err := platform.OpenProc(s.Record().PID); err == nil {
			p.Kill()
			p.Close()
		}
	} else {
		facts("couldn't take the game port for the check: %v, %v", err1, err2)
	}
}

// cleanUpSaves removes what the check left in the Windows user's saves
// folder: its world and the server's settings for its port.
func cleanUpSaves(t *testing.T, srv *server, rec game.Record) {
	if files, err := srv.worlds.files("EGS Real Check"); err == nil {
		for _, f := range files {
			os.Remove(f)
		}
	}
	os.Remove(srv.settingsFile(rec.Ports["game"]))
}

func relTo(srv *server, p string) string {
	if r, err := filepath.Rel(srv.env.Dirs.Server, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

func fileThere(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func listing(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		out = append(out, e.Name()+" ("+strconv.FormatInt(size, 10)+" bytes)")
	}
	return strings.Join(out, ", ")
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "  " + strings.Join(lines, "\n  ")
}

func grep(path, sub string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, sub) && len(out) < n {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return "  " + strings.Join(out, "\n  ")
}

// shape is a JSON answer with its string values cut short, so a token in it
// isn't printed whole.
func shape(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	var cut func(any) any
	cut = func(v any) any {
		switch x := v.(type) {
		case string:
			if len(x) > 12 {
				return x[:6] + "…(" + strconv.Itoa(len(x)) + " characters)"
			}
		case map[string]any:
			for k, e := range x {
				x[k] = cut(e)
			}
		case []any:
			for i, e := range x {
				x[i] = cut(e)
			}
		}
		return v
	}
	b, _ := json.Marshal(cut(v))
	return string(b)
}

func savesTimes(srv *server) map[string]time.Time {
	out := map[string]time.Time{}
	files, _ := srv.worlds.saves()
	for _, f := range files {
		out[f.path] = f.modified
	}
	return out
}

func savesChanged(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return true
	}
	for k, v := range a {
		if !b[k].Equal(v) {
			return true
		}
	}
	return false
}

func waitExited(p *platform.Proc, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.Exited() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return p.Exited()
}

func waitUntil(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Errorf("gave up waiting after %v", within)
			return
		}
		time.Sleep(time.Second)
	}
}

// waitFor says whether the run's log gave an event of kind within the
// time, without failing the test when it didn't: a fresh Monitor reads the
// log so far.
func waitFor(s *gametest.Session, kind string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(s.Plan().LogPath); err == nil {
			m := s.Server().Watch(s.Record(), nil)
			for _, l := range strings.Split(string(b), "\n") {
				for _, e := range m.Line(l, time.Now()).Events {
					if e.Kind == kind {
						return true
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}
