package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub serves a releases list and the files in it.
func fakeGitHub(t *testing.T, files map[string]string, releases func(base string) any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "GitHub wants a User-Agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/repos/" + Repo:
			json.NewEncoder(w).Encode(map[string]any{"id": RepoID, "full_name": Repo})
			return
		case "/repos/" + Repo + "/releases":
			json.NewEncoder(w).Encode(releases(srv.URL))
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func release(base, tag, body string, assets ...string) map[string]any {
	var list []map[string]string
	for _, a := range assets {
		list = append(list, map[string]string{"name": a, "browser_download_url": base + "/" + tag + "/" + a})
	}
	return map[string]any{"tag_name": tag, "body": body, "published_at": "2026-10-24T10:00:00Z", "assets": list}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestLatestAndDownload(t *testing.T) {
	program := "MZ the new app"
	files := map[string]string{
		"/build-5/" + ExeName:             program,
		"/build-5/" + ExeName + ".sha256": sum(program) + "  " + ExeName + "\n",
	}
	srv := fakeGitHub(t, files, func(base string) any {
		pre := release(base, "build-9", "Not ready", ExeName, ExeName+".sha256")
		pre["prerelease"] = true
		return []any{
			pre,
			release(base, "build-7", "No checksum", ExeName),
			release(base, "v1.0", "A named release", ExeName, ExeName+".sha256"),
			release(base, "build-5", "\nRemind friends what changed before they join\n\nMore details.", ExeName, ExeName+".sha256"),
			release(base, "build-3", "Older", ExeName, ExeName+".sha256"),
		}
	})
	u := &Updater{API: srv.URL}
	rel, ok, err := u.Latest(context.Background())
	if err != nil || !ok {
		t.Fatalf("latest: %v %v", ok, err)
	}
	if rel.Build != 5 || rel.Title != "Remind friends what changed before they join" {
		t.Fatalf("latest = %+v", rel)
	}

	path := filepath.Join(t.TempDir(), ExeName+".new")
	if err := u.Download(context.Background(), rel, path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != program {
		t.Fatalf("downloaded %q", b)
	}

	// A file that doesn't match its checksum isn't kept.
	files["/build-5/"+ExeName] = "MZ something else"
	if err := u.Download(context.Background(), rel, path); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a changed file: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the bad download was left behind")
	}
	// Nor something that isn't a program.
	files["/build-5/"+ExeName] = "<html>"
	files["/build-5/"+ExeName+".sha256"] = sum("<html>")
	if err := u.Download(context.Background(), rel, path); err == nil || !strings.Contains(err.Error(), "Windows program") {
		t.Fatalf("not a program: %v", err)
	}

	// GitHub may spell the repository's name with other capitals.
	if !under(strings.Replace(rel.exeURL, "build-5", "BUILD-5", 1), rel.files) {
		t.Fatal("capitals made a difference")
	}
	// Downloads only come from where the releases are.
	rel.exeURL = "https://example.com/" + ExeName
	if err := u.Download(context.Background(), rel, path); err == nil || !strings.Contains(err.Error(), "unexpected download address") {
		t.Fatalf("another site: %v", err)
	}
	if got := (&Updater{}).downloads(Repo); got != "https://github.com/"+Repo+"/releases/download/" {
		t.Fatalf("downloads = %q", got)
	}
	for _, link := range []string{
		"https://github.com/" + Repo + "/releases/download/build-5/../../../../evil/x.exe",
		"https://github.com/" + Repo + "/releases/download/build-5/%2e%2e/x.exe",
		"https://someone@github.com/" + Repo + "/releases/download/build-5/" + ExeName,
		"https://github.com/" + Repo + "/releases/download/build-5/" + ExeName + "?x=1",
	} {
		if under(link, "https://github.com/"+Repo+"/releases/download/build-5/") {
			t.Errorf("accepted %q", link)
		}
	}
}

// Only this project's repository, and only each release's own files, count.
func TestLatestChecks(t *testing.T) {
	srv := fakeGitHub(t, nil, func(base string) any {
		elsewhere := release(base, "build-8", "Files from elsewhere", ExeName, ExeName+".sha256")
		for _, a := range elsewhere["assets"].([]map[string]string) {
			a["browser_download_url"] = base + "/build-7/" + a["name"]
		}
		return []any{
			release(base, "build-99999999999999999999", "A number too big", ExeName, ExeName+".sha256"),
			elsewhere,
			release(base, "build-4", "Fine", ExeName, ExeName+".sha256"),
		}
	})
	rel, ok, err := (&Updater{API: srv.URL}).Latest(context.Background())
	if err != nil || !ok || rel.Build != 4 {
		t.Fatalf("latest = %+v %v %v", rel, ok, err)
	}
	if _, _, err := (&Updater{API: srv.URL, RepoID: 12345}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't the one") {
		t.Fatalf("another repository: %v", err)
	}
}

func TestRedirects(t *testing.T) {
	u := &Updater{}
	for link, ok := range map[string]bool{
		"https://objects.githubusercontent.com/x":        true,
		"https://release-assets.githubusercontent.com/x": true,
		"http://objects.githubusercontent.com/x":         false,
		"https://evil.example/x":                         false,
		"https://githubusercontent.com.evil.example/x":   false,
	} {
		req, _ := http.NewRequest("GET", link, nil)
		if err := u.redirect(req, nil); (err == nil) != ok {
			t.Errorf("redirect to %s: %v", link, err)
		}
	}
}

func TestLatestWithNothing(t *testing.T) {
	srv := fakeGitHub(t, nil, func(base string) any { return []any{} })
	if _, ok, err := (&Updater{API: srv.URL}).Latest(context.Background()); ok || err != nil {
		t.Fatalf("no releases: %v %v", ok, err)
	}
	if _, _, err := (&Updater{API: srv.URL, Repo: "someone/else"}).Latest(context.Background()); err == nil {
		t.Fatal("a missing repository wasn't an error")
	}
}

func TestSwap(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, ExeName)
	os.WriteFile(exe, []byte("old app"), 0o755)
	os.WriteFile(exe+".new", []byte("new app"), 0o755)
	if err := Swap(exe, exe+".new"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new app" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old app" {
		t.Fatalf("old = %q", b)
	}
	// The previous version stays until the next update, and can be put back.
	os.WriteFile(exe+".new", []byte("half a download"), 0o644)
	CleanUp(exe)
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Fatal("the unfinished download wasn't cleaned up")
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old app" {
		t.Fatal("clean-up removed the previous version")
	}
	if err := Restore(exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old app" {
		t.Fatalf("restored exe = %q", b)
	}
	if _, err := os.Stat(exe + ".old"); err == nil {
		t.Fatal("the old copy is still there after putting it back")
	}
}
