package manager

// Tests of what R3 adds to the manager's core (stream M3, spec 9.3): the
// list of games (servers.json) and Valheim-only safe mode, a hidden
// Valheim, the other games' files and how their servers are launched and
// followed (their output, console, probe), and that New and Status stay
// quick with every game added.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/game/gametest/demo"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// demoGame is the gametest demo (cmd/fakeserver's made-up game) as a game
// this version runs: its own stage is hidden, which would make it foreign.
// With gameLog it writes its own log at <server>/game.log (Output GameLog)
// instead of to the file the app gives it.
type demoGame struct {
	demo.Game
	gameLog bool
}

func (g demoGame) Info() game.Info {
	in := g.Game.Info()
	in.Stage = game.Preview
	return in
}

func (g demoGame) New(env game.Env) game.Server {
	srv := g.Game.New(env)
	if g.gameLog {
		return gameLogServer{Server: srv, dir: env.Dirs.Server}
	}
	return srv
}

// gameLogServer is a demo server whose log is its own file.
type gameLogServer struct {
	game.Server
	dir string
}

func (s gameLogServer) Plan(ctx ctxT, req game.PlanRequest) (game.Plan, error) {
	p, err := s.Server.Plan(ctx, req)
	p.Output, p.LogPath = game.GameLog, filepath.Join(s.dir, "game.log")
	return p, err
}

var portClaims struct {
	sync.Mutex
	next int
}

// freeGamePorts finds n ports in a row, from base, that are free for TCP
// and UDP on 127.0.0.1, where the stand-in listens. It keeps to 32100-32767,
// which no other test helper uses (gametest has 20000-29999, the stand-in's
// own tests 30000-31999) and the system doesn't hand out for outgoing
// connections, so nothing takes them meanwhile.
func freeGamePorts(t *testing.T, n int) int {
	t.Helper()
	portClaims.Lock()
	defer portClaims.Unlock()
	if portClaims.next == 0 {
		portClaims.next = 32100 + int(time.Now().UnixNano()%300)
	}
	for tries := 0; tries < 600; tries++ {
		base := portClaims.next
		portClaims.next += n
		if portClaims.next+n > 32767 {
			portClaims.next = 32100
		}
		ok := true
		for p := base; p < base+n && ok; p++ {
			l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				ok = false
				break
			}
			u, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", p))
			l.Close()
			if err != nil {
				ok = false
				break
			}
			u.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatal("no free ports for a fake game")
	return 0
}

// demoEntry is a servers.json entry for a demo game on free ports.
func demoEntry(t *testing.T, id string, opts ...func(*servers.Instance)) servers.Instance {
	base := freeGamePorts(t, 2)
	in := servers.Instance{ID: id, Game: id, Added: time.Now().UTC(), World: "Home",
		Settings: game.Values{"serverName": "Demo " + id}, Ports: map[string]int{"game": base, "rest": base + 1}}
	for _, o := range opts {
		o(&in)
	}
	return in
}

// writeServers writes servers.json into data as a newer build would have.
func writeServers(t *testing.T, data string, instances ...servers.Instance) {
	t.Helper()
	f := servers.New(game.Legacy().Info().ID, time.Now())
	f.Instances = append(f.Instances, instances...)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, servers.FileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeGames sets the options for running demo games with the stand-in:
// script is what its players do.
func fakeGames(t *testing.T, script string, games ...game.Game) func(*Options) {
	prog := gametest.BuildFakeserver(t)
	return func(o *Options) {
		o.Games = games
		o.Fake = &game.Fake{Program: prog, Script: script}
	}
}

// startGames makes a manager for data with servers.json already there,
// starts it, and at the end forces every game it runs to stop before it is
// closed, so no stand-in is left writing into the test's folder.
func startGames(t *testing.T, data string, change ...func(*Options)) *testEnv {
	t.Helper()
	env := &testEnv{data: data}
	env.m = newTestManager(t, data, env, change...)
	env.m.Start()
	t.Cleanup(func() { stopEverything(t, env) })
	return env
}

func stopEverything(t *testing.T, env *testEnv) {
	m := env.m
	m.eachInstance(func(inst *instance) {
		if m.forceStopGame(inst) == nil {
			waitFor(t, inst.id+" to exit", 20*time.Second, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return inst.run == nil
			})
		}
	})
	m.Close()
}

// gameOf is one game's part of Status; ok is false when Status has none.
func gameOf(m *Manager, id string) (GameStatus, bool) {
	for _, g := range m.Status().Games {
		if g.ID == id {
			return g, true
		}
	}
	return GameStatus{}, false
}

// waitInstJob waits for a job of the instance newer than after to finish.
func waitInstJob(t *testing.T, m *Manager, id string, after int) Job {
	t.Helper()
	inst, err := m.instanceByID(id)
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	waitFor(t, id+"'s job to finish", 90*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		if j := inst.job; j != nil && j.ID > after && j.Finished != nil {
			job = *j
			return true
		}
		return false
	})
	return job
}

// installGame installs a fake game.
func installGame(t *testing.T, m *Manager, id string) {
	t.Helper()
	before := currentJobID(m)
	if err := m.InstallGame(id, false); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m, id, before); j.Error != "" {
		t.Fatalf("installing %s: %s", id, j.Error)
	}
}

// startDemo installs and starts a fake game and waits for it to be online.
func startDemo(t *testing.T, m *Manager, id string) {
	t.Helper()
	installGame(t, m, id)
	if g, _ := gameOf(m, id); !g.Installed || g.Build == nil {
		t.Fatalf("after the install job: %+v", g)
	}
	before := currentJobID(m)
	if err := m.StartGame(id, StartOptions{Memory: "anyway", Source: SourcePanel}); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m, id, before); j.Error != "" {
		t.Fatalf("starting %s: %s", id, j.Error)
	}
	if g, _ := gameOf(m, id); g.State != StateOnline {
		t.Fatalf("%s is %s after its start", id, g.State)
	}
}

func runOf(m *Manager, id string) *serverRun {
	inst, _ := m.instanceByID(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	return inst.run
}

func hasGameActivity(m *Manager, id, kind, text string) bool {
	acts, _ := m.Activity(0, 0)
	for _, a := range acts {
		if a.Game == id && a.Kind == kind && strings.Contains(a.Text, text) {
			return true
		}
	}
	return false
}

// --- servers.json and safe mode ---------------------------------------------

// The first start of R3 makes servers.json with only Valheim, starred, and
// leaves Valheim's files alone (spec 3.4).
func TestServersCreated(t *testing.T) {
	data := t.TempDir()
	env := &testEnv{data: data}
	m := newTestManager(t, data, env)
	f, st, err := servers.Load(filepath.Join(data, servers.FileName))
	if err != nil || st != servers.OK {
		t.Fatalf("servers.json: %v %v", st, err)
	}
	id := m.legacyID
	if len(f.Instances) != 1 || f.Instances[0].ID != id || !f.Instances[0].Legacy || !slices.Equal(f.App.Stars, []string{id}) {
		t.Fatalf("servers.json = %+v", f)
	}
	s := m.Status()
	if s.SafeMode != "" || !slices.Equal(s.Stars, []string{id}) || len(s.Games) != 1 || !s.Games[0].Starred {
		t.Fatalf("status: safe mode %q, stars %v, games %+v", s.SafeMode, s.Stars, s.Games)
	}
	if s.Sidebar != (SidebarStatus{ActiveOpen: true, InactiveOpen: false}) {
		t.Fatalf("sidebar %+v", s.Sidebar)
	}
}

// Valheim-only safe mode (spec 8.3): a list from a newer version, or one
// that can't be read and whose backup can't either, or EGS_DEV_SAFE_MODE:
// only the legacy game runs, servers.json is never written, the other
// games' run files still keep the computer awake, and the Overview says why.
func TestSafeMode(t *testing.T) {
	newer := `{"schema": 2, "app": {"stars": ["valheim", "minecraft"]}, "instances": [{"id": "minecraft", "game": "minecraft", "added": "2026-12-03T19:12:00Z"}], "future": true}` + "\n"
	cases := []struct {
		name, file, backup, env, why string
	}{
		{"newer", newer, "", "", "newer version"},
		{"damaged", "{not json", "{nor this", "", "can't be read"},
		{"switched on", "", "", "1", "development"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("EGS_DEV_SAFE_MODE", c.env)
			path := filepath.Join(data, servers.FileName)
			if c.file != "" {
				os.WriteFile(path, []byte(c.file), 0o600)
			} else {
				writeServers(t, data, servers.Instance{ID: "minecraft", Game: "minecraft", Added: time.Now().UTC()})
			}
			if c.backup != "" {
				os.WriteFile(path+servers.BackupSuffix, []byte(c.backup), 0o600)
			}
			before, _ := os.ReadFile(path)
			// A game the newer version started, still running.
			pid, started := thisProcess(t)
			writeRun(t, data, "minecraft", game.Record{PID: pid, Started: started, Launched: started, Game: "minecraft", Instance: "minecraft", Name: "Minecraft"})

			env := &testEnv{data: data}
			m := newTestManager(t, data, env)
			st := m.Status()
			if !strings.Contains(st.SafeMode, c.why) {
				t.Fatalf("safe mode %q, want it to say %q", st.SafeMode, c.why)
			}
			if len(m.order) != 1 || len(st.Games) != 1 || st.Games[0].ID != m.legacyID {
				t.Fatalf("instances %v, games %+v: only the legacy game should run", m.order, st.Games)
			}
			if err := m.StartGame("minecraft", StartOptions{Memory: "anyway"}); err == nil {
				t.Fatal("another game started in safe mode")
			}
			if err := m.StartGame(m.legacyID, StartOptions{Memory: "anyway"}); err == nil || !strings.Contains(err.Error(), "isn't installed") {
				t.Fatalf("the legacy game's start in safe mode = %v, want it refused only because it isn't installed", err)
			}
			if err := m.SetSidebar(false, true); err == nil {
				t.Fatal("servers.json was changed in safe mode")
			}
			if err := m.SetStar(m.legacyID, false); err == nil {
				t.Fatal("a star was changed in safe mode")
			}
			m.scanForeign()
			if !slices.Contains(m.Status().Warnings, "Minecraft is running, started by a newer version of the app; this version can't control it.") {
				t.Fatalf("no warning about the other game's server: %q", m.Status().Warnings)
			}
			m.mu.Lock()
			awake := m.wantAwakeLocked(time.Now())
			m.mu.Unlock()
			if !awake {
				t.Fatal("the computer isn't kept awake for the other game's server")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatalf("servers.json changed in safe mode:\n%s\nto\n%s", before, after)
			}
			if c.backup != "" {
				if b, _ := os.ReadFile(path + servers.BackupSuffix); string(b) != c.backup {
					t.Fatal("the backup changed in safe mode")
				}
			}
		})
	}
}

// A damaged list falls back to its backup by itself, with a warning in the
// feed, and isn't safe mode (spec 3.4).
func TestServersDamagedUsesBackup(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, servers.Instance{ID: "minecraft", Game: "minecraft", Added: time.Now().UTC()})
	path := filepath.Join(data, servers.FileName)
	good, _ := os.ReadFile(path)
	os.WriteFile(path+servers.BackupSuffix, good, 0o600)
	os.WriteFile(path, []byte("{damaged"), 0o600)
	env := &testEnv{data: data}
	m := newTestManager(t, data, env)
	if st := m.Status(); st.SafeMode != "" || len(st.Games) != 2 {
		t.Fatalf("safe mode %q, games %+v", st.SafeMode, st.Games)
	}
	acts, _ := m.Activity(0, 0)
	if !slices.ContainsFunc(acts, func(a Activity) bool { return a.Kind == "warning" && strings.Contains(a.Text, "damaged") }) {
		t.Fatalf("no warning about the damaged list: %+v", acts)
	}
}

// I1: removing Valheim from the panel only hides it, and a hidden Valheim
// never runs unseen: nothing starts or installs it (the old routes, the new
// ones, a restart, a wake, start-with-app, the tray), it isn't starred or in
// the sidebar, and its files stay.
func TestHiddenLegacy(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	inst := m.legacy()
	s := m.Settings()
	s.StartWithApp = true
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := m.updateServers(func(f *servers.File) error {
		f.Instance(m.legacyID).Hidden = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := inst.short() + " is removed from the panel. Add it again first."
	jobs := currentJobID(m)
	for what, try := range map[string]func() error{
		"StartServer":   m.StartServer,
		"StartGame":     func() error { return m.StartGame(m.legacyID, StartOptions{Memory: "anyway", Source: SourcePanel}) },
		"a wake":        func() error { return m.startGameWith(inst, StartOptions{Source: SourceWake}) },
		"a restart":     func() error { return m.startGameWith(inst, StartOptions{Source: SourceRestart}) },
		"RestartServer": m.RestartServer, // of a stopped server: a start
		"RestartGame":   func() error { return m.RestartGame(m.legacyID) },
		"InstallServer": func() error { return m.InstallServer(false) },
		"a star":        func() error { return m.SetStar(m.legacyID, true) },
	} {
		if err := try(); err == nil || err.Error() != want {
			t.Errorf("%s while hidden = %v, want %q", what, err, want)
		}
	}
	st := m.Status()
	if len(st.Games) != 0 || len(st.Stars) != 0 {
		t.Fatalf("a hidden legacy game is shown: games %+v, stars %v", st.Games, st.Stars)
	}
	if st.State != StateStopped || currentJobID(m) != jobs {
		t.Fatalf("a refused start or restart ran: state %s, job %+v", st.State, st.Job)
	}
	m.mu.Lock()
	starred := m.isStarredLocked(inst)
	m.mu.Unlock()
	if starred {
		t.Fatal("a hidden legacy game counts as starred")
	}

	// Start-with-app doesn't start it when the app opens again.
	m.Close()
	m2 := newTestManager(t, env.data, env)
	env.m = m2
	m2.Start()
	time.Sleep(time.Second)
	if st := m2.Status(); st.State != StateStopped || st.Job != nil {
		t.Fatalf("a hidden legacy game was started with the app: state %s, job %+v", st.State, st.Job)
	}
	if _, err := os.Stat(m2.legacy().srv.Program()); err != nil {
		t.Fatalf("its server is gone: %v", err)
	}

	// Shown again, it can start.
	if err := m2.updateServers(func(f *servers.File) error {
		f.Instance(m2.legacyID).Hidden = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m2.startChecks(m2.legacy()); err != nil {
		t.Fatalf("shown again: %v", err)
	}
}

// A hidden Valheim whose server runs all the same is shown while it runs,
// so it never runs unseen (I1). The way there: going back to R2 after an
// update (spec 3.6), whose panel knows nothing of hiding and shows Valheim
// again, the owner started it there, and the next R3 build took it over.
// It can be stopped but not restarted, and it leaves the panel again once
// it stops.
func TestHiddenLegacyTakenOver(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("start: %s", job.Error)
	}
	m.Close() // the app closes; the server keeps running
	if err := servers.Update(filepath.Join(env.data, servers.FileName), func(f *servers.File) error {
		f.Instance(m.legacyID).Hidden = true // as it was before the older version ran
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	m2 := newTestManager(t, env.data, env)
	env.m = m2
	m2.Start()
	waitFor(t, "the takeover", 20*time.Second, func() bool { return m2.Status().State == StateOnline })
	st := m2.Status()
	g, ok := gameOf(m2, m2.legacyID)
	if !ok || g.State != StateOnline || st.Online != 1 {
		t.Fatalf("a hidden Valheim that runs isn't shown: %+v (online %d)", st.Games, st.Online)
	}
	if g.Starred || len(st.Stars) != 0 {
		t.Fatalf("a hidden Valheim that runs is starred: %v", st.Stars)
	}
	if !slices.ContainsFunc(g.Warnings, func(w string) bool { return strings.Contains(w, "removed from the panel") }) {
		t.Fatalf("warnings %q don't say why it shows", g.Warnings)
	}
	want := hiddenError(m2.legacy()).Error()
	jobs := currentJobID(m2)
	for what, try := range map[string]func() error{
		"RestartServer": m2.RestartServer,
		"RestartGame":   func() error { return m2.RestartGame(m2.legacyID) },
	} {
		if err := try(); err == nil || err.Error() != want {
			t.Errorf("%s of a hidden Valheim that runs = %v, want %q", what, err, want)
		}
	}
	if st := m2.Status(); st.State != StateOnline || currentJobID(m2) != jobs {
		t.Fatalf("a refused restart did something: state %s, job %+v", st.State, st.Job)
	}
	if err := m2.StopServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m2, currentJobID(m2)); job.Error != "" {
		t.Fatalf("stop: %s", job.Error)
	}
	if st := m2.Status(); st.State != StateStopped || len(st.Games) != 0 {
		t.Fatalf("after its stop: state %s, games %+v", st.State, st.Games)
	}
}

// Stars, the sidebar's lists and turning a game off are kept in
// servers.json (spec 3.2, 8.3); a game turned off can't start.
func TestStarsSidebarDisabled(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "demo"))
	env := &testEnv{data: data}
	m := newTestManager(t, data, env, fakeGames(t, "", demoGame{Game: demo.Game{ID: "demo"}}))
	if err := m.SetStar("demo", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetStar(m.legacyID, false); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSidebar(false, true); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !slices.Equal(st.Stars, []string{"demo"}) || st.Sidebar != (SidebarStatus{ActiveOpen: false, InactiveOpen: true}) {
		t.Fatalf("stars %v, sidebar %+v", st.Stars, st.Sidebar)
	}
	f, _, _ := servers.Load(filepath.Join(data, servers.FileName))
	if !slices.Equal(f.App.Stars, []string{"demo"}) || f.App.ActiveListOpen() || !f.App.InactiveListOpen() {
		t.Fatalf("servers.json app = %+v", f.App)
	}
	if err := m.updateServers(func(f *servers.File) error {
		f.App.DisabledGames = []string{"demo"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if g, _ := gameOf(m, "demo"); !g.Disabled {
		t.Fatalf("not shown as turned off: %+v", g)
	}
	if err := m.StartGame("demo", StartOptions{Memory: "anyway"}); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("a game turned off started: %v", err)
	}
	// A restart of a stopped server is a start, so it is refused the same way.
	if err := m.RestartGame("demo"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("a game turned off was restarted: %v", err)
	}
	if g, _ := gameOf(m, "demo"); g.State != StateNotInstalled || g.Job != nil {
		t.Fatalf("a game turned off is %s, job %+v", g.State, g.Job)
	}
}

// A game whose server can't run on this computer (spec 2.10: its data
// came from another computer, say) is refused by a start and by a restart,
// with what it needs.
func TestCantRunHereRefused(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "elsewhere"))
	env := &testEnv{data: data}
	m := newTestManager(t, data, env, func(o *Options) { o.Games = []game.Game{elsewhereGame{demoGame{Game: demo.Game{ID: "elsewhere"}}}} })
	g, ok := gameOf(m, "elsewhere")
	if !ok || g.CanRun != game.CantRun || g.NeedsText != "Needs another computer" {
		t.Fatalf("shown as %+v", g)
	}
	want := "The " + g.Short + " server can't run on this computer. Needs another computer."
	for what, try := range map[string]func() error{
		"StartGame":   func() error { return m.StartGame("elsewhere", StartOptions{Memory: "anyway", Source: SourcePanel}) },
		"a wake":      func() error { return m.StartGame("elsewhere", StartOptions{Source: SourceWake}) },
		"RestartGame": func() error { return m.RestartGame("elsewhere") },
	} {
		if err := try(); err == nil || err.Error() != want {
			t.Errorf("%s = %v, want %q", what, err, want)
		}
	}
	if g, _ := gameOf(m, "elsewhere"); g.Job != nil {
		t.Fatalf("a refused start ran a job: %+v", g.Job)
	}
}

// elsewhereGame is a demo game whose server runs only on a system no test
// runs on.
type elsewhereGame struct{ demoGame }

func (g elsewhereGame) Info() game.Info {
	in := g.demoGame.Info()
	in.OS = map[string]game.Level{"plan9": game.Runs}
	in.NeedsText = map[string]string{runtime.GOOS: "Needs another computer"}
	return in
}

// --- another game's files and run -------------------------------------------

// I8: another game's players, records, logs and worlds go into its own
// folder, never into Valheim's places; its run file is private and goes
// when it stops; its feed entries carry its id.
func TestNoForeignFilesInLegacyPlaces(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "demo"))
	legacyPlaces := []string{"logs", "backups", "uploads", "players.json", "records.json", "lastplayed.json", "stats.json", "history.json"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, p := range legacyPlaces {
			path := filepath.Join(data, p)
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				entries, _ := os.ReadDir(path)
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				out[p] = strings.Join(names, ",")
			} else if b, err := os.ReadFile(path); err == nil {
				out[p] = string(b)
			}
		}
		return out
	}
	env := startGames(t, data, fakeGames(t, "join:Alex@0.5,death:Alex@1,leave:Alex@1.5", demoGame{Game: demo.Game{ID: "demo"}}))
	m := env.m
	before := snapshot()

	startDemo(t, m, "demo")
	if fi, err := os.Stat(filepath.Join(data, "games", "demo", "run.json")); err != nil || !isWindows && fi.Mode().Perm() != 0o600 {
		t.Fatalf("the run file: %v %v", fi, err)
	}
	waitFor(t, "Alex to join, die and leave", 20*time.Second, func() bool { return hasGameActivity(m, "demo", "leave", "Alex") })
	if !hasGameActivity(m, "demo", "join", "Alex") || !hasGameActivity(m, "demo", "death", "Alex") {
		t.Fatal("the join or death isn't in the feed under the game")
	}
	jobs := currentJobID(m)
	if err := m.StopGame("demo"); err != nil {
		t.Fatal(err)
	}
	// The stop job ends once the exit is wrapped up (the records saved).
	if j := waitInstJob(t, m, "demo", jobs); j.Error != "" {
		t.Fatalf("the stop: %+v", j)
	}
	m.saveData()

	for p, was := range before {
		if now := snapshot()[p]; now != was && strings.Contains(now, "Alex") {
			t.Errorf("%s gained the other game's data:\n%s", p, now)
		}
	}
	after := snapshot()
	for _, p := range legacyPlaces {
		if strings.Contains(after[p], "Alex") || strings.Contains(after[p], "demo-") {
			t.Errorf("the legacy %s has the other game's data: %s", p, after[p])
		}
	}
	if logs, _ := filepath.Glob(filepath.Join(data, "logs", "server-*.log")); len(logs) != 0 {
		t.Errorf("logs/server-*.log: %v", logs)
	}
	own := filepath.Join(data, "games", "demo")
	for _, name := range []string{"players.json", "records.json"} {
		if b, err := os.ReadFile(filepath.Join(own, name)); err != nil || !strings.Contains(string(b), "Alex") {
			t.Errorf("games/demo/%s: %q %v", name, b, err)
		}
	}
	if logs, _ := filepath.Glob(filepath.Join(own, "logs", "demo-*.log")); len(logs) != 1 {
		t.Errorf("its logs: %v", logs)
	}
	if _, err := os.Stat(filepath.Join(own, "run.json")); !os.IsNotExist(err) {
		t.Errorf("its run file is still there after the stop: %v", err)
	}
	if known, err := m.KnownPlayers(), error(nil); err != nil || len(known) != 0 {
		t.Errorf("the legacy game's players: %+v", known)
	}
}

// A console game (spec 2.11): the app keeps a pipe on its standard input,
// so its stop types its stop line and the app types its save line before
// it exits (Quit, an update). After a takeover by a new copy of the app
// there is no pipe: the console step gives way to Ctrl+C, and the stop
// still saves and ends the server.
func TestConsoleGameSavesOnQuit(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "console"))
	games := fakeGames(t, "", demoGame{Game: demo.Game{ID: "console", Console: true}})
	env := startGames(t, data, games)
	m := env.m
	startDemo(t, m, "console")
	run := runOf(m, "console")
	m.mu.Lock()
	hasPipe, line := run.stdin != nil, run.saveLine
	m.mu.Unlock()
	if !hasPipe || line != "save" {
		t.Fatalf("console pipe %v, save line %q", hasPipe, line)
	}
	// Nobody's join has been seen yet, so someone may be playing: an
	// automatic update waits for it (2.11).
	if g, _ := gameOf(m, "console"); !g.ConsoleInUse {
		t.Fatal("a console game that may be played isn't shown as holding updates back")
	}
	m.mu.Lock()
	waits := len(m.consoleInUseLocked()) == 1
	m.mu.Unlock()
	if !waits {
		t.Fatal("an automatic update wouldn't wait for the console game")
	}
	logPath := run.logFile()
	savesIn := func() int {
		b, _ := os.ReadFile(logPath)
		return bytes.Count(b, []byte("] World saved"))
	}
	before := savesIn()

	m.Close() // Quit: the game keeps running and is told to save first
	if n := savesIn(); n <= before {
		t.Fatalf("the game didn't save when the app closed (%d saves, before %d)", n, before)
	}
	if !hasGameActivity(m, "console", "saved", "before the app closed") {
		t.Fatal("no note that it saved before the app closed")
	}
	pid := run.proc.PID()
	if p, err := platform.OpenProc(pid); err != nil || p.Exited() {
		t.Fatal("the game stopped when the app closed")
	} else {
		p.Close()
	}

	// The next copy of the app takes it over: no console now.
	m2 := newTestManager(t, data, env, games)
	env.m = m2
	m2.Start()
	waitFor(t, "the takeover", 20*time.Second, func() bool {
		r := runOf(m2, "console")
		if r == nil {
			return false
		}
		m2.mu.Lock()
		defer m2.mu.Unlock()
		return !r.replaying && r.ready
	})
	run2 := runOf(m2, "console")
	m2.mu.Lock()
	noPipe := run2.stdin == nil && run2.proc.PID() == pid
	m2.mu.Unlock()
	if !noPipe {
		t.Fatal("the taken-over game has a console pipe, or isn't the same process")
	}
	before = savesIn()
	jobs := currentJobID(m2)
	if err := m2.StopGame("console"); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m2, "console", jobs); j.Error != "" || j.Result != "The world was saved and the server stopped." {
		t.Fatalf("the stop after the takeover: %+v", j)
	}
	b, _ := os.ReadFile(logPath)
	if !bytes.Contains(b, []byte("Ctrl+C received")) || savesIn() <= before {
		t.Fatalf("it wasn't stopped and saved through Ctrl+C:\n%s", b)
	}
}

// A game that writes its own log (Output GameLog, spec 2.4): the app
// follows it through the game rotating it, copies every line into the
// run's own log in the game's logs folder, and never reads the last run's
// lines as this one's.
func TestGameLogSurvivesRotation(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "logger"))
	env := startGames(t, data, fakeGames(t, "", demoGame{Game: demo.Game{ID: "logger"}, gameLog: true}))
	m := env.m
	installGame(t, m, "logger")
	inst, _ := m.instanceByID("logger")
	gameLog := filepath.Join(inst.dirs.Server, "game.log")
	write := func(path, text string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), text)
		f.Close()
	}
	// The last run's log is still there: its lines aren't this run's.
	write(gameLog, "Server ready: players can join \"Old\" on port 1")
	write(gameLog, "Player joined: Ghost")

	jobs := currentJobID(m)
	if err := m.StartGame("logger", StartOptions{Memory: "anyway"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the launch", 20*time.Second, func() bool { return runOf(m, "logger") != nil })
	time.Sleep(time.Second)
	if g, _ := gameOf(m, "logger"); g.State == StateOnline || len(g.Players) > 0 {
		t.Fatalf("the last run's lines were read as this run's: %+v", g)
	}
	write(gameLog, "Server ready: players can join \"Demo\" on port 1")
	if j := waitInstJob(t, m, "logger", jobs); j.Error != "" {
		t.Fatalf("the start: %+v", j)
	}
	write(gameLog, "Player joined: Alex")
	// The game rotates its log right after that line, and goes on in a new one.
	if err := os.Rename(gameLog, filepath.Join(inst.dirs.Server, "game-1.log")); err != nil {
		t.Fatal(err)
	}
	write(gameLog, "Player joined: Bjorn")
	waitFor(t, "both joins", 10*time.Second, func() bool {
		g, _ := gameOf(m, "logger")
		return len(g.Players) == 2
	})
	run := runOf(m, "logger")
	own := run.logFile()
	if filepath.Dir(own) != inst.dirs.Logs || !strings.HasPrefix(filepath.Base(own), "logger-") {
		t.Fatalf("the run's own log is %s", own)
	}
	waitFor(t, "the copy", 5*time.Second, func() bool {
		b, _ := os.ReadFile(own)
		return bytes.Contains(b, []byte("Player joined: Bjorn"))
	})
	b, _ := os.ReadFile(own)
	for _, want := range []string{"players can join \"Demo\"", "Player joined: Alex", "Player joined: Bjorn"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("the copy lacks %q:\n%s", want, b)
		}
	}
	if bytes.Contains(b, []byte("Ghost")) {
		t.Errorf("the copy has the last run's lines:\n%s", b)
	}
	if got := m.CurrentLogPath(); got == own {
		t.Error("the legacy game's log path is another game's")
	}
}

// Start-with-app is per game (spec 2.9): another game's comes from its
// servers.json entry, and nothing starts when the app restarted to update.
func TestStartWithAppPerGame(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "auto", func(in *servers.Instance) { in.StartWithApp = true }), demoEntry(t, "manual"))
	games := fakeGames(t, "", demoGame{Game: demo.Game{ID: "auto"}}, demoGame{Game: demo.Game{ID: "manual"}})
	env := &testEnv{data: data}
	m := newTestManager(t, data, env, games)
	env.m = m
	t.Cleanup(func() { stopEverything(t, env) })
	m.Start()
	installGame(t, m, "auto")
	installGame(t, m, "manual")
	m.Close()

	m = newTestManager(t, data, env, games)
	env.m = m
	m.Start()
	waitFor(t, "the game that starts with the app", 30*time.Second, func() bool {
		g, _ := gameOf(m, "auto")
		return g.State == StateOnline
	})
	if g, _ := gameOf(m, "manual"); g.State != StateStopped {
		t.Fatalf("a game without start-with-app is %s", g.State)
	}
	if st := m.Status(); st.State != StateNotInstalled || st.Job != nil {
		t.Fatalf("the legacy game: %s, job %+v", st.State, st.Job)
	}
}

// Probes (spec 2.6): a game with an API is asked who is on, the answer is
// authoritative and proves its presence; its first join is noted in
// servers.json, which proves presence for games without a probe.
func TestProbeAndPresenceSeen(t *testing.T) {
	oldStarting, oldReady := probeStarting, probeReady
	probeStarting, probeReady = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { probeStarting, probeReady = oldStarting, oldReady })
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "api"), demoEntry(t, "plain"))
	env := startGames(t, data, fakeGames(t, "join:Alex@0.5", demoGame{Game: demo.Game{ID: "api", API: true}}, demoGame{Game: demo.Game{ID: "plain"}}))
	m := env.m
	api, _ := m.instanceByID("api")
	plain, _ := m.instanceByID("plain")
	proven := func(inst *instance) bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.presenceProvenLocked(inst)
	}
	if proven(api) || proven(plain) {
		t.Fatal("presence counts as proven before anything was seen")
	}
	startDemo(t, m, "api")
	waitFor(t, "a probe with a count", 10*time.Second, func() bool { return proven(api) })
	startDemo(t, m, "plain")
	waitFor(t, "the first join", 20*time.Second, func() bool { return proven(plain) })
	f, _, _ := servers.Load(filepath.Join(data, servers.FileName))
	if f.Instance("plain").PresenceSeen.IsZero() {
		t.Fatal("presenceSeen isn't in servers.json")
	}
	waitFor(t, "Alex on the API game", 20*time.Second, func() bool {
		g, _ := gameOf(m, "api")
		return g.Count == 1 && len(g.Players) == 1
	})
}

// A probe that stops answering no longer proves the game's presence
// (2.6): the count it last gave may be 0 while people play, so auto-stop
// and idle sleep mustn't trust it until the probe answers with a count
// again. Nobody joins here, so only the probe can prove it.
func TestProbeStopsAnswering(t *testing.T) {
	oldStarting, oldReady := probeStarting, probeReady
	probeStarting, probeReady = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { probeStarting, probeReady = oldStarting, oldReady })
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "api"))
	var down atomic.Bool
	env := startGames(t, data, fakeGames(t, "", downProbeGame{demoGame{Game: demo.Game{ID: "api", API: true}}, &down}))
	m := env.m
	api, _ := m.instanceByID("api")
	proven := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.presenceProvenLocked(api)
	}
	warned := func() bool {
		g, _ := gameOf(m, "api")
		return slices.ContainsFunc(g.Warnings, func(w string) bool { return strings.Contains(w, "can't reach") })
	}
	startDemo(t, m, "api")
	waitFor(t, "a probe with a count", 10*time.Second, proven)

	down.Store(true)
	waitFor(t, "a failed probe to take the proof back", 5*time.Second, func() bool { return !proven() })
	waitFor(t, "the warning after three failures", 10*time.Second, warned)
	if proven() {
		t.Fatal("presence counts as proven while the probe fails")
	}

	down.Store(false)
	waitFor(t, "the probe to answer again", 5*time.Second, proven)
	waitFor(t, "the warning to go", 5*time.Second, func() bool { return !warned() })
	f, _, _ := servers.Load(filepath.Join(data, servers.FileName))
	if !f.Instance("api").PresenceSeen.IsZero() {
		t.Fatal("presenceSeen was noted, but nobody joined")
	}
}

// downProbeGame is the demo game with an API whose probe can be made to
// stop answering, as when a game's API hangs while its server runs on.
type downProbeGame struct {
	demoGame
	down *atomic.Bool
}

func (g downProbeGame) New(env game.Env) game.Server {
	srv := g.demoGame.New(env)
	return downProbeServer{Server: srv, probe: srv.(game.Prober), down: g.down}
}

type downProbeServer struct {
	game.Server
	probe game.Prober
	down  *atomic.Bool
}

func (s downProbeServer) Probe(ctx ctxT, rec game.Record) (game.ProbeResult, error) {
	if s.down.Load() {
		return game.ProbeResult{Count: -1}, errors.New("the API doesn't answer")
	}
	return s.probe.Probe(ctx, rec)
}

// Status is read all the time by the panel while several games run, their
// settings change and their output comes in (run with -race). The old
// panel's job (Status.job) never shows another game's job (spec 4.2).
func TestStatusWhileGamesRun(t *testing.T) {
	data := t.TempDir()
	ids := []string{"one", "two", "three"}
	var entries []servers.Instance
	var games []game.Game
	for i, id := range ids {
		entries = append(entries, demoEntry(t, id))
		games = append(games, demoGame{Game: demo.Game{ID: id, Console: i == 1, API: i == 2}})
	}
	writeServers(t, data, entries...)
	env := startGames(t, data, fakeGames(t, "join:Alex@0.3,leave:Alex@1", games...))
	m := env.m
	for _, id := range ids {
		startDemo(t, m, id)
	}
	if st := m.Status(); st.Job != nil || st.Online != 3 || len(st.Jobs) != 3 {
		t.Fatalf("job %+v, online %d, jobs %d", st.Job, st.Online, len(st.Jobs))
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var reads atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Status()
				reads.Add(1)
			}
		}
	}()
	for _, id := range ids {
		inst, _ := m.instanceByID(id)
		run := runOf(m, id)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := inst.srv.Store().Save(func(s *game.Saved) error {
					s.Server["serverName"] = fmt.Sprintf("Night %d", i%3)
					return nil
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				m.handleLine(run, fmt.Sprintf("[%s] Players online: %d", time.Now().Format("2006-01-02 15:04:05"), i%2))
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	st := m.Status()
	for _, g := range st.Games {
		if g.ID != m.legacyID && (g.State != StateOnline || !strings.HasPrefix(g.ServerName, "Night") || !g.NeedsRestart) {
			t.Errorf("%s: state %s, name %q, restart needed %v", g.ID, g.State, g.ServerName, g.NeedsRestart)
		}
	}
	if st.Job != nil || reads.Load() == 0 {
		t.Fatalf("job %+v after %d reads", st.Job, reads.Load())
	}

	// Going to sleep stops them all at the same time (spec 2.7), each by
	// its own chain: Ctrl+C, a console line, an API call.
	var insts []*instance
	for _, id := range ids {
		inst, _ := m.instanceByID(id)
		insts = append(insts, inst)
	}
	j := &jobCtl{m: m, job: &Job{Kind: "hibernate"}}
	start := time.Now()
	if errs := m.stopInstances(m.ctx, j, insts); errs != nil {
		t.Fatalf("stopping them all: %v", errs)
	}
	for _, id := range ids {
		if runOf(m, id) != nil {
			t.Errorf("%s still runs", id)
		}
	}
	t.Logf("three games stopped together in %s", time.Since(start).Round(time.Millisecond))
}

// --- quickness with every game added (I5, spec 4.8) ---------------------------

// countingGame wraps a registered game to count the adapter calls Status
// and New must never make: the ones that touch the disk or the network.
type countingGame struct {
	game.Game
	n *atomic.Int32
}

func (g countingGame) New(env game.Env) game.Server { return countingServer{g.Game.New(env), g.n} }

type countingServer struct {
	game.Server
	n *atomic.Int32
}

func (s countingServer) Build() (game.Build, bool)     { s.n.Add(1); return s.Server.Build() }
func (s countingServer) Installer() game.Installer     { s.n.Add(1); return s.Server.Installer() }
func (s countingServer) Worlds() game.Worlds           { s.n.Add(1); return s.Server.Worlds() }
func (s countingServer) ExitHint(tail []string) string { s.n.Add(1); return s.Server.ExitHint(tail) }
func (s countingServer) Plan(ctx ctxT, r game.PlanRequest) (game.Plan, error) {
	s.n.Add(1)
	return s.Server.Plan(ctx, r)
}

// elevenInstances gives data a servers.json with Valheim and the ten other
// games, each game's folder with a run file of a running server (this test
// program) and 30 logs.
func elevenInstances(t *testing.T, data string) {
	t.Helper()
	var entries []servers.Instance
	pid, started := thisProcess(t)
	for i, id := range otherGames {
		entries = append(entries, servers.Instance{ID: id, Game: id, Added: time.Now().UTC(), World: "World",
			Settings: game.Values{"serverName": "Friends"}, Ports: map[string]int{"game": 41000 + 10*i}})
		writeRun(t, data, id, game.Record{PID: pid, Started: started, Name: id, Game: id, Instance: id})
		logs := filepath.Join(data, "games", id, "logs")
		os.MkdirAll(logs, 0o755)
		for i := 0; i < 30; i++ {
			os.WriteFile(filepath.Join(logs, fmt.Sprintf("%s-2026-11-%02d_18-00-00.log", id, i+1)), []byte("a line\n"), 0o644)
		}
	}
	writeServers(t, data, entries...)
}

// I5: with every game added, New reads none of their folders (only
// settings.json, servers.json and the legacy files) and opens no process:
// an older build kills a new one that doesn't answer within a minute.
func TestNewIsQuick(t *testing.T) {
	data := t.TempDir()
	elevenInstances(t, data)
	var took time.Duration
	var m *Manager
	games, procs := countAccess(t, func() {
		start := time.Now()
		env := &testEnv{data: data}
		m = newTestManager(t, data, env)
		took = time.Since(start)
	})
	if games != 0 || procs != 0 {
		t.Fatalf("New read %d of the other games' files and opened %d processes", games, procs)
	}
	if len(m.order) != 11 {
		t.Fatalf("%d instances, want 11", len(m.order))
	}
	if !raceEnabled && took > 2*time.Second {
		t.Fatalf("New took %s", took)
	}
}

// 4.8: with every game added and three running, Status reads nothing of
// theirs from the disk, opens no process, doesn't list the network
// adapters and asks their adapters nothing that would, so the panel's
// polling stays quick.
func TestStatusIsQuick(t *testing.T) {
	data := t.TempDir()
	elevenInstances(t, data)
	var calls atomic.Int32
	var wrapped []game.Game
	for _, id := range otherGames {
		g, ok := game.Get(id)
		if !ok {
			// A game whose stream hasn't registered it yet: the demo stands
			// in, so there are still eleven games this version runs.
			g = demoGame{Game: demo.Game{ID: id}}
		}
		wrapped = append(wrapped, countingGame{g, &calls})
	}
	env := &testEnv{data: data}
	m := newTestManager(t, data, env, func(o *Options) { o.Games = wrapped })
	if len(m.order) != 11 {
		t.Fatalf("%d instances, want 11", len(m.order))
	}
	self, err := platform.OpenProc(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer self.Close()
	for _, id := range otherGames[:3] { // three of them run
		inst, _ := m.instanceByID(id)
		rec := game.Record{PID: self.PID(), Started: self.StartTime(), Launched: time.Now(), World: "World",
			Game: id, Instance: id, Name: inst.short(), Played: game.Played{Mods: map[string]string{}}}
		run := &serverRun{inst: inst, proc: self, rec: rec, done: make(chan struct{}), readyCh: make(chan struct{}), ready: true}
		run.mon = inst.srv.Watch(rec, func(string) string { return "" })
		m.mu.Lock()
		inst.run = run
		m.mu.Unlock()
	}
	m.Status() // reads the LAN addresses once, as the checks loop would have
	calls.Store(0)
	var nets atomic.Int32
	oldLAN := lanAddrs
	lanAddrs = func() []string { nets.Add(1); return oldLAN() }
	t.Cleanup(func() { lanAddrs = oldLAN })
	var took time.Duration
	var st Status
	games, procs := countAccess(t, func() {
		start := time.Now()
		for i := 0; i < 20; i++ {
			st = m.Status()
		}
		took = time.Since(start) / 20
	})
	if games != 0 || procs != 0 || nets.Load() != 0 || calls.Load() != 0 {
		t.Fatalf("Status read %d of the other games' files, opened %d processes, listed the network %d times and made %d adapter calls that touch the disk",
			games, procs, nets.Load(), calls.Load())
	}
	if len(st.Games) != 11 || st.Online != 3 {
		t.Fatalf("%d games, %d online", len(st.Games), st.Online)
	}
	if !raceEnabled && took > 100*time.Millisecond {
		t.Fatalf("Status took %s", took)
	}
	m.mu.Lock()
	for _, id := range otherGames[:3] {
		inst, _ := m.instanceByID(id)
		inst.run = nil
	}
	m.mu.Unlock()
}

// The new panel's per-game methods work for another game through its
// adapter and its servers.json entry: settings (merged, checked, keyed by
// scope), worlds (new, selected, values, backup, restore, delete), its
// players and its log.
func TestGameMethods(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, demoEntry(t, "demo"))
	env := startGames(t, data, fakeGames(t, "join:Alex@0.3,leave:Alex@0.6", demoGame{Game: demo.Game{ID: "demo"}}))
	m := env.m

	v, err := m.GameSettings("demo")
	if err != nil || v.Values.Server["serverName"] != "Demo demo" || len(v.Ports) != 1 || v.Ports[0].Port == 0 || len(v.Schema) == 0 {
		t.Fatalf("settings %+v, %v", v, err)
	}
	if err := m.UpdateGameSettings("demo", GameSettingsChange{Values: game.Values{"nonsense": "1"}}); err == nil {
		t.Fatal("an unknown setting was saved")
	}
	var fe game.FieldErrors
	if err := m.UpdateGameSettings("demo", GameSettingsChange{Values: game.Values{"maxPlayers": "1000"}}); !errors.As(err, &fe) || fe["server.maxPlayers"] == "" {
		t.Fatalf("a bad value: %v, want a field error keyed server.maxPlayers", err)
	}
	off, on := 0, true
	if err := m.UpdateGameSettings("demo", GameSettingsChange{Values: game.Values{"motd": "Hello"}, AutoStopMinutes: &off, StartWithApp: &on}); err != nil {
		t.Fatal(err)
	}
	f, _, _ := servers.Load(filepath.Join(data, servers.FileName))
	in := f.Instance("demo")
	if in.Settings["motd"] != "Hello" || in.Settings["serverName"] != "Demo demo" || in.AutoStopMinutes == nil || *in.AutoStopMinutes != 0 || !in.StartWithApp {
		t.Fatalf("servers.json entry %+v", in)
	}

	if err := m.CreateWorldGame("demo", "Second", game.Values{"seed": "42", "difficulty": "hard"}, false); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateWorldGame("demo", "Third", game.Values{"colour": "red"}, false); err == nil {
		t.Fatal("a world with an unknown setting was made")
	}
	if err := m.SetWorldValuesGame("demo", "Second", game.Values{"difficulty": "easy"}); err != nil {
		t.Fatal(err)
	}
	w, err := m.WorldsGame("demo")
	if err != nil || w.Selected != "Second" || w.Values["Second"]["seed"] != "42" || w.Values["Second"]["difficulty"] != "easy" || len(w.Schema.NewWorld) == 0 {
		t.Fatalf("worlds %+v, %v", w, err)
	}
	startDemo(t, m, "demo") // makes "Second" from its values
	waitFor(t, "Alex to come and go", 20*time.Second, func() bool { return hasGameActivity(m, "demo", "leave", "Alex") })
	if lines, _, err := m.LogsGame("demo", 0, 0); err != nil || len(lines) == 0 {
		t.Fatalf("log %d lines, %v", len(lines), err)
	}
	if p, err := m.CurrentLogPathGame("demo"); err != nil || filepath.Dir(p) != filepath.Join(data, "games", "demo", "logs") {
		t.Fatalf("log path %q, %v", p, err)
	}
	if known, err := m.KnownPlayersGame("demo"); err != nil || len(known) != 1 || known[0].Name != "Alex" {
		t.Fatalf("players %+v, %v", known, err)
	}
	if err := m.RestoreWorldGame("demo", "Second", "x"); err == nil {
		t.Fatal("the running world was restored")
	}
	jobs := currentJobID(m)
	if err := m.BackupWorldGame("demo", "Second"); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m, "demo", jobs); j.Error != "" {
		t.Fatalf("backup: %+v", j)
	}
	jobs = currentJobID(m)
	if err := m.StopGame("demo"); err != nil {
		t.Fatal(err)
	}
	waitInstJob(t, m, "demo", jobs)
	w, _ = m.WorldsGame("demo")
	i := slices.IndexFunc(w.Worlds, func(x worlds.World) bool { return x.Name == "Second" })
	if i < 0 || len(w.Worlds[i].Backups) == 0 {
		t.Fatalf("worlds after the backup: %+v", w.Worlds)
	}
	jobs = currentJobID(m)
	if err := m.RestoreWorldGame("demo", "Second", w.Worlds[i].Backups[0].ID); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m, "demo", jobs); j.Error != "" {
		t.Fatalf("restore: %+v", j)
	}
	if err := m.DeleteWorldGame("demo", "Second"); err == nil {
		t.Fatal("the selected world was deleted")
	}
	if err := m.SelectWorldGame("demo", "Home", false); err != nil {
		t.Fatal(err)
	}
	jobs = currentJobID(m)
	if err := m.DeleteWorldGame("demo", "Second"); err != nil {
		t.Fatal(err)
	}
	if j := waitInstJob(t, m, "demo", jobs); j.Error != "" {
		t.Fatalf("delete: %+v", j)
	}
	if lists, err := m.ListsGame("demo"); err != nil || len(lists) != 0 {
		t.Fatalf("lists %+v, %v", lists, err)
	}
}

// EGS_DEV_FAKE_SERVERS puts every game but the legacy one in fake mode
// (spec 7.1): their adapters get Env.Fake with the stand-in's path, and
// the OS check is ignored, so they run on any system.
func TestFakeServersFromEnv(t *testing.T) {
	data := t.TempDir()
	writeServers(t, data, servers.Instance{ID: "minecraft", Game: "minecraft", Added: time.Now().UTC()})
	t.Setenv("EGS_DEV_FAKE_SERVERS", "fakeserver")
	env := &testEnv{data: data}
	m := newTestManager(t, data, env)
	want, _ := filepath.Abs("fakeserver")
	if m.fake == nil || m.fake.Program != want {
		t.Fatalf("fake mode: %+v", m.fake)
	}
	inst, err := m.instanceByID("minecraft")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	level, _ := m.canRunLocked(inst)
	m.mu.Unlock()
	if level != game.Runs {
		t.Fatalf("a fake game can't run here: %s", level)
	}
	t.Setenv("EGS_DEV_FAKE_SERVERS", "")
	if m2 := newTestManager(t, t.TempDir(), env); m2.fake != nil {
		t.Fatal("fake mode without the variable")
	}
}
