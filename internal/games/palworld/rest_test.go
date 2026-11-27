package palworld

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// fakeAPI answers like the server's REST API, from recorded answers in
// testdata/rest (FACTS.md has where each comes from), and records what it
// was asked.
type fakeAPI struct {
	t        *testing.T
	srv      *httptest.Server
	password string
	info     string // testdata file
	players  string // testdata file; "" = answer 500
	saveWait time.Duration
	refuse   map[string]int // endpoint → status to answer

	mu   sync.Mutex
	got  []string // "POST save len=0 body="
	auth []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, password: "secret", info: "info-v1.0.5.102999.json", players: "players-empty-v1.0.5.102999.json", refuse: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	endpoint := strings.TrimPrefix(r.URL.Path, "/v1/api/")
	user, pw, _ := r.BasicAuth()
	f.mu.Lock()
	f.got = append(f.got, r.Method+" "+endpoint+" len="+r.Header.Get("Content-Length")+" body="+string(body))
	f.auth = append(f.auth, user+":"+pw)
	f.mu.Unlock()
	if user != "admin" || pw != f.password {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if code := f.refuse[endpoint]; code != 0 {
		http.Error(w, "nope", code)
		return
	}
	file := ""
	switch r.Method + " " + endpoint {
	case "GET info":
		file = f.info
	case "GET players":
		file = f.players
		if file == "" {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	case "POST save":
		time.Sleep(f.saveWait)
		w.Write([]byte("OK"))
		return
	case "POST shutdown":
		w.Write([]byte("OK"))
		return
	default:
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile("testdata/rest/" + file)
	if err != nil {
		f.t.Error(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (f *fakeAPI) record() game.Record {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	return game.Record{Ports: map[string]int{"game": 8211, "rest": p}, Secrets: map[string]string{restSecret: f.password}}
}

func (f *fakeAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// The recorded 1.0.5 answers: up, nobody on, the version.
func TestProbeRealAnswers(t *testing.T) {
	api := newFakeAPI(t)
	res, err := (&server{}).Probe(context.Background(), api.record())
	if err != nil || !res.Up || res.Count != 0 || res.Names == nil || len(res.Names) != 0 || res.Version != "v1.0.5.102999" {
		t.Errorf("Probe = %+v, %v", res, err)
	}
	for _, a := range api.auth {
		if a != "admin:secret" {
			t.Errorf("basic auth %q, want admin and the run's password", a)
		}
	}
	in, err := (&restClient{port: api.record().Ports["rest"], password: "secret", http: localHTTP}).info(context.Background())
	if err != nil || in.WorldGUID != "36B882106A4F4EBCAA16940C167F791E" || in.ServerName != "My Palworld Server" {
		t.Errorf("info = %+v, %v", in, err)
	}
}

// The documentation's answers: a player with every field; and a player
// still loading in, who counts but has no name yet.
func TestProbeDocsAndLoadingPlayer(t *testing.T) {
	api := newFakeAPI(t)
	api.info, api.players = "info-docs.json", "players-docs.json"
	res, err := (&server{}).Probe(context.Background(), api.record())
	if err != nil || res.Count != 1 || !slices.Equal(res.Names, []string{"PalUser"}) || res.Version != "v0.1.5.0" {
		t.Errorf("Probe = %+v, %v", res, err)
	}
	c := &restClient{port: api.record().Ports["rest"], password: "secret", http: localHTTP}
	list, err := c.players(context.Background())
	if err != nil || len(list) != 1 || list[0].UserID != "steam_00000000000000000" || list[0].PlayerID != "AFAFD830000000000000000000000000" || list[0].BuildingCount != 119 || list[0].Ping != 3.14 {
		t.Errorf("players = %+v, %v", list, err)
	}
	api.players = "players-one-loading-written.json"
	res, err = (&server{}).Probe(context.Background(), api.record())
	if err != nil || res.Count != 2 || !slices.Equal(res.Names, []string{"PalUser"}) {
		t.Errorf("with a player loading in: %+v, %v", res, err)
	}
}

// When only the player list fails the server is still up, with nobody
// named; when nothing answers, or the password is refused, the probe
// fails and says why.
func TestProbeFailures(t *testing.T) {
	api := newFakeAPI(t)
	api.players = ""
	res, err := (&server{}).Probe(context.Background(), api.record())
	if err != nil || !res.Up || res.Count != -1 || res.Names != nil {
		t.Errorf("players failing: %+v, %v", res, err)
	}
	rec := api.record()
	rec.Secrets[restSecret] = "old"
	if _, err := (&server{}).Probe(context.Background(), rec); !errors.Is(err, errUnauthorized) {
		t.Errorf("a wrong password: %v", err)
	}
	api.srv.Close()
	if _, err := (&server{}).Probe(context.Background(), api.record()); err == nil {
		t.Error("a server that's gone answered")
	}
	if _, err := (&server{}).Probe(context.Background(), game.Record{}); err == nil {
		t.Error("a record with no REST port")
	}
}

// The stop: save, then shutdown with the required waittime. The POST
// without a body still says its length.
func TestRESTStop(t *testing.T) {
	api := newFakeAPI(t)
	steps := (&server{}).Stop(api.record())
	if len(steps) != 2 || steps[0].Kind != "call" || steps[0].Wait != 3*time.Minute || steps[1].Kind != "interrupt" || steps[1].Wait != 2*time.Minute || steps[1].Say == "" {
		t.Fatalf("steps = %+v", steps)
	}
	if err := steps[0].Call(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := api.calls()
	if len(got) != 2 || got[0] != "POST save len=0 body=" || !strings.HasPrefix(got[1], "POST shutdown ") {
		t.Fatalf("calls = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got[1][strings.Index(got[1], "body=")+5:]), &body); err != nil || body["waittime"] != float64(1) || body["message"] == "" {
		t.Errorf("shutdown body %q: %v", got[1], err)
	}
}

// A save that is refused or turned down ends the step at once (the chain
// goes on to Ctrl+C); a save that takes longer than the caller waits is
// still followed by the shutdown, which saves too.
func TestRESTStopFailures(t *testing.T) {
	api := newFakeAPI(t)
	rec := api.record()
	rec.Secrets[restSecret] = "old"
	if err := restStop(context.Background(), rec); !errors.Is(err, errUnauthorized) {
		t.Errorf("wrong password: %v", err)
	}
	if got := api.calls(); len(got) != 1 {
		t.Errorf("after a refused save the shutdown was still sent: %q", got)
	}

	api = newFakeAPI(t)
	api.saveWait = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := restStop(ctx, api.record()); err != nil {
		t.Errorf("a slow save: %v", err)
	}
	if got := api.calls(); len(got) != 2 || !strings.HasPrefix(got[1], "POST shutdown") {
		t.Errorf("after a slow save: %q", got)
	}

	api = newFakeAPI(t)
	api.refuse["shutdown"] = http.StatusBadRequest
	if err := restStop(context.Background(), api.record()); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("a shutdown turned down: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	err = restStop(context.Background(), game.Record{Ports: map[string]int{"rest": port}})
	if err == nil || !refused(err) {
		t.Errorf("nothing listening: %v", err)
	}
}
