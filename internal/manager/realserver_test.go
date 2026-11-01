//go:build realserver

package manager

// The real thing, for checking the app on Windows and on a Mac: it installs
// the actual Valheim server with SteamCMD (about 2 GB), makes a new world
// with crossplay and stops it as soon as it's online (the world must be
// saved), plays it with a mod and checks BepInEx loaded it, then plays it
// without crossplay or mods. Run it with:
//
//	go test -tags realserver -run TestRealServer -v -timeout 75m ./internal/manager/

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
	s.ServerName, s.Password = "Valheim Easy Server CI", "ci-check-4821" // the world is Midgard, the first one
	s.Crossplay, s.Public, s.UpdateOnStart = true, false, false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}

	// Online means the world is loaded and the server is open for players,
	// which it says in its log: opened is that line.
	start := func(what, opened string) {
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
		if b, _ := os.ReadFile(m.CurrentLogPath()); !strings.Contains(string(b), opened) {
			dumpLog(t, m)
			t.Fatalf("online before the server said %q", opened)
		}
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

	// The join code comes once the world is up and friends can join.
	waitCode := func() {
		deadline := time.Now().Add(4 * time.Minute)
		for m.Status().Join.Code == "" && m.Status().Join.Problem == "" && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
		}
		join := m.Status().Join
		t.Logf("join code %q, problem %q", join.Code, join.Problem)
		if join.Code == "" {
			t.Errorf("no join code (crossplay): %q", join.Problem)
		}
	}
	// Stopped as soon as it's online, a new world is saved: it was made
	// before the server said it was online.
	start("a new world with crossplay", "Opened PlayFab server")
	logPath := m.CurrentLogPath()
	stop()
	if b, err := os.ReadFile(logPath); err == nil { // how the server starts and stops, line by line
		t.Logf("the whole server log:\n%s", b)
	}
	w, err := m.Worlds()
	if err != nil || len(w.Worlds) != 1 || w.Worlds[0].Name != "Midgard" || w.Worlds[0].NeverSaved {
		t.Fatalf("worlds after saving: %+v %v", w, err)
	}
	t.Logf("world saved: %+v", w.Worlds[0])

	if err := m.SetWorldMods("Midgard", []string{"planteverything"}); err != nil {
		t.Fatal(err)
	}
	start("the same world with a mod (Plant Everything)", "Opened PlayFab server")
	waitCode()
	bep := filepath.Join(p.Server, "BepInEx", "LogOutput.log")
	b, err := os.ReadFile(bep)
	text := string(b)
	t.Logf("BepInEx log (%v):\n%s", err, tail(text, 40))
	if !strings.Contains(text, "Chainloader startup complete") || !strings.Contains(strings.ToLower(text), "planteverything") {
		t.Error("BepInEx didn't load the mod")
	}
	before := w.Worlds[0].Modified
	stop()
	dumpLog(t, m) // how the server stopped, with BepInEx
	w, err = m.Worlds()
	if err != nil || len(w.Worlds) != 1 || !w.Worlds[0].Modified.After(before) {
		t.Fatalf("the world wasn't saved when the server stopped with mods: %+v %v (before %v)", w.Worlds, err, before)
	}
	t.Logf("saved again with the mod: %v", w.Worlds[0].Modified)

	// Without crossplay or mods: Valheim's own server, through Steam only.
	s = m.Settings()
	s.Crossplay = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorldMods("Midgard", nil); err != nil {
		t.Fatal(err)
	}
	bepInfo, _ := os.Stat(bep)
	before = w.Worlds[0].Modified
	start("the same world without crossplay or mods", "Opened Steam server")
	if st := m.Status(); st.Join.Code != "" {
		t.Errorf("a join code without crossplay: %q", st.Join.Code)
	}
	stop()
	dumpLog(t, m)
	if now, err := os.Stat(bep); err == nil && bepInfo != nil && !now.ModTime().Equal(bepInfo.ModTime()) {
		t.Error("BepInEx was loaded for a world without mods")
	}
	w, err = m.Worlds()
	if err != nil || len(w.Worlds) != 1 || !w.Worlds[0].Modified.After(before) {
		t.Fatalf("the world wasn't saved when the server stopped without crossplay: %+v %v (before %v)", w.Worlds, err, before)
	}
	t.Logf("saved again without crossplay: %v", w.Worlds[0].Modified)
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
