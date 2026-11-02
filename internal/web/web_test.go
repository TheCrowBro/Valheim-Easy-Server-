package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/worlds"
)

type testServer struct {
	t   *testing.T
	s   *Server
	m   *manager.Manager
	dir string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	m, err := manager.New(manager.Options{DataDir: dir, Logger: logger, NoPublicIP: true,
		HibernateStatus: func() (bool, string) { return true, "" }, Hibernate: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	m.Start()
	t.Cleanup(m.Close)
	return &testServer{t: t, s: New(Options{Manager: m, Logger: logger, Version: "test"}), m: m, dir: dir}
}

// do sends a request from addr ("127.0.0.1" is this computer) to the panel
// at 127.0.0.1:8456, unless headers give another Host.
func (ts *testServer) do(method, path, addr string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	ts.t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = net.JoinHostPort(addr, "50000")
	req.Host = "127.0.0.1:8456"
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	ts.s.ServeHTTP(rec, req)
	return rec
}

var csrf = map[string]string{"X-VES": "1"}

func TestOnlyThisComputer(t *testing.T) {
	ts := newTestServer(t)
	for _, c := range []struct {
		addr, host string
		want       int
	}{
		{"127.0.0.1", "127.0.0.1:8456", http.StatusOK},
		{"127.0.0.1", "localhost:8456", http.StatusOK},
		{"::1", "[::1]:8456", http.StatusOK},
		{"192.0.2.10", "127.0.0.1:8456", http.StatusForbidden},   // another device
		{"127.0.0.1", "evil.example:8456", http.StatusForbidden}, // a website pointing its name at this computer
		{"127.0.0.1", "192.168.1.20:8456", http.StatusForbidden}, // not addressed as this computer
	} {
		for _, path := range []string{"/api/status", "/"} {
			if rec := ts.do("GET", path, c.addr, nil, map[string]string{"Host": c.host}); rec.Code != c.want {
				t.Errorf("GET %s from %s as %s = %d, want %d", path, c.addr, c.host, rec.Code, c.want)
			}
		}
	}
}

func TestBlocksOtherSites(t *testing.T) {
	ts := newTestServer(t)
	// Other sites can't read the API or start downloads…
	if rec := ts.do("GET", "/api/status", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d", rec.Code)
	}
	if rec := ts.do("GET", "/api/worlds/download?name=Midgard", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "same-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("same-site download = %d", rec.Code)
	}
	// …or open the panel from a link: it opens from the app (or the address
	// typed in), not from another site.
	if rec := ts.do("GET", "/", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site link to the page = %d", rec.Code)
	}
	if rec := ts.do("GET", "/", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate"}); rec.Code != http.StatusOK {
		t.Fatalf("opening the page from the app = %d", rec.Code)
	}
	// A browser only sends a port number after the address.
	if rec := ts.do("GET", "/api/status", "127.0.0.1", nil, map[string]string{"Host": "127.0.0.1:8456.evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("odd Host = %d", rec.Code)
	}
	if rec := ts.do("GET", "/api/status", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusOK {
		t.Fatalf("the panel's own request = %d", rec.Code)
	}
	// Actions need the anti-forgery header…
	if rec := ts.do("POST", "/api/server/start", "127.0.0.1", nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without header = %d", rec.Code)
	}
	// …and a matching origin when the browser sends one.
	if rec := ts.do("POST", "/api/server/start", "127.0.0.1", nil, map[string]string{"X-VES": "1", "Origin": "http://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("POST from another site = %d", rec.Code)
	}
	rec := ts.do("POST", "/api/server/start", "127.0.0.1", nil, map[string]string{"X-VES": "1", "Origin": "http://127.0.0.1:8456"})
	var body map[string]any
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusBadRequest || body["action"] != "install" {
		t.Fatalf("start before install = %d %v", rec.Code, body)
	}
}

func TestUploadKeepsFolders(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("POST", "/api/uploads", "127.0.0.1", nil, csrf)
	var up struct{ ID string }
	json.NewDecoder(rec.Body).Decode(&up)
	if up.ID == "" {
		t.Fatalf("no upload id: %s", rec.Body.String())
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	add := func(name string, data []byte) {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		w, _ := mw.CreatePart(h)
		w.Write(data)
	}
	meta := worlds.BuildMeta(worlds.Meta{Version: 37, Name: "Asgard", SeedName: "AbC123", Seed: 1, UID: 2}, true)
	add("Asgard/_main.1.fwl2", meta)
	add("Asgard/_main.1.db2", []byte("world data"))
	add("Asgard/00_00__0_1.chunk", []byte("chunk"))
	mw.Close()
	rec = ts.do("POST", "/api/uploads/"+up.ID+"/files", "127.0.0.1", &buf,
		map[string]string{"X-VES": "1", "Content-Type": mw.FormDataContentType()})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	rec = ts.do("POST", "/api/uploads/"+up.ID+"/scan", "127.0.0.1", nil, csrf)
	var scan struct{ Candidates []worlds.Candidate }
	json.NewDecoder(rec.Body).Decode(&scan)
	if len(scan.Candidates) != 1 || scan.Candidates[0].Name != "Asgard" || scan.Candidates[0].Format != worlds.FormatFolder {
		t.Fatalf("scan = %d %+v", rec.Code, scan)
	}
	rec = ts.do("POST", "/api/uploads/"+up.ID+"/import", "127.0.0.1",
		strings.NewReader(`{"choices":[{"id":"`+scan.Candidates[0].ID+`","action":"import"}]}`), csrf)
	var imp struct{ Job int }
	json.NewDecoder(rec.Body).Decode(&imp)
	if rec.Code != http.StatusOK || imp.Job == 0 {
		t.Fatalf("import = %d job %d", rec.Code, imp.Job)
	}
	placed := func() bool {
		v, _ := ts.m.Worlds()
		return len(v.Worlds) == 1 && v.Worlds[0].Name == "Asgard"
	}
	for i := 0; i < 100 && !placed(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !placed() {
		t.Fatal("the world wasn't put in the worlds folder")
	}

	// Downloading it gives a dated zip with the world and a how-to note,
	// and leaves no temporary file behind.
	rec = ts.do("GET", "/api/worlds/download?name=Asgard", "127.0.0.1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="Asgard 20`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Fatal("no Content-Length")
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["Asgard/_main.1.fwl2"] || !got["How to play this world.txt"] {
		t.Fatalf("zip has %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(ts.m.Paths().Staging, ".download-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if rec := ts.do("GET", "/api/worlds/download?name=Nowhere", "127.0.0.1", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("download of a missing world = %d", rec.Code)
	}
}

func TestStaticAndHeaders(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("GET", "/", "127.0.0.1", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Valheim Easy Server") {
		t.Fatalf("index = %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	for _, f := range []string{"/assets/app.js", "/assets/app.css", "/assets/logo.svg"} {
		if rec := ts.do("GET", f, "127.0.0.1", nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", f, rec.Code)
		}
	}
}

func TestMissingBackupIsAnError(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("GET", "/api/worlds/backup-download?name=Midgard&id=gone", "127.0.0.1", nil, nil)
	if rec.Code == http.StatusOK || rec.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("a missing backup downloaded: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A backup download that fails once part of the zip has been sent must not
// end like a finished download, or the browser saves a broken zip.
func TestBrokenBackupDownloadFails(t *testing.T) {
	ts := newTestServer(t)
	// A Valheim backup of a deleted world, with a file that can't be read
	// (as if Valheim removed it while the zip was being made). The sorted
	// zip order puts it after a big file, so the download has started.
	dir := filepath.Join(ts.m.Paths().Worlds, "Asgard_backup_auto-20260101120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 200<<10)
	rand.Read(big) // doesn't compress, so it's sent before the zip ends
	os.WriteFile(filepath.Join(dir, "_main.1.db2"), big, 0o644)
	os.WriteFile(filepath.Join(dir, "_main.1.fwl2"), worlds.BuildMeta(worlds.Meta{Version: 37, Name: "Asgard"}, true), 0o644)
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "zz.chunk")); err != nil {
		t.Skipf("can't make a broken link here: %v", err)
	}

	srv := httptest.NewServer(ts.s)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/worlds/backup-download?name=Asgard&id=" + url.QueryEscape("v:Asgard_backup_auto-20260101120000"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the download to start", res.StatusCode)
	}
	if b, err := io.ReadAll(res.Body); err == nil {
		t.Fatalf("the broken download ended normally after %d bytes", len(b))
	}
}

// Problems with the settings found by other actions (choosing a world,
// starting the server) say what's wrong, since those pages have nothing to
// highlight.
func TestFieldErrorsSayWhy(t *testing.T) {
	ts := newTestServer(t)
	s := ts.m.Settings()
	s.Password = "frost"
	if err := ts.m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rec := ts.do("POST", "/api/worlds/select", "127.0.0.1", strings.NewReader(`{"name":"Frostheim"}`), csrf)
	var body struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusBadRequest || body.Fields["password"] == "" {
		t.Fatalf("select = %d %+v", rec.Code, body)
	}
	if body.Error != body.Fields["password"] {
		t.Fatalf("error = %q, want the reason %q", body.Error, body.Fields["password"])
	}
}

func TestRunningSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	samples := []stats.Sample{
		{T: at(2 * time.Hour), Running: true}, // then the computer slept for an hour
		{T: at(time.Hour), Running: true},
		{T: at(time.Hour - 30*time.Second), Running: true}, // a 30-second reading kept from before the app started
		{T: at(time.Hour - 60*time.Second)},
		{T: at(10 * time.Second), Running: true},
		{T: at(8 * time.Second), Running: true},
	}
	// 1 minute (capped) + 30 s + 30 s + 2 s + 8 s (until now)
	if got, want := runningSeconds(samples, now), 130.0; got != want {
		t.Fatalf("runningSeconds = %v, want %v", got, want)
	}
	if got := runningSeconds(nil, now); got != 0 {
		t.Fatalf("no readings = %v", got)
	}
}

func TestRecords(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("GET", "/api/records?period=week", "127.0.0.1", nil, nil)
	var sum struct {
		Players []any `json:"players"`
		Records []any `json:"records"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &sum) != nil || sum.Players == nil || sum.Records == nil {
		t.Fatalf("records = %d %s", rec.Code, rec.Body)
	}
	if rec := ts.do("GET", "/api/records?period=forever", "127.0.0.1", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown period = %d", rec.Code)
	}
}
