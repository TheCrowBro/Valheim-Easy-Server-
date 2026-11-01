package waker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// fakeDiscord stands in for Discord's API and gateway.
type fakeDiscord struct {
	srv    *httptest.Server
	events chan map[string]any // pushed to the connected bot
	guilds []string            // the Discord servers the bot is in

	mu         sync.Mutex
	commands   map[string][]map[string]any // by guild
	replies    []string
	edits      []string
	identifies int
	resumes    int
	beats      int
}

func newFakeDiscord(t *testing.T, token string) *fakeDiscord {
	f := &fakeDiscord{events: make(chan map[string]any, 10), commands: map[string][]map[string]any{}, guilds: []string{"g1"}}
	mux := http.NewServeMux()
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bot "+token {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message": "401: Unauthorized", "code": 0}`)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v10/users/@me", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			io.WriteString(w, `{"id": "9", "username": "valheim", "global_name": "Valheim"}`)
		}
	})
	mux.HandleFunc("GET /api/v10/applications/@me", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			io.WriteString(w, `{"id": "app1", "name": "Valheim"}`)
		}
	})
	mux.HandleFunc("PUT /api/v10/applications/app1/guilds/{guild}/commands", func(w http.ResponseWriter, r *http.Request) {
		var cmds []map[string]any
		json.NewDecoder(r.Body).Decode(&cmds)
		f.mu.Lock()
		f.commands[r.PathValue("guild")] = cmds
		f.mu.Unlock()
		io.WriteString(w, `[]`)
	})
	mux.HandleFunc("POST /api/v10/interactions/{id}/{token}/callback", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type int `json:"type"`
			Data struct {
				Content string `json:"content"`
				Flags   int    `json:"flags"`
			} `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Data.Flags&64 != 0 {
			body.Data.Content = "(only them) " + body.Data.Content
		}
		f.mu.Lock()
		f.replies = append(f.replies, body.Data.Content)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH /api/v10/webhooks/app1/{token}/messages/@original", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.edits = append(f.edits, r.PathValue("token")+": "+body.Content)
		f.mu.Unlock()
		io.WriteString(w, `{}`)
	})
	mux.Handle("/gateway/", websocket.Handler(f.gateway))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// gateway plays Discord's gateway for one connection.
func (f *fakeDiscord) gateway(ws *websocket.Conn) {
	var wmu sync.Mutex
	send := func(v map[string]any) error {
		wmu.Lock()
		defer wmu.Unlock()
		return websocket.JSON.Send(ws, v)
	}
	send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 200}})
	var hi payload
	if websocket.JSON.Receive(ws, &hi) != nil {
		return
	}
	f.mu.Lock()
	switch hi.Op {
	case 2:
		f.identifies++
	case 6:
		f.resumes++
	}
	f.mu.Unlock()
	if hi.Op == 2 {
		send(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{
			"session_id": "s1", "resume_gateway_url": "ws://" + f.srv.Listener.Addr().String() + "/gateway",
			"application": map[string]any{"id": "app1"}, "user": map[string]any{"username": "valheim", "global_name": "Valheim"},
		}})
		for _, g := range f.guilds {
			send(map[string]any{"op": 0, "s": 2, "t": "GUILD_CREATE", "d": map[string]any{"id": g}})
		}
	} else {
		send(map[string]any{"op": 0, "s": 3, "t": "RESUMED", "d": nil})
	}
	done := make(chan struct{})
	go func() { // heartbeats get their answer
		defer close(done)
		for {
			var p payload
			if websocket.JSON.Receive(ws, &p) != nil {
				return
			}
			if p.Op == 1 {
				f.mu.Lock()
				f.beats++
				f.mu.Unlock()
				send(map[string]any{"op": 11})
			}
		}
	}()
	for {
		select {
		case ev := <-f.events:
			if send(ev) != nil || ev["op"] == 7 {
				ws.Close()
				return
			}
		case <-done:
			return
		}
	}
}

func (f *fakeDiscord) interaction(id, nick string) { f.interactionIn("g1", id, nick) }

func (f *fakeDiscord) interactionIn(guild, id, nick string) {
	f.events <- map[string]any{"op": 0, "s": 5, "t": "INTERACTION_CREATE", "d": map[string]any{
		"id": id, "token": "tok-" + id, "type": 2, "application_id": "app1", "guild_id": guild, "data": map[string]any{"name": "valheim"},
		"member": map[string]any{"nick": nick, "user": map[string]any{"username": "x"}},
	}}
}

func (f *fakeDiscord) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		ok := cond()
		f.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("timed out waiting for %s; replies %q, edits %q", what, f.replies, f.edits)
}

func has(list []string, text string) bool {
	for _, s := range list {
		if strings.Contains(s, text) {
			return true
		}
	}
	return false
}

func testBot(t *testing.T, f *fakeDiscord, token string) *Bot {
	b := New(Config{Token: token, MAC: "3C:07:54:5A:21:9F", Query: "192.0.2.20:2457", Name: "Viking_Night",
		API: f.srv.URL + "/api/v10", Gateway: "ws://" + f.srv.Listener.Addr().String() + "/gateway"},
		log.New(io.Discard, "", 0))
	b.StatusFile = filepath.Join(t.TempDir(), "status.json")
	b.wakeEvery, b.wakeFor, b.waitFor = 20*time.Millisecond, 150*time.Millisecond, 5*time.Second
	return b
}

func run(t *testing.T, b *Bot) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestWakeFromDiscord(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	b := testBot(t, f, "secret")
	var sent atomic.Int32
	var up atomic.Bool
	b.send = func(mac net.HardwareAddr, tag []byte, targets []string) error {
		if mac.String() != "3c:07:54:5a:21:9f" || len(tag) != 6 || len(targets) == 0 {
			t.Errorf("sent for %s (tag %x) to %v", mac, tag, targets)
		}
		sent.Add(1)
		return nil
	}
	b.query = func(ctx context.Context, addr string) (Info, error) {
		if addr != "192.0.2.20:2457" || !up.Load() {
			return Info{}, errors.New("no answer")
		}
		return Info{Name: "Viking Night", Players: 2}, nil
	}
	run(t, b)
	f.wait(t, "/valheim to be set up", func() bool {
		c := f.commands["g1"]
		return len(c) == 1 && c[0]["name"] == "valheim"
	})

	// The server is down: wake it.
	f.interaction("i1", "Astrid")
	f.wait(t, "the answer", func() bool { return has(f.replies, "⏰ Waking up the server computer for Astrid") })
	for sent.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	f.interaction("i2", "Bjorn")
	f.wait(t, "the second answer", func() bool { return has(f.replies, "already waking up: Astrid asked just now") })
	up.Store(true)
	f.wait(t, "the update", func() bool {
		return has(f.edits, "tok-i1: ✅ **Viking\\_Night** is online, with 2 playing. Have fun!")
	})
	n := sent.Load()
	time.Sleep(100 * time.Millisecond)
	if sent.Load() != n {
		t.Error("still sending wake packets after the server came up")
	}

	// Already up: nothing to wake.
	f.interaction("i3", "Sigrid")
	f.wait(t, "the answer when online", func() bool { return has(f.replies, "🟢 **Viking\\_Night** is already online, with 2 playing.") })
	if sent.Load() != n {
		t.Error("woke a server that was up")
	}

	var st Status
	data, _ := os.ReadFile(b.StatusFile)
	if json.Unmarshal(data, &st) != nil || !st.Connected || st.Bot != "Valheim" || st.Servers != 1 || st.WokenBy != "Astrid" || st.LastWake == nil {
		t.Fatalf("status = %s", data)
	}
}

func TestReconnectResumes(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	b := testBot(t, f, "secret")
	run(t, b)
	f.wait(t, "the first session", func() bool { return len(f.commands["g1"]) == 1 && f.beats > 0 })
	f.events <- map[string]any{"op": 7}
	f.wait(t, "a resumed session", func() bool { return f.resumes == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identifies != 1 {
		t.Fatalf("identified %d times", f.identifies)
	}
}

func TestWakeTimesOut(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	b := testBot(t, f, "secret")
	b.waitFor = 200 * time.Millisecond
	b.send = func(net.HardwareAddr, []byte, []string) error { return nil }
	b.query = func(context.Context, string) (Info, error) { return Info{}, errors.New("no answer") }
	run(t, b)
	f.wait(t, "/valheim to be set up", func() bool { return len(f.commands["g1"]) == 1 })
	f.interaction("i1", "Astrid")
	// Valheim never answered a query, so this may be how it always is.
	f.wait(t, "the update", func() bool {
		return has(f.edits, "tok-i1: ⏰ The server computer was woken up and should start Valheim.")
	})
	// It answered before: now something's wrong.
	b.mu.Lock()
	b.queryWorks = true
	b.mu.Unlock()
	f.interaction("i2", "Astrid")
	f.wait(t, "the warning", func() bool {
		return has(f.edits, "tok-i2: ⚠️ The server computer was woken up, but Valheim didn’t answer")
	})
}

// With a Discord server set, the bot answers only there, and takes
// /valheim away from any other it's in.
func TestOnlyItsOwnServer(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	f.guilds = []string{"g1", "g2"}
	f.commands["g2"] = []map[string]any{command} // from before it was limited
	b := testBot(t, f, "secret")
	b.Config.Guild = "g1"
	var sent atomic.Int32
	b.send = func(net.HardwareAddr, []byte, []string) error { sent.Add(1); return nil }
	b.query = func(context.Context, string) (Info, error) { return Info{}, errors.New("no answer") }
	run(t, b)
	f.wait(t, "/valheim in g1 only", func() bool { return len(f.commands["g1"]) == 1 && len(f.commands["g2"]) == 0 })
	f.interactionIn("g2", "i1", "Loki")
	f.wait(t, "the refusal", func() bool { return has(f.replies, "(only them) 🔒 This only works in the Discord server") })
	if sent.Load() != 0 {
		t.Fatal("woke the server for another Discord server")
	}
	f.interaction("i2", "Astrid")
	f.wait(t, "the answer", func() bool { return has(f.replies, "⏰ Waking up the server computer for Astrid") })
	f.wait(t, "the status", func() bool {
		var st Status
		data, _ := os.ReadFile(b.StatusFile)
		return json.Unmarshal(data, &st) == nil && st.Servers == 1
	})
}

func TestBadToken(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	b := testBot(t, f, "wrong")
	if _, err := b.Check(context.Background()); !errors.Is(err, ErrToken) {
		t.Fatalf("err = %v", err)
	}
	if _, err := b.Application(context.Background()); !errors.Is(err, ErrToken) {
		t.Fatalf("application err = %v", err)
	}
	good := testBot(t, f, "secret")
	if name, err := good.Check(context.Background()); err != nil || name != "Valheim" {
		t.Fatalf("check = %q %v", name, err)
	}
	if app, err := good.Application(context.Background()); err != nil || app != "app1" {
		t.Fatalf("application = %q %v", app, err)
	}
	want := "https://discord.com/oauth2/authorize?client_id=app1&disable_guild_select=true&guild_id=g1&permissions=0&scope=bot+applications.commands"
	if got := Invite("app1", "g1"); got != want {
		t.Fatalf("invite = %s", got)
	}
}

func TestQuery(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		buf := make([]byte, 1400)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n == 25 { // no challenge yet: ask for one
				conn.WriteTo([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'A', 1, 2, 3, 4}, addr)
				continue
			}
			if n != 29 || string(buf[25:29]) != "\x01\x02\x03\x04" {
				continue
			}
			info := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'I', 17}, "Viking Night\x00Midgard\x00valheim\x00Valheim\x00"...)
			info = append(info, 0x0C, 0x22, 3, 10, 0)
			conn.WriteTo(info, addr)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := Query(ctx, conn.LocalAddr().String())
	if err != nil || info != (Info{Name: "Viking Night", Players: 3}) {
		t.Fatalf("info = %+v, %v", info, err)
	}
	// Nothing answers: it gives up.
	silent, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer silent.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := Query(ctx2, silent.LocalAddr().String()); err == nil {
		t.Fatal("no error without an answer")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "config.json")
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	if c, err := LoadConfig(write(`{"token": "abc", "mac": "3C:07:54:5A:21:9F", "query": "192.168.1.20:2457"}`)); err != nil || c.Query != "192.168.1.20:2457" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	for _, bad := range []string{`{"mac": "3C:07:54:5A:21:9F"}`, `{"token": "abc", "mac": "nope"}`, `{`} {
		if _, err := LoadConfig(write(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
