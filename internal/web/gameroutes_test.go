package web

// The new panel's routes (docs/dev/multigame/spec.md 6.2), asked the way
// the panel asks them, of a real manager that runs the legacy game with
// the stand-in Valheim server (cmd/fakevalheim).
//
// The manager's R3 methods are being filled in by other streams. Where one
// still answers "This part of the app isn't finished yet." (errNotYet), a
// test checks that the route answers exactly what the manager does, and
// checks what the spec says the route does as soon as the manager does it,
// so these tests need no change when those streams land.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math/rand"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/worlds"
)

// fakeValheim is the stand-in Valheim server, built once for these tests.
var fakeValheim string

func TestMain(m *testing.M) {
	// Like the real app: a Ctrl+C meant for the server never stops the
	// tests.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)

	dir, err := os.MkdirTemp("", "web-fakevalheim")
	if err != nil {
		panic(err)
	}
	fakeValheim = filepath.Join(dir, "fakevalheim")
	if runtime.GOOS == "windows" {
		fakeValheim += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", fakeValheim, "../../cmd/fakevalheim").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the stand-in Valheim server: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// notYet is the manager's answer for an R3 part another stream hasn't
// built yet (manager.errNotYet).
const notYet = "This part of the app isn't finished yet."

// gameServer is a control panel whose manager installs and runs the
// stand-in Valheim server, and whose Discord is a stand-in that keeps what
// is posted.
type gameServer struct {
	*routesServer
	id    string // the legacy game's instance
	posts *postLog
}

type postLog struct {
	mu    sync.Mutex
	posts []string
}

func (p *postLog) add(s string) {
	p.mu.Lock()
	p.posts = append(p.posts, s)
	p.mu.Unlock()
}

func (p *postLog) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.posts)
}

func newGameServer(t *testing.T, change ...func(*manager.Options)) *gameServer {
	t.Helper()
	dir := t.TempDir()
	posts := &postLog{}
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posts.add(r.Method + " " + r.URL.Path + " " + string(b))
		w.Write([]byte(`{"id": "1", "guild_id": "g1", "channel_id": "2"}`))
	}))
	t.Cleanup(discord.Close)
	logger := log.New(io.Discard, "", 0)
	opt := manager.Options{
		DataDir: filepath.Join(dir, "data"), Logger: logger, NoPublicIP: true, DiscordURL: discord.URL,
		NewInstaller: func(p config.Paths) steamcmd.Installer {
			return &steamcmd.Fake{Source: fakeValheim, InstallDir: p.Server, ExeName: config.ServerExeName(), Duration: 50 * time.Millisecond}
		},
		SampleInterval: 200 * time.Millisecond, RestartDelay: 500 * time.Millisecond,
		Hibernate: func() error { return nil }, HibernateStatus: func() (bool, string) { return true, "" },
		Sleep: func() error { return nil }, SleepStatus: func() (bool, string) { return true, "" },
		NetAdapters: func() []platform.NetAdapter {
			return []platform.NetAdapter{{Name: "Ethernet", Description: "Stand-in", MAC: "3C:07:54:5A:21:9F",
				IPv4: []string{"192.168.1.20"}, Wired: true, Connected: true, WakeArmed: true}}
		},
		WakerBinary: func() ([]byte, error) { return []byte("stand-in waker"), nil },
	}
	for _, c := range change {
		c(&opt)
	}
	m, err := manager.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	s.Port = 20000 + rand.Intn(20000)
	s.SaveIntervalMinutes = 1
	s.UpdateOnStart = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	m.Start()
	t.Cleanup(m.Close)
	t.Cleanup(func() { // runs before Close
		if m.ForceStop() == nil {
			waitUntil(t, "the forced stop", 15*time.Second, func() bool { return m.Status().State != manager.StateOnline })
		}
	})
	rs := &routesServer{t: t, s: New(Options{Manager: m, Logger: logger, Version: "test"}), m: m, dir: dir}
	return &gameServer{routesServer: rs, id: game.Legacy().Info().ID, posts: posts}
}

// path is a route of the legacy game: "/api/games/<id>" + rest.
func (ts *gameServer) path(rest string) string { return "/api/games/" + ts.id + rest }

// gameStatus is the legacy game's entry in Status.games.
func (ts *gameServer) gameStatus() manager.GameStatus {
	ts.t.Helper()
	for _, g := range ts.m.Status().Games {
		if g.ID == ts.id {
			return g
		}
	}
	ts.t.Fatalf("Status has no game %q", ts.id)
	return manager.GameStatus{}
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// answer is an error answer's JSON.
type answer struct {
	OK     bool              `json:"ok"`
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields"`
	Action string            `json:"action"`
	Data   json.RawMessage   `json:"data"`
	Job    int               `json:"job"`
}

func answerOfRec(t *testing.T, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	var a answer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("not a JSON object (%d): %v\n%s", rec.Code, err, rec.Body)
	}
	return a
}

// built reports whether the manager did the request, rather than answering
// that this part isn't built yet.
func built(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code == http.StatusBadRequest && answerOfRec(t, rec).Error == notYet {
		t.Logf("not built yet in the manager: %s", rec.Body)
		return false
	}
	return true
}

// wantOK fails unless a route answered {ok: true}.
func wantOK(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK || !answerOfRec(t, rec).OK {
		t.Fatalf("%s = %d %s", what, rec.Code, rec.Body)
	}
}

// wantRefused fails unless a route refused with 400 and a message.
func wantRefused(t *testing.T, what string, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	a := answerOfRec(t, rec)
	if rec.Code != http.StatusBadRequest || a.Error == "" {
		t.Fatalf("%s = %d %s, want it refused", what, rec.Code, rec.Body)
	}
	return a
}

// answersAsManager checks that a route answered as the manager's method
// answers the same request: err's message with 400 (409 when another job
// is in the way), or success. Used for requests the manager refuses the
// same way each time, or whose success changes nothing a second call
// would see.
func answersAsManager(t *testing.T, what string, rec *httptest.ResponseRecorder, err error) {
	t.Helper()
	if err == nil {
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s, but the manager does it", what, rec.Code, rec.Body)
		}
		return
	}
	want := http.StatusBadRequest
	var be *manager.BusyError
	if errors.As(err, &be) {
		want = http.StatusConflict
	}
	if a := answerOfRec(t, rec); rec.Code != want || a.Error != err.Error() {
		t.Fatalf("%s = %d %q, the manager answers %d %q", what, rec.Code, a.Error, want, err.Error())
	}
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields() // the answer is exactly the manager's type
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("answer doesn't read as %T: %v\n%s", v, err, rec.Body)
	}
	return v
}

// worldFolder is a Valheim 1.0 world folder as multipart form data.
func worldFolder(name string) (io.Reader, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for file, data := range map[string][]byte{
		name + "/_main.1.fwl2": worlds.BuildMeta(worlds.Meta{Version: 37, Name: name, SeedName: "AbC123", Seed: 1, UID: 2}, true),
		name + "/_main.1.db2":  []byte("world data"),
	} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+file+`"`)
		w, _ := mw.CreatePart(h)
		w.Write(data)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// --- guard -----------------------------------------------------------------------

// specRoutes are the new routes of spec 6.2's table (with the per-game
// upload routes it calls "…", and the Stats reset the old Stats page has).
// POST /api/discord/test is an old route that gained "game".
var specRoutes = []string{
	"GET /api/catalog",
	"GET /api/catalog/{game}/local",
	"POST /api/games",
	"POST /api/games/{id}/remove",
	"POST /api/games/{id}/star",
	"POST /api/app/sidebar",
	"POST /api/games/{id}/start",
	"POST /api/games/{id}/stop",
	"POST /api/games/{id}/restart",
	"POST /api/games/{id}/force-stop",
	"POST /api/games/{id}/cancel",
	"POST /api/games/{id}/install",
	"GET /api/games/{id}/settings",
	"POST /api/games/{id}/settings",
	"POST /api/games/{id}/ports/pick",
	"GET /api/games/{id}/worlds",
	"POST /api/games/{id}/worlds/select",
	"POST /api/games/{id}/worlds/create",
	"POST /api/games/{id}/worlds/delete",
	"POST /api/games/{id}/worlds/backup",
	"POST /api/games/{id}/worlds/restore",
	"POST /api/games/{id}/worlds/rename",
	"POST /api/games/{id}/worlds/values",
	"GET /api/games/{id}/worlds/download",
	"GET /api/games/{id}/worlds/backup-download",
	"POST /api/games/{id}/uploads",
	"POST /api/games/{id}/uploads/{upload}/files",
	"POST /api/games/{id}/uploads/{upload}/scan",
	"POST /api/games/{id}/uploads/{upload}/import",
	"DELETE /api/games/{id}/uploads/{upload}",
	"GET /api/games/{id}/local-worlds",
	"POST /api/games/{id}/local-worlds/import",
	"GET /api/games/{id}/players",
	"GET /api/games/{id}/records",
	"GET /api/games/{id}/lists",
	"POST /api/games/{id}/lists",
	"POST /api/games/{id}/allow",
	"GET /api/games/{id}/stats",
	"POST /api/games/{id}/stats/reset",
	"GET /api/games/{id}/logs",
	"GET /api/games/{id}/logs/download",
	"POST /api/games/{id}/keep-on",
	"GET /api/router",
	"POST /api/router/check",
	"POST /api/app/settings",
	"POST /api/discord/main",
	"POST /api/power/stop-all",
}

// Every new route is under /api/, so guard checks it: other websites and
// other computers are turned away, and a change needs the panel's header
// and, when the browser sends one, the panel's origin. None of the refused
// requests reaches the manager.
func TestNewRoutesAreGuarded(t *testing.T) {
	ts := newRoutesServer(t)
	id := game.Legacy().Info().ID
	before := ts.m.Status()
	settings := ts.m.Settings()
	routes := ts.s.newRoutes()
	have := map[string]bool{}
	for _, rt := range routes {
		have[rt.pattern] = true
	}
	for _, p := range specRoutes {
		if !have[p] {
			t.Errorf("spec 6.2's %s isn't among the new routes", p)
		}
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		if seen[rt.pattern] {
			t.Errorf("%s is listed twice", rt.pattern)
		}
		seen[rt.pattern] = true
		method, path, _ := strings.Cut(rt.pattern, " ")
		if !strings.HasPrefix(path, "/api/") {
			t.Errorf("%s isn't under /api/, so guard doesn't check its changes", rt.pattern)
			continue
		}
		path = strings.NewReplacer("{id}", id, "{game}", id, "{upload}", "x").Replace(path)
		type attempt struct {
			what    string
			addr    string
			headers map[string]string
		}
		attempts := []attempt{
			{"from another website", "127.0.0.1", map[string]string{"X-VES": "1", "Sec-Fetch-Site": "cross-site"}},
			{"from a website on the same site", "127.0.0.1", map[string]string{"X-VES": "1", "Sec-Fetch-Site": "same-site"}},
			{"from another computer", "192.0.2.10", map[string]string{"X-VES": "1"}},
			{"for another host name", "127.0.0.1", map[string]string{"X-VES": "1", "Host": "evil.example:8456"}},
		}
		if method != http.MethodGet {
			attempts = append(attempts,
				attempt{"without the panel's header", "127.0.0.1", nil},
				attempt{"from another origin", "127.0.0.1", map[string]string{"X-VES": "1", "Origin": "http://evil.example"}})
		}
		for _, a := range attempts {
			req := httptest.NewRequest(method, path, strings.NewReader(`{"on":true,"memory":"anyway","name":"Midgard","webhook":"","activeOpen":false}`))
			req.RemoteAddr = net.JoinHostPort(a.addr, "50000")
			req.Host = "127.0.0.1:8456"
			for k, v := range a.headers {
				if k == "Host" {
					req.Host = v
				} else {
					req.Header.Set(k, v)
				}
			}
			rec := httptest.NewRecorder()
			ts.s.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d %s, want it refused", rt.pattern, a.what, rec.Code, rec.Body)
			}
		}
	}

	// Nothing was done: no job ran, nothing was starred, the sidebar and
	// the settings are as they were.
	after := ts.m.Status()
	if after.Job != nil || len(after.Jobs) != len(before.Jobs) || !slices.Equal(after.Stars, before.Stars) || after.Sidebar != before.Sidebar {
		t.Errorf("a refused request changed something: job %+v, jobs %d → %d, stars %v → %v, sidebar %+v → %+v",
			after.Job, len(before.Jobs), len(after.Jobs), before.Stars, after.Stars, before.Sidebar, after.Sidebar)
	}
	if !reflect.DeepEqual(ts.m.Settings(), settings) {
		t.Error("a refused request changed the settings")
	}
}

// --- reading requests ----------------------------------------------------------------

func readBodyOf(t *testing.T, body string, v any) error {
	t.Helper()
	return readBody(httptest.NewRequest("POST", "/", strings.NewReader(body)), v)
}

func TestReadBody(t *testing.T) {
	// No body is the same as {}.
	var o manager.StartOptions
	if err := readBodyOf(t, "", &o); err != nil || o.Memory != "" {
		t.Fatalf("empty body: %v %+v", err, o)
	}
	if err := readBodyOf(t, " \n", &o); err != nil {
		t.Fatalf("blank body: %v", err)
	}

	// Numbers and true/false in "values" are read as the text a setting
	// is, at any depth; anything else keeps its type.
	var add manager.AddGameRequest
	err := readBodyOf(t, `{"game":"x","values":{"maxPlayers":8,"pvp":true,"motd":"Hi","ratio":1.50},
		"world":{"mode":"new","name":"W","values":{"seed":-12345678901234567890,"hardcore":false}},"star":true}`, &add)
	if err != nil {
		t.Fatal(err)
	}
	wantServer := game.Values{"maxPlayers": "8", "pvp": "true", "motd": "Hi", "ratio": "1.50"}
	wantWorld := game.Values{"seed": "-12345678901234567890", "hardcore": "false"}
	if !reflect.DeepEqual(add.Values, wantServer) || !reflect.DeepEqual(add.World.Values, wantWorld) || !add.Star || add.World.Mode != "new" {
		t.Fatalf("add = %+v", add)
	}
	var ch manager.GameSettingsChange
	if err := readBodyOf(t, `{"ports":{"game":25565},"values":{"port":2457}}`, &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Ports["game"] != 25565 || ch.Values["port"] != "2457" {
		t.Fatalf("change = %+v", ch)
	}

	// What can't be read says so.
	for _, bad := range []string{`{`, `{"memory": 3}`, `{} {}`, `[1, 2]`, `{"values": {"a": {"b": 1}}}`, `{"ports": {"game": "x"}}`} {
		var o struct {
			manager.StartOptions
			manager.GameSettingsChange
		}
		if err := readBodyOf(t, bad, &o); err == nil || !strings.Contains(err.Error(), "couldn't be read") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	var big manager.StartOptions
	if err := readBodyOf(t, `{"memory":"`+strings.Repeat("x", maxBody)+`"}`, &big); err == nil {
		t.Error("a request over the limit was read")
	}
}

// A settings save changes only what it has: a part that's left out stays
// nil (unchanged), and one that's there is set even when false or 0.
func TestSettingsChangeIsPartial(t *testing.T) {
	var none manager.GameSettingsChange
	if err := readBodyOf(t, `{}`, &none); err != nil || !reflect.DeepEqual(none, manager.GameSettingsChange{}) {
		t.Fatalf("{} = %+v %v", none, err)
	}
	var ch manager.GameSettingsChange
	if err := readBodyOf(t, `{"autoStopMinutes":0,"disabled":false,"discord":{"on":false}}`, &ch); err != nil {
		t.Fatal(err)
	}
	if ch.AutoStopMinutes == nil || *ch.AutoStopMinutes != 0 || ch.Disabled == nil || *ch.Disabled ||
		ch.StartWithApp != nil || ch.EULA != nil || ch.Values != nil || ch.Ports != nil {
		t.Fatalf("change = %+v", ch)
	}
	if d := ch.Discord; d == nil || d.On == nil || *d.On || d.Webhook != nil || d.Stopped != nil || d.Daily != nil || d.Password != nil {
		t.Fatalf("discord change = %+v", ch.Discord)
	}
	var app manager.AppSettingsChange
	if err := readBodyOf(t, `{"idleMinutes":45}`, &app); err != nil || app.IdleMinutes == nil || *app.IdleMinutes != 45 ||
		app.IdleHibernate != nil || app.AutoUpdate != nil || app.StartAfterWake != nil || app.RouterPorts != nil {
		t.Fatalf("app change = %+v %v", app, err)
	}
}

// --- errors ---------------------------------------------------------------------------

func TestFailAnswers(t *testing.T) {
	failed := func(err error) (int, map[string]json.RawMessage) {
		rec := httptest.NewRecorder()
		fail(rec, err)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
		return rec.Code, body
	}

	// The memory refusal carries what the warning shows (spec 6.1).
	short := manager.MemoryShort{NeedMB: 10240, FreeMB: 6349, Running: []manager.RunningGame{{ID: "g1", Name: "Game One", MemMB: 3170}}}
	code, body := failed(fmt.Errorf("start: %w", &manager.ActionError{Msg: "There may not be enough memory.", Action: "memory", Data: short}))
	var data manager.MemoryShort
	if code != http.StatusBadRequest || string(body["error"]) != `"There may not be enough memory."` || string(body["action"]) != `"memory"` ||
		json.Unmarshal(body["data"], &data) != nil || !reflect.DeepEqual(data, short) {
		t.Fatalf("memory refusal = %d %v", code, body)
	}
	if !strings.Contains(string(body["data"]), `"needMB":10240`) || !strings.Contains(string(body["data"]), `"memMB":3170`) {
		t.Fatalf("data = %s", body["data"])
	}

	// An action without data answers exactly as before: no "data".
	code, body = failed(&manager.ActionError{Msg: "Install it first.", Action: "install"})
	if _, has := body["data"]; code != http.StatusBadRequest || has || len(body) != 2 {
		t.Fatalf("action without data = %d %v", code, body)
	}

	// Field errors of the new routes, keyed by scope, answer like the old
	// ones.
	code, body = failed(game.FieldErrors{"server.password": "Too short.", "newWorld.seed": "Not a number."})
	if code != http.StatusBadRequest || string(body["error"]) != `"Not a number. Too short."` ||
		string(body["fields"]) != `{"newWorld.seed":"Not a number.","server.password":"Too short."}` {
		t.Fatalf("field errors = %d %v", code, body)
	}
	code, body = failed(config.FieldErrors{"password": "Too short."})
	if code != http.StatusBadRequest || string(body["fields"]) != `{"password":"Too short."}` {
		t.Fatalf("old field errors = %d %v", code, body)
	}

	// Another job in the way: 409.
	if code, _ := failed(&manager.BusyError{Title: "installing"}); code != http.StatusConflict {
		t.Fatalf("busy = %d", code)
	}
	if code, body := failed(errors.New("No.")); code != http.StatusBadRequest || string(body["error"]) != `"No."` {
		t.Fatalf("plain error = %d %v", code, body)
	}
}

func TestWorldNameProblem(t *testing.T) {
	legacy := game.Legacy().Info().ID
	// The legacy game: Valheim's own rule where the old routes check it,
	// nothing (the manager's check) where they don't.
	for _, name := range []string{"", "../Midgard", "a:b", "Midgard_backup_auto-1", "CON"} {
		if got, want := worldNameProblem(legacy, name, true), config.WorldNameProblem(name); got != want || got == "" {
			t.Errorf("legacy %q = %q, want %q", name, got, want)
		}
		if got := worldNameProblem(legacy, name, false); got != "" {
			t.Errorf("legacy %q where the old route doesn't check = %q", name, got)
		}
	}
	// Any other game: only names that can't be a world in a folder.
	other := "another-game"
	for _, name := range []string{"", "  ", "a/b", `a\b`, ".", "..", "a\x00b", strings.Repeat("x", 256)} {
		if worldNameProblem(other, name, true) == "" {
			t.Errorf("%q was let through", name)
		}
	}
	for _, name := range []string{"Riverbend", "My World: 2", "world_backup_1", "-x", "CON", "Ünïcödé"} {
		if msg := worldNameProblem(other, name, true); msg != "" {
			t.Errorf("%q refused: %s", name, msg)
		}
	}
}

// --- one game's routes ------------------------------------------------------------------

// The legacy game through the new routes: install, start (with the memory
// step), players and logs while it runs, restart, stop and stop-all, as
// its old routes do.
func TestGameRoutesRunTheLegacyGame(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3,die:Astrid@0.6")
	ts := newGameServer(t)

	// A game this computer doesn't have.
	for _, p := range []string{"/start", "/stop", "/install", "/settings", "/worlds", "/players", "/logs"} {
		method := "POST"
		if p == "/settings" || p == "/worlds" || p == "/players" || p == "/logs" {
			method = "GET"
		}
		rec := ts.call(method, "/api/games/nosuchgame"+p, `{}`)
		if a := wantRefused(t, method+" "+p+" of an unknown game", rec); !strings.Contains(a.Error, "nosuchgame") {
			t.Errorf("%s of an unknown game: %q", p, a.Error)
		}
	}

	// Before it's installed, a start offers the install, as on Home.
	if a := wantRefused(t, "start before the install", ts.call("POST", ts.path("/start"), `{}`)); a.Action != "install" {
		t.Fatalf("start before the install: %+v", a)
	}
	wantOK(t, "install", ts.call("POST", ts.path("/install"), ``)) // no body: {repair: false}
	ts.idle()
	if !ts.gameStatus().Installed {
		t.Fatal("not installed after the install job")
	}
	if !strings.Contains(ts.call("GET", ts.path("/logs/download"), "").Body.String(), "no server log") {
		t.Error("the log download before any run doesn't say there's none")
	}

	// Start with the memory check. If the manager finds too little free
	// memory here, the refusal carries the warning's data, and "anyway"
	// starts it as the warning's button does.
	rec := ts.call("POST", ts.path("/start"), `{"memory":""}`)
	if rec.Code != http.StatusOK {
		a := wantRefused(t, "start", rec)
		if a.Action != "memory" {
			t.Fatalf("start = %s", rec.Body)
		}
		var short manager.MemoryShort
		if err := json.Unmarshal(a.Data, &short); err != nil || short.NeedMB <= 0 || short.Running == nil {
			t.Fatalf("memory refusal without its data: %s", rec.Body)
		}
		wantOK(t, "start anyway", ts.call("POST", ts.path("/start"), `{"memory":"anyway"}`))
	}
	waitUntil(t, "the server online", 30*time.Second, func() bool { return ts.gameStatus().State == manager.StateOnline })
	ts.idle()
	if j := ts.m.Status().Job; j != nil && j.Error != "" {
		t.Fatalf("start job: %s", j.Error)
	}
	if st := ts.m.Status(); st.State != manager.StateOnline || st.Online != 1 {
		t.Fatalf("status: state %s, online %d", st.State, st.Online)
	}

	// While it runs: its players, records, log and stats answer as the
	// old routes do.
	waitUntil(t, "Astrid in the players", 20*time.Second, func() bool {
		return strings.Contains(ts.call("GET", ts.path("/players"), "").Body.String(), `"Astrid"`)
	})
	type playersAnswer struct {
		Players []manager.KnownPlayer `json:"players"`
	}
	oldPlayers := decodeJSON[playersAnswer](t, ts.call("GET", "/api/players", ""))
	newPlayers := decodeJSON[playersAnswer](t, ts.call("GET", ts.path("/players"), ""))
	if !reflect.DeepEqual(oldPlayers.Players, newPlayers.Players) {
		t.Errorf("players: old %+v, new %+v", oldPlayers.Players, newPlayers.Players)
	}
	if rec := ts.call("GET", ts.path("/records?period=today"), ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Astrid"`) {
		t.Errorf("records while Astrid plays = %d %s", rec.Code, rec.Body)
	}
	wantRefused(t, "records for an unknown period", ts.call("GET", ts.path("/records?period=forever"), ""))

	waitUntil(t, "log lines", 10*time.Second, func() bool {
		return strings.Contains(ts.call("GET", ts.path("/logs?after=0"), "").Body.String(), `"text"`)
	})
	if old, now := ts.call("GET", "/api/logs?after=0", ""), ts.call("GET", ts.path("/logs?after=0"), ""); old.Body.String() != now.Body.String() {
		t.Errorf("logs: old %s, new %s", old.Body, now.Body)
	}
	logs := decodeJSON[struct {
		Lines []manager.LogLine `json:"lines"`
		Last  int64             `json:"last"`
	}](t, ts.call("GET", ts.path("/logs?after=0"), ""))
	if len(logs.Lines) == 0 || logs.Last != logs.Lines[len(logs.Lines)-1].Seq {
		t.Fatalf("logs = %+v", logs)
	}
	if later := decodeJSON[struct {
		Lines []manager.LogLine `json:"lines"`
		Last  int64             `json:"last"`
	}](t, ts.call("GET", ts.path(fmt.Sprintf("/logs?after=%d", logs.Last)), "")); later.Lines == nil || later.Last < logs.Last {
		t.Fatalf("logs after the last line = %+v", later)
	}
	if rec := ts.call("GET", ts.path("/logs/download"), ""); rec.Code != http.StatusOK ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || !strings.Contains(rec.Header().Get("Content-Disposition"), "server-") {
		t.Fatalf("log download = %d %v", rec.Code, rec.Header())
	}

	waitUntil(t, "a usage sample", 10*time.Second, func() bool {
		return strings.Contains(ts.call("GET", ts.path("/stats?range=15m"), "").Body.String(), `"running":true`)
	})
	oldStats, newStats := shapeOf(t, ts.call("GET", "/api/stats?range=15m", "").Body.Bytes()), shapeOf(t, ts.call("GET", ts.path("/stats?range=15m"), "").Body.Bytes())
	if p := shapeProblems(oldStats, newStats, func(string) bool { return false }); len(p) > 0 {
		t.Errorf("stats differ from the old route's: %v", p)
	}
	wantOK(t, "stats reset", ts.call("POST", ts.path("/stats/reset"), `{}`))

	// Restart, then stop.
	wantOK(t, "restart", ts.call("POST", ts.path("/restart"), `{}`))
	ts.idle()
	waitUntil(t, "the server online again", 30*time.Second, func() bool { return ts.gameStatus().State == manager.StateOnline })
	wantOK(t, "stop", ts.call("POST", ts.path("/stop"), ``))
	ts.idle()
	waitUntil(t, "the server stopped", 30*time.Second, func() bool { return ts.gameStatus().State == manager.StateStopped })
	answersAsManager(t, "force stop of a stopped game", ts.call("POST", ts.path("/force-stop"), `{}`), ts.m.ForceStopGame(ts.id))
	wantOK(t, "cancel with no job", ts.call("POST", ts.path("/cancel"), `{}`))

	// Now that nobody plays, the records stay put: both routes give the
	// same answer.
	for _, q := range []string{"", "?period=today", "?period=week", "?period=all"} {
		old, now := ts.call("GET", "/api/records"+q, ""), ts.call("GET", ts.path("/records"+q), "")
		if now.Code != http.StatusOK || old.Body.String() != now.Body.String() {
			t.Errorf("records%s: old %d %s, new %d %s", q, old.Code, old.Body, now.Code, now.Body)
		}
	}

	// Stop all games, from the top bar or the tray.
	wantOK(t, "start anyway", ts.call("POST", ts.path("/start"), `{"memory":"anyway"}`))
	waitUntil(t, "the server online", 30*time.Second, func() bool { return ts.gameStatus().State == manager.StateOnline })
	ts.idle()
	wantOK(t, "stop all", ts.call("POST", "/api/power/stop-all", `{}`))
	waitUntil(t, "every game stopped", 30*time.Second, func() bool { return ts.m.Status().Online == 0 })
	ts.idle()
	wantOK(t, "stop all with nothing running", ts.call("POST", "/api/power/stop-all", ``))
}

// The legacy game's worlds through the new routes: an upload, a backup,
// both downloads, restore, select, rename and delete answer as the old
// routes do, and the names the old routes refuse are refused.
func TestGameWorldRoutes(t *testing.T) {
	ts := newGameServer(t)

	// An upload of a world folder.
	var up struct{ ID string }
	json.Unmarshal(ts.call("POST", ts.path("/uploads"), ``).Body.Bytes(), &up)
	if up.ID == "" {
		t.Fatal("no upload id")
	}
	body, contentType := worldFolder("Vanaheim")
	if rec := ts.send("POST", ts.path("/uploads/"+up.ID+"/files"), body, contentType); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files":2`) {
		t.Fatalf("files = %d %s", rec.Code, rec.Body)
	}
	scan := decodeJSON[struct {
		Candidates []worlds.Candidate `json:"candidates"`
	}](t, ts.call("POST", ts.path("/uploads/"+up.ID+"/scan"), ``))
	if len(scan.Candidates) != 1 || scan.Candidates[0].Name != "Vanaheim" {
		t.Fatalf("scan = %+v", scan)
	}
	rec := ts.call("POST", ts.path("/uploads/"+up.ID+"/import"), `{"choices":[{"id":"`+scan.Candidates[0].ID+`","action":"import"}]}`)
	if a := answerOfRec(t, rec); rec.Code != http.StatusOK || !a.OK || a.Job == 0 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	ts.idle()
	hasWorld := func(name string) bool {
		v, _ := ts.m.Worlds()
		return slices.ContainsFunc(v.Worlds, func(w worlds.World) bool { return w.Name == name })
	}
	if !hasWorld("Vanaheim") {
		t.Fatal("the uploaded world wasn't imported")
	}
	// One thrown away, and one that's gone.
	json.Unmarshal(ts.call("POST", ts.path("/uploads"), `{}`).Body.Bytes(), &up)
	wantOK(t, "discard", ts.call("DELETE", ts.path("/uploads/"+up.ID), ``))
	body, contentType = worldFolder("Alfheim")
	wantRefused(t, "files for a discarded upload", ts.send("POST", ts.path("/uploads/"+up.ID+"/files"), body, contentType))

	// A backup, and both downloads.
	wantOK(t, "backup", ts.call("POST", ts.path("/worlds/backup"), `{"name":"Vanaheim"}`))
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
	rec = ts.call("GET", ts.path("/worlds/backup-download?name=Vanaheim&id="+url.QueryEscape(backup)), "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("backup download = %d %s", rec.Code, rec.Body)
	}
	rec = ts.call("GET", ts.path("/worlds/download?name=Vanaheim"), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="Vanaheim 20`) {
		t.Fatalf("download = %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || !slices.ContainsFunc(zr.File, func(f *zip.File) bool { return f.Name == "Vanaheim/_main.1.fwl2" }) {
		t.Fatalf("download isn't the world's zip: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(ts.m.Paths().Staging, ".download-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	wantRefused(t, "download of a missing world", ts.call("GET", ts.path("/worlds/download?name=Nowhere"), ""))
	if rec := ts.call("GET", ts.path("/worlds/backup-download?name=Vanaheim&id=gone"), ""); rec.Code == http.StatusOK {
		t.Fatal("a missing backup downloaded")
	}

	// Restore, select, rename (a 1.0 world can't be) and delete.
	wantOK(t, "restore", ts.call("POST", ts.path("/worlds/restore"), `{"name":"Vanaheim","backupId":"`+backup+`"}`))
	ts.idle()
	wantOK(t, "select", ts.call("POST", ts.path("/worlds/select"), `{"name":"Vanaheim","restart":false}`))
	if ts.m.Settings().World != "Vanaheim" || ts.gameStatus().World != "Vanaheim" {
		t.Fatalf("selected world = %q", ts.m.Settings().World)
	}
	wantRefused(t, "delete of the selected world", ts.call("POST", ts.path("/worlds/delete"), `{"name":"Vanaheim"}`))
	answersAsManager(t, "rename", ts.call("POST", ts.path("/worlds/rename"), `{"name":"Vanaheim","newName":"Jotunheim"}`),
		ts.m.RenameWorld("Vanaheim", "Jotunheim"))
	wantOK(t, "select back", ts.call("POST", ts.path("/worlds/select"), `{"name":"Midgard"}`))
	wantOK(t, "delete", ts.call("POST", ts.path("/worlds/delete"), `{"name":"Vanaheim"}`))
	ts.idle()
	if hasWorld("Vanaheim") {
		t.Fatal("the world wasn't deleted")
	}

	// The names Valheim's old routes refuse before asking the manager.
	for _, name := range []string{"../Midgard", "", "Midgard_backup_auto-20260101120000"} {
		js, _ := json.Marshal(name)
		want := config.WorldNameProblem(name)
		for _, p := range []string{"/worlds/delete", "/worlds/backup", "/worlds/restore", "/worlds/values"} {
			if a := wantRefused(t, p+" "+name, ts.call("POST", ts.path(p), `{"name":`+string(js)+`,"values":{}}`)); a.Error != want {
				t.Errorf("%s %q: %q, want %q", p, name, a.Error, want)
			}
		}
		if a := wantRefused(t, "download "+name, ts.call("GET", ts.path("/worlds/download?name="+url.QueryEscape(name)), "")); a.Error != want {
			t.Errorf("download %q: %q, want %q", name, a.Error, want)
		}
	}
	ts.idle()
	if ts.m.Status().Job != nil && ts.m.Status().Job.Kind != "delete" {
		t.Fatalf("a refused name started a job: %+v", ts.m.Status().Job)
	}

	// The owner's own worlds.
	old := ts.call("GET", "/api/local-worlds", "")
	if now := ts.call("GET", ts.path("/local-worlds"), ""); now.Code != http.StatusOK || now.Body.String() != old.Body.String() {
		t.Errorf("local worlds: old %s, new %d %s", old.Body, now.Code, now.Body)
	}
	if old, now := ts.call("POST", "/api/local-worlds/import", `{"choices":[]}`), ts.call("POST", ts.path("/local-worlds/import"), `{"choices":[]}`); old.Code != now.Code || old.Body.String() != now.Body.String() {
		t.Errorf("local import: old %d %s, new %d %s", old.Code, old.Body, now.Code, now.Body)
	}

	// The Worlds tab, a new world and a world's settings.
	view, err := ts.m.WorldsGame(ts.id)
	rec = ts.call("GET", ts.path("/worlds"), "")
	if err != nil {
		answersAsManager(t, "worlds", rec, err)
	} else {
		got := decodeJSON[manager.GameWorldsView](t, rec)
		if got.Selected != view.Selected || len(got.Worlds) != len(view.Worlds) || got.Worlds == nil || got.Orphans == nil ||
			got.Schema.World == nil || got.Schema.NewWorld == nil || got.Values == nil {
			t.Fatalf("worlds = %s", rec.Body)
		}
	}
	if rec := ts.call("POST", ts.path("/worlds/create"), `{"name":"Asgard","values":{},"restart":false}`); built(t, rec) {
		wantOK(t, "create", rec)
		if v, err := ts.m.WorldsGame(ts.id); err == nil && !slices.ContainsFunc(v.Worlds, func(w worlds.World) bool { return w.Name == "Asgard" }) && v.Selected != "Asgard" {
			t.Errorf("the new world is neither listed nor selected: %+v", v)
		}
	}
	wantRefused(t, "world values without values", ts.call("POST", ts.path("/worlds/values"), `{"name":"Midgard"}`))
	if rec := ts.call("POST", ts.path("/worlds/values"), `{"name":"Midgard","values":{}}`); built(t, rec) {
		wantOK(t, "world values", rec)
	}
}

// The legacy game's players and lists through the new routes.
func TestGamePlayerRoutes(t *testing.T) {
	ts := newGameServer(t)

	lists := decodeJSON[[]manager.GameList](t, ts.call("GET", ts.path("/lists"), ""))
	old := decodeJSON[map[string][]string](t, ts.call("GET", "/api/lists", ""))
	if len(lists) != len(old) || len(lists) == 0 {
		t.Fatalf("lists: %+v, old %+v", lists, old)
	}
	for _, l := range lists {
		if l.Kind.ID == "" || l.Kind.Label == "" || l.IDs == nil || !slices.Equal(l.IDs, old[l.Kind.ID]) && !(len(l.IDs) == 0 && len(old[l.Kind.ID]) == 0) {
			t.Errorf("list %+v, old %v", l, old[l.Kind.ID])
		}
	}
	wantOK(t, "set a list", ts.call("POST", ts.path("/lists"), `{"kind":"admins","ids":["76561198000000001"]}`))
	if got, _ := ts.m.Lists(); !slices.Equal(got[config.ListKind("admins")], []string{"76561198000000001"}) {
		t.Fatalf("admins = %v", got)
	}
	// A request that doesn't say who is on the list never empties it.
	wantRefused(t, "a list without ids", ts.call("POST", ts.path("/lists"), `{"kind":"admins"}`))
	wantRefused(t, "a list without a kind", ts.call("POST", ts.path("/lists"), `{"ids":[]}`))
	if got, _ := ts.m.Lists(); len(got[config.ListKind("admins")]) != 1 {
		t.Fatalf("admins after the refused requests = %v", got)
	}
	answersAsManager(t, "an unknown list", ts.call("POST", ts.path("/lists"), `{"kind":"nosuchlist","ids":[]}`),
		ts.m.SetListGame(ts.id, "nosuchlist", []string{}))
	wantOK(t, "empty a list", ts.call("POST", ts.path("/lists"), `{"kind":"admins","ids":[]}`))
	if got, _ := ts.m.Lists(); len(got[config.ListKind("admins")]) != 0 {
		t.Fatalf("admins = %v", got)
	}

	// Allow (Minecraft's one click; Valheim has no allow list of names)
	// and Keep it on answer as the manager does.
	wantRefused(t, "allow nobody", ts.call("POST", ts.path("/allow"), `{"name":" "}`))
	answersAsManager(t, "allow", ts.call("POST", ts.path("/allow"), `{"name":"Alex"}`), ts.m.AllowPlayer(ts.id, "Alex"))
	answersAsManager(t, "keep it on", ts.call("POST", ts.path("/keep-on"), `{}`), ts.m.KeepOn(ts.id))

	players := decodeJSON[struct {
		Players []manager.KnownPlayer `json:"players"`
	}](t, ts.call("GET", ts.path("/players"), ""))
	if players.Players == nil {
		t.Fatal("no players list")
	}
}

// A game's Settings tab: the page answers the manager's view, and a save
// changes only what it has, so a page that is out of date never blanks a
// setting (spec 6.2).
func TestGameSettingsRoutes(t *testing.T) {
	ts := newGameServer(t)
	view, err := ts.m.GameSettings(ts.id)
	rec := ts.call("GET", ts.path("/settings"), "")
	if err != nil {
		answersAsManager(t, "settings", rec, err)
		answersAsManager(t, "a settings save", ts.call("POST", ts.path("/settings"), `{"startWithApp":true}`),
			ts.m.UpdateGameSettings(ts.id, manager.GameSettingsChange{StartWithApp: &[]bool{true}[0]}))
		return
	}
	got := decodeJSON[manager.GameSettingsView](t, rec)
	if got.Schema == nil || got.Values.Server == nil || got.Values.Worlds == nil || got.Ports == nil ||
		!reflect.DeepEqual(got.Values.Server, view.Values.Server) || got.StartWithApp != view.StartWithApp {
		t.Fatalf("settings = %s", rec.Body)
	}
	before := got
	read := func() manager.GameSettingsView {
		t.Helper()
		return decodeJSON[manager.GameSettingsView](t, ts.call("GET", ts.path("/settings"), ""))
	}

	// One server value: only it changes.
	if _, has := before.Values.Server["serverName"]; !has {
		t.Fatalf("the legacy game's settings have no serverName: %+v", before.Values.Server)
	}
	rec = ts.call("POST", ts.path("/settings"), `{"values":{"serverName":"Partial Test"}}`)
	if !built(t, rec) {
		return
	}
	wantOK(t, "save a value", rec)
	after := read()
	want := before.Values.Server.Clone()
	want["serverName"] = "Partial Test"
	if !reflect.DeepEqual(after.Values.Server, want) {
		t.Errorf("server values after saving one:\n got %v\nwant %v", after.Values.Server, want)
	}
	if after.StartWithApp != before.StartWithApp || after.AutoStopMinutes != before.AutoStopMinutes ||
		after.Disabled != before.Disabled || after.Discord != before.Discord || !reflect.DeepEqual(after.Ports, before.Ports) {
		t.Errorf("saving a value changed something else: %+v → %+v", before, after)
	}
	if ts.m.Settings().ServerName != "Partial Test" {
		t.Errorf("the legacy game's settings.json has %q", ts.m.Settings().ServerName)
	}

	// Start with the app: only it changes.
	flip := !after.StartWithApp
	wantOK(t, "start with the app", ts.call("POST", ts.path("/settings"), fmt.Sprintf(`{"startWithApp":%v}`, flip)))
	again := read()
	if again.StartWithApp != flip || !reflect.DeepEqual(again.Values.Server, after.Values.Server) || again.Disabled != after.Disabled {
		t.Errorf("after start with the app: %+v", again)
	}

	// An empty save changes nothing.
	wantOK(t, "an empty save", ts.call("POST", ts.path("/settings"), `{}`))
	if last := read(); !reflect.DeepEqual(last, again) {
		t.Errorf("an empty save changed the settings: %+v → %+v", again, last)
	}

	// A value the game doesn't have is refused, as a field error.
	rec = ts.call("POST", ts.path("/settings"), `{"values":{"nosuchsetting":"1"}}`)
	if a := wantRefused(t, "an unknown setting", rec); len(a.Fields) == 0 {
		t.Errorf("an unknown setting: %s", rec.Body)
	}

	answersAsManager(t, "pick ports", ts.call("POST", ts.path("/ports/pick"), `{}`), func() error { _, err := ts.m.PickPorts(ts.id); return err }())
}

// Stars and the sidebar's lists.
func TestStarAndSidebarRoutes(t *testing.T) {
	ts := newGameServer(t)
	wantRefused(t, "a star that doesn't say on or off", ts.call("POST", ts.path("/star"), `{}`))
	wantRefused(t, "a star for an unknown game", ts.call("POST", "/api/games/nosuchgame/star", `{"on":true}`))
	if rec := ts.call("POST", ts.path("/star"), `{"on":true}`); built(t, rec) {
		wantOK(t, "star", rec)
		if st := ts.m.Status(); !slices.Contains(st.Stars, ts.id) || !ts.gameStatus().Starred {
			t.Fatalf("starred: %v", st.Stars)
		}
		wantOK(t, "unstar", ts.call("POST", ts.path("/star"), `{"on":false}`))
		if st := ts.m.Status(); slices.Contains(st.Stars, ts.id) || ts.gameStatus().Starred {
			t.Fatalf("unstarred: %v", st.Stars)
		}
	}

	before := ts.m.Status().Sidebar
	wantOK(t, "an empty sidebar change", ts.call("POST", "/api/app/sidebar", `{}`))
	if rec := ts.call("POST", "/api/app/sidebar", fmt.Sprintf(`{"inactiveOpen":%v}`, !before.InactiveOpen)); built(t, rec) {
		wantOK(t, "sidebar", rec)
		want := manager.SidebarStatus{ActiveOpen: before.ActiveOpen, InactiveOpen: !before.InactiveOpen}
		if got := ts.m.Status().Sidebar; got != want {
			t.Fatalf("sidebar = %+v, want %+v", got, want)
		}
		// Opening one list leaves the other as it is.
		wantOK(t, "sidebar", ts.call("POST", "/api/app/sidebar", fmt.Sprintf(`{"activeOpen":%v}`, !before.ActiveOpen)))
		want.ActiveOpen = !before.ActiveOpen
		if got := ts.m.Status().Sidebar; got != want {
			t.Fatalf("sidebar = %+v, want %+v", got, want)
		}
	}
}

// Removing a game (for the legacy game: hiding it) answers its job.
func TestRemoveGameRoute(t *testing.T) {
	ts := newGameServer(t)
	answersAsManager(t, "remove an unknown game", ts.call("POST", "/api/games/nosuchgame/remove", `{}`), func() error {
		_, err := ts.m.RemoveGame("nosuchgame")
		return err
	}())
	rec := ts.call("POST", ts.path("/remove"), `{}`)
	if built(t, rec) {
		var a struct {
			Job *int `json:"job"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil || a.Job == nil {
			t.Fatalf("remove = %d %s", rec.Code, rec.Body)
		}
	}
}

// --- the app's routes --------------------------------------------------------------------

func TestAppRoutes(t *testing.T) {
	ts := newGameServer(t)

	// Add a game: the catalog, the owner's own worlds, adding.
	catalog := decodeJSON[[]manager.CatalogGame](t, ts.call("GET", "/api/catalog", ""))
	if catalog == nil || len(catalog) != len(ts.m.Catalog()) {
		t.Fatalf("catalog = %+v", catalog)
	}
	for _, g := range catalog {
		if g.Stage == game.Hidden || g.ID == "" || (g.CanRun != game.Runs && g.CanRun != game.CantRun) {
			t.Errorf("catalog entry %+v", g)
		}
	}
	for _, id := range []string{ts.id, "nosuchgame"} {
		cands, err := ts.m.CatalogLocal(id)
		rec := ts.call("GET", "/api/catalog/"+id+"/local", "")
		answersAsManager(t, "catalog local "+id, rec, err)
		if err == nil {
			if got := decodeJSON[struct {
				Candidates []worlds.Candidate `json:"candidates"`
			}](t, rec); got.Candidates == nil || len(got.Candidates) != len(cands) {
				t.Errorf("catalog local = %s", rec.Body)
			}
		}
	}
	wantRefused(t, "add an unknown game", ts.call("POST", "/api/games", `{"game":"nosuchgame","serverName":"x","world":{"mode":"new","name":"W"}}`))
	wantRefused(t, "add with a body that can't be read", ts.call("POST", "/api/games", `{"game":`))

	// App settings: only what is sent changes.
	before := ts.m.Settings()
	if rec := ts.call("POST", "/api/app/settings", fmt.Sprintf(`{"idleMinutes":%d}`, before.IdleMinutes+5)); built(t, rec) {
		wantOK(t, "app settings", rec)
		after := ts.m.Settings()
		if after.IdleMinutes != before.IdleMinutes+5 || after.IdleHibernate != before.IdleHibernate ||
			after.AutoUpdate != before.AutoUpdate || after.StartAfterWake != before.StartAfterWake {
			t.Fatalf("app settings: %+v → %+v", before, after)
		}
	}

	// The main Discord channel.
	wantRefused(t, "main channel without a link", ts.call("POST", "/api/discord/main", `{}`))
	if rec := ts.call("POST", "/api/discord/main", `{"webhook":"https://discord.com/channels/1/2"}`); built(t, rec) {
		wantRefused(t, "main channel with a link that isn't a webhook", rec)
		wantOK(t, "main channel", ts.call("POST", "/api/discord/main", `{"webhook":"https://discord.com/api/webhooks/123/abc"}`))
		if got := ts.m.Settings().DiscordWebhook; got != "https://discord.com/api/webhooks/123/abc" {
			t.Fatalf("main channel = %q", got)
		}
	}

	// A test post, worded for the game whose channel it is.
	// (The manager may also look the link up, or post something of its
	// own, at the same time: only the test post says "can post here".)
	n := len(ts.posts.all())
	wantOK(t, "test post", ts.call("POST", "/api/discord/test", `{"webhook":"https://discord.com/api/webhooks/123/abc","game":"`+ts.id+`"}`))
	waitUntil(t, "the test post", 10*time.Second, func() bool {
		return slices.ContainsFunc(ts.posts.all()[n:], func(p string) bool {
			return strings.HasPrefix(p, "POST /api/webhooks/123/abc ") && strings.Contains(p, "can post here")
		})
	})
	answersAsManager(t, "test post for an unknown game",
		ts.call("POST", "/api/discord/test", `{"webhook":"https://discord.com/api/webhooks/123/abc","game":"nosuchgame"}`),
		ts.m.TestGameDiscord("https://discord.com/api/webhooks/123/abc", "nosuchgame"))
	rec := ts.call("POST", "/api/discord/test", `{"webhook":"https://discord.com/channels/1/2","game":"`+ts.id+`"}`)
	if a := wantRefused(t, "test post with a link that isn't a webhook", rec); a.Fields["discordWebhook"] == "" {
		t.Fatalf("test post with a bad link = %s", rec.Body)
	}

	// The router page.
	router := decodeJSON[manager.RouterView](t, ts.call("GET", "/api/router", ""))
	if router.LAN == nil || router.Mappings == nil || router.Steam == nil {
		t.Fatalf("router = %+v", router)
	}
	answersAsManager(t, "router check", ts.call("POST", "/api/router/check", `{}`), ts.m.CheckRouter())
}

// --- a game added on the new panel -----------------------------------------------------

// waitJobDone waits for job id to finish and returns it.
func (ts *gameServer) waitJobDone(id int) manager.Job {
	ts.t.Helper()
	var done manager.Job
	waitUntil(ts.t, fmt.Sprintf("job %d to finish", id), 90*time.Second, func() bool {
		for _, j := range ts.m.Status().Jobs {
			if j.ID == id && j.Finished != nil {
				done = j
				return true
			}
		}
		return false
	})
	return done
}

// gameIdle waits until an instance has no job going (its start or stop
// job ends a moment after its state changes).
func (ts *gameServer) gameIdle(id string) {
	ts.t.Helper()
	waitUntil(ts.t, id+"'s job to finish", 60*time.Second, func() bool {
		g, _ := ts.gameOf(id)
		return g.Job == nil || g.Job.Finished != nil
	})
}

// gameOf is an instance's entry in Status.games; ok is false when there is
// none.
func (ts *gameServer) gameOf(id string) (manager.GameStatus, bool) {
	for _, g := range ts.m.Status().Games {
		if g.ID == id {
			return g, true
		}
	}
	return manager.GameStatus{}, false
}

// fakeGames makes every game but the legacy one the stand-in server
// (cmd/fakeserver) playing script, as EGS_DEV_FAKE_SERVERS does for
// development (spec 7.1). The manager's Options.Fake does it (stream M3
// adds it); it is set by name, so these tests also build against a manager
// that doesn't have it yet, where it does nothing. The variable is set too:
// Add a game reads it to offer every game on every system in fake mode.
func fakeGames(t *testing.T, script string) func(*manager.Options) {
	prog := gametest.BuildFakeserver(t)
	t.Setenv("EGS_DEV_FAKE_SERVERS", prog)
	return func(o *manager.Options) {
		f := reflect.ValueOf(o).Elem().FieldByName("Fake")
		if f.IsValid() && f.Type() == reflect.TypeFor[*game.Fake]() {
			f.Set(reflect.ValueOf(&game.Fake{Program: prog, Script: script, Delay: 100 * time.Millisecond}))
		}
	}
}

// A game added on the new panel, through its routes only: added (not
// starred), installed, set up on its Settings tab, turned off and on,
// starred, started, played, looked at, stopped and removed, with every
// game but the legacy one the stand-in server. It needs the manager's fake
// mode and Add a game (streams M3 and A); in a build without them it says
// so and skips.
func TestAddedGameThroughTheRoutes(t *testing.T) {
	ts := newGameServer(t, fakeGames(t, "join:Alex@0.3"))

	var pick *manager.CatalogGame
	catalog := decodeJSON[[]manager.CatalogGame](t, ts.call("GET", "/api/catalog", ""))
	for i, g := range catalog {
		if g.ID != ts.id && !g.Added && g.CanRun == game.Runs {
			pick = &catalog[i]
			break
		}
	}
	if pick == nil {
		t.Skip("the catalog has no game to add here yet")
	}
	password := ""
	if pick.Password.Use != "none" {
		password = strings.Repeat("owl", max(pick.Password.Min, 8)/3+1)
		if pick.Password.Max > 0 && len(password) > pick.Password.Max {
			password = password[:pick.Password.Max]
		}
	}
	body, _ := json.Marshal(map[string]any{"game": pick.ID, "serverName": "Night Owls", "password": password,
		"eula": pick.EULA != "", "star": false, "world": map[string]any{"mode": "new", "name": "Riverbend", "values": map[string]any{}}})
	rec := ts.call("POST", "/api/games", string(body))
	if !built(t, rec) {
		t.Skip("this build's manager can't add a game yet")
	}
	added := decodeJSON[manager.AddedGame](t, rec)
	if added.ID == "" || added.Job == 0 {
		t.Fatalf("add = %s", rec.Body)
	}
	id := added.ID
	path := func(rest string) string { return "/api/games/" + id + rest }
	if j := ts.waitJobDone(added.Job); j.Error != "" {
		t.Fatalf("install job: %s", j.Error)
	}
	g, ok := ts.gameOf(id)
	if !ok || !g.Installed || g.Starred || slices.Contains(ts.m.Status().Stars, id) {
		t.Fatalf("after adding: %+v (stars %v)", g, ts.m.Status().Stars)
	}
	isAdded := func(c manager.CatalogGame) bool { return c.ID == pick.ID && c.Added }
	if again := decodeJSON[[]manager.CatalogGame](t, ts.call("GET", "/api/catalog", "")); !slices.ContainsFunc(again, isAdded) {
		t.Errorf("the catalog doesn't say %s is added", pick.ID)
	}
	wantRefused(t, "adding it twice", ts.call("POST", "/api/games", string(body)))

	// Its Settings tab: a save changes only what it has.
	before := decodeJSON[manager.GameSettingsView](t, ts.call("GET", path("/settings"), ""))
	if before.Values.Server["serverName"] != "Night Owls" || len(before.Ports) != len(pick.Ports) {
		t.Fatalf("settings after adding = %+v", before)
	}
	wantOK(t, "rename the server", ts.call("POST", path("/settings"), `{"values":{"serverName":"Late Owls"}}`))
	after := decodeJSON[manager.GameSettingsView](t, ts.call("GET", path("/settings"), ""))
	want := before.Values.Server.Clone()
	want["serverName"] = "Late Owls"
	if !reflect.DeepEqual(after.Values.Server, want) || after.AutoStopMinutes != before.AutoStopMinutes || !reflect.DeepEqual(after.Ports, before.Ports) {
		t.Fatalf("after renaming:\n got %+v\nwant server values %v", after, want)
	}
	wantOK(t, "auto-stop", ts.call("POST", path("/settings"), `{"autoStopMinutes":30}`))
	if v := decodeJSON[manager.GameSettingsView](t, ts.call("GET", path("/settings"), "")); v.AutoStopMinutes != 30 || !reflect.DeepEqual(v.Values.Server, want) {
		t.Fatalf("after the auto-stop: %+v", v)
	}
	rec = ts.call("POST", path("/settings"), `{"values":{"nosuchsetting":"1"}}`)
	if a := wantRefused(t, "an unknown setting", rec); !slices.ContainsFunc(slices.Collect(maps.Keys(a.Fields)), func(k string) bool { return strings.HasPrefix(k, "server.") }) {
		t.Errorf("an unknown setting's error isn't keyed server.<key>: %s", rec.Body)
	}

	// Turned off, it can't start; turned on again, it can.
	wantOK(t, "turn it off", ts.call("POST", path("/settings"), `{"disabled":true}`))
	if g, _ := ts.gameOf(id); !g.Disabled {
		t.Fatal("not shown as turned off")
	}
	wantRefused(t, "start while turned off", ts.call("POST", path("/start"), `{"memory":"anyway"}`))
	wantOK(t, "turn it on", ts.call("POST", path("/settings"), `{"disabled":false}`))

	// Starred, started, played.
	wantOK(t, "star", ts.call("POST", path("/star"), `{"on":true}`))
	if !slices.Contains(ts.m.Status().Stars, id) {
		t.Fatalf("stars = %v", ts.m.Status().Stars)
	}
	wantOK(t, "start", ts.call("POST", path("/start"), `{"memory":"anyway"}`))
	waitUntil(t, id+" online", 60*time.Second, func() bool { g, _ := ts.gameOf(id); return g.State == manager.StateOnline })
	ts.gameIdle(id)
	waitUntil(t, id+"'s log", 20*time.Second, func() bool {
		return strings.Contains(ts.call("GET", path("/logs?after=0"), "").Body.String(), `"text"`)
	})
	if rec := ts.call("GET", path("/logs/download"), ""); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Errorf("log download = %d", rec.Code)
	}
	worldsView := decodeJSON[manager.GameWorldsView](t, ts.call("GET", path("/worlds"), ""))
	if worldsView.Selected != "Riverbend" || worldsView.Running != "Riverbend" {
		t.Errorf("worlds = %+v", worldsView)
	}
	if g, _ := ts.gameOf(id); g.Presence == game.ByName {
		waitUntil(t, "Alex in the players", 30*time.Second, func() bool {
			return strings.Contains(ts.call("GET", path("/players"), "").Body.String(), `"Alex"`)
		})
	}
	decodeJSON[[]manager.GameList](t, ts.call("GET", path("/lists"), ""))
	if rec := ts.call("GET", path("/stats?range=15m"), ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"samples"`) {
		t.Errorf("stats = %d %s", rec.Code, rec.Body)
	}
	if rec := ts.call("GET", path("/records"), ""); rec.Code != http.StatusOK {
		t.Errorf("records = %d %s", rec.Code, rec.Body)
	}
	wantRefused(t, "deleting the running world", ts.call("POST", path("/worlds/delete"), `{"name":"Riverbend"}`))

	// Stopped, then removed: it leaves the list, and the catalog offers it
	// again.
	wantOK(t, "stop", ts.call("POST", path("/stop"), `{}`))
	waitUntil(t, id+" stopped", 60*time.Second, func() bool { g, _ := ts.gameOf(id); return g.State == manager.StateStopped })
	ts.gameIdle(id)
	rec = ts.call("POST", path("/remove"), `{}`)
	var removed struct {
		Job int `json:"job"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &removed) != nil || removed.Job == 0 {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	// The job goes with the game's own slot, so what shows it worked is
	// that the game leaves the list (and no job ends with an error).
	waitUntil(t, id+" to leave the list", 90*time.Second, func() bool {
		for _, j := range ts.m.Status().Jobs {
			if j.ID == removed.Job && j.Finished != nil && j.Error != "" {
				t.Fatalf("remove job: %s", j.Error)
			}
		}
		_, still := ts.gameOf(id)
		return !still
	})
	if slices.Contains(ts.m.Status().Stars, id) {
		t.Fatalf("%s is still starred after its removal", id)
	}
	if again := decodeJSON[[]manager.CatalogGame](t, ts.call("GET", "/api/catalog", "")); slices.ContainsFunc(again, isAdded) {
		t.Errorf("the catalog still says %s is added", pick.ID)
	}
}
