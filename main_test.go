package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/manager"
)

// TestMain lets the test program stand in for an updated copy of the app.
func TestMain(m *testing.M) {
	switch os.Getenv("VES_TEST_APP") {
	case "":
		os.Exit(m.Run())
	case "starts": // opens its control panel, like the app
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		b, _ := json.Marshal(instanceInfo{Port: ln.Addr().(*net.TCPAddr).Port})
		os.WriteFile(filepath.Join(os.Getenv("VES_TEST_DATA"), ".instance.json"), b, 0o644)
		http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "{}")
		}))
	case "closes":
		os.Exit(3)
	case "hangs":
		time.Sleep(time.Minute)
	}
}

func TestRelaunchArgs(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want []string
	}{
		{nil, []string{"--background", "--port=8456"}},
		{[]string{"--data", `C:\Valheim`}, []string{"--data", `C:\Valheim`, "--background", "--port=8456"}},
		{[]string{"--background", "--port=8456"}, []string{"--background", "--port=8456"}},
		{[]string{"-no-window", "--port", "9000"}, []string{"-no-window", "--port=8456"}},
		{[]string{"--data", "port", "-background=false"}, []string{"--data", "port", "-background=false", "--background", "--port=8456"}},
	} {
		if got := relaunchArgs(c.in, 8456); !reflect.DeepEqual(got, c.want) {
			t.Errorf("relaunchArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoopbackURL(t *testing.T) {
	for s, want := range map[string]bool{
		"http://127.0.0.1:8460":            true,
		"http://localhost:18500/x":         true,
		"http://[::1]:8460":                true,
		"https://discord.com/api/webhooks": false,
		"http://127.0.0.1.evil.example":    false,
		"http://user@127.0.0.1:8460":       false,
		"file:///C:/x":                     false,
	} {
		if got := loopbackURL(s); got != want {
			t.Errorf("loopbackURL(%q) = %v", s, got)
		}
	}
}

func TestWaitStarted(t *testing.T) {
	for _, c := range []struct {
		mode string
		ok   bool
	}{{"starts", true}, {"closes", false}, {"hangs", false}} {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "VES_TEST_APP="+c.mode, "VES_TEST_DATA="+dir)
		err := waitStarted(cmd, dir, 3*time.Second)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.mode, err)
		}
		if c.ok {
			cmd.Process.Kill()
		}
	}
}

func TestStartUpdatedGoesBack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	for _, path := range []string{exe, exe + ".old"} {
		copyFile(t, os.Args[0], path)
	}
	t.Setenv("VES_TEST_APP", "closes") // the update, and then the previous version too
	var logs strings.Builder
	startUpdated(exe, nil, dir, log.New(&logs, "", 0))
	if _, err := os.Stat(exe + ".old"); err == nil || !strings.Contains(logs.String(), "going back to the previous version") {
		t.Fatalf("the previous version wasn't put back:\n%s", logs.String())
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
}

// fakeStopper is a server that may be busy with a job for a while.
type fakeStopper struct {
	mu       sync.Mutex
	state    manager.State
	job      *manager.Job // in progress
	busyFor  int          // how many more stop requests find the job still running
	updating bool         // the app is updating, so no job can start
	stops    int
	cancels  int
}

func (f *fakeStopper) StopServer() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job != nil && !f.job.Cancellable {
		if f.busyFor > 0 {
			f.busyFor--
			return &manager.BusyError{Title: strings.ToLower(f.job.Title)}
		}
		f.job = nil // it got to waiting for the world to load, which Stop ends
	}
	if f.state == manager.StateStopped {
		return errors.New("The server isn't running.")
	}
	if f.updating {
		return &manager.BusyError{Title: "updating the app"}
	}
	f.stops++
	f.state = manager.StateStopped
	return nil
}

func (f *fakeStopper) CancelJob() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job != nil && f.job.Cancellable {
		f.cancels++
		f.job = nil
	}
	return nil
}

func (f *fakeStopper) Status() manager.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return manager.Status{State: f.state, Job: f.job, App: manager.AppInfo{Updating: f.updating}}
}

// Stopping the server before the app closes waits for a job in progress,
// such as a restart, instead of leaving the server running.
func TestStopAndWait(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	restart := &manager.Job{Kind: "restart", Title: "Restarting the server"}

	f := &fakeStopper{state: manager.StateStarting, job: restart, busyFor: 3}
	if err := stopAndWait(f, logger); err != nil || f.stops != 1 {
		t.Fatalf("during a restart: %v, stopped %d times", err, f.stops)
	}

	f = &fakeStopper{state: manager.StateStopped}
	if err := stopAndWait(f, logger); err != nil || f.stops != 0 {
		t.Fatalf("already stopped: %v, stopped %d times", err, f.stops)
	}

	// Getting ready to start the server is cancelled.
	f = &fakeStopper{state: manager.StateStopped, job: &manager.Job{Kind: "start", Cancellable: true}}
	if err := stopAndWait(f, logger); err != nil || f.cancels != 1 || f.stops != 0 {
		t.Fatalf("while getting ready to start: %v, cancelled %d, stopped %d times", err, f.cancels, f.stops)
	}

	// While the app updates, nothing can stop it: that's said, not waited
	// for.
	f = &fakeStopper{state: manager.StateOnline, updating: true}
	start := time.Now()
	if err := stopAndWait(f, logger); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("while updating: %v after %s", err, time.Since(start))
	}
}

// A copy run straight from a zip, which Windows unpacks into the temp
// folder, keeps its files elsewhere: they'd be cleaned up with the temp
// folder. %TEMP% may name the folder differently from the program's own
// path, with a short 8.3 name such as JOHNSM~1 (a symbolic link here).
func TestDataDirNotInTemp(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("a Mac app always keeps its files in Application Support")
	}
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "TEMPOR~1")
	if err := os.Symlink(temp, other); err != nil {
		t.Skip("can't make a symbolic link here:", err)
	}
	cfg := t.TempDir()
	for _, name := range []string{"XDG_CONFIG_HOME", "AppData", "LOCALAPPDATA"} {
		t.Setenv(name, cfg)
	}
	setTemp := func(dir string) {
		for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
			t.Setenv(name, dir)
		}
	}
	unzipped := filepath.Join(temp, "Temp1_ValheimEasyServer-windows.zip")
	os.MkdirAll(unzipped, 0o755)
	setTemp(other)
	if got, err := resolveDataDir("", filepath.Join(unzipped, "ValheimEasyServer.exe")); err != nil || got != filepath.Join(cfg, "ValheimEasyServer") {
		t.Fatalf("run from the temp folder: %q, %v", got, err)
	}

	// A folder next to the temp folder isn't in it.
	beside := temp + "orary"
	os.MkdirAll(beside, 0o755)
	setTemp(temp)
	if got, err := resolveDataDir("", filepath.Join(beside, "ValheimEasyServer.exe")); err != nil || got != beside {
		t.Fatalf("run from a folder next to the temp folder: %q, %v", got, err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
}
