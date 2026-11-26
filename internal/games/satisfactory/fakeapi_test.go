package satisfactory

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// fakeAPI is a recording stand-in for the server's HTTPS API, behind a TLS
// certificate of its own (httptest's), for the client's and the setup's
// tests. It keeps the state the setup changes and writes real-format saves
// into dir, so the world steps can be checked too. Every call is recorded,
// passwords and tokens replaced by what they are, for the golden files.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server
	dir string // the saves folder

	mu       sync.Mutex
	calls    []string
	claimed  bool
	name     string
	admin    string
	client   string
	session  string
	running  bool
	autoload string
	players  int
	tokens   map[string]string
	n        int
	// answer, when set, answers a function instead (the call is still
	// recorded): for errors and real answers from testdata.
	answer map[string]func(w http.ResponseWriter)
}

func newFakeAPI(t *testing.T, dir string) *fakeAPI {
	f := &fakeAPI{t: t, dir: dir, tokens: map[string]string{}, answer: map[string]func(http.ResponseWriter){}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.handle))
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// port is where the API listens on 127.0.0.1.
func (f *fakeAPI) port() int {
	return f.srv.Listener.Addr().(*net.TCPAddr).Port
}

func (f *fakeAPI) record(s string) {
	f.calls = append(f.calls, s)
}

// Calls returns the calls so far, and forgets them.
func (f *fakeAPI) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func (f *fakeAPI) token(level string) string {
	f.n++
	tok := fmt.Sprintf("tok%d-%s", f.n, level)
	f.tokens[tok] = level
	return tok
}

// redact names a secret by what it is, for the golden files.
func (f *fakeAPI) redact(function, v string) string {
	switch {
	case v == "":
		return `""`
	case function == "ClaimServer":
		return "<admin password>"
	case function == "SetAdminPassword":
		return "<new admin password>"
	case function == "SetClientPassword":
		return "<friends' password>"
	case v == f.admin:
		return "<admin password>"
	}
	return "<a wrong password>"
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	var env struct {
		Function string          `json:"function"`
		Data     json.RawMessage `json:"data"`
	}
	body, _ := io.ReadAll(r.Body)
	if r.URL.Path != "/api/v1" || json.Unmarshal(body, &env) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var data map[string]json.RawMessage
	json.Unmarshal(env.Data, &data)
	str := func(k string) string {
		var v string
		json.Unmarshal(data[k], &v)
		return v
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	auth := r.Header.Get("Authorization")
	level := ""
	switch {
	case auth == "":
	case strings.HasPrefix(auth, "Bearer ") && f.tokens[strings.TrimPrefix(auth, "Bearer ")] != "":
		level = f.tokens[strings.TrimPrefix(auth, "Bearer ")]
	default:
		f.record(env.Function + " (with a token it doesn't know)")
		writeAPI(w, 401, `{"errorCode":"invalid_token","errorMessage":"The provided token is invalid"}`)
		return
	}
	// The record line: the function, its parameters in the order sent,
	// secrets replaced, and who called.
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		switch k {
		case "password", "adminPassword":
			parts = append(parts, k+"="+f.redact(env.Function, str(k)))
		case "newGameData":
			var nd map[string]any
			json.Unmarshal(data[k], &nd)
			b, _ := json.Marshal(nd)
			parts = append(parts, k+"="+string(b))
		default:
			parts = append(parts, k+"="+string(data[k]))
		}
	}
	who := "no token"
	if level != "" {
		who = level
	}
	f.record(fmt.Sprintf("%s(%s) as %s", env.Function, strings.Join(parts, ", "), who))
	if a := f.answer[env.Function]; a != nil {
		a(w)
		return
	}
	admin := level == "Administrator"
	deny := func() { writeAPI(w, 403, `{"errorCode":"insufficient_scope"}`) }
	switch env.Function {
	case "HealthCheck":
		writeAPI(w, 200, `{"data":{"health":"healthy","serverCustomData":""}}`)
	case "PasswordlessLogin":
		if !f.claimed {
			writeAPI(w, 200, fmt.Sprintf(`{"data":{"authenticationToken":%q}}`, f.token("InitialAdmin")))
		} else {
			writeAPI(w, 200, `{"errorCode":"passwordless_login_not_possible","errorMessage":"Passwordless login is not possible"}`)
		}
	case "PasswordLogin":
		if f.claimed && str("password") == f.admin {
			writeAPI(w, 200, fmt.Sprintf(`{"data":{"authenticationToken":%q}}`, f.token("Administrator")))
		} else {
			writeAPI(w, 200, `{"errorCode":"wrong_password","errorMessage":"Wrong password"}`)
		}
	case "ClaimServer":
		if level != "InitialAdmin" {
			deny()
			return
		}
		if f.claimed {
			writeAPI(w, 200, `{"errorCode":"server_claimed"}`)
			return
		}
		f.claimed, f.name, f.admin = true, str("serverName"), str("adminPassword")
		writeAPI(w, 200, fmt.Sprintf(`{"data":{"authenticationToken":%q}}`, f.token("Administrator")))
	case "RenameServer":
		if !admin {
			deny()
			return
		}
		f.name = str("serverName")
		w.WriteHeader(204)
	case "SetClientPassword":
		if !admin {
			deny()
			return
		}
		f.client = str("password")
		w.WriteHeader(204)
	case "SetAdminPassword":
		if !admin {
			deny()
			return
		}
		f.admin = str("password")
		for k, l := range f.tokens {
			if l == "Administrator" {
				delete(f.tokens, k)
			}
		}
		writeAPI(w, 200, fmt.Sprintf(`{"data":{"authenticationToken":%q}}`, f.token("Administrator")))
	case "QueryServerState":
		if !admin {
			deny()
			return
		}
		st := map[string]any{"activeSessionName": f.session, "numConnectedPlayers": f.players, "playerLimit": 4,
			"isGameRunning": f.running, "autoLoadSessionName": f.autoload}
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"serverGameState": st}})
		writeAPI(w, 200, string(b))
	case "CreateNewGame":
		if !admin {
			deny()
			return
		}
		var nd struct {
			SessionName string `json:"sessionName"`
		}
		json.Unmarshal(data["newGameData"], &nd)
		f.session, f.running = nd.SessionName, true
		w.WriteHeader(202)
	case "LoadGame":
		if !admin {
			deny()
			return
		}
		h, err := readSaveHeaderFile(filepath.Join(f.dir, str("saveName")+".sav"))
		if err != nil {
			writeAPI(w, 200, `{"errorCode":"save_game_load_failed"}`)
			return
		}
		f.session, f.running = h.SessionName, true
		w.WriteHeader(202)
	case "SaveGame":
		if !admin {
			deny()
			return
		}
		writeTestSave(f.t, f.dir, str("saveName"), f.session, time.Now())
		w.WriteHeader(204)
	case "SetAutoLoadSessionName":
		if !admin {
			deny()
			return
		}
		f.autoload = str("sessionName")
		w.WriteHeader(204)
	case "Shutdown":
		if !admin {
			deny()
			return
		}
		f.running = false
		w.WriteHeader(204)
	default:
		writeAPI(w, 404, `{"errorCode":"function_not_found"}`)
	}
}

func writeAPI(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// buildHeader is a save header in the game's layout (savheader.go).
func buildHeader(version int32, saveName, session string, played int32, at time.Time) []byte {
	var b []byte
	i32 := func(v int32) { b = binary.LittleEndian.AppendUint32(b, uint32(v)) }
	str := func(s string) {
		if s == "" {
			i32(0)
			return
		}
		i32(int32(len(s) + 1))
		b = append(b, s...)
		b = append(b, 0)
	}
	i32(version)
	i32(52)
	i32(418783)
	if version >= 14 {
		str(saveName)
	}
	str("Persistent_Level")
	str("?sessionName=" + session)
	str(session)
	i32(played)
	b = binary.LittleEndian.AppendUint64(b, uint64(timeTicks(at)))
	b = append(b, 0)
	return b
}

// writeTestSave writes <dir>/<name>.sav for session, saved at.
func writeTestSave(t *testing.T, dir, name, session string, at time.Time) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, name+".sav")
	data := append(buildHeader(14, name, session, 60, at), make([]byte, 512)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, at, at)
	return path
}

// testServer is an adapter for an instance in a temporary folder, with its
// settings in memory and its log in the test's.
func testServer(t *testing.T, fake bool) *server {
	t.Helper()
	root := t.TempDir()
	env := game.Env{
		Instance: ID,
		Data:     root,
		Dirs: game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
			Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Staging: filepath.Join(root, "uploads")},
		Log:   log.New(newTestWriter(t), "satisfactory: ", 0),
		Store: &memStore{},
	}
	if fake {
		env.Fake = &game.Fake{Program: "fakeserver"}
	}
	s := Game{}.New(env).(*server)
	s.store.Save(func(sv *game.Saved) error {
		*sv = game.Saved{Server: game.Values{"serverName": "Factory Friends"}, Worlds: map[string]game.Values{}, World: "Factory",
			Ports: map[string]int{"game": 7777, "reliable": 8888}, State: game.Values{}}
		return nil
	})
	return s
}

// testWriter sends the adapter's log to the test's, and drops what comes
// after the test ends (a setup goroutine may still be writing), which
// testing would otherwise panic on.
type testWriter struct {
	mu     sync.Mutex
	t      *testing.T
	closed bool
}

func newTestWriter(t *testing.T) *testWriter {
	w := &testWriter{t: t}
	t.Cleanup(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
	})
	return w
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// restart acts out the server starting again: tokens from before are
// gone, and it loads the session set to load at start, if it has saves.
func (f *fakeAPI) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = map[string]string{}
	f.running, f.session, f.players = false, "", 0
	if f.autoload == "" {
		return
	}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if h, err := readSaveHeaderFile(filepath.Join(f.dir, e.Name())); err == nil && strings.EqualFold(h.SessionName, f.autoload) {
			f.session, f.running = h.SessionName, true
			return
		}
	}
}

// set changes the fake's state under its lock.
func (f *fakeAPI) set(change func(f *fakeAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}
