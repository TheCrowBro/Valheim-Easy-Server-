package manager

import (
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/mods/modstest"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

var fakeServer string

func TestMain(m *testing.M) {
	// Like the real app: never let a Ctrl+C meant for the server stop us.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)

	dir, err := os.MkdirTemp("", "fakevalheim")
	if err != nil {
		panic(err)
	}
	fakeServer = filepath.Join(dir, "fakevalheim")
	if runtime.GOOS == "windows" {
		fakeServer += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", fakeServer, "../../cmd/fakevalheim").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building fake server: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type testEnv struct {
	m          *Manager
	data       string
	hibernated atomic.Int32
	slept      atomic.Int32
}

func newTestManager(t *testing.T, data string, env *testEnv, change ...func(*Options)) *Manager {
	t.Helper()
	opt := Options{
		DataDir:         data,
		NoPublicIP:      true,
		SampleInterval:  200 * time.Millisecond,
		RestartDelay:    500 * time.Millisecond,
		HibernateStatus: func() (bool, string) { return true, "" },
		Hibernate: func() error {
			env.hibernated.Add(1)
			return nil
		},
		SleepStatus: func() (bool, string) { return true, "" },
		Sleep: func() error {
			env.slept.Add(1)
			return nil
		},
	}
	for _, c := range change {
		c(&opt)
	}
	m, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	m.opt.Installer = &steamcmd.Fake{Source: fakeServer, InstallDir: m.Paths().Server,
		ExeName: config.ServerExeName(), Duration: 300 * time.Millisecond}
	return m
}

// setup starts a manager for a test; change sets options before it starts.
func setup(t *testing.T, change ...func(*Options)) *testEnv {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	env.m = newTestManager(t, env.data, env, change...)
	s := env.m.Settings()
	s.Port = 20000 + rand.Intn(20000)
	s.SaveIntervalMinutes = 1
	s.UpdateOnStart = false
	if err := env.m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	env.m.Start()
	t.Cleanup(func() {
		if env.m.ForceStop() == nil {
			waitFor(t, "forced stop", 10*time.Second, func() bool { return env.m.Status().State != StateOnline })
		}
		env.m.Close()
	})
	return env
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitJob(t *testing.T, m *Manager, id int) *Job {
	t.Helper()
	var job *Job
	waitFor(t, "job to finish", 60*time.Second, func() bool {
		st := m.Status()
		if st.Job != nil && st.Job.ID >= id && st.Job.Finished != nil {
			job = st.Job
			return true
		}
		return false
	})
	return job
}

func currentJobID(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobSeq
}

func install(t *testing.T, m *Manager) {
	t.Helper()
	if m.Status().State != StateNotInstalled {
		t.Fatalf("state = %s, want not_installed", m.Status().State)
	}
	if err := m.InstallServer(false); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("install failed: %s", job.Error)
	}
	if st := m.Status(); st.State != StateStopped || st.Build == "" {
		t.Fatalf("after install: state=%s build=%q", st.State, st.Build)
	}
}

func hasActivity(m *Manager, kind, name string) bool {
	acts, _ := m.Activity(0, 0)
	for _, a := range acts {
		if a.Kind == kind && (name == "" || a.Name == name) {
			return true
		}
	}
	return false
}

func TestLifecycleWithFakeServer(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3,join:Bjorn@0.6,die:Bjorn@0.9,leave:Astrid@1.5")
	env := setup(t)
	m := env.m
	install(t, m)

	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("start job: %s", job.Error)
	}
	waitFor(t, "join code", 10*time.Second, func() bool { return m.Status().Join.Code != "" })

	waitFor(t, "players to join and leave", 15*time.Second, func() bool {
		st := m.Status()
		return len(st.Players) == 1 && st.Players[0].Name == "Bjorn" && hasActivity(m, "leave", "Astrid")
	})
	if !hasActivity(m, "death", "Bjorn") || !hasActivity(m, "join", "Astrid") {
		t.Fatal("missing join or death activity")
	}
	if p := m.Status().Players[0]; !strings.HasPrefix(p.PlatformID, "Steam_7656") {
		t.Fatalf("platform id = %q", p.PlatformID)
	}
	known := m.KnownPlayers()
	if len(known) != 2 {
		t.Fatalf("known players = %+v", known)
	}
	waitFor(t, "a usage sample", 5*time.Second, func() bool { return m.Status().Live.Mem > 0 })

	// Settings changes while running should ask for a restart.
	s := m.Settings()
	s.ServerName = "Renamed Server"
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if !m.Status().NeedsRestart {
		t.Fatal("NeedsRestart should be true after changing the server name")
	}

	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, currentJobID(m))
	if job.Error != "" {
		t.Fatalf("stop failed: %s", job.Error)
	}
	st := m.Status()
	if st.State != StateStopped || st.LastExit == nil || !st.LastExit.Expected {
		t.Fatalf("after stop: state=%s exit=%+v", st.State, st.LastExit)
	}
	lines, _ := m.Logs(0, 0)
	sawQuit := false
	for _, l := range lines {
		if strings.Contains(l.Text, "OnApplicationQuit") {
			sawQuit = true
		}
	}
	if !sawQuit {
		t.Fatal("the server didn't get the save-and-quit signal")
	}
	if len(st.Players) != 0 {
		t.Fatalf("players after stop: %+v", st.Players)
	}

	w, err := m.Worlds()
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Worlds) != 1 || w.Worlds[0].Name != "Midgard" || w.Worlds[0].Format != "folder" || w.Worlds[0].Seed == "" {
		t.Fatalf("worlds = %+v", w.Worlds)
	}
	if _, err := os.Stat(filepath.Join(env.data, "runtime.json")); err == nil {
		t.Fatal("runtime.json left behind after stop")
	}
}

func TestModsAtStart(t *testing.T) {
	store := httptest.NewServer(modstest.New())
	defer store.Close()
	env := setup(t)
	m := env.m
	m.opt.ModsURL = store.URL
	install(t, m)

	world := m.Settings().World
	if err := m.SetWorldMods(world, []string{"advancedportals"}); err != nil {
		t.Fatal(err)
	}
	// Saving the world's difficulty keeps its mods.
	if err := m.SetWorldOptions(world, config.WorldOptions{Keys: []string{"fire"}}); err != nil {
		t.Fatal(err)
	}
	if o := m.Settings().WorldOptions[world]; len(o.Mods) != 1 || len(o.Keys) != 1 {
		t.Fatalf("world options = %+v", o)
	}
	if st := m.Status(); len(st.Mods) != 1 || st.Mods[0].Name != "Advanced Portals" || st.Mods[0].URL == "" || st.Mods[0].Version != "" {
		t.Fatalf("status mods = %+v", st.Mods)
	}
	if err := m.SetWorldMods(world, []string{"nope"}); err == nil {
		t.Fatal("an unknown mod was accepted")
	}
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("start: %s", job.Error)
	}
	dir := m.Paths().Server
	exists := func(rel ...string) bool {
		_, err := os.Stat(filepath.Join(append([]string{dir}, rel...)...))
		return err == nil
	}
	if !exists("BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "AdvancedPortals.dll") || !exists("winhttp.dll") {
		t.Fatal("the mod wasn't installed and switched on")
	}
	acts, _ := m.Activity(0, 0)
	installed := false
	for _, a := range acts {
		installed = installed || a.Text == "Installed Advanced Portals 1.2.0."
	}
	if !installed {
		t.Fatalf("no activity about the install: %+v", acts)
	}
	if st := m.Status(); len(st.Mods) != 1 || st.Mods[0].Version != "1.2.0" {
		t.Fatalf("status mods after install = %+v", st.Mods)
	}

	// Turning it off while the server runs needs a restart, which switches
	// the mod loader off.
	if err := m.SetWorldMods(world, nil); err != nil {
		t.Fatal(err)
	}
	if !m.Status().NeedsRestart {
		t.Fatal("NeedsRestart should be true after turning a mod off")
	}
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("restart: %s", job.Error)
	}
	if exists("winhttp.dll") || !exists("winhttp.dll.off") {
		t.Fatal("the mod loader is still on")
	}

	// Another mod for the world: only that one is loaded.
	if err := m.SetWorldMods(world, []string{"seasonality"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("restart with seasonality: %s", job.Error)
	}
	if !exists("winhttp.dll") || !exists("BepInEx", "plugins", "RustyMods-Seasonality", "Seasonality.dll") ||
		!exists("BepInEx", "plugins-off", "RandyKnapp-AdvancedPortals") || exists("BepInEx", "plugins", "RandyKnapp-AdvancedPortals") {
		t.Fatal("the world's mods weren't the only ones switched on")
	}
	if st := m.Status(); len(st.Mods) != 1 || st.Mods[0].Name != "Seasonality" || st.Mods[0].Version != "3.8.4" {
		t.Fatalf("status mods = %+v", st.Mods)
	}

	// A mod just for players' own games needs no restart, and the server
	// never downloads it (the stand-in Thunderstore doesn't have it).
	if err := m.SetWorldMods(world, []string{"seasonality", "farlands"}); err != nil {
		t.Fatal(err)
	}
	if m.Status().NeedsRestart {
		t.Fatal("a mod only players use asked for a restart")
	}
	if err := m.SetWorldMods(world, []string{"farlands"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("restart with only FarLands: %s", job.Error)
	}
	if exists("winhttp.dll") || exists("BepInEx", "plugins", "Ivvty-FarLands") {
		t.Fatal("the server loads mods for a world with only a players' mod")
	}
	if st := m.Status(); len(st.Mods) != 1 || st.Mods[0].Name != "FarLands" || st.Mods[0].Players != mods.PlayersOnly {
		t.Fatalf("status mods = %+v", st.Mods)
	}
}

func TestCrashRestartsAutomatically(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_CRASH", "3")
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	waitFor(t, "crash", 15*time.Second, func() bool { return hasActivity(m, "crashed", "") })
	st := m.Status()
	if st.LastExit == nil || st.LastExit.Expected || st.LastExit.Code != 3 {
		t.Fatalf("last exit = %+v", st.LastExit)
	}
	os.Unsetenv("FAKE_VALHEIM_CRASH")
	waitFor(t, "automatic restart", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", 20*time.Second, func() bool { return m.Status().State == StateStopped })
}

func TestTakesOverRunningServer(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Gunnar@2")
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	pid := m.run.proc.PID()
	m.Close()                   // the app closes; the server keeps running
	time.Sleep(3 * time.Second) // Gunnar joins while the app is closed

	m2 := newTestManager(t, env.data, env)
	m2.Start()
	env.m = m2
	waitFor(t, "takeover", 10*time.Second, func() bool {
		st := m2.Status()
		return st.State == StateOnline && m2.run != nil && m2.run.proc.PID() == pid
	})
	if !hasActivity(m2, "info", "") {
		t.Fatal("no activity about taking over")
	}
	waitFor(t, "the player who joined meanwhile to be remembered", 10*time.Second, func() bool {
		for _, p := range m2.KnownPlayers() {
			if p.Name == "Gunnar" && p.PlatformID != "" {
				return true
			}
		}
		return false
	})
	if err := m2.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", 20*time.Second, func() bool { return m2.Status().State == StateStopped })
	if st := m2.Status(); st.LastExit == nil || !st.LastExit.Expected {
		t.Fatalf("exit after takeover = %+v", st.LastExit)
	}
}

func TestHibernateStopsServerFirst(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, currentJobID(m))
	if job.Error != "" {
		t.Fatalf("hibernate: %s", job.Error)
	}
	if env.hibernated.Load() != 1 {
		t.Fatalf("hibernate called %d times", env.hibernated.Load())
	}
	st := m.Status()
	if st.State != StateStopped || st.LastExit == nil || !st.LastExit.Expected {
		t.Fatalf("server not stopped cleanly before hibernating: %s %+v", st.State, st.LastExit)
	}
	m.mu.Lock()
	resume := m.resumeOnWake
	m.mu.Unlock()
	if !resume {
		t.Fatal("should restart the server after waking up")
	}
}

// After waking up, the computer stays awake for a while even with nobody
// on and the shortest idle time: friends need time to join.
func TestWakeKeepsAwake(t *testing.T) {
	env := setup(t)
	m := env.m
	s := m.Settings()
	s.IdleHibernate, s.IdleMinutes = true, 10
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	m.onWake(8 * time.Hour)
	now := time.Now()
	m.mu.Lock()
	deadline := m.idleDeadlineLocked()
	awake, later := m.wantAwakeLocked(now), m.wantAwakeLocked(now.Add(wakeGrace+time.Minute))
	m.mu.Unlock()
	if deadline.Before(now.Add(wakeGrace - time.Second)) {
		t.Fatalf("rests %s after waking", deadline.Sub(now).Round(time.Second))
	}
	if !awake || later {
		t.Fatalf("Windows kept awake: %v now, %v after %s", awake, later, wakeGrace)
	}
	if !hasText(m, "wake", "The computer woke up. It stays awake for at least 15 minutes") {
		t.Fatal("no activity about staying awake")
	}
	// A job in progress keeps it awake too, unless it's going to rest.
	m.mu.Lock()
	m.job = &Job{Kind: "install"}
	busy := m.wantAwakeLocked(now.Add(wakeGrace + time.Minute))
	m.job.Kind = "hibernate"
	resting := m.wantAwakeLocked(now.Add(wakeGrace + time.Minute))
	m.job = nil
	m.mu.Unlock()
	if !busy || resting {
		t.Fatalf("awake for a job %v, while resting %v", busy, resting)
	}
}

func TestSleepInsteadOfHibernate(t *testing.T) {
	env := setup(t)
	m := env.m
	s := m.Settings()
	s.RestMode = config.RestSleep
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.RestMode != config.RestSleep {
		t.Fatalf("status rest mode = %q", st.RestMode)
	}
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, currentJobID(m))
	if job.Error != "" || env.slept.Load() != 1 || env.hibernated.Load() != 0 {
		t.Fatalf("job %+v, slept %d, hibernated %d", job, env.slept.Load(), env.hibernated.Load())
	}
	s.RestMode = "nap"
	if err := m.UpdateSettings(s); err == nil {
		t.Fatal("an unknown rest mode was accepted")
	}
}

func TestWorldSwitchAndCreate(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.CreateWorld("Second World", config.WorldOptions{Preset: "hard"}, false); err != nil {
		t.Fatal(err)
	}
	if got := m.Settings().World; got != "Second World" {
		t.Fatalf("selected = %q", got)
	}
	args, _ := m.Settings().ServerArgs("x", "y")
	if !strings.Contains(strings.Join(args, " "), "-preset hard") {
		t.Fatalf("args = %v", args)
	}
	if err := m.CreateWorld("bad/name", config.WorldOptions{}, false); err == nil {
		t.Fatal("invalid world name accepted")
	}
	if err := m.DeleteWorld("Second World"); err == nil {
		t.Fatal("deleting the selected world should be refused")
	}
}

func TestReplaceRunningWorld(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 30*time.Second, func() bool { return m.Status().State == StateOnline })
	waitJob(t, m, currentJobID(m))

	// A friend brings their own copy of the world the server is playing.
	u, err := m.NewUpload()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(u.Content, "Midgard")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "_main.7.fwl2"), worlds.BuildMeta(worlds.Meta{Version: 37, Name: "Midgard", SeedName: "FromFriend", Seed: 5, UID: 6}, true), 0o644)
	os.WriteFile(filepath.Join(dir, "_main.7.db2"), []byte("friend's world"), 0o644)
	os.WriteFile(filepath.Join(dir, "00_00__0_7.chunk"), []byte("chunk"), 0o644)
	cands, err := m.FinishUpload(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || !cands[0].Exists || !cands[0].InUse {
		t.Fatalf("candidates = %+v", cands)
	}
	id, err := m.ImportUpload(u.ID, []worlds.Choice{{ID: cands[0].ID, Action: "replace"}})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Error != "" || !strings.Contains(job.Result, "restarted") {
		t.Fatalf("job = %+v", job)
	}
	st := m.Status()
	if st.State != StateOnline || st.RunningWorld != "Midgard" {
		t.Fatalf("after replacing: state=%s world=%q", st.State, st.RunningWorld)
	}
	w, err := m.library().Get("Midgard")
	if err != nil || w.Seed != "FromFriend" {
		t.Fatalf("Midgard = %+v, %v", w, err)
	}
	kept := false
	for _, b := range w.Backups {
		if b.Kind == worlds.KindApp && strings.Contains(b.Label, "replac") {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("no backup of the replaced world: %+v", w.Backups)
	}
	if _, err := m.Upload(u.ID); err == nil {
		t.Fatal("the upload should be cleaned up")
	}
}

func TestFirstWorldAddedIsPlayed(t *testing.T) {
	env := setup(t)
	m := env.m
	add := func(name string) *Job {
		t.Helper()
		u, err := m.NewUpload()
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(u.Content, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "_main.1.fwl2"), worlds.BuildMeta(worlds.Meta{Version: 37, Name: name, SeedName: "S", Seed: 1, UID: 2}, true), 0o644)
		os.WriteFile(filepath.Join(dir, "_main.1.db2"), []byte("db"), 0o644)
		cands, err := m.FinishUpload(u.ID)
		if err != nil || len(cands) != 1 || cands[0].Exists {
			t.Fatalf("scan %s: %+v %v", name, cands, err)
		}
		id, err := m.ImportUpload(u.ID, []worlds.Choice{{ID: cands[0].ID, Action: "import"}})
		if err != nil {
			t.Fatal(err)
		}
		return waitJob(t, m, id)
	}
	if job := add("Our World"); job.Error != "" || !strings.Contains(job.Result, "now the world the server plays") {
		t.Fatalf("first world: %+v", job)
	}
	if got := m.Settings().World; got != "Our World" {
		t.Fatalf("selected = %q", got)
	}
	if job := add("Another"); job.Error != "" || strings.Contains(job.Result, "plays") {
		t.Fatalf("second world: %+v", job)
	}
	if got := m.Settings().World; got != "Our World" {
		t.Fatalf("a second world changed the selection to %q", got)
	}
}
