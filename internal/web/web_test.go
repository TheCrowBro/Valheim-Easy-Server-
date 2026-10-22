package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

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
	return &testServer{t: t, s: New(Options{Manager: m, Logger: logger, DataDir: dir, Version: "test"}), m: m, dir: dir}
}

// do sends a request from addr ("127.0.0.1" is this computer).
func (ts *testServer) do(method, path, addr string, cookie *http.Cookie, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	ts.t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = addr + ":50000"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ts.s.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

var csrf = map[string]string{"X-VES": "1"}

func TestLocalTokenLogin(t *testing.T) {
	ts := newTestServer(t)
	if rec := ts.do("GET", "/api/status", "127.0.0.1", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without login = %d", rec.Code)
	}
	if rec := ts.do("GET", "/?token=wrong", "127.0.0.1", nil, nil, nil); sessionCookie(rec) != nil {
		t.Fatal("a wrong token logged in")
	}
	// The token only works from this computer.
	if rec := ts.do("GET", "/?token="+ts.s.Token(), "192.0.2.10", nil, nil, nil); sessionCookie(rec) != nil {
		t.Fatal("the app token worked from another device")
	}
	rec := ts.do("GET", "/?token="+ts.s.Token(), "127.0.0.1", nil, nil, nil)
	c := sessionCookie(rec)
	if rec.Code != http.StatusFound || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("token login: code %d cookie %+v", rec.Code, c)
	}
	if rec := ts.do("GET", "/api/status", "127.0.0.1", c, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// A copied local cookie is no good from another device.
	if rec := ts.do("GET", "/api/status", "192.0.2.10", c, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("local cookie from another device = %d", rec.Code)
	}
	// Actions need the anti-forgery header…
	if rec := ts.do("POST", "/api/server/start", "127.0.0.1", c, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without header = %d", rec.Code)
	}
	// …and a matching origin when the browser sends one.
	if rec := ts.do("POST", "/api/server/start", "127.0.0.1", c, nil, map[string]string{"X-VES": "1", "Origin": "http://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("POST from another site = %d", rec.Code)
	}
	rec = ts.do("POST", "/api/server/start", "127.0.0.1", c, nil, csrf)
	var body map[string]any
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusBadRequest || body["action"] != "install" {
		t.Fatalf("start before install = %d %v", rec.Code, body)
	}
}

func TestRemoteLoginAndLimits(t *testing.T) {
	ts := newTestServer(t)
	login := func(pw, addr string) *httptest.ResponseRecorder {
		return ts.do("POST", "/api/login", addr, nil, strings.NewReader(`{"password":"`+pw+`"}`), csrf)
	}
	if rec := login("anything", "192.0.2.20"); rec.Code != http.StatusForbidden {
		t.Fatalf("login with remote access off = %d", rec.Code)
	}
	if _, err := ts.m.SetPanel(true, 8456, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if rec := login("nope", "192.0.2.20"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", rec.Code)
	}
	rec := login("correct horse", "192.0.2.20")
	c := sessionCookie(rec)
	if rec.Code != http.StatusOK || c == nil || c.MaxAge <= 0 {
		t.Fatalf("login = %d cookie %+v", rec.Code, c)
	}
	if rec := ts.do("GET", "/api/status", "192.0.2.20", c, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("friend status = %d", rec.Code)
	}
	// Friends can't use the server-computer-only actions…
	if rec := ts.do("POST", "/api/system/firewall", "192.0.2.20", c, nil, csrf); rec.Code != http.StatusForbidden {
		t.Fatalf("friend firewall = %d", rec.Code)
	}
	if rec := ts.do("POST", "/api/panel", "192.0.2.20", c, strings.NewReader(`{"remote":false,"port":8456}`), csrf); rec.Code != http.StatusForbidden {
		t.Fatalf("friend changing access = %d", rec.Code)
	}
	// …and don't see folders or the stored password hash.
	rec = ts.do("GET", "/api/settings", "192.0.2.20", c, nil, nil)
	if strings.Contains(rec.Body.String(), "pbkdf2") || strings.Contains(rec.Body.String(), ts.dir) {
		t.Fatalf("settings leak to friends: %s", rec.Body.String())
	}
	// Guessing is slowed down per address.
	for i := 0; i < maxFailures; i++ {
		login("guess", "192.0.2.30")
	}
	if rec := login("correct horse", "192.0.2.30"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after many wrong passwords = %d", rec.Code)
	}
	// Changing the password logs friends out.
	ts.s.sessions.removeRemote()
	if rec := ts.do("GET", "/api/status", "192.0.2.20", c, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout of friends = %d", rec.Code)
	}
}

func TestUploadKeepsFolders(t *testing.T) {
	ts := newTestServer(t)
	c := sessionCookie(ts.do("GET", "/?token="+ts.s.Token(), "127.0.0.1", nil, nil, nil))
	rec := ts.do("POST", "/api/uploads", "127.0.0.1", c, nil, csrf)
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
	rec = ts.do("POST", "/api/uploads/"+up.ID+"/files", "127.0.0.1", c, &buf,
		map[string]string{"X-VES": "1", "Content-Type": mw.FormDataContentType()})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	rec = ts.do("POST", "/api/uploads/"+up.ID+"/scan", "127.0.0.1", c, nil, csrf)
	var scan struct{ Candidates []worlds.Candidate }
	json.NewDecoder(rec.Body).Decode(&scan)
	if len(scan.Candidates) != 1 || scan.Candidates[0].Name != "Asgard" || scan.Candidates[0].Format != worlds.FormatFolder {
		t.Fatalf("scan = %d %+v", rec.Code, scan)
	}
}

func TestStaticAndHeaders(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("GET", "/", "192.0.2.40", nil, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Valheim Easy Server") {
		t.Fatalf("index = %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	for _, f := range []string{"/assets/app.js", "/assets/app.css", "/assets/logo.svg"} {
		if rec := ts.do("GET", f, "192.0.2.40", nil, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", f, rec.Code)
		}
	}
}
