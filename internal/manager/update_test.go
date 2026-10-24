package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/selfupdate"
	"valheim-easy-server/internal/worlds"
)

func TestAppUpdates(t *testing.T) {
	program := "MZ build 5"
	h := sha256.Sum256([]byte(program))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + selfupdate.Repo:
			json.NewEncoder(w).Encode(map[string]any{"id": selfupdate.RepoID, "full_name": selfupdate.Repo})
		case "/repos/" + selfupdate.Repo + "/releases":
			asset := func(name string) map[string]string {
				return map[string]string{"name": name, "browser_download_url": srv.URL + "/build-5/" + name}
			}
			json.NewEncoder(w).Encode([]any{map[string]any{
				"tag_name": "build-5", "body": "Remind friends what changed before they join",
				"assets": []any{asset(selfupdate.ExeName), asset(selfupdate.ExeName + ".sha256")},
			}})
		case "/build-5/" + selfupdate.ExeName:
			w.Write([]byte(program))
		case "/build-5/" + selfupdate.ExeName + ".sha256":
			w.Write([]byte(hex.EncodeToString(h[:]) + "  " + selfupdate.ExeName + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	if err := m.CheckForUpdate(false); err == nil {
		t.Fatal("a development build checked for updates")
	}
	// Set up as main does, before the app starts.
	exe := filepath.Join(t.TempDir(), selfupdate.ExeName)
	os.WriteFile(exe, []byte("MZ build 4"), 0o755)
	setUp := func(m *Manager, build int, relaunch func()) {
		m.opt.AppExe, m.opt.AppBuild, m.opt.AppVersion = exe, build, fmt.Sprintf("build-%d", build)
		m.opt.Updater = &selfupdate.Updater{API: srv.URL}
		m.opt.Relaunch = relaunch
	}
	relaunched := make(chan struct{}, 1)
	setUp(m, 4, func() { relaunched <- struct{}{} })
	m.Start()

	// Checking says what's new without installing it.
	if err := m.CheckForUpdate(false); err != nil {
		t.Fatal(err)
	}
	a := m.Status().App
	if a.Build != 4 || a.Latest == nil || a.Latest.Build != 5 || a.CheckedAt == nil || a.Error != "" {
		t.Fatalf("app info = %+v", a)
	}
	if b, _ := os.ReadFile(exe); string(b) != "MZ build 4" {
		t.Fatal("checking replaced the program")
	}

	// Not in the middle of something.
	m.mu.Lock()
	m.uploads["u1"] = &worlds.Upload{Created: time.Now()}
	m.mu.Unlock()
	if err := m.CheckForUpdate(true); !errors.Is(err, errUpdateBusy) {
		t.Fatalf("busy: %v", err)
	}
	m.mu.Lock()
	m.uploads["u1"].Created = time.Now().Add(-3 * time.Hour) // forgotten about
	m.mu.Unlock()

	if err := m.CheckForUpdate(true); err != nil {
		t.Fatal(err)
	}
	waitRelaunch(t, relaunched)
	if b, _ := os.ReadFile(exe); string(b) != program {
		t.Fatalf("program = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "MZ build 4" {
		t.Fatalf("the old copy = %q", b)
	}
	if !hasText(m, "update", "Updating the app to build 5: Remind friends what changed before they join.") || !m.Status().App.Updating {
		t.Fatal("no activity about the update, or not shown as updating")
	}
	// Nothing new starts while the app restarts.
	nothing := func(context.Context, *jobCtl) error { return nil }
	if err := m.runJob("backup", "Backing up", false, nothing); !errors.Is(err, errUpdatingBusy) {
		t.Fatalf("a job started while updating: %v", err)
	}
	if _, err := m.NewUpload(); !errors.Is(err, errUpdatingBusy) {
		t.Fatalf("an upload started while updating: %v", err)
	}
	if rec := readRecord(t, env.data); rec != (updateRecord{From: 4, To: 5, Title: "Remind friends what changed before they join"}) {
		t.Fatalf("update.json = %+v", rec)
	}
	m.Close()

	// The restarted app says it worked.
	m = newTestManager(t, env.data, env)
	setUp(m, 5, func() { t.Error("build 5 restarted") })
	m.Start()
	if !hasText(m, "update", "The app was updated to build 5: Remind friends what changed before they join.") {
		t.Error("no activity about the finished update")
	}
	if !hasText(m, "update", "Updating the app to build 5") {
		t.Error("the activity from before the restart is gone")
	}
	if _, err := os.Stat(filepath.Join(env.data, updateFile)); err == nil {
		t.Error("update.json is still there")
	}
	if err := m.CheckForUpdate(true); err != nil || m.Status().App.Latest != nil {
		t.Fatalf("build 5 is up to date: %v %+v", err, m.Status().App)
	}
	m.Close()

	// When the new build doesn't start, main puts the previous version back
	// and starts that. It says so, and doesn't install that build by itself
	// again.
	os.WriteFile(exe, []byte("MZ build 4"), 0o755)
	data, _ := json.Marshal(updateRecord{From: 4, To: 5})
	os.WriteFile(filepath.Join(env.data, updateFile), data, 0o644)
	m = newTestManager(t, env.data, env)
	setUp(m, 4, func() { relaunched <- struct{}{} })
	m.Start()
	if !hasText(m, "warning", "Build 5 of the app didn't start on this computer, so the app went back to build 4.") || m.Status().App.Failed != 5 {
		t.Fatalf("not reported: %+v", m.Status().App)
	}
	if err := m.checkForUpdate(true, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "MZ build 4" || m.Status().App.Latest == nil || m.Status().App.Updating {
		t.Fatalf("installed a build that didn't start: %+v", m.Status().App)
	}
	// Someone can still try it.
	if err := m.CheckForUpdate(true); err != nil {
		t.Fatal(err)
	}
	waitRelaunch(t, relaunched)
	if rec := readRecord(t, env.data); rec.To != 5 || rec.Failed != 5 {
		t.Fatalf("update.json = %+v", rec)
	}
	m.Close()

	// This time it started, so it's no longer remembered as a build that
	// doesn't start.
	m = newTestManager(t, env.data, env)
	setUp(m, 5, func() { t.Error("build 5 restarted") })
	m.Start()
	t.Cleanup(m.Close)
	if f := m.Status().App.Failed; f != 0 || !hasText(m, "update", "The app was updated to build 5") {
		t.Fatalf("failed = %d", f)
	}
}

func waitRelaunch(t *testing.T, relaunched chan struct{}) {
	t.Helper()
	select {
	case <-relaunched:
	case <-time.After(5 * time.Second):
		t.Fatal("the app didn't restart")
	}
}

func hasText(m *Manager, kind, prefix string) bool {
	acts, _ := m.Activity(0, 0)
	for _, a := range acts {
		if a.Kind == kind && strings.HasPrefix(a.Text, prefix) {
			return true
		}
	}
	return false
}

func readRecord(t *testing.T, data string) updateRecord {
	t.Helper()
	var rec updateRecord
	b, err := os.ReadFile(filepath.Join(data, updateFile))
	if err != nil || json.Unmarshal(b, &rec) != nil {
		t.Fatalf("update.json: %v %s", err, b)
	}
	return rec
}
