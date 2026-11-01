//go:build realserver

package manager

// The real thing, for checking the app on a new kind of computer (a Mac):
// it installs the actual Valheim server with SteamCMD (about 2 GB), starts
// a world with crossplay, saves and stops it, then plays it with a mod and
// checks BepInEx loaded it. Run it with:
//
//	go test -tags realserver -run TestRealServer -v -timeout 60m ./internal/manager/

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealServer(t *testing.T) {
	data := t.TempDir()
	m, err := New(Options{DataDir: data, NoPublicIP: true, Logger: log.New(os.Stdout, "app: ", log.Ltime)})
	if err != nil {
		t.Fatal(err)
	}
	m.Start()
	defer m.Close()
	defer func() { // whatever happened, the server stops
		if m.Status().State != StateStopped && m.Status().State != StateNotInstalled {
			m.StopServer()
			waitFor(t, "stop at the end", 3*time.Minute, func() bool { return m.Status().State == StateStopped })
		}
	}()

	step := func(what string) { t.Logf("=== %s (%s)", what, time.Now().Format(time.TimeOnly)) }
	step("installing the server with SteamCMD")
	if err := m.InstallServer(false); err != nil {
		t.Fatal(err)
	}
	if job := waitJobFor(t, m, 40*time.Minute); job.Error != "" {
		t.Fatalf("install: %s", job.Error)
	}
	p := m.Paths()
	t.Logf("server at %s", p.ServerExe)

	s := m.Settings()
	s.ServerName, s.World, s.Password = "Valheim Easy Server CI", "CITest", "ci-check-4821"
	s.Crossplay, s.Public, s.UpdateOnStart = true, false, false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}

	start := func(what string) {
		step(what)
		if err := m.StartServer(); err != nil {
			t.Fatal(err)
		}
		job := waitJobFor(t, m, 20*time.Minute)
		if job.Error != "" || m.Status().State != StateOnline {
			dumpLog(t, m)
			t.Fatalf("%s: state %s, %s", what, m.Status().State, job.Error)
		}
		step("online")
	}
	stop := func() {
		step("stopping (the world is saved first)")
		if err := m.StopServer(); err != nil {
			t.Fatal(err)
		}
		if job := waitJobFor(t, m, 5*time.Minute); job.Error != "" {
			dumpLog(t, m)
			t.Fatalf("stop: %s", job.Error)
		}
		if st := m.Status(); st.State != StateStopped || st.LastExit == nil || !st.LastExit.Expected {
			dumpLog(t, m)
			t.Fatalf("after stop: %s, %+v", st.State, st.LastExit)
		}
	}

	start("a new world with crossplay")
	deadline := time.Now().Add(4 * time.Minute)
	for m.Status().Join.Code == "" && m.Status().Join.Problem == "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
	}
	join := m.Status().Join
	t.Logf("join code %q, problem %q", join.Code, join.Problem)
	if join.Code == "" {
		t.Errorf("no join code (crossplay): %q", join.Problem)
	}
	stop()
	w, err := m.Worlds()
	if err != nil || len(w.Worlds) != 1 || w.Worlds[0].Name != "CITest" {
		t.Fatalf("worlds after saving: %+v %v", w, err)
	}
	t.Logf("world saved: %+v", w.Worlds[0])

	if err := m.SetWorldMods("CITest", []string{"planteverything"}); err != nil {
		t.Fatal(err)
	}
	start("the same world with a mod (Plant Everything)")
	bep := filepath.Join(p.Server, "BepInEx", "LogOutput.log")
	b, err := os.ReadFile(bep)
	text := string(b)
	t.Logf("BepInEx log (%v):\n%s", err, tail(text, 40))
	if !strings.Contains(text, "Chainloader startup complete") || !strings.Contains(strings.ToLower(text), "planteverything") {
		t.Error("BepInEx didn't load the mod")
	}
	stop()
}

// waitJobFor waits for the current job to finish.
func waitJobFor(t *testing.T, m *Manager, limit time.Duration) *Job {
	t.Helper()
	id := currentJobID(m)
	var job *Job
	last := ""
	waitFor(t, "the job", limit, func() bool {
		st := m.Status()
		if st.Job != nil && st.Job.ID >= id {
			if line := fmt.Sprintf("%s %.0f%%", st.Job.Message, st.Job.Percent); line != last {
				last = line
				t.Logf("  %s", line)
			}
			if st.Job.Finished != nil {
				job = st.Job
				return true
			}
		}
		return false
	})
	return job
}

func dumpLog(t *testing.T, m *Manager) {
	f, err := os.Open(m.CurrentLogPath())
	if err != nil {
		t.Logf("no server log: %v", err)
		return
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	t.Logf("server log (last 80 lines):\n%s", strings.Join(lines[max(0, len(lines)-80):], "\n"))
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
