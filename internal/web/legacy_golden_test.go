package web

// The control panel's API as bed004b answers it (docs/dev/multigame/spec.md
// I5, I7, 4.9 and 6.2). A panel page that's open while the app updates
// talks to the new copy, the old copy after a rollback talks to whatever
// the new one left, and the update tests in CI read /api/status from both,
// so every route stays, answering the way it does today.
//
// The answers are recorded in internal/manager/testdata/golden, beside the
// manager's other goldens. A golden is never re-recorded to make a test
// pass: the code must match it. VES_RECORD_GOLDEN=1 writes a golden file
// that doesn't exist yet; it never overwrites one.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

const recordGoldenEnv = "VES_RECORD_GOLDEN"

// golden returns the golden file name (in the manager's testdata/golden),
// recording got there first if it doesn't exist and recording is on.
func golden(t *testing.T, name string, got any) []byte {
	t.Helper()
	path := filepath.Join("..", "manager", "testdata", "golden", name)
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv(recordGoldenEnv) == "1" {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(got); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s", path)
		return b.Bytes()
	}
	if err != nil {
		t.Fatalf("golden %s: %v (set %s=1 to record a new one)", path, err, recordGoldenEnv)
	}
	return want
}

// --- shapes -----------------------------------------------------------------

// shapeOf lists every place in a JSON document with its JSON type: $ is
// the document, .key an object's key and [] an array's items (all of them
// together).
func shapeOf(t *testing.T, b []byte) map[string]string {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	out := map[string]string{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		kind := "object"
		switch x := v.(type) {
		case nil:
			kind = "null"
		case bool:
			kind = "bool"
		case json.Number:
			kind = "number"
		case string:
			kind = "string"
		case []any:
			kind = "array"
			for _, c := range x {
				walk(path+"[]", c)
			}
		case map[string]any:
			for k, c := range x {
				walk(path+"."+k, c)
			}
		}
		if prev, ok := out[path]; ok && prev != kind && !strings.Contains("|"+prev+"|", "|"+kind+"|") {
			kinds := append(strings.Split(prev, "|"), kind)
			sort.Strings(kinds)
			kind = strings.Join(kinds, "|")
		}
		out[path] = kind
	}
	walk("$", v)
	return out
}

// sameKind reports whether a place recorded as want may now be got: the
// same type, or null for a list or object (Go writes an empty one as null).
func sameKind(want, got string) bool {
	if want == got {
		return true
	}
	loose := func(k string) bool { return k == "null" || k == "array" || k == "object" }
	return (want == "null" && loose(got)) || (got == "null" && loose(want))
}

// shapeProblems lists what in got doesn't have the shape recorded as want.
// A recorded place inside a list may be missing when that list is empty
// now. New places are allowed only where newOK says.
func shapeProblems(want, got map[string]string, newOK func(path string) bool) []string {
	var out []string
	for _, p := range sortedPaths(want) {
		g, ok := got[p]
		if !ok {
			if !inEmptyList(p, got) {
				out = append(out, p+" ("+want[p]+") is gone")
			}
			continue
		}
		if !sameKind(want[p], g) {
			out = append(out, p+" is a "+g+", was a "+want[p])
		}
	}
	for _, p := range sortedPaths(got) {
		if _, ok := want[p]; !ok && !inEmptyList(p, want) && !newOK(p) {
			out = append(out, p+" ("+got[p]+") is new and not on the allowed list")
		}
	}
	return out
}

// inEmptyList reports whether p is inside a list (or object) that's empty
// or null in shape.
func inEmptyList(p string, shape map[string]string) bool {
	for i := strings.Index(p, "[]"); i >= 0; {
		list := p[:i]
		if k, ok := shape[list]; ok && (k == "null" || shape[list+"[]"] == "") {
			return true
		}
		next := strings.Index(p[i+2:], "[]")
		if next < 0 {
			break
		}
		i += 2 + next
	}
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '.' || p[i] == '[' {
			if shape[p[:i]] == "null" {
				return true
			}
		}
	}
	return false
}

func sortedPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filled returns a value of type T with every field set: one item in each
// list and map, so every key is written.
func filled[T any]() T {
	var v T
	fill(reflect.ValueOf(&v).Elem(), 0)
	return v
}

var filledTime = time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)

func fill(v reflect.Value, depth int) {
	if depth > 12 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(filledTime))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), depth+1)
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k, depth+1)
		fill(e, depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	}
}

// --- /api/status --------------------------------------------------------

// statusAllowed are the keys /api/status may gain at the top (spec 4.9),
// and in the objects the old panel reads closely.
var statusAllowed = map[string]bool{
	"$.games": true, "$.jobs": true, "$.stars": true, "$.sidebar": true, "$.online": true,
	"$.playing": true, "$.router": true, "$.safeMode": true,
	"$.job.game": true, // the instance a job is for (spec 4.2)
}

// statusNewOK says whether a new place in /api/status is allowed: at the
// top and in job, lastExit, join and app only from statusAllowed, and
// anywhere else (the old panel ignores what it doesn't know).
func statusNewOK(p string) bool {
	for a := range statusAllowed {
		if p == a || strings.HasPrefix(p, a+".") || strings.HasPrefix(p, a+"[]") {
			return true
		}
	}
	if strings.Count(p, ".") == 1 && !strings.Contains(p, "[]") {
		return false // a new key at the top
	}
	for _, watched := range []string{"$.job.", "$.lastExit.", "$.join.", "$.app."} {
		if strings.HasPrefix(p, watched) && strings.Count(p[len(watched):], ".") == 0 && !strings.Contains(p[len(watched):], "[]") {
			return false
		}
	}
	return true
}

func TestStatusTopLevelKeys(t *testing.T) {
	// Every key Status can have, with every field set.
	b, err := json.Marshal(filled[manager.Status]())
	if err != nil {
		t.Fatal(err)
	}
	got := shapeOf(t, b)
	var want map[string]string
	if err := json.Unmarshal(golden(t, "status.json", got), &want); err != nil {
		t.Fatal(err)
	}
	for _, p := range shapeProblems(want, got, statusNewOK) {
		t.Errorf("/api/status: %s", p)
	}

	// And as it answers: 200, with the keys that are always there.
	ts := newRoutesServer(t)
	rec := ts.call("GET", "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d", rec.Code)
	}
	live := shapeOf(t, rec.Body.Bytes())
	zero, _ := json.Marshal(manager.Status{})
	for _, p := range sortedPaths(shapeOf(t, zero)) {
		if _, old := want[p]; !old || strings.Count(p, ".") != 1 || strings.Contains(p, "[]") {
			continue // new keys are checked below
		}
		if g, ok := live[p]; !ok {
			t.Errorf("/api/status has no %s", p)
		} else if !sameKind(want[p], g) {
			t.Errorf("/api/status: %s is a %s, was a %s", p, g, want[p])
		}
	}
	for _, p := range sortedPaths(live) {
		if strings.Count(p, ".") == 1 && !strings.Contains(p, "[]") && want[p] == "" && !statusAllowed[p] {
			t.Errorf("/api/status has a new key %s that isn't on the allowed list", p)
		}
	}
}

// --- every route ------------------------------------------------------------

type routesServer struct {
	t   *testing.T
	s   *Server
	m   *manager.Manager
	dir string
}

// newRoutesServer is a control panel whose manager stands in for
// everything outside the app: a pretend install, Discord, resting, network
// adapters. Nothing it's asked to do reaches this computer's settings.
func newRoutesServer(t *testing.T) *routesServer {
	t.Helper()
	dir := t.TempDir()
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id": "1", "guild_id": "g1", "channel_id": "2"}`))
	}))
	t.Cleanup(discord.Close)
	server := filepath.Join(dir, "stand-in-server") // copied by the pretend install, never run
	os.WriteFile(server, []byte("not a server"), 0o755)
	logger := log.New(io.Discard, "", 0)
	m, err := manager.New(manager.Options{
		DataDir: filepath.Join(dir, "data"), Logger: logger, NoPublicIP: true, DiscordURL: discord.URL,
		NewInstaller: func(p config.Paths) steamcmd.Installer {
			return &steamcmd.Fake{Source: server, InstallDir: p.Server, ExeName: config.ServerExeName(), Duration: 30 * time.Millisecond}
		},
		Hibernate: func() error { return nil }, HibernateStatus: func() (bool, string) { return true, "" },
		Sleep: func() error { return nil }, SleepStatus: func() (bool, string) { return true, "" },
		NetAdapters: func() []platform.NetAdapter {
			return []platform.NetAdapter{{Name: "Ethernet", Description: "Stand-in", MAC: "3C:07:54:5A:21:9F",
				IPv4: []string{"192.168.1.20"}, Wired: true, Connected: true, WakeArmed: true}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Start()
	t.Cleanup(m.Close)
	return &routesServer{t: t, s: New(Options{Manager: m, Logger: logger, Version: "test"}), m: m, dir: dir}
}

// call asks the panel, as the panel itself does.
func (ts *routesServer) call(method, path, body string) *httptest.ResponseRecorder {
	return ts.send(method, path, strings.NewReader(body), "application/json")
}

func (ts *routesServer) send(method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	ts.t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = net.JoinHostPort("127.0.0.1", "50000")
	req.Host = "127.0.0.1:8456"
	if method != "GET" {
		req.Header.Set("X-VES", "1")
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	ts.s.ServeHTTP(rec, req)
	return rec
}

// idle waits for the job in progress, if any, to finish.
func (ts *routesServer) idle() {
	ts.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if j := ts.m.Status().Job; j == nil || j.Finished != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	ts.t.Fatal("a job didn't finish")
}

// routeAnswer is how a route answered one request.
type routeAnswer struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Body   string            `json:"body,omitempty"`
	Status int               `json:"status"`
	Type   string            `json:"type,omitempty"`  // for the page, the API and downloads
	Shape  map[string]string `json:"shape,omitempty"` // for JSON answers
}

// TestLegacyRoutes asks every route bed004b has, the way the panel does,
// and compares the answers with the ones bed004b gave: the status, the
// kind of answer and the JSON's shape. Answers may gain keys (the old
// panel ignores them); none may lose one or change its type.
func TestLegacyRoutes(t *testing.T) {
	ts := newRoutesServer(t)
	var got []routeAnswer
	ask := func(method, path, body string, values ...string) *httptest.ResponseRecorder {
		t.Helper()
		real := path
		realBody := body
		for i := 0; i+1 < len(values); i += 2 { // placeholders such as {id}
			real = strings.ReplaceAll(real, values[i], values[i+1])
			realBody = strings.ReplaceAll(realBody, values[i], values[i+1])
		}
		rec := ts.call(method, real, realBody)
		got = append(got, answerOf(t, method, path, body, rec))
		return rec
	}

	// Not installed yet.
	ask("GET", "/", "")
	ask("GET", "/assets/app.js", "")
	ask("GET", "/api/status", "")
	ask("POST", "/api/server/start", "")
	ask("POST", "/api/server/stop", "")
	ask("POST", "/api/server/force-stop", "")
	ask("POST", "/api/server/restart", "")
	ts.idle()
	ask("POST", "/api/job/cancel", "")
	ask("POST", "/api/power/keep-awake", "")
	ask("POST", "/api/power/enable-hibernate", "")

	// Worlds.
	ask("GET", "/api/worlds", "")
	ask("POST", "/api/worlds/create", `{"name":"Asgard","options":{"preset":"hard"},"restart":false}`)
	ask("POST", "/api/worlds/options", `{"name":"Asgard","options":{"preset":"casual","modifiers":{"combat":"hard"}}}`)
	ask("POST", "/api/worlds/mods", `{"name":"Asgard","mods":["planteverything"]}`)
	ask("POST", "/api/worlds/select", `{"name":"Midgard","restart":false}`)

	// Adding a world from an upload, then one that's thrown away.
	var up struct{ ID string }
	json.Unmarshal(ask("POST", "/api/uploads", "").Body.Bytes(), &up)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range map[string][]byte{
		"Vanaheim/_main.1.fwl2": worlds.BuildMeta(worlds.Meta{Version: 37, Name: "Vanaheim", SeedName: "AbC123", Seed: 1, UID: 2}, true),
		"Vanaheim/_main.1.db2":  []byte("world data"),
	} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		w, _ := mw.CreatePart(h)
		w.Write(data)
	}
	mw.Close()
	rec := ts.send("POST", "/api/uploads/"+up.ID+"/files", &buf, mw.FormDataContentType())
	got = append(got, answerOf(t, "POST", "/api/uploads/{id}/files", "(a world folder, as multipart form data)", rec))
	var scan struct {
		Candidates []struct{ ID string }
	}
	json.Unmarshal(ask("POST", "/api/uploads/{id}/scan", "", "{id}", up.ID).Body.Bytes(), &scan)
	if len(scan.Candidates) != 1 {
		t.Fatalf("the upload has %d worlds", len(scan.Candidates))
	}
	ask("POST", "/api/uploads/{id}/import", `{"choices":[{"id":"{candidate}","action":"import"}]}`, "{id}", up.ID, "{candidate}", scan.Candidates[0].ID)
	ts.idle()
	json.Unmarshal(ask("POST", "/api/uploads", "").Body.Bytes(), &up)
	ask("DELETE", "/api/uploads/{id}", "", "{id}", up.ID)

	ask("GET", "/api/worlds", "")
	ask("POST", "/api/worlds/backup", `{"name":"Vanaheim"}`)
	ts.idle()
	backup := ""
	if v, err := ts.m.Worlds(); err == nil {
		for _, w := range v.Worlds {
			if w.Name == "Vanaheim" && len(w.Backups) > 0 {
				backup = w.Backups[0].ID
			}
		}
	}
	if backup == "" {
		t.Fatal("no backup of Vanaheim")
	}
	ask("GET", "/api/worlds/backup-download?name=Vanaheim&id={backup}", "", "{backup}", url.QueryEscape(backup))
	ask("GET", "/api/worlds/download?name=Vanaheim", "")
	ask("POST", "/api/worlds/restore", `{"name":"Vanaheim","backupId":"{backup}"}`, "{backup}", backup)
	ts.idle()
	ask("POST", "/api/worlds/rename", `{"name":"Vanaheim","newName":"Jotunheim"}`)
	ask("POST", "/api/worlds/delete", `{"name":"Vanaheim"}`) // the first world added is the one played
	ask("POST", "/api/worlds/select", `{"name":"Midgard","restart":false}`)
	ask("POST", "/api/worlds/delete", `{"name":"Vanaheim"}`)
	ts.idle()
	ask("GET", "/api/worlds/download?name=Nowhere", "")
	ask("GET", "/api/worlds/backup-download?name=Midgard&id=gone", "")
	ask("GET", "/api/local-worlds", "")
	ask("POST", "/api/local-worlds/import", `{"choices":[]}`)

	// Settings, Discord and the waker.
	settings := ask("GET", "/api/settings", "")
	var view struct{ Settings json.RawMessage }
	json.Unmarshal(settings.Body.Bytes(), &view)
	ask("POST", "/api/settings", "{the settings, as GET gave them}", "{the settings, as GET gave them}", string(view.Settings))
	ask("POST", "/api/discord/test", `{"webhook":"https://discord.com/api/webhooks/123/abc"}`)
	ask("POST", "/api/discord/test", `{"webhook":"https://discord.com/channels/1/2"}`)
	ask("GET", "/api/jetkvm", "")
	ask("POST", "/api/jetkvm/install", `{"address":"","token":"","trust":false}`)
	ask("POST", "/api/jetkvm/remove", `{"force":true}`)
	ask("POST", "/api/jetkvm/post", `{"what":"invite"}`)
	ask("POST", "/api/app/update", `{"install":false}`)

	// Players, records, stats and logs.
	ask("GET", "/api/lists", "")
	ask("POST", "/api/lists", `{"kind":"admins","ids":["76561198000000001"]}`)
	ask("GET", "/api/players", "")
	ask("GET", "/api/records?period=week", "")
	ask("GET", "/api/records?period=forever", "")
	ask("GET", "/api/records", "")
	ask("GET", "/api/stats?range=1h", "")
	ask("POST", "/api/stats/reset", "")
	ask("GET", "/api/logs?after=0", "")
	ask("GET", "/api/logs/download", "")

	// This computer, then installing and resting.
	ask("POST", "/api/system/open-folder", `{"which":"nowhere"}`)
	ask("POST", "/api/server/install", `{"repair":false}`)
	ts.idle()
	ask("GET", "/api/status", "")
	ask("POST", "/api/power/hibernate", "")
	ts.idle()
	ask("POST", "/api/system/quit", `{"stopServer":false}`)

	var want []routeAnswer
	if err := json.Unmarshal(golden(t, "routes.json", got), &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d answers, %d recorded", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		what := w.Method + " " + w.Path
		if g.Method != w.Method || g.Path != w.Path || g.Body != w.Body {
			t.Fatalf("answer %d is to %s %s, recorded to %s", i, g.Method, g.Path, what)
		}
		if g.Status != w.Status || g.Type != w.Type {
			t.Errorf("%s %s: %d %s, was %d %s", what, w.Body, g.Status, g.Type, w.Status, w.Type)
			continue
		}
		for _, p := range shapeProblems(w.Shape, g.Shape, func(string) bool { return true }) {
			t.Errorf("%s %s: %s", what, w.Body, p)
		}
	}

	// The routes that change this computer (its firewall, network adapter
	// and startup items) aren't asked above, but every route is still
	// there: a PUT, which no route takes, is refused as the wrong method,
	// which happens only on a path that has a route.
	for _, route := range bed004bRoutes {
		_, path, _ := strings.Cut(route, " ")
		path = strings.NewReplacer("{id}", "x", "{$}", "").Replace(path)
		if rec := ts.call("PUT", path, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: PUT %s = %d, want 405 (the route is gone)", route, path, rec.Code)
		}
	}
}

// answerOf notes how a route answered.
func answerOf(t *testing.T, method, path, body string, rec *httptest.ResponseRecorder) routeAnswer {
	t.Helper()
	a := routeAnswer{Method: method, Path: path, Body: body, Status: rec.Code}
	kind, _, _ := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	switch {
	case kind == "application/json":
		a.Type, a.Shape = kind, shapeOf(t, rec.Body.Bytes())
	case kind == "text/html", kind == "application/zip", strings.HasPrefix(path, "/api/"):
		a.Type = kind
	}
	return a
}

// bed004bRoutes is every route bed004b's panel has (api.go).
var bed004bRoutes = []string{
	"GET /{$}",
	"GET /assets/app.js",
	"GET /api/status",
	"POST /api/server/start",
	"POST /api/server/stop",
	"POST /api/server/restart",
	"POST /api/server/force-stop",
	"POST /api/server/install",
	"POST /api/job/cancel",
	"POST /api/power/hibernate",
	"POST /api/power/keep-awake",
	"POST /api/power/enable-hibernate",
	"POST /api/power/enable-wake-on-lan",
	"GET /api/worlds",
	"POST /api/worlds/select",
	"POST /api/worlds/create",
	"POST /api/worlds/options",
	"POST /api/worlds/mods",
	"POST /api/worlds/rename",
	"POST /api/worlds/delete",
	"POST /api/worlds/backup",
	"POST /api/worlds/restore",
	"GET /api/worlds/download",
	"GET /api/worlds/backup-download",
	"POST /api/uploads",
	"POST /api/uploads/{id}/files",
	"POST /api/uploads/{id}/scan",
	"POST /api/uploads/{id}/import",
	"DELETE /api/uploads/{id}",
	"GET /api/local-worlds",
	"POST /api/local-worlds/import",
	"GET /api/settings",
	"POST /api/settings",
	"POST /api/discord/test",
	"GET /api/jetkvm",
	"POST /api/jetkvm/install",
	"POST /api/jetkvm/remove",
	"POST /api/jetkvm/post",
	"POST /api/app/update",
	"GET /api/lists",
	"POST /api/lists",
	"GET /api/players",
	"GET /api/records",
	"GET /api/stats",
	"POST /api/stats/reset",
	"GET /api/logs",
	"GET /api/logs/download",
	"POST /api/system/firewall",
	"POST /api/system/open-folder",
	"POST /api/system/launch-on-login",
	"POST /api/system/quit",
}
