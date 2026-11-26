package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	a := NewArgs([]string{"-port", "7777", "--world", "My World", "-Port2=8888", "players=16", "-nogui", "-modifier", "raids", "none", "-Dlog4j=x", "-jar", "server.jar"})
	cases := []struct{ got, want any }{
		{a.Int("port", 0), 7777},
		{a.Int("PORT", 0), 7777},
		{a.String("world", ""), "My World"},
		{a.Int("port2", 0), 8888},
		{a.Int("players", 0), 16},
		{a.Has("nogui"), true},
		{a.Has("gui"), false},
		{a.String("missing", "def"), "def"},
		{a.Int("world", 5), 5}, // not a number
		{a.After("modifier", 2), []string{"raids", "none"}},
		{a.After("jar", 2), []string(nil)}, // only one after it
		{a.String("Dlog4j", ""), "x"},
		{a.Contains("SERVER.JAR"), true},
		{a.Contains("server"), false},
		{a.String("jar", ""), "server.jar"},
	}
	for i, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("case %d: got %#v, want %#v", i, c.got, c.want)
		}
	}
	if NewArgs([]string{"-last"}).String("last", "def") != "" {
		t.Error("an option at the end has no value, but is there")
	}
}

func TestParseScript(t *testing.T) {
	got, err := parseScript(" join:Alex@1 , leave:Alex@9,death:Alex@5.5,save@7,count,deny:Big Bob@6")
	if err != nil {
		t.Fatal(err)
	}
	want := []Step{
		{Verb: "count"},
		{Verb: "join", Name: "Alex", At: time.Second},
		{Verb: "death", Name: "Alex", At: 5500 * time.Millisecond},
		{Verb: "deny", Name: "Big Bob", At: 6 * time.Second},
		{Verb: "save", At: 7 * time.Second},
		{Verb: "leave", Name: "Alex", At: 9 * time.Second},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"join:Alex@soon", ":Alex@1", "leave@-1"} {
		if _, err := parseScript(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if s, err := parseScript(""); err != nil || len(s) != 0 {
		t.Errorf("empty script: %v, %v", s, err)
	}
}

// TestLoadConfig: fakeserver.json is found beside the program or up to
// three folders above it, read strictly, and defaults without one.
func TestLoadConfig(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c", "d")
	os.MkdirAll(deep, 0o755)

	c, err := loadConfig(deep)
	if err != nil || c.Speed != 1 || c.Game != "" {
		t.Fatalf("no file: %+v, %v", c, err)
	}

	// Four folders up from deep is too far.
	os.WriteFile(filepath.Join(root, configName), []byte(`{"game":"far"}`), 0o644)
	if c, _ := loadConfig(deep); c.Game != "" {
		t.Errorf("used a fakeserver.json four folders up: %+v", c)
	}
	os.WriteFile(filepath.Join(root, "a", configName), []byte(`{"game":"near","script":"join:Alex@1","speed":60,"memMB":40,"crashAfter":2.5,"noReady":true,"version":"26.3","host":"0.0.0.0","options":{"party":"missing"}}`), 0o644)
	c, err = loadConfig(deep)
	if err != nil || c.Game != "near" || c.Speed != 60 || c.MemMB != 40 || c.CrashAfter != 2.5 || !c.NoReady ||
		c.Version != "26.3" || c.Host != "0.0.0.0" || c.Options["party"] != "missing" || len(c.steps) != 1 {
		t.Errorf("three up: %+v, %v", c, err)
	}
	// The nearest one wins.
	os.WriteFile(filepath.Join(deep, configName), []byte(`{"game":"here"}`), 0o644)
	if c, _ := loadConfig(deep); c.Game != "here" || c.Speed != 1 {
		t.Errorf("beside the program: %+v", c)
	}

	for _, bad := range []string{`{"scirpt":"join:Alex@1"}`, `{"script":"join@x"}`, `{"speed":-1}`, `not json`} {
		os.WriteFile(filepath.Join(deep, configName), []byte(bad), 0o644)
		if _, err := loadConfig(deep); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// TestChoose: fakeserver.json's game wins; otherwise exactly one
// personality must recognise the command line.
func TestChoose(t *testing.T) {
	saved := personalities
	defer func() { personalities = saved }()
	personalities = map[string]*Personality{}
	run := func(*Server) {}
	register(&Personality{ID: "jar", Run: run, Match: func(c Call) bool { return c.Program == "java" && c.Args.Contains("server.jar") }})
	register(&Personality{ID: "java", Run: run, Match: func(c Call) bool { return c.Program == "java" }})
	register(&Personality{ID: "plain", Run: run, Match: func(c Call) bool { return c.Program == "plain" }})

	java := Call{Program: "java", Args: NewArgs([]string{"-jar", "server.jar"})}
	if _, err := choose(Config{}, java); err == nil || !strings.Contains(err.Error(), "jar or java") {
		t.Errorf("two match: %v", err)
	}
	if p, err := choose(Config{Game: "jar"}, java); err != nil || p.ID != "jar" {
		t.Errorf("named: %v, %v", p, err)
	}
	if p, err := choose(Config{}, Call{Program: "plain"}); err != nil || p.ID != "plain" {
		t.Errorf("one matches: %v, %v", p, err)
	}
	if _, err := choose(Config{}, Call{Program: "other"}); err == nil {
		t.Error("none matches: no error")
	}
	if _, err := choose(Config{Game: "nope"}, java); err == nil || !strings.Contains(err.Error(), "jar, java, plain") {
		t.Errorf("unknown game: %v", err)
	}
	if programName(filepath.Join("x", "TerrariaServer.EXE")) != "terrariaserver" {
		t.Error("programName")
	}
}

// startLoop runs a server's callbacks in this process, for the tests of
// the framework's parts that don't end the program.
func startLoop(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.Speed == 0 {
		cfg.Speed = 1
	}
	s := newServer(&Personality{ID: "test", Run: func(*Server) {}}, cfg, Call{})
	go func() {
		for f := range s.calls {
			f()
		}
	}()
	t.Cleanup(func() {
		s.mu.Lock()
		closers := s.closers
		s.mu.Unlock()
		for _, c := range closers {
			c.Close()
		}
	})
	return s
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestListeners: the ports really are taken, so a second program (or the
// app's check) can't open them.
func TestListeners(t *testing.T) {
	s := startLoop(t, Config{})
	port := freePort(t)
	if err := s.ListenTCP(port); err != nil {
		t.Fatal(err)
	}
	if err := s.ListenUDP(port); err != nil {
		t.Fatal(err)
	}
	if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		l.Close()
		t.Error("the TCP port could be opened again")
	}
	if c, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		c.Close()
		t.Error("the UDP port could be opened again")
	}
	if err := s.ListenTCP(port); err == nil {
		t.Error("listening twice on one port worked")
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		t.Fatalf("connecting to the game port: %v", err)
	}
	c.Close()
}

// TestHTTPS: the JSON helper over HTTPS with a certificate made up for the
// run; handlers run on the server's goroutine.
func TestHTTPS(t *testing.T) {
	s := startLoop(t, Config{})
	port := freePort(t)
	err := s.HTTP(port, true, func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"path": r.URL.Path, "players": s.Players()})
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Join("Alex")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://127.0.0.1:" + strconv.Itoa(port) + "/v1/api")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Path    string   `json:"path"`
		Players []string `json:"players"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || got.Path != "/v1/api" || len(got.Players) != 1 || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("got %+v, %v, %s", got, err, resp.Header)
	}
	if resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("plain HTTP worked on the HTTPS port")
		}
	}
}

// TestClocks: the game's clocks run at the configured speed.
func TestClocks(t *testing.T) {
	s := startLoop(t, Config{Speed: 600})
	if d := s.Scaled(10 * time.Minute); d != time.Second {
		t.Errorf("10 minutes at speed 600 = %v", d)
	}
	var n atomic.Int32
	s.Every(time.Minute, func() { n.Add(1) }) // every 100 ms
	done := make(chan struct{})
	s.After(time.Minute, func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("After never ran")
	}
	time.Sleep(450 * time.Millisecond)
	if got := n.Load(); got < 3 {
		t.Errorf("Every ran %d times in about half a second", got)
	}
}

func TestPlayers(t *testing.T) {
	s := startLoop(t, Config{})
	if !s.Join("Alex") || s.Join("Alex") || !s.Join("Bo") {
		t.Error("Join")
	}
	if !s.Leave("Alex") || s.Leave("Alex") {
		t.Error("Leave")
	}
	if p := s.Players(); !reflect.DeepEqual(p, []string{"Bo"}) {
		t.Errorf("Players = %q", p)
	}
}

// TestCloseAllDoesNotHang: closing the ports in Exit can't hang a server
// on its way out. Here a responder's Close waits for a probe in progress,
// as RCON's and A2S's do, and the probe waits for the server's goroutine,
// which is the one closing: what a probe that arrives while a stopping
// server saves would do to it. closeAll gives up on that responder after
// its wait, and the other ports are closed by then.
func TestCloseAllDoesNotHang(t *testing.T) {
	s := startLoop(t, Config{})
	port := freePort(t)
	if err := s.ListenTCP(port); err != nil {
		t.Fatal(err)
	}
	probed := make(chan struct{})
	s.closeOnExit(closerFunc(func() error { <-probed; return nil }))
	took := make(chan time.Duration, 1)
	s.post(func() { // queued first, so the probe below waits for it
		start := time.Now()
		s.closeAll(100 * time.Millisecond)
		took <- time.Since(start)
	})
	go func() {
		s.Do(func() {})
		close(probed)
	}()
	select {
	case d := <-took:
		if d < 100*time.Millisecond {
			t.Errorf("closeAll returned after %v, before the probe could finish: the test didn't test the hang", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the ports hung")
	}
	if portTaken("tcp", port) {
		t.Error("the game port is still open after closeAll")
	}
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		t.Error("the probe never finished after closeAll returned")
	}
}
