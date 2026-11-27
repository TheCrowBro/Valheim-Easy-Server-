package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"valheim-easy-server/internal/web"
)

// recording is one recorded answer: a file in recorded/, written by
// internal/web's TestRecordDevFixtures (its devRecording).
type recording struct {
	Route   string          `json:"route"`          // the app's pattern: "GET /api/games/{id}/settings"
	Request string          `json:"request"`        // what was asked: "GET /api/games/minecraft/settings"
	Body    json.RawMessage `json:"body,omitempty"` // a change's request body
	Note    string          `json:"note,omitempty"`
	Code    int             `json:"code"`
	Answer  json.RawMessage `json:"answer"`
}

// fixtures answers the panel from the recordings.
type fixtures struct {
	ui  string
	log *log.Logger

	gets    map[string]recording   // by request: "GET /api/status", "GET /api/games/x/logs?after=57"
	changes map[string][]recording // by method and path, without the query

	mu     sync.Mutex
	status map[string]any // the recorded status, with the stars and the sidebar as the panel set them
}

// load reads the recordings in dir.
func load(dir, ui string, logger *log.Logger) (*fixtures, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no recorded answers in %s: record them with go test ./internal/web -run TestRecordDevFixtures -record-devfixtures", dir)
	}
	f := &fixtures{ui: ui, log: logger, gets: map[string]recording{}, changes: map[string][]recording{}}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var d recording
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("%s: %v", file, err)
		}
		// Sent on one line, as the app sends it (the files are indented
		// for people to read).
		var answer bytes.Buffer
		if err := json.Compact(&answer, d.Answer); err != nil {
			return nil, fmt.Errorf("%s: %v", file, err)
		}
		d.Answer = answer.Bytes()
		method, path, _ := strings.Cut(d.Request, " ")
		if method == http.MethodGet {
			f.gets[d.Request] = d
			continue
		}
		path, _, _ = strings.Cut(path, "?")
		f.changes[method+" "+path] = append(f.changes[method+" "+path], d)
	}
	st, ok := f.gets["GET /api/status"]
	if !ok {
		return nil, fmt.Errorf("%s has no recorded GET /api/status", dir)
	}
	if f.status, err = decodeObject(st.Answer); err != nil {
		return nil, fmt.Errorf("the recorded status: %v", err)
	}
	return f, nil
}

// handler serves the panel's page and files, and the app's routes.
func (f *fixtures) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", f.index)
	files := http.FileServer(http.Dir(f.ui))
	mux.Handle("GET /assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	for _, pattern := range web.Patterns() {
		mux.HandleFunc(pattern, f.answer)
	}
	return web.Protect(mux)
}

func (f *fixtures) index(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(f.ui, "index.html"))
	if err != nil {
		http.Error(w, "devfixtures: no index.html in "+f.ui, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// answer answers one of the app's routes.
func (f *fixtures) answer(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		f.mu.Lock()
		b, err := json.Marshal(f.status)
		f.mu.Unlock()
		if err != nil {
			say(w, http.StatusInternalServerError, err.Error())
			return
		}
		send(w, http.StatusOK, b)
	case r.Method == http.MethodGet:
		f.get(w, r)
	case r.Pattern == "POST /api/games/{id}/star":
		f.star(w, r)
	case r.Pattern == "POST /api/app/sidebar":
		f.sidebar(w, r)
	default:
		f.change(w, r)
	}
}

// get answers a read with its recording: the one for the same query if
// there is one (a log after its last line), else the one without a query
// (the records of another period, say, are the recorded period's).
func (f *fixtures) get(w http.ResponseWriter, r *http.Request) {
	asked := "GET " + r.URL.Path
	d, ok := f.gets[asked+"?"+r.URL.RawQuery]
	if !ok {
		d, ok = f.gets[asked]
	}
	if !ok {
		say(w, http.StatusNotImplemented, "no recorded answer for "+asked+" (downloads are never recorded). "+
			"Try it in the app with --dev-fake-server and EGS_DEV_FAKE_SERVERS, or record the answers again.")
		return
	}
	send(w, d.Code, d.Answer)
}

// change answers a change: as the app answered the same request when it
// was recorded, else with the app's {"ok": true}. Either way nothing
// changes here.
func (f *fixtures) change(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		say(w, http.StatusBadRequest, "the request couldn't be read: "+err.Error())
		return
	}
	for _, d := range f.changes[r.Method+" "+r.URL.Path] {
		if sameRequest(d.Body, body) {
			send(w, d.Code, d.Answer)
			return
		}
	}
	f.log.Printf("%s %s: answered {\"ok\": true}; nothing changes here", r.Method, r.URL.Path)
	send(w, http.StatusOK, []byte(`{"ok":true}`))
}

// sameRequest reports whether a request's JSON body asks what a recorded
// one asked: each field the recording has, with the same value. A field
// the request leaves out counts as its empty value, as the app reads it,
// so {} asks the same as {"memory": ""}.
func sameRequest(recorded json.RawMessage, got []byte) bool {
	var want, have map[string]any
	if json.Unmarshal(recorded, &want) != nil || want == nil {
		return false
	}
	if len(bytes.TrimSpace(got)) > 0 && json.Unmarshal(got, &have) != nil {
		return false
	}
	for k, v := range want {
		h, ok := have[k]
		if !ok && v != nil {
			h = reflect.Zero(reflect.TypeOf(v)).Interface()
		}
		if !reflect.DeepEqual(h, v) {
			return false
		}
	}
	return true
}

// star stars or unstars one of the recorded games: {on}.
func (f *fixtures) star(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.On == nil {
		say(w, http.StatusBadRequest, `a star needs {"on": true} or {"on": false}.`)
		return
	}
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	games, _ := f.status["games"].([]any)
	found := false
	for _, g := range games {
		if g, ok := g.(map[string]any); ok && g["id"] == id {
			g["starred"], found = *body.On, true
		}
	}
	if !found {
		say(w, http.StatusNotImplemented, "no recorded game "+id+".")
		return
	}
	var stars []any
	if old, ok := f.status["stars"].([]any); ok {
		stars = slices.DeleteFunc(slices.Clone(old), func(s any) bool { return s == id })
	}
	if *body.On {
		stars = append(stars, id)
	}
	if stars == nil {
		stars = []any{}
	}
	f.status["stars"] = stars
	send(w, http.StatusOK, []byte(`{"ok":true}`))
}

// sidebar remembers which sidebar lists are open: {activeOpen?,
// inactiveOpen?}; a list the request leaves out stays as it is.
func (f *fixtures) sidebar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveOpen   *bool `json:"activeOpen"`
		InactiveOpen *bool `json:"inactiveOpen"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &body); err != nil {
			say(w, http.StatusBadRequest, "the request couldn't be read: "+err.Error())
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	side, ok := f.status["sidebar"].(map[string]any)
	if !ok {
		side = map[string]any{}
		f.status["sidebar"] = side
	}
	if body.ActiveOpen != nil {
		side["activeOpen"] = *body.ActiveOpen
	}
	if body.InactiveOpen != nil {
		side["inactiveOpen"] = *body.InactiveOpen
	}
	send(w, http.StatusOK, []byte(`{"ok":true}`))
}

// decodeObject reads a JSON object, keeping its numbers exactly as they
// were written.
func decodeObject(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return v, nil
}

// send writes a JSON answer as the app does.
func send(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(body)
	w.Write([]byte("\n"))
}

// say writes an error of devfixtures' own, in the app's error shape.
func say(w http.ResponseWriter, code int, msg string) {
	b, _ := json.Marshal(map[string]string{"error": "devfixtures: " + msg})
	send(w, code, b)
}
