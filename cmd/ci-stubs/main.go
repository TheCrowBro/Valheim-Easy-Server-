// Command ci-stubs stands in for GitHub's releases and for a Discord webhook
// in CI's upgrade test (.github/scripts/upgrade-test.ps1 and .sh). Copies of
// the app started with --dev-fake-github and --dev-fake-discord pointing here
// update themselves and post in "Discord" without reaching the real
// services, so a test copy can never update itself to a real release in the
// middle of a test. It is a test tool: it is never shipped.
//
// The GitHub stand-in (-github, default 127.0.0.1:18500) answers what the
// app's updater asks (internal/selfupdate):
//
//	GET /repos/TheCrowBro/Valheim-Easy-Server-           the repository
//	GET /repos/TheCrowBro/Valheim-Easy-Server-/releases  the published builds, newest first
//	GET /build-N/<file>                                  a build's program and its .sha256
//
// The test script drives it with:
//
//	POST /ci/publish   {"build": N, "file": "path/to/program"}  publish a copy of the program as build-N
//	POST /ci/unpublish {"above": N}                             take back every build above N
//	GET  /ci/ready                                              answers once both stand-ins listen
//
// The Discord stand-in (-discord, default 127.0.0.1:18501) takes webhook
// posts (POST /api/webhooks/<id>/<token>, with or without /v10), answers
// like Discord, and writes every post to posts.json in the -dir folder.
// GET /ci/posts answers with the same list, which the scripts read.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What GitHub answers for the real repository. These are written out here
// rather than taken from internal/selfupdate on purpose: if the app's own
// values ever changed, the upgrade test must fail instead of following them.
const (
	repoName = "TheCrowBro/Valheim-Easy-Server-"
	repoID   = 1430331409
)

// assetName is the release file the app on this system updates from: the
// Mac program on macOS, the Windows program everywhere else (as in
// internal/selfupdate, so the Linux tests of this stub see the same).
func assetName() string {
	if runtime.GOOS == "darwin" {
		return "ValheimEasyServer-mac"
	}
	return "ValheimEasyServer.exe"
}

func main() {
	dir := flag.String("dir", "", "folder for the published copies and posts.json (required)")
	githubAddr := flag.String("github", "127.0.0.1:18500", "where the GitHub stand-in listens")
	discordAddr := flag.String("discord", "127.0.0.1:18501", "where the Discord stand-in listens")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "ci-stubs: -dir is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	logger := log.New(os.Stderr, "ci-stubs: ", log.LstdFlags|log.Lmicroseconds)

	// Both listen before either serves, so /ci/ready means both are up.
	gl, err := net.Listen("tcp", *githubAddr)
	if err != nil {
		logger.Fatal(err)
	}
	dl, err := net.Listen("tcp", *discordAddr)
	if err != nil {
		logger.Fatal(err)
	}
	gh := newGitHub(filepath.Join(*dir, "releases"), assetName(), logger)
	dc := newDiscord(filepath.Join(*dir, "posts.json"), logger)
	logger.Printf("GitHub stand-in on %s, Discord stand-in on %s", gl.Addr(), dl.Addr())
	go func() { logger.Fatal(http.Serve(dl, logRequests(logger, "discord", dc))) }()
	logger.Fatal(http.Serve(gl, logRequests(logger, "github", gh)))
}

// logRequests notes every request, so a failed CI run shows what the app
// asked for.
func logRequests(l *log.Logger, name string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.Printf("%s: %s %s", name, r.Method, r.URL.RequestURI())
		h.ServeHTTP(w, r)
	})
}

// --- GitHub ---------------------------------------------------------------

type build struct {
	n         int
	path      string // the copy that is served
	sum       string // its SHA-256, in hex
	published time.Time
}

type githubStub struct {
	dir, asset string
	log        *log.Logger

	mu     sync.Mutex
	builds map[int]*build
}

func newGitHub(dir, asset string, l *log.Logger) *githubStub {
	return &githubStub{dir: dir, asset: asset, log: l, builds: map[int]*build{}}
}

var reDownload = regexp.MustCompile(`^/build-(\d{1,9})/([^/]+)$`)

func (g *githubStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo := "/repos/" + repoName
	switch p := r.URL.Path; {
	case r.Method == http.MethodGet && p == "/ci/ready":
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && p == "/ci/publish":
		g.publish(w, r)
	case r.Method == http.MethodPost && p == "/ci/unpublish":
		g.unpublish(w, r)
	case r.Method == http.MethodGet && p == repo:
		writeJSON(w, http.StatusOK, map[string]any{"id": repoID, "full_name": repoName})
	case r.Method == http.MethodGet && p == repo+"/releases":
		g.releases(w, r)
	case r.Method == http.MethodGet && reDownload.MatchString(p):
		g.download(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

// publish copies the program into the stand-in's own folder before it is
// listed, so the app never sees a build whose file is still being written,
// and the test can't change it afterwards.
func (g *githubStub) publish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Build int    `json:"build"`
		File  string `json:"file"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || in.Build <= 0 || in.File == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"build": N, "file": "path"} with N > 0`})
		return
	}
	b, err := g.store(in.Build, in.File)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	g.mu.Lock()
	g.builds[b.n] = b
	g.mu.Unlock()
	g.log.Printf("published build-%d (%s, sha256 %s)", b.n, in.File, b.sum)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "build": b.n, "sha256": b.sum})
}

func (g *githubStub) store(n int, src string) (*build, error) {
	folder := filepath.Join(g.dir, "build-"+strconv.Itoa(n))
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	dst := filepath.Join(folder, g.asset)
	tmp, err := os.CreateTemp(folder, ".copy-*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Remove(dst) // Windows won't rename over an existing file
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return &build{n: n, path: dst, sum: hex.EncodeToString(h.Sum(nil)), published: time.Now().UTC()}, nil
}

func (g *githubStub) unpublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Above *int `json:"above"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || in.Above == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"above": N}`})
		return
	}
	var gone []int
	g.mu.Lock()
	for n := range g.builds {
		if n > *in.Above {
			delete(g.builds, n)
			gone = append(gone, n)
		}
	}
	g.mu.Unlock()
	sort.Ints(gone)
	g.log.Printf("took back the builds above %d: %v", *in.Above, gone)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": gone})
}

// releases answers like GitHub's list: newest first, with download links on
// this same address, which is where the app expects a stand-in's files.
func (g *githubStub) releases(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	g.mu.Lock()
	list := make([]*build, 0, len(g.builds))
	for _, b := range g.builds {
		list = append(list, b)
	}
	g.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	out := []map[string]any{}
	for _, b := range list {
		tag := "build-" + strconv.Itoa(b.n)
		var assets []map[string]string
		for _, name := range []string{g.asset, g.asset + ".sha256"} {
			assets = append(assets, map[string]string{"name": name, "browser_download_url": base + "/" + tag + "/" + name})
		}
		out = append(out, map[string]any{
			"tag_name": tag, "name": "Build " + strconv.Itoa(b.n),
			"body":  fmt.Sprintf("CI test build %d\n\nPublished by the upgrade test's stand-in for GitHub.", b.n),
			"draft": false, "prerelease": false, "published_at": b.published.Format(time.RFC3339),
			"assets": assets,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (g *githubStub) download(w http.ResponseWriter, r *http.Request) {
	m := reDownload.FindStringSubmatch(r.URL.Path)
	n, _ := strconv.Atoi(m[1])
	g.mu.Lock()
	b := g.builds[n]
	g.mu.Unlock()
	switch {
	case b == nil:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case m[2] == g.asset:
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, b.path)
	case m[2] == g.asset+".sha256":
		// The same line sha256sum writes, as the release job publishes.
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprintf(w, "%s  %s\n", b.sum, g.asset)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

// --- Discord --------------------------------------------------------------

// post is one message the app sent to a webhook.
type post struct {
	N       int       `json:"n"`
	At      time.Time `json:"at"`
	Path    string    `json:"path"`
	Content string    `json:"content"`
}

type discordStub struct {
	file string
	log  *log.Logger

	mu    sync.Mutex
	posts []post
}

func newDiscord(file string, l *log.Logger) *discordStub {
	d := &discordStub{file: file, log: l, posts: []post{}}
	d.save() // an empty list until something is posted
	return d
}

var reWebhook = regexp.MustCompile(`^/api(?:/v\d+)?/webhooks/(\d+)/[\w-]+/?$`)

func (d *discordStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/ci/ready":
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && r.URL.Path == "/ci/posts":
		d.mu.Lock()
		list := append([]post{}, d.posts...)
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, list)
	case r.Method == http.MethodPost && reWebhook.MatchString(r.URL.Path):
		d.receive(w, r)
	case r.Method == http.MethodGet && reWebhook.MatchString(r.URL.Path):
		// What Discord says about a webhook (the waker asks for its server).
		id := reWebhook.FindStringSubmatch(r.URL.Path)[1]
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "guild_id": "1", "channel_id": "1", "name": "CI"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown Webhook", "code": 10015})
	}
}

func (d *discordStub) receive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || in.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Cannot send an empty message", "code": 50006})
		return
	}
	d.mu.Lock()
	p := post{N: len(d.posts) + 1, At: time.Now().UTC(), Path: r.URL.Path, Content: in.Content}
	d.posts = append(d.posts, p)
	d.mu.Unlock()
	d.save()
	d.log.Printf("discord post %d: %q", p.N, firstLine(p.Content))
	// With ?wait=true Discord answers with the message it made.
	writeJSON(w, http.StatusOK, map[string]any{"id": strconv.Itoa(p.N), "channel_id": "1", "content": p.Content})
}

// save writes posts.json through a temporary file, so a reader never sees
// half of it. Windows refuses to replace a file someone has open, so the
// rename is tried a few times.
func (d *discordStub) save() {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, err := json.MarshalIndent(d.posts, "", "  ")
	if err != nil {
		d.log.Printf("posts.json: %v", err)
		return
	}
	tmp := d.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		d.log.Printf("posts.json: %v", err)
		return
	}
	for try := 0; ; try++ {
		err = os.Rename(tmp, d.file)
		if err == nil || try == 20 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		d.log.Printf("posts.json: %v", err)
	}
}

// --- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) // an error here means the client went away
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
