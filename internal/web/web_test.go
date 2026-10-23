package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/manager"
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
	// …but a link to the panel itself still opens it.
	if rec := ts.do("GET", "/", "127.0.0.1", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusOK {
		t.Fatalf("cross-site link to the page = %d", rec.Code)
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
