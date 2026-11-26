package satisfactory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// tlsAPI is an HTTPS stand-in for the API that answers with handle and
// keeps the last request.
type tlsAPI struct {
	srv  *httptest.Server
	last *http.Request
	body []byte
}

func newTLSAPI(t *testing.T, handle func(w http.ResponseWriter, function string)) *tlsAPI {
	t.Helper()
	a := &tlsAPI{}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.last, a.body = r, nil
		a.body, _ = io.ReadAll(r.Body)
		var env struct {
			Function string `json:"function"`
		}
		json.Unmarshal(a.body, &env)
		handle(w, env.Function)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *tlsAPI) port() int { return a.srv.Listener.Addr().(*net.TCPAddr).Port }

// key is the fingerprint of the stand-in's certificate key.
func (a *tlsAPI) key() string { return keyFingerprint(a.srv.Certificate()) }

func testdataFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "api", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// A real server's answers (testdata/README.md) read as the app expects.
func TestRealAnswers(t *testing.T) {
	a := newTLSAPI(t, func(w http.ResponseWriter, function string) {
		switch function {
		case "QueryServerState":
			writeAPI(w, 200, testdataFile(t, "QueryServerState-1.2.4.0.json"))
		case "CreateNewGame":
			writeAPI(w, 200, testdataFile(t, "CreateNewGame-missing-1.2.4.0.json"))
		}
	})
	c := newAPI(a.port(), a.key())
	st, err := c.queryState(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := serverGameState{ActiveSessionName: "docs-vault-spike", PlayerLimit: 4, TechTier: 2, ActiveSchematic: "None",
		GamePhase:     "/Script/FactoryGame.FGGamePhase'/Game/FactoryGame/GamePhases/GP_Project_Assembly_Phase_0.GP_Project_Assembly_Phase_0'",
		IsGameRunning: true, TotalGameDuration: 29, AverageTickRate: 29.885358810424805, AutoLoadSessionName: "docs-vault-spike"}
	if st != want {
		t.Errorf("queryState = %+v\nwant         %+v", st, want)
	}

	err = c.createNewGame(ctx(t), "x", "")
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "missing_params" || ae.Status != 200 || !strings.Contains(ae.Message, "bSkipOnboarding") {
		t.Fatalf("createNewGame against the real error = %#v", err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "Satisfactory's server API turned down CreateNewGame: ") || !strings.HasSuffix(msg, "(missing_params)") {
		t.Errorf("error text = %q", msg)
	}
}

// What the client sends: a POST to /api/v1 of {"function", "data"} in
// camelCase, every newGameData field the real server insists on, no data
// for a function without parameters, and an Authorization header only with
// a token (an empty "Bearer" is refused as malformed).
func TestRequestShape(t *testing.T) {
	a := newTLSAPI(t, func(w http.ResponseWriter, function string) { w.WriteHeader(204) })
	c := newAPI(a.port(), a.key())
	if err := c.createNewGame(ctx(t), "Factory", "Dune Desert"); err != nil {
		t.Fatal(err)
	}
	if a.last.Method != http.MethodPost || a.last.URL.Path != "/api/v1" || !strings.HasPrefix(a.last.Header.Get("Content-Type"), "application/json") {
		t.Errorf("request %s %s, %q", a.last.Method, a.last.URL.Path, a.last.Header.Get("Content-Type"))
	}
	if _, ok := a.last.Header["Authorization"]; ok {
		t.Errorf("an Authorization header without a token: %q", a.last.Header.Get("Authorization"))
	}
	const want = `{"function":"CreateNewGame","data":{"newGameData":{"sessionName":"Factory","mapName":"","startingLocation":"Dune Desert","bSkipOnboarding":true,"advancedGameSettings":{},"gameModeSettings":{},"customOptionsOnlyForModding":{}}}}`
	if string(a.body) != want {
		t.Errorf("body = %s\nwant   %s", a.body, want)
	}
	c.token = "abc.DEF"
	if err := c.shutdown(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if string(a.body) != `{"function":"Shutdown"}` || a.last.Header.Get("Authorization") != "Bearer abc.DEF" {
		t.Errorf("Shutdown sent %s with %q", a.body, a.last.Header.Get("Authorization"))
	}
	for _, f := range []func() error{
		func() error { return c.loadGame(ctx(t), "Factory_autosave_1") },
		func() error { return c.saveGame(ctx(t), "Factory") },
		func() error { return c.setAutoLoadSession(ctx(t), "Factory") },
		func() error { return c.setClientPassword(ctx(t), "") },
		func() error { return c.renameServer(ctx(t), "Name") },
	} {
		if err := f(); err != nil {
			t.Error(err)
		}
	}
	if string(a.body) != `{"function":"RenameServer","data":{"serverName":"Name"}}` {
		t.Errorf("RenameServer sent %s", a.body)
	}
}

// Statuses: 202 and 204 are success without data; 401 is a token to renew;
// a function that needs data and got none is an error, not a zero value.
func TestAnswerStatuses(t *testing.T) {
	status := 202
	a := newTLSAPI(t, func(w http.ResponseWriter, function string) {
		switch status {
		case 401:
			writeAPI(w, 401, `{"errorCode":"invalid_token","errorMessage":"Expired"}`)
		case 200:
			writeAPI(w, 200, `{"data":null}`)
		default:
			w.WriteHeader(status)
		}
	})
	c := newAPI(a.port(), a.key())
	if err := c.loadGame(ctx(t), "x"); err != nil {
		t.Errorf("202: %v", err)
	}
	status = 401
	if err := c.saveGame(ctx(t), "x"); !authFailed(err) || codeOf(err) != codeInvalidToken {
		t.Errorf("401: %v (authFailed %v)", err, authFailed(err))
	}
	status = 200
	if _, err := c.queryState(ctx(t)); err == nil || authFailed(err) {
		t.Errorf("an answer without data: %v", err)
	}
}

// The certificate check: the pinned key is the only one accepted, and
// nothing is sent to another (the request never reaches the server).
func TestPinnedKey(t *testing.T) {
	reached := 0
	a := newTLSAPI(t, func(w http.ResponseWriter, function string) {
		reached++
		writeAPI(w, 200, `{"data":{"health":"healthy","serverCustomData":""}}`)
	})
	if h, err := newAPI(a.port(), a.key()).healthCheck(ctx(t)); err != nil || h != "healthy" {
		t.Fatalf("with the right key: %q, %v", h, err)
	}
	c := newAPI(a.port(), "SHA256:somethingelse0000000000000000000000000000000=")
	_, err := c.passwordLogin(ctx(t), levelAdmin, "secret")
	var pe *pinError
	if !errors.As(err, &pe) || pe.key != a.key() || pe.cert != certFingerprint(a.srv.Certificate()) {
		t.Fatalf("with another key pinned: %v", err)
	}
	if reached != 1 {
		t.Errorf("the request with the password reached a server whose key isn't pinned")
	}
}

// Whether a key the app doesn't know yet is trusted: the program holding
// the port must be the server the app started; where the system can't
// say, the server's own log must name the key (first contact without a log
// line trusts what it finds).
func TestTrustKey(t *testing.T) {
	logWith := func(fp string) string {
		p := filepath.Join(t.TempDir(), "FactoryGame.log")
		os.WriteFile(p, []byte("[2026.11.26-20.00.00:000][  0]LogServer: Warning: "+fp+"\n"), 0o644)
		return p
	}
	const key, cert = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	for _, tc := range []struct {
		name  string
		owner int
		log   string
		first bool
		ok    bool
	}{
		{"held by the server", 4242, "", false, true},
		{"held by another program", 99, "", true, false},
		{"owner unknown, log names the key", 0, logWith(key), false, true},
		{"owner unknown, log names the certificate", 0, logWith(cert), false, true},
		{"owner unknown, log names another", 0, logWith("SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="), true, false},
		{"owner unknown, no log line, first contact", 0, "", true, true},
		{"owner unknown, no log line, changed key", 0, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := portOwner
			portOwner = func(proto string, port int) (bool, int) { return tc.owner != 0, tc.owner }
			defer func() { portOwner = old }()
			s := testServer(t, true)
			err := s.trustKey(game.Record{PID: 4242, LogPath: tc.log}, 7777, key, cert, tc.first)
			var de *distrustError
			switch {
			case tc.ok && err != nil:
				t.Errorf("not trusted: %v", err)
			case !tc.ok && !errors.As(err, &de):
				t.Errorf("trusted (%v)", err)
			case tc.ok:
				if st := s.store.Get().State; st[stateCert] != key || st[stateCertPort] != "7777" {
					t.Errorf("not pinned: %v", st)
				}
			}
		})
	}
}

// A server whose key changed (it made itself a new certificate) is
// trusted again when it is the one the app started, and the calls go on.
func TestChangedKeyOfOwnServer(t *testing.T) {
	a := newTLSAPI(t, func(w http.ResponseWriter, function string) {
		writeAPI(w, 200, `{"data":{"serverGameState":{"isGameRunning":true}}}`)
	})
	s := testServer(t, true)
	s.store.Save(func(sv *game.Saved) error {
		sv.State = game.Values{stateCert: "SHA256:old0000000000000000000000000000000000000000=", stateCertPort: itoa(a.port())}
		return nil
	})
	rec := game.Record{PID: os.Getpid(), Ports: map[string]int{"game": a.port()}}
	c, err := s.client(ctx(t), rec)
	if err != nil {
		t.Fatal(err)
	}
	var st serverGameState
	if err := s.checked(rec, c, func() (e error) { st, e = c.queryState(ctx(t)); return e }); err != nil || !st.IsGameRunning {
		t.Fatalf("after the key changed: %+v, %v", st, err)
	}
	if got := s.store.Get().State[stateCert]; got != a.key() {
		t.Errorf("pinned %q, want the new key %q", got, a.key())
	}
}
