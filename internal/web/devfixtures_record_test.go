package web

// The answers internal/web/devfixtures serves to the panel while it is
// built (spec 6.2: "recorded status and settings JSON fixtures served by a
// small dev handler"), and the checks that they are still the app's.
//
// TestRecordDevFixtures records them. It runs the app's own manager and
// routes with the stand-in servers, sets up a few games in the states the
// new panel shows (online with players, stopped, running without a star,
// turned off, and a start refused for want of memory), asks every GET
// route the panel reads for each of them, and writes each answer, exactly
// as the app gave it, into devfixtures/recorded. So every text and shape
// there is the app's own; devfixtures makes nothing up. It runs only when
// asked:
//
//	go test ./internal/web -run TestRecordDevFixtures -record-devfixtures
//
// and needs a build whose manager can add a game (streams M3 and A); in an
// older one it says so and fails.
//
// The other two tests run every time, and fail once the recordings no
// longer match this build: a route the panel reads that has no recording,
// or a field the app's answers have that the recordings lack. Either
// means: record them again. Recordings made by a newer build than this one
// may have more fields than this build's types; that is allowed.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/manager"
)

var recordDevFixtures = flag.Bool("record-devfixtures", false,
	"record internal/web/devfixtures/recorded from this build's manager (TestRecordDevFixtures)")

// devFixturesDir is where the recordings are, from this package's folder.
const devFixturesDir = "devfixtures/recorded"

// devRecording is one recorded answer, one file in devfixtures/recorded.
// devfixtures reads the same fields (its recording type).
type devRecording struct {
	Route   string          `json:"route"`          // the app's pattern: "GET /api/games/{id}/settings"
	Request string          `json:"request"`        // what was asked: "GET /api/games/minecraft/settings"
	Body    json.RawMessage `json:"body,omitempty"` // a change's request body
	Note    string          `json:"note,omitempty"` // how it was set up, where that isn't plain
	Code    int             `json:"code"`           // the HTTP status the app answered
	Answer  json.RawMessage `json:"answer"`         // the app's answer, as it gave it
}

// moreMemory is a game whose memory need is raised above what this
// computer has, so the manager's own memory check refuses its start: test
// machines are never short of memory, and the panel's memory dialog needs
// a refusal to show.
type moreMemory struct {
	game.Game
	mb atomic.Int64
}

func (g *moreMemory) Info() game.Info {
	info := g.Game.Info()
	if mb := g.mb.Load(); mb > 0 {
		info.MemoryMB = int(mb)
	}
	return info
}

// devRecorder collects the recordings.
type devRecorder struct {
	t    *testing.T
	ts   *gameServer
	recs map[string]devRecording // by file name
}

// get records the answer to a GET.
func (r *devRecorder) get(route, path, note string) {
	r.t.Helper()
	rec := r.ts.call("GET", path, "")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		r.t.Fatalf("GET %s answered %q, not JSON; downloads aren't recorded", path, ct)
	}
	r.add(devRecording{Route: route, Request: "GET " + path, Note: note, Code: rec.Code, Answer: bytes.TrimSpace(rec.Body.Bytes())})
}

// change records the answer to a change (a POST).
func (r *devRecorder) change(route, path, body, note string) devRecording {
	r.t.Helper()
	rec := r.ts.call("POST", path, body)
	d := devRecording{Route: route, Request: "POST " + path, Body: json.RawMessage(body), Note: note,
		Code: rec.Code, Answer: bytes.TrimSpace(rec.Body.Bytes())}
	r.add(d)
	return d
}

func (r *devRecorder) add(d devRecording) {
	r.t.Helper()
	name := devRecordingFile(d.Request)
	if _, dup := r.recs[name]; dup {
		r.t.Fatalf("%s is recorded twice", d.Request)
	}
	r.recs[name] = d
}

// devRecordingFile names a recording's file after its request:
// "GET /api/games/minecraft/logs?after=57" is
// "get-games.minecraft.logs~after=57.json".
func devRecordingFile(request string) string {
	method, path, _ := strings.Cut(request, " ")
	path, query, _ := strings.Cut(strings.TrimPrefix(path, "/api/"), "?")
	name := strings.ToLower(method) + "-" + strings.ReplaceAll(path, "/", ".")
	if query != "" {
		name += "~" + query
	}
	return name + ".json"
}

func TestRecordDevFixtures(t *testing.T) {
	if !*recordDevFixtures {
		t.Skip("records only when asked: go test ./internal/web -run TestRecordDevFixtures -record-devfixtures")
	}
	prog := gametest.BuildFakeserver(t) // before HOME changes, so the build uses the usual cache

	// The recordings are committed, so nothing of this computer's may end up
	// in them: the owner's own games are looked for in an empty home folder,
	// and the folders and the computer's name are replaced when they are
	// written (devScrub).
	realHome, _ := os.UserHomeDir()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(k, home)
	}
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3,join:Bjorn@0.6")
	// Fake mode, as for trying the app: Add a game offers every game on
	// every system, and each one is the stand-in server.
	t.Setenv("EGS_DEV_FAKE_SERVERS", prog)

	// The first four games after the legacy one, in the panel's order.
	var picked []game.Game
	for _, g := range game.All() {
		if info := g.Info(); !info.Legacy && info.Stage != game.Hidden && len(picked) < 4 {
			picked = append(picked, g)
		}
	}
	if len(picked) < 4 {
		t.Fatalf("only %d games other than the legacy one are offered; the recordings need 4", len(picked))
	}
	// The second one's start is refused for memory. Its wrapper (moreMemory)
	// hides what its Game has beyond game.Game (game.Licensed), so it must
	// be one without a licence to accept.
	for i := 2; i < len(picked); i++ {
		if _, licensed := picked[1].(game.Licensed); !licensed {
			break
		}
		picked[1], picked[i] = picked[i], picked[1]
	}
	short := &moreMemory{Game: picked[1]}
	ts := newGameServer(t, func(o *manager.Options) {
		setOption(o, "Fake", &game.Fake{Program: prog, Script: "join:Alex@0.3,join:Sam@0.6", Delay: 100 * time.Millisecond})
		setOption(o, "Games", []game.Game{short})
	})
	r := &devRecorder{t: t, ts: ts, recs: map[string]devRecording{}}
	// The game whose start is refused asks for 4 GB more than this computer
	// has. It reads its Info when it is added, so this comes first.
	short.mb.Store(int64(ts.m.Status().Machine.MemTotal>>30+4) * 1024)
	path := func(id, rest string) string { return "/api/games/" + id + rest }

	start := func(id string) {
		t.Helper()
		wantOK(t, "start "+id, ts.call("POST", path(id, "/start"), `{"memory":"anyway"}`))
		waitUntil(t, id+" online", 60*time.Second, func() bool { g, _ := ts.gameOf(id); return g.State == manager.StateOnline })
		ts.gameIdle(id)
	}
	stop := func(id string) {
		t.Helper()
		wantOK(t, "stop "+id, ts.call("POST", path(id, "/stop"), `{}`))
		waitUntil(t, id+" stopped", 60*time.Second, func() bool { g, _ := ts.gameOf(id); return g.State == manager.StateStopped })
		ts.gameIdle(id)
	}
	players := func(id string, n int) {
		t.Helper()
		if g, _ := ts.gameOf(id); g.Presence == game.Unknown {
			return // this game can't say who is on
		}
		waitUntil(t, fmt.Sprintf("%d players in %s", n, id), 30*time.Second, func() bool { g, _ := ts.gameOf(id); return g.Count >= n })
	}
	add := func(g game.Game, star bool) string {
		t.Helper()
		info := g.Info()
		password := ""
		if info.Password.Use != "none" {
			password = strings.Repeat("owl", max(info.Password.Min, 8)/3+1)
			if info.Password.Max > 0 && len(password) > info.Password.Max {
				password = password[:info.Password.Max]
			}
		}
		_, licensed := g.(game.Licensed)
		body, _ := json.Marshal(map[string]any{"game": info.ID, "serverName": "Friends' " + info.Short, "password": password,
			"eula": licensed, "star": star, "world": map[string]any{"mode": "new", "name": "Riverbend", "values": map[string]any{}}})
		rec := ts.call("POST", "/api/games", string(body))
		if !built(t, rec) {
			t.Fatal("this build's manager can't add a game yet: record in a build with streams M3 and A merged")
		}
		added := decodeJSON[manager.AddedGame](t, rec)
		if j := ts.waitJobDone(added.Job); j.Error != "" {
			t.Fatalf("installing %s: %s", info.ID, j.Error)
		}
		// The licence box of the Settings tab, ticked for every game: a game
		// whose adapter doesn't offer its licence through game.Licensed
		// (Minecraft's doesn't yet) gets no agreement from Add a game, and
		// its server won't start without one. Other adapters ignore it.
		wantOK(t, "the licence of "+added.ID, ts.call("POST", path(added.ID, "/settings"), `{"eula":true}`))
		return added.ID
	}

	// Valheim: installed, starred, online with two players.
	legacy := ts.id
	wantOK(t, "install Valheim", ts.call("POST", path(legacy, "/install"), `{}`))
	ts.idle()
	start(legacy)
	wantOK(t, "star Valheim", ts.call("POST", path(legacy, "/star"), `{"on":true}`))
	players(legacy, 2)

	// The others: starred and online with players; starred and stopped
	// after a run; running without a star; added and turned off.
	online := add(picked[0], true)
	start(online)
	players(online, 2)

	stopped := add(picked[1], true)
	start(stopped)
	stop(stopped)

	unstarred := add(picked[2], false)
	start(unstarred)

	off := add(picked[3], false)
	wantOK(t, "turn "+off+" off", ts.call("POST", path(off, "/settings"), `{"disabled":true}`))

	// The main Discord channel, so the games' Discord parts show it.
	wantOK(t, "main Discord channel", ts.call("POST", "/api/discord/main", `{"webhook":"https://discord.com/api/webhooks/123456789012345678/stand-in"}`))

	// The memory dialog: the stopped game asks for more memory than this
	// computer has (short, above), so a start from the panel is refused
	// with the choices.
	refused := r.change("POST /api/games/{id}/start", path(stopped, "/start"), `{"memory":""}`,
		"recorded with "+stopped+"'s memory need raised above this computer's memory, so the app's own check refuses it")
	var refusal struct {
		Action string              `json:"action"`
		Data   manager.MemoryShort `json:"data"`
	}
	err := json.Unmarshal(refused.Answer, &refusal)
	if err != nil || refused.Code != 400 || refusal.Action != "memory" || len(refusal.Data.Running) == 0 {
		t.Fatalf("the start of %s wasn't refused for memory: %d %s", stopped, refused.Code, refused.Answer)
	}

	// Every GET route the panel reads, for every game (downloads aren't
	// JSON, so they aren't recorded). A log is recorded twice: all of it,
	// and what comes after its last line, which is what the panel asks for
	// next, so polling it doesn't show the same lines again.
	//
	// Out of fake mode from here on, so the catalog says what this computer
	// really runs, and Add a game shows the games it can't run greyed out
	// with their reasons ("Needs Windows"), as on the Mac Pro; the games
	// added above keep running as stand-ins.
	t.Setenv("EGS_DEV_FAKE_SERVERS", "")
	notes := map[string]string{
		"GET /api/catalog": "recorded out of fake mode, so canRun and needsText say what the recording computer runs; " +
			"the games marked added were added in fake mode and run as stand-ins",
	}
	var ids []string
	for _, g := range ts.m.Status().Games {
		ids = append(ids, g.ID)
	}
	var catalog []string
	for _, c := range decodeJSON[[]manager.CatalogGame](t, ts.call("GET", "/api/catalog", "")) {
		catalog = append(catalog, c.ID)
	}
	for _, pattern := range Patterns() {
		method, p, _ := strings.Cut(pattern, " ")
		if method != "GET" || strings.Contains(p, "download") {
			continue
		}
		var paths []string
		switch {
		case strings.Contains(p, "{id}"):
			for _, id := range ids {
				paths = append(paths, strings.ReplaceAll(p, "{id}", id))
			}
		case strings.Contains(p, "{game}"):
			for _, id := range catalog {
				paths = append(paths, strings.ReplaceAll(p, "{game}", id))
			}
		case strings.Contains(p, "{"):
			t.Fatalf("the recorder doesn't know what to put in %s", pattern)
		default:
			paths = []string{p}
		}
		for _, one := range paths {
			if !strings.HasSuffix(one, "/logs") {
				r.get(pattern, one, notes[pattern])
				continue
			}
			r.log(pattern, one)
		}
	}

	r.write(devScrub(ts, realHome))
}

// log records a log route: all of it, and the empty answer after its
// last line. A server may write a line between the two, so it asks again
// until the two agree.
func (r *devRecorder) log(pattern, path string) {
	r.t.Helper()
	type logAnswer struct {
		Lines []json.RawMessage `json:"lines"`
		Last  int64             `json:"last"`
	}
	for range 20 {
		all := r.ts.call("GET", path, "")
		var a logAnswer
		if err := json.Unmarshal(all.Body.Bytes(), &a); err != nil {
			r.t.Fatalf("GET %s: %v\n%s", path, err, all.Body)
		}
		after := path + "?after=" + strconv.FormatInt(a.Last, 10)
		tail := r.ts.call("GET", after, "")
		var b logAnswer
		json.Unmarshal(tail.Body.Bytes(), &b)
		if len(b.Lines) > 0 {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		r.add(devRecording{Route: pattern, Request: "GET " + path, Code: all.Code, Answer: bytes.TrimSpace(all.Body.Bytes())})
		r.add(devRecording{Route: pattern, Request: "GET " + after, Code: tail.Code, Answer: bytes.TrimSpace(tail.Body.Bytes())})
		return
	}
	r.t.Fatalf("%s kept growing", path)
}

// write replaces the recordings in devfixtures/recorded with these.
func (r *devRecorder) write(scrub func([]byte) []byte) {
	r.t.Helper()
	old, _ := filepath.Glob(filepath.Join(devFixturesDir, "*.json"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			r.t.Fatal(err)
		}
	}
	if err := os.MkdirAll(devFixturesDir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	for name, d := range r.recs {
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			r.t.Fatalf("%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(devFixturesDir, name), append(scrub(b), '\n'), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.t.Logf("recorded %d answers in %s", len(r.recs), devFixturesDir)
}

// devScrub replaces what would say whose computer recorded the answers:
// the app's data folder becomes the Mac's usual one, the home and
// temporary folders neutral ones, the computer's name "GAMING-PC", and its
// addresses on the home network the stand-in network adapter's.
func devScrub(ts *gameServer, realHome string) func([]byte) []byte {
	esc := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b[1 : len(b)-1])
	}
	const macData = "/Users/owner/Library/Application Support/ValheimEasyServer"
	pairs := []string{esc(filepath.Join(ts.dir, "data")), macData, esc(ts.dir), "/tmp/recording"}
	for _, home := range []string{os.Getenv("HOME"), realHome} {
		if len(home) > 1 {
			pairs = append(pairs, esc(home), "/Users/owner")
		}
	}
	pairs = append(pairs, esc(os.TempDir()), "/tmp")
	if host, err := os.Hostname(); err == nil && host != "" {
		pairs = append(pairs, `"`+esc(host)+`"`, `"GAMING-PC"`)
	}
	const lan = "192.168.1.20" // newGameServer's stand-in adapter
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.String() == lan {
				continue
			}
			ip := ipn.IP.String()
			// Whole values and address:port, never a longer address that
			// merely starts the same way.
			pairs = append(pairs, `"`+ip+`"`, `"`+lan+`"`, `"`+ip+`:`, `"`+lan+`:`, `"[`+ip+`]:`, `"`+lan+`:`)
		}
	}
	rep := strings.NewReplacer(pairs...)
	return func(b []byte) []byte { return []byte(rep.Replace(string(b))) }
}

// loadDevRecordings reads devfixtures/recorded.
func loadDevRecordings(t *testing.T) []devRecording {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(devFixturesDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no recordings in %s (%v)", devFixturesDir, err)
	}
	var out []devRecording
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d devRecording
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if devRecordingFile(d.Request) != filepath.Base(f) {
			t.Errorf("%s holds the answer to %s", f, d.Request)
		}
		out = append(out, d)
	}
	return out
}

const recordAgain = "record them again: go test ./internal/web -run TestRecordDevFixtures -record-devfixtures (in a build with every R3 manager stream merged)"

// Every GET route the panel reads has a recorded answer (downloads
// aren't JSON, and devfixtures says so instead), every recording is of a
// route the app has, and the memory dialog has its refusal.
func TestDevFixturesCoverTheRoutes(t *testing.T) {
	recs := loadDevRecordings(t)
	routes := map[string]bool{}
	for _, d := range recs {
		routes[d.Route] = true
		if method, path, _ := strings.Cut(d.Request, " "); !strings.HasPrefix(d.Route, method+" ") || !strings.HasPrefix(path, "/api/") {
			t.Errorf("%s is recorded as %s", d.Request, d.Route)
		}
	}
	app := Patterns()
	for _, p := range app {
		if strings.HasPrefix(p, "GET ") && !strings.Contains(p, "download") && !routes[p] {
			t.Errorf("%s has no recorded answer; %s", p, recordAgain)
		}
	}
	for r := range routes {
		if !slices.Contains(app, r) {
			t.Errorf("recordings of %s, which the app doesn't have; %s", r, recordAgain)
		}
	}
	if !slices.ContainsFunc(recs, func(d devRecording) bool {
		var a struct {
			Action string `json:"action"`
		}
		json.Unmarshal(d.Answer, &a)
		return d.Route == "POST /api/games/{id}/start" && d.Code == 400 && a.Action == "memory"
	}) {
		t.Errorf("no start refused for memory is recorded; %s", recordAgain)
	}
}

// The recordings have every field this build's answers have, so the
// panel built against them sees all of them. (A field missing from every
// recording means the app gained it after they were made.)
func TestDevFixturesHaveTheAppsFields(t *testing.T) {
	type players struct {
		Players []manager.KnownPlayer `json:"players"`
	}
	type memoryRefusal struct {
		Error  string              `json:"error"`
		Action string              `json:"action"`
		Data   manager.MemoryShort `json:"data"`
	}
	types := map[string]reflect.Type{
		"GET /api/status":              reflect.TypeFor[manager.Status](),
		"GET /api/catalog":             reflect.TypeFor[[]manager.CatalogGame](),
		"GET /api/games/{id}/settings": reflect.TypeFor[manager.GameSettingsView](),
		"GET /api/games/{id}/worlds":   reflect.TypeFor[manager.GameWorldsView](),
		"GET /api/games/{id}/lists":    reflect.TypeFor[[]manager.GameList](),
		"GET /api/games/{id}/players":  reflect.TypeFor[players](),
		"GET /api/router":              reflect.TypeFor[manager.RouterView](),
		"GET /api/jetkvm":              reflect.TypeFor[manager.WakerView](),
		"POST /api/games/{id}/start":   reflect.TypeFor[memoryRefusal](),
	}
	answers := map[string][]any{}
	for _, d := range loadDevRecordings(t) {
		// The answers that went through, and the refusal of the memory
		// check, the one change recorded.
		if _, typed := types[d.Route]; !typed || d.Code != 200 && !strings.HasPrefix(d.Route, "POST ") {
			continue
		}
		var v any
		if err := json.Unmarshal(d.Answer, &v); err != nil {
			t.Fatalf("%s: %v", d.Request, err)
		}
		answers[d.Route] = append(answers[d.Route], v)
	}
	routes := make([]string, 0, len(types))
	for r := range types {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	for _, route := range routes {
		if len(answers[route]) == 0 {
			t.Errorf("%s has no recorded answer; %s", route, recordAgain)
			continue
		}
		var missing []string
		missingFields(types[route], answers[route], "", &missing)
		for _, f := range missing {
			t.Errorf("%s: no recording has %q; %s", route, f, recordAgain)
		}
	}
}

// missingFields walks a Go type and the JSON values found at the same
// place in the recordings, and lists the fields the type always writes
// (not omitempty) that none of the values has.
func missingFields(t reflect.Type, vals []any, at string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	marshaler := reflect.TypeFor[json.Marshaler]()
	if t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler) {
		return // written its own way (times, for one)
	}
	switch t.Kind() {
	case reflect.Struct:
		var objs []map[string]any
		for _, v := range vals {
			if o, ok := v.(map[string]any); ok {
				objs = append(objs, o)
			}
		}
		if len(objs) == 0 {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" || !f.IsExported() && !f.Anonymous {
				continue
			}
			if f.Anonymous && name == "" {
				missingFields(f.Type, vals, at, out) // its fields are written as this one's
				continue
			}
			if name == "" {
				name = f.Name
			}
			var sub []any
			for _, o := range objs {
				if v, ok := o[name]; ok {
					sub = append(sub, v)
				}
			}
			if len(sub) == 0 {
				if !strings.Contains(","+opts+",", ",omitempty,") && !strings.Contains(","+opts+",", ",omitzero,") {
					*out = append(*out, at+"."+name)
				}
				continue
			}
			missingFields(f.Type, sub, at+"."+name, out)
		}
	case reflect.Slice, reflect.Array:
		var items []any
		for _, v := range vals {
			if a, ok := v.([]any); ok {
				items = append(items, a...)
			}
		}
		missingFields(t.Elem(), items, at+"[]", out)
	case reflect.Map:
		var items []any
		for _, v := range vals {
			if o, ok := v.(map[string]any); ok {
				for _, x := range o {
					items = append(items, x)
				}
			}
		}
		missingFields(t.Elem(), items, at+"{}", out)
	}
}
