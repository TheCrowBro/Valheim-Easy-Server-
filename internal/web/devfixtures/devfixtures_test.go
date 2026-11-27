package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/worlds"
)

type fixtures struct {
	t  *testing.T
	st *state
	h  http.Handler
}

func newFixtures(t *testing.T, change ...func(*options)) *fixtures {
	t.Helper()
	opt := options{GOOS: "windows", GOARCH: "amd64", FreeMB: 12000, Step: 5 * time.Millisecond, Dir: t.TempDir()}
	for _, c := range change {
		c(&opt)
	}
	st := newState(opt)
	return &fixtures{t: t, st: st, h: newHandler(st, defaultUI())}
}

// call asks as the panel does: from this computer, with X-VES on changes.
func (f *fixtures) call(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	if method != http.MethodGet {
		req.Header.Set("X-VES", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// strict decodes an answer as exactly the app's type: a key the type
// doesn't have fails.
func strict[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("doesn't read as %T: %v\n%s", v, err, rec.Body)
	}
	return v
}

func (f *fixtures) status() manager.Status {
	f.t.Helper()
	return strict[manager.Status](f.t, f.call("GET", "/api/status", ""))
}

func (f *fixtures) gameOf(id string) (manager.GameStatus, bool) {
	for _, g := range f.status().Games {
		if g.ID == id {
			return g, true
		}
	}
	return manager.GameStatus{}, false
}

func (f *fixtures) waitFor(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

// appRoutes are the app's route patterns, read from internal/web's source:
// every new route (newRoutes' lists) and the old GET routes.
func appRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for file, re := range map[string]*regexp.Regexp{
		"gameroutes.go": regexp.MustCompile(`\{"((?:GET|POST|DELETE) /api/[^"]+)"`),
		"approutes.go":  regexp.MustCompile(`\{"((?:GET|POST|DELETE) /api/[^"]+)"`),
		"api.go":        regexp.MustCompile(`a\("(GET /api/[^"]+)"`),
	} {
		b, err := os.ReadFile(filepath.Join("..", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) < 50 {
		t.Fatalf("found only %d routes in internal/web", len(out))
	}
	slices.Sort(out)
	return out
}

// Every route of the app has a made-up answer here, of the kind the app
// gives, so the panel can be built against all of them.
func TestEveryAppRouteHasAnAnswer(t *testing.T) {
	f := newFixtures(t)
	legacy := game.Legacy().Info().ID
	var up struct{ ID string }
	json.Unmarshal(f.call("POST", "/api/games/"+legacy+"/uploads", "").Body.Bytes(), &up)
	for _, p := range appRoutes(t) {
		method, path, _ := strings.Cut(p, " ")
		path = strings.NewReplacer("{id}", legacy, "{game}", legacy, "{upload}", up.ID).Replace(path)
		if strings.Contains(path, "download") {
			path += "?name=Midgard&id=auto-1"
		}
		body := `{}`
		if strings.HasSuffix(path, "/files") {
			continue // multipart: TestUpload
		}
		rec := f.call(method, path, body)
		switch rec.Code {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden, http.StatusInternalServerError:
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct == "" {
			t.Errorf("%s: no Content-Type", p)
		}
	}
}

// The answers are the app's own types, so the panel built on them works
// with the app.
func TestAnswersAreTheAppsTypes(t *testing.T) {
	f := newFixtures(t)
	st := f.status()
	if len(st.Games) < 4 {
		t.Fatalf("only %d made-up games", len(st.Games))
	}
	strict[[]manager.CatalogGame](t, f.call("GET", "/api/catalog", ""))
	strict[manager.RouterView](t, f.call("GET", "/api/router", ""))
	for _, g := range st.Games {
		base := "/api/games/" + g.ID
		s := strict[manager.GameSettingsView](t, f.call("GET", base+"/settings", ""))
		if s.Values.Server["serverName"] == "" || len(s.Ports) == 0 {
			t.Errorf("%s settings: %+v", g.ID, s)
		}
		wv := strict[manager.GameWorldsView](t, f.call("GET", base+"/worlds", ""))
		if wv.Selected == "" || len(wv.Worlds) == 0 {
			t.Errorf("%s worlds: %+v", g.ID, wv)
		}
		strict[[]manager.GameList](t, f.call("GET", base+"/lists", ""))
		strict[struct {
			Players []manager.KnownPlayer `json:"players"`
		}](t, f.call("GET", base+"/players", ""))
		strict[struct {
			Lines []manager.LogLine `json:"lines"`
			Last  int64             `json:"last"`
		}](t, f.call("GET", base+"/logs?after=0", ""))
		if rec := f.call("GET", base+"/records?period=week", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"players"`) {
			t.Errorf("%s records: %d %s", g.ID, rec.Code, rec.Body)
		}
		if rec := f.call("GET", base+"/stats?range=1h", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"samples"`) {
			t.Errorf("%s stats: %d", g.ID, rec.Code)
		}
		if g.Join.LAN == nil || g.Join.Steps == nil || g.Ports == nil || g.Players == nil || g.Reminder == nil || g.Warnings == nil {
			t.Errorf("%s has a null list: %+v", g.ID, g)
		}
	}
}

// The made-up games show every state the new panel draws.
func TestFixturesShowEveryState(t *testing.T) {
	f := newFixtures(t, func(o *options) { o.GOOS, o.GOARCH = "darwin", "amd64" })
	st := f.status()
	has := func(what string, ok func(g manager.GameStatus) bool) {
		t.Helper()
		if !slices.ContainsFunc(st.Games, ok) {
			t.Errorf("no made-up game is %s", what)
		}
	}
	has("starred and online with players", func(g manager.GameStatus) bool {
		return g.Starred && g.State == manager.StateOnline && g.Count > 0 && g.Join.How != ""
	})
	has("starred and stopped", func(g manager.GameStatus) bool { return g.Starred && g.State == manager.StateStopped })
	has("running without a star", func(g manager.GameStatus) bool { return !g.Starred && g.State == manager.StateOnline })
	has("stopped without a star", func(g manager.GameStatus) bool { return !g.Starred && g.State == manager.StateStopped && g.Installed })
	has("not installed", func(g manager.GameStatus) bool { return !g.Installed })
	has("one that needs Windows on this Mac", func(g manager.GameStatus) bool {
		return g.CanRun == game.CantRun && g.NeedsText != ""
	})
	if st.Online < 2 || st.Playing < 2 || len(st.Stars) < 2 || !st.Sidebar.ActiveOpen || st.Sidebar.InactiveOpen || st.Machine.Hostname == "" {
		t.Errorf("status: online %d, playing %d, stars %v, sidebar %+v, machine %+v", st.Online, st.Playing, st.Stars, st.Sidebar, st.Machine)
	}
	catalog := strict[[]manager.CatalogGame](t, f.call("GET", "/api/catalog", ""))
	if !slices.ContainsFunc(catalog, func(c manager.CatalogGame) bool { return c.CanRun == game.CantRun && c.NeedsText != "" }) ||
		!slices.ContainsFunc(catalog, func(c manager.CatalogGame) bool { return c.Added }) ||
		!slices.ContainsFunc(catalog, func(c manager.CatalogGame) bool { return !c.Added }) {
		t.Errorf("catalog = %+v", catalog)
	}
	for _, c := range catalog {
		if !slices.ContainsFunc(c.Schema, func(fl game.Field) bool { return fl.Add }) && len(c.Schema) > 0 {
			t.Errorf("%s: the catalog's schema has fields Add a game doesn't ask", c.ID)
		}
	}
}

// A start that doesn't fit in the free memory shows the warning, with the
// running games to stop; its buttons start the game.
func TestMemoryWarning(t *testing.T) {
	f := newFixtures(t, func(o *options) { o.FreeMB = 1000 })
	var stopped manager.GameStatus
	for _, g := range f.status().Games {
		if g.State == manager.StateStopped && g.Installed && g.CanRun == game.Runs {
			stopped = g
			break
		}
	}
	if stopped.ID == "" {
		t.Fatal("no stopped game to start")
	}
	rec := f.call("POST", "/api/games/"+stopped.ID+"/start", `{"memory":""}`)
	var a struct {
		Error  string              `json:"error"`
		Action string              `json:"action"`
		Data   manager.MemoryShort `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &a)
	if rec.Code != http.StatusBadRequest || a.Action != "memory" || a.Data.NeedMB != stopped.NeedMB || len(a.Data.Running) == 0 || !strings.Contains(a.Error, "memory") {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	other := a.Data.Running[0].ID
	if rec := f.call("POST", "/api/games/"+stopped.ID+"/start", `{"memory":"stop:`+other+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("stop and start = %d %s", rec.Code, rec.Body)
	}
	if g, _ := f.gameOf(other); g.State != manager.StateStopped {
		t.Fatalf("%s wasn't stopped: %s", other, g.State)
	}
	f.waitFor("the start", func() bool { g, _ := f.gameOf(stopped.ID); return g.State == manager.StateOnline })
}

// Stars, the sidebar and a settings save change only what they're asked.
func TestChanges(t *testing.T) {
	f := newFixtures(t)
	st := f.status()
	g := st.Games[1]
	f.call("POST", "/api/games/"+g.ID+"/star", `{"on":false}`)
	if s := f.status(); slices.Contains(s.Stars, g.ID) {
		t.Fatalf("still starred: %v", s.Stars)
	}
	if rec := f.call("POST", "/api/games/"+g.ID+"/star", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a star without on = %d", rec.Code)
	}
	f.call("POST", "/api/app/sidebar", `{"inactiveOpen":true}`)
	if s := f.status().Sidebar; !s.ActiveOpen || !s.InactiveOpen {
		t.Fatalf("sidebar = %+v", s)
	}

	before := strict[manager.GameSettingsView](t, f.call("GET", "/api/games/"+g.ID+"/settings", ""))
	if rec := f.call("POST", "/api/games/"+g.ID+"/settings", `{"values":{"serverName":"Late Owls"},"autoStopMinutes":0}`); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	after := strict[manager.GameSettingsView](t, f.call("GET", "/api/games/"+g.ID+"/settings", ""))
	want := before.Values.Server.Clone()
	want["serverName"] = "Late Owls"
	if after.AutoStopMinutes != 0 || after.StartWithApp != before.StartWithApp || after.Discord != before.Discord ||
		fmt.Sprint(after.Values.Server) != fmt.Sprint(want) {
		t.Fatalf("after the save: %+v", after)
	}
	rec := f.call("POST", "/api/games/"+g.ID+"/settings", `{"values":{"nosuchsetting":"1"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"server.nosuchsetting"`) {
		t.Fatalf("an unknown setting = %d %s", rec.Code, rec.Body)
	}
}

// Add a game, its install, a start and a stop, and removing it.
func TestAddStartStopRemove(t *testing.T) {
	f := newFixtures(t)
	var pick manager.CatalogGame
	for _, c := range strict[[]manager.CatalogGame](t, f.call("GET", "/api/catalog", "")) {
		if !c.Added && c.CanRun == game.Runs {
			pick = c
			break
		}
	}
	if pick.ID == "" {
		t.Skip("every game is added already")
	}
	if rec := f.call("POST", "/api/games", `{"game":"`+pick.ID+`","world":{"mode":"new"}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"server.serverName"`) {
		t.Fatalf("add without a name = %d %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(map[string]any{"game": pick.ID, "serverName": "Night Owls", "password": "nightowls42", "eula": true,
		"world": map[string]any{"mode": "new", "name": "Riverbend", "values": map[string]any{"seed": 12345}}})
	added := strict[manager.AddedGame](t, f.call("POST", "/api/games", string(body)))
	f.waitFor("the install", func() bool { g, _ := f.gameOf(added.ID); return g.Installed })
	if g, _ := f.gameOf(added.ID); g.Starred || g.World != "Riverbend" || g.ServerName != "Night Owls" {
		t.Fatalf("added: %+v", g)
	}
	if rec := f.call("POST", "/api/games/"+added.ID+"/start", `{"memory":"anyway"}`); rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	f.waitFor("online", func() bool { g, _ := f.gameOf(added.ID); return g.State == manager.StateOnline })
	f.call("POST", "/api/games/"+added.ID+"/stop", `{}`)
	f.waitFor("stopped", func() bool { g, _ := f.gameOf(added.ID); return g.State == manager.StateStopped })
	f.call("POST", "/api/games/"+added.ID+"/remove", `{}`)
	f.waitFor("removed", func() bool { _, still := f.gameOf(added.ID); return !still })
}

func TestUpload(t *testing.T) {
	f := newFixtures(t)
	legacy := game.Legacy().Info().ID
	var up struct{ ID string }
	json.Unmarshal(f.call("POST", "/api/games/"+legacy+"/uploads", "").Body.Bytes(), &up)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", "World/world.db")
	w.Write([]byte("data"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/games/"+legacy+"/uploads/"+up.ID+"/files", &buf)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("X-VES", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files":1`) {
		t.Fatalf("files = %d %s", rec.Code, rec.Body)
	}
	scan := strict[struct {
		Candidates []worlds.Candidate `json:"candidates"`
	}](t, f.call("POST", "/api/games/"+legacy+"/uploads/"+up.ID+"/scan", ""))
	if len(scan.Candidates) != 1 {
		t.Fatalf("scan = %+v", scan)
	}
}

// The guard refuses what the app's does.
func TestGuard(t *testing.T) {
	f := newFixtures(t)
	req := httptest.NewRequest("POST", "/api/power/stop-all", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a change without X-VES = %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "192.0.2.10:50000"
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another computer = %d", rec.Code)
	}
	if f.status().Online == 0 {
		t.Fatal("a refused stop-all stopped the games")
	}
}

// The panel's own files are served from internal/web/ui.
func TestPanelFiles(t *testing.T) {
	f := newFixtures(t)
	if rec := f.call("GET", "/", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("index = %d", rec.Code)
	}
	if rec := f.call("GET", "/assets/app.js", ""); rec.Code != http.StatusOK {
		t.Fatalf("app.js = %d", rec.Code)
	}
	if rec := f.call("GET", "/api/nothing-here", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("an unknown read = %d", rec.Code)
	}
}
