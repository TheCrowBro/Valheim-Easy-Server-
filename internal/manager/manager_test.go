package manager

import (
	"fmt"
	"math/rand"
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
	"valheim-easy-server/internal/steamcmd"
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
}

func newTestManager(t *testing.T, data string, env *testEnv) *Manager {
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
	}
	m, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	m.opt.Installer = &steamcmd.Fake{Source: fakeServer, InstallDir: m.Paths().Server,
		ExeName: config.ServerExeName(), Duration: 300 * time.Millisecond}
	return m
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	env.m = newTestManager(t, env.data, env)
	s := env.m.Settings()
	s.Port = 20000 + rand.Intn(20000)
	s.SaveIntervalMinutes = 1
	s.UpdateOnStart = false
	if err := env.m.UpdateSettings(s, true); err != nil {
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
	if err := m.UpdateSettings(s, false); err != nil {
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
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	pid := m.run.proc.PID()
	m.Close() // the app closes; the server keeps running

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
