package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/selfupdate"
)

func postJSON(t *testing.T, url, body string) map[string]any {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %s %v", url, res.Status, out)
	}
	return out
}

// program writes a file that starts like a program for this system, so the
// app's updater accepts it.
func program(t *testing.T, dir, name, filler string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(selfupdate.ProgramHeader+filler), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The stand-in must answer the way the app's own updater expects, or the
// upgrade test would test nothing: the updater finds the newest published
// build, downloads it and checks its checksum.
func TestGitHubWithTheAppsUpdater(t *testing.T) {
	dir := t.TempDir()
	quiet := log.New(io.Discard, "", 0)
	srv := httptest.NewServer(newGitHub(filepath.Join(dir, "releases"), assetName(), quiet))
	defer srv.Close()
	u := &selfupdate.Updater{API: srv.URL}
	ctx := context.Background()

	if _, ok, err := u.Latest(ctx); err != nil || ok {
		t.Fatalf("before anything is published: ok=%v err=%v", ok, err)
	}
	postJSON(t, srv.URL+"/ci/publish", `{"build": 30, "file": `+quote(program(t, dir, "a", "thirty"))+`}`)
	postJSON(t, srv.URL+"/ci/publish", `{"build": 31, "file": `+quote(program(t, dir, "b", "thirty-one"))+`}`)

	rel, ok, err := u.Latest(ctx)
	if err != nil || !ok || rel.Build != 31 {
		t.Fatalf("Latest = %+v, %v, %v; want build 31", rel, ok, err)
	}
	if !strings.HasPrefix(rel.Title, "CI test build 31") {
		t.Errorf("title %q", rel.Title)
	}
	got := filepath.Join(dir, "downloaded")
	if err := u.Download(ctx, rel, got); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != selfupdate.ProgramHeader+"thirty-one" {
		t.Errorf("downloaded %q", b)
	}

	// Taking the newer one back leaves the older one, and it is no longer served.
	postJSON(t, srv.URL+"/ci/unpublish", `{"above": 30}`)
	if rel, ok, err := u.Latest(ctx); err != nil || !ok || rel.Build != 30 {
		t.Fatalf("after taking 31 back: %+v, %v, %v", rel, ok, err)
	}
	res, err := http.Get(srv.URL + "/build-31/" + assetName())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("a build taken back is still served: %s", res.Status)
	}
	postJSON(t, srv.URL+"/ci/unpublish", `{"above": 0}`)
	if _, ok, err := u.Latest(ctx); err != nil || ok {
		t.Fatalf("after taking everything back: ok=%v err=%v", ok, err)
	}
}

// Publishing a copy keeps what was published even if the test's file
// changes afterwards.
func TestPublishCopiesTheFile(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(newGitHub(filepath.Join(dir, "releases"), assetName(), log.New(io.Discard, "", 0)))
	defer srv.Close()
	src := program(t, dir, "p", "first")
	postJSON(t, srv.URL+"/ci/publish", `{"build": 5, "file": `+quote(src)+`}`)
	os.WriteFile(src, []byte("changed"), 0o755)
	res, err := http.Get(srv.URL + "/build-5/" + assetName())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != selfupdate.ProgramHeader+"first" {
		t.Errorf("served %q", b)
	}
	for _, body := range []string{`{}`, `{"build": 0, "file": "x"}`, `{"build": 6}`, `{"build": 7, "file": "/no/such/file"}`} {
		res, err := http.Post(srv.URL+"/ci/publish", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("publish %s was accepted", body)
		}
	}
}

// The app's own Discord code posts to the stand-in, which keeps the posts
// in order in posts.json and answers them on /ci/posts.
func TestDiscordRecordsPosts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "posts.json")
	srv := httptest.NewServer(newDiscord(file, log.New(io.Discard, "", 0)))
	defer srv.Close()

	if b, err := os.ReadFile(file); err != nil || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("posts.json before any post: %q, %v", b, err)
	}
	link := srv.URL + "/api/webhooks/100000000000000001/ci-upgrade-test"
	for _, text := range []string{"🟢 **Test** is online\nWorld: Midgard", "🔴 **Test** has stopped."} {
		if err := discord.Post(context.Background(), nil, link, text); err != nil {
			t.Fatal(err)
		}
	}
	if guild, err := discord.Guild(context.Background(), nil, link); err != nil || guild == "" {
		t.Errorf("Guild = %q, %v", guild, err)
	}
	var fromFile, fromURL []post
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &fromFile); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(srv.URL + "/ci/posts")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(res.Body).Decode(&fromURL)
	res.Body.Close()
	for _, list := range [][]post{fromFile, fromURL} {
		if len(list) != 2 || list[0].N != 1 || !strings.Contains(list[0].Content, "is online") ||
			list[1].N != 2 || !strings.Contains(list[1].Content, "has stopped") {
			t.Errorf("posts: %+v", list)
		}
	}

	// Anything that isn't a webhook is refused, as Discord would.
	res, err = http.Post(srv.URL+"/api/channels/1/messages", "application/json", bytes.NewReader([]byte(`{"content":"x"}`)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("a post that isn't to a webhook: %s", res.Status)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
