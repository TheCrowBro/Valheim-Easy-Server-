package main

// devfixtures answers with the recordings, and keeps only the stars and
// the sidebar. (internal/web's tests check that the recordings cover the
// app's routes and still have its fields.)

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"valheim-easy-server/internal/web"
)

func newFixtures(t *testing.T) (*fixtures, http.Handler) {
	t.Helper()
	f, err := load(besideSource("devfixtures/recorded"), besideSource("ui"), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return f, f.handler()
}

// call asks as the panel does: from this computer, with X-VES on changes.
func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8457"
	if method != http.MethodGet {
		req.Header.Set("X-VES", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

type statusPart struct {
	Stars []string `json:"stars"`
	Games []struct {
		ID      string `json:"id"`
		Starred bool   `json:"starred"`
	} `json:"games"`
	Sidebar struct {
		ActiveOpen   bool `json:"activeOpen"`
		InactiveOpen bool `json:"inactiveOpen"`
	} `json:"sidebar"`
}

func status(t *testing.T, h http.Handler) statusPart {
	t.Helper()
	rec := call(h, "GET", "/api/status", "")
	var st statusPart
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	return st
}

// Each recorded request gets exactly the answer the app gave it.
func TestAnswersAsRecorded(t *testing.T) {
	f, h := newFixtures(t)
	var all []recording
	for _, d := range f.gets {
		all = append(all, d)
	}
	for _, list := range f.changes {
		all = append(all, list...)
	}
	if len(all) < 20 {
		t.Fatalf("only %d recordings", len(all))
	}
	for _, d := range all {
		method, path, _ := strings.Cut(d.Request, " ")
		rec := call(h, method, path, string(d.Body))
		if rec.Code != d.Code {
			t.Errorf("%s = %d, recorded %d", d.Request, rec.Code, d.Code)
			continue
		}
		if !jsonEqual(t, rec.Body.Bytes(), d.Answer) {
			t.Errorf("%s answers\n%s\nrecorded\n%s", d.Request, rec.Body, d.Answer)
		}
	}
}

// A log asked for after its last line has nothing new, so a panel that
// polls it doesn't show the same lines again and again.
func TestLogPollingEnds(t *testing.T) {
	f, h := newFixtures(t)
	n := 0
	for req, d := range f.gets {
		if !strings.HasSuffix(req, "/logs") {
			continue
		}
		var all struct {
			Last json.Number `json:"last"`
		}
		json.Unmarshal(d.Answer, &all)
		var next struct {
			Lines []any       `json:"lines"`
			Last  json.Number `json:"last"`
		}
		path := strings.TrimPrefix(req, "GET ") + "?after=" + all.Last.String()
		rec := call(h, "GET", path, "")
		if json.Unmarshal(rec.Body.Bytes(), &next) != nil || len(next.Lines) != 0 || next.Last != all.Last {
			t.Errorf("%s = %s", path, rec.Body)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no log is recorded")
	}
}

// The stars and the sidebar's lists change as the panel asks, and nothing
// else does.
func TestStarsAndSidebar(t *testing.T) {
	_, h := newFixtures(t)
	before := status(t, h)
	var id string
	for _, g := range before.Games {
		if !g.Starred {
			id = g.ID
			break
		}
	}
	if id == "" {
		t.Fatal("every recorded game is starred; the recordings need an unstarred one")
	}
	starred := func() (inList, flag bool) {
		st := status(t, h)
		for _, g := range st.Games {
			if g.ID == id {
				flag = g.Starred
			}
		}
		return slices.Contains(st.Stars, id), flag
	}

	if rec := call(h, "POST", "/api/games/"+id+"/star", `{"on":true}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("star = %d %s", rec.Code, rec.Body)
	}
	if inList, flag := starred(); !inList || !flag {
		t.Fatalf("after starring %s: in stars %v, starred %v", id, inList, flag)
	}
	call(h, "POST", "/api/games/"+id+"/star", `{"on":true}`)
	if st := status(t, h); len(st.Stars) != len(before.Stars)+1 {
		t.Errorf("starring twice: stars %v", st.Stars)
	}
	call(h, "POST", "/api/games/"+id+"/star", `{"on":false}`)
	if inList, flag := starred(); inList || flag {
		t.Fatalf("after unstarring %s: in stars %v, starred %v", id, inList, flag)
	}
	if rec := call(h, "POST", "/api/games/"+id+"/star", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a star without on = %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/api/games/nosuchgame/star", `{"on":true}`); rec.Code != http.StatusNotImplemented {
		t.Errorf("starring a game that isn't recorded = %d %s", rec.Code, rec.Body)
	}

	// One list at a time: the other stays as it was.
	call(h, "POST", "/api/app/sidebar", `{"inactiveOpen":true}`)
	if st := status(t, h); !st.Sidebar.InactiveOpen || st.Sidebar.ActiveOpen != before.Sidebar.ActiveOpen {
		t.Errorf("sidebar after opening Inactive: %+v", st.Sidebar)
	}
	call(h, "POST", "/api/app/sidebar", `{"activeOpen":false}`)
	if st := status(t, h); !st.Sidebar.InactiveOpen || st.Sidebar.ActiveOpen {
		t.Errorf("sidebar after closing Active: %+v", st.Sidebar)
	}

	// Another change answers as the app does and changes nothing.
	after := call(h, "GET", "/api/status", "").Body.Bytes()
	if rec := call(h, "POST", "/api/games/"+id+"/start", `{"memory":"anyway"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("start = %d %s", rec.Code, rec.Body)
	}
	if again := call(h, "GET", "/api/status", "").Body.Bytes(); !bytes.Equal(again, after) {
		t.Error("a start changed the status")
	}
}

// The start refused for memory is answered to the request it was
// recorded for, written either way the panel may write it, and not to a
// start that says to go ahead.
func TestMemoryRefusal(t *testing.T) {
	f, h := newFixtures(t)
	var refused *recording
	for _, list := range f.changes {
		for i, d := range list {
			if d.Route == "POST /api/games/{id}/start" && d.Code == http.StatusBadRequest {
				refused = &list[i]
			}
		}
	}
	if refused == nil {
		t.Fatal("no refused start is recorded")
	}
	path := strings.TrimPrefix(refused.Request, "POST ")
	for _, body := range []string{`{"memory":""}`, `{}`, ``} {
		rec := call(h, "POST", path, body)
		if rec.Code != http.StatusBadRequest || !jsonEqual(t, rec.Body.Bytes(), refused.Answer) {
			t.Errorf("start %s = %d %s", body, rec.Code, rec.Body)
		}
	}
	var answer struct {
		Action string `json:"action"`
		Data   struct {
			Running []struct {
				ID string `json:"id"`
			} `json:"running"`
		} `json:"data"`
	}
	json.Unmarshal(refused.Answer, &answer)
	if answer.Action != "memory" || len(answer.Data.Running) == 0 {
		t.Fatalf("the recorded refusal isn't the memory warning: %s", refused.Answer)
	}
	for _, body := range []string{`{"memory":"anyway"}`, `{"memory":"stop:` + answer.Data.Running[0].ID + `"}`} {
		if rec := call(h, "POST", path, body); rec.Code != http.StatusOK {
			t.Errorf("start %s = %d %s", body, rec.Code, rec.Body)
		}
	}
}

// Only the app's routes answer, behind the app's guard; what isn't
// recorded says so rather than pretending.
func TestOnlyTheAppsRoutes(t *testing.T) {
	_, h := newFixtures(t)
	if rec := call(h, "POST", "/api/games/x/no-such-route", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("a route the app doesn't have = %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/api/games/x/start", ``); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET of a change = %d %s", rec.Code, rec.Body)
	}

	req := httptest.NewRequest("POST", "/api/games/x/stop", strings.NewReader(`{}`))
	req.RemoteAddr, req.Host = "127.0.0.1:50000", "127.0.0.1:8457"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a change without X-VES = %d %s", rec.Code, rec.Body)
	}

	st := status(t, h)
	download := "/api/games/" + st.Games[0].ID + "/logs/download"
	if rec := call(h, "GET", download, ""); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "devfixtures: ") {
		t.Errorf("a download = %d %s", rec.Code, rec.Body)
	}

	// Every route of the app answers something other than 404 or 405.
	for _, p := range web.Patterns() {
		method, path, _ := strings.Cut(p, " ")
		path = strings.NewReplacer("{id}", st.Games[0].ID, "{game}", st.Games[0].ID, "{upload}", "u1").Replace(path)
		if rec := call(h, method, path, `{}`); rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

func TestPanelLoads(t *testing.T) {
	_, h := newFixtures(t)
	for _, p := range []string{"/", "/assets/app.js"} {
		if rec := call(h, "GET", p, ""); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
}
