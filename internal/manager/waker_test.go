package manager

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/jetkvm"
	"valheim-easy-server/internal/jetkvm/fakekvm"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/waker"
)

// newFakeBotAPI stands in for the part of Discord's API that checks a bot
// token.
func newFakeBotAPI(t *testing.T, token string) string {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Header.Get("Authorization") != "Bot "+token {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message": "401: Unauthorized", "code": 0}`)
			return
		}
		io.WriteString(w, body)
	}
	mux.HandleFunc("GET /users/@me", func(w http.ResponseWriter, r *http.Request) {
		auth(w, r, `{"id": "app1", "username": "valheim", "global_name": "Valheim"}`)
	})
	mux.HandleFunc("GET /applications/@me", func(w http.ResponseWriter, r *http.Request) {
		auth(w, r, `{"id": "app1", "name": "Valheim"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func freeUDPPort(t *testing.T) int {
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// The whole setup from the Settings page: the bot token is checked, the
// waker goes on the JetKVM once it lets the app in, the app listens for wake
// signals, and the waker is kept up to date and can be removed.
func wakerOptions(t *testing.T, kvm *fakekvm.Server, hook string, adapters ...platform.NetAdapter) func(*Options) {
	api := newFakeBotAPI(t, "secret")
	port := freeUDPPort(t)
	return func(o *Options) {
		o.DiscordURL, o.DiscordAPI, o.AppVersion, o.WakePort = hook, api, "build-7", port
		o.WakerBinary = func() ([]byte, error) { return []byte(fakekvm.Waker), nil }
		if kvm != nil {
			o.JetKVMDirs = &jetkvm.Target{Base: kvm.Base, Run: kvm.Run, Arch: "-"}
		}
		o.NetAdapters = func() []platform.NetAdapter { return adapters }
	}
}

func TestWakerInstall(t *testing.T) {
	kvm := fakekvm.New(t, nil) // the app's key isn't in the JetKVM yet
	hook := newFakeDiscord(t)
	opts := wakerOptions(t, kvm, hook.URL,
		platform.NetAdapter{Name: "Wi-Fi", MAC: "AA:BB:CC:DD:EE:FF", IPv4: []string{"192.168.1.30"}, Connected: true},
		platform.NetAdapter{Name: "Ethernet", MAC: "3C:07:54:5A:21:9F", IPv4: []string{"192.168.1.20"}, Wired: true, Connected: true})
	env := setup(t, opts)
	m := env.m
	turnOnDiscord(t, m) // so the bot only answers in that channel's Discord server

	var fields config.FieldErrors
	if err := m.InstallWaker("", "", false); !errors.As(err, &fields) || fields["botToken"] == "" || fields["jetkvm"] == "" {
		t.Fatalf("nothing filled in: %v", err)
	}
	if err := m.InstallWaker("192.168.1.50; reboot", "secret", false); !errors.As(err, &fields) || fields["jetkvm"] == "" {
		t.Fatalf("a bad address: %v", err)
	}
	if err := m.InstallWaker(kvm.Addr, "wrong", false); !errors.As(err, &fields) || !strings.Contains(fields["botToken"], "Discord doesn’t accept the bot token") {
		t.Fatalf("a wrong token: %v", err)
	}
	var ae *ActionError
	if err := m.InstallWaker(kvm.Addr, "secret", false); !errors.As(err, &ae) || ae.Action != "key" {
		t.Fatalf("before the key is in the JetKVM: %v", err)
	}
	v := m.WakerView(false)
	if v.On || v.Key == "" || v.Missing != "" {
		t.Fatalf("view before = %+v", v)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(v.Key))
	if err != nil {
		t.Fatal(err)
	}
	kvm.Allow(key) // pasted in the JetKVM's Developer Mode

	if err := m.InstallWaker(" "+kvm.Addr+" ", "secret", false); err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if !s.DiscordWake || s.JetKVM != kvm.Addr || s.DiscordBotToken != "secret" {
		t.Fatalf("settings: wake %v, address %q", s.DiscordWake, s.JetKVM)
	}
	v = m.WakerView(true)
	if !v.On || !v.Token || v.Bot != "Valheim" || !v.Guild || v.Status == nil || !v.Status.Connected || v.Problem != "" ||
		!strings.Contains(v.Invite, "client_id=app1") || !strings.Contains(v.Invite, "guild_id=g1") {
		t.Fatalf("view after = %+v", v)
	}
	readCfg := func() waker.Config {
		var c waker.Config
		data, _ := os.ReadFile(filepath.Join(kvm.Base, "valheim-waker", "config.json"))
		json.Unmarshal(data, &c)
		return c
	}
	want := waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F", Query: net.JoinHostPort("192.168.1.20", strconv.Itoa(s.Port+1)), Name: s.ServerName, Guild: "g1"}
	if c := readCfg(); !reflect.DeepEqual(c, want) {
		t.Fatalf("the waker's settings = %+v, want %+v", c, want)
	}
	m.mu.Lock()
	listening := m.wakeConn != nil
	m.mu.Unlock()
	if !listening {
		t.Fatal("not listening for wake signals")
	}

	// Up to date: left alone.
	before, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid"))
	m.upkeepWaker()
	if after, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid")); string(after) != string(before) {
		t.Fatal("reinstalled for nothing")
	}
	// An older app installed it: it's brought up to date.
	m.mu.Lock()
	m.wk.file.Setup.Version = "build-6"
	m.mu.Unlock()
	m.upkeepWaker()
	if after, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid")); string(after) == string(before) {
		t.Fatal("not reinstalled")
	}
	m.mu.Lock()
	version := m.wk.file.Setup.Version
	m.mu.Unlock()
	if version != "build-7" {
		t.Fatalf("installed by %q", version)
	}
	// The server's name changes: the waker gets it straight away.
	s.ServerName = "Odin's Rest"
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the waker to get the new name", 20*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return readCfg().Name == "Odin's Rest" && m.wk.file.Setup.Name == "Odin's Rest" && !m.wk.busy
	})

	// Links to open on another computer are posted in the join-code channel.
	if err := m.PostWakerLink("invite"); err != nil {
		t.Fatal(err)
	}
	if err := m.PostWakerLink("key"); err != nil {
		t.Fatal(err)
	}
	if hook.count("client_id=app1") != 1 || hook.count(v.Key) != 1 {
		t.Fatalf("posts: %q", hook.all())
	}

	// The JetKVM was reset: its identity changed.
	m.mu.Lock()
	m.wk.file.HostKey = []byte("before the reset")
	m.mu.Unlock()
	if v := m.WakerView(true); v.Action != "trust" || !strings.Contains(v.Problem, "doesn’t identify itself") {
		t.Fatalf("after a reset: %+v", v)
	}
	if err := m.InstallWaker("", "", false); !errors.As(err, &ae) || ae.Action != "trust" {
		t.Fatalf("installing again: %v", err)
	}
	if err := m.InstallWaker("", "", true); err != nil {
		t.Fatal(err)
	}

	// The app is restarted: it remembers the JetKVM.
	m2 := newTestManager(t, env.data, env, opts)
	m2.mu.Lock()
	known := m2.wk.file
	m2.mu.Unlock()
	if len(known.HostKey) == 0 || known.AppID != "app1" || known.Setup.MAC != "3C:07:54:5A:21:9F" {
		t.Fatalf("after a restart: %+v", known)
	}

	if err := m.RemoveWaker(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(kvm.Base, "valheim-waker")); !os.IsNotExist(err) {
		t.Fatal("the waker is still on the JetKVM")
	}
	m.mu.Lock()
	listening = m.wakeConn != nil
	m.mu.Unlock()
	if s := m.Settings(); s.DiscordWake || s.DiscordBotToken != "secret" || listening {
		t.Fatalf("after removing: wake %v, listening %v", s.DiscordWake, listening)
	}

	// Removing when the JetKVM can't be reached: it can be turned off here.
	if err := m.InstallWaker("", "", false); err != nil {
		t.Fatal(err)
	}
	kvm.Allow(nil)
	if err := m.RemoveWaker(false); !errors.As(err, &ae) || ae.Action != "force" {
		t.Fatalf("removing without the JetKVM: %v", err)
	}
	if err := m.RemoveWaker(true); err != nil || m.Settings().DiscordWake {
		t.Fatalf("forced: %v", err)
	}
}

func TestWakerNeedsACable(t *testing.T) {
	env := setup(t, wakerOptions(t, nil, "", platform.NetAdapter{Name: "Wi-Fi", MAC: "AA:BB:CC:DD:EE:FF", Connected: true}))
	m := env.m
	if err := m.InstallWaker("192.168.1.50", "secret", false); err == nil || !strings.Contains(err.Error(), "network cable") {
		t.Fatalf("err = %v", err)
	}
	m.opt.WakerBinary = nil // only read by what the test calls
	if !jetkvm.HasWaker() {
		if err := m.InstallWaker("192.168.1.50", "secret", false); err == nil || !strings.Contains(err.Error(), "built without the JetKVM waker") {
			t.Fatalf("without the waker: %v", err)
		}
		if v := m.WakerView(false); v.Missing == "" {
			t.Fatal("the view doesn't say the waker is missing")
		}
	}
}

func TestJetKVMAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.50":          "192.168.1.50",
		" http://192.168.1.50/": "192.168.1.50",
		"https://jetkvm.local":  "jetkvm.local",
		"192.168.1.50:2222":     "192.168.1.50:2222",
	} {
		if got := jetkvmAddr(in); got != want || !validJetKVMAddr(got) {
			t.Errorf("%q read as %q", in, got)
		}
	}
	for _, bad := range []string{"192.168.1.50; reboot", "a b", "192.168.1.50:99999", "$(id)"} {
		if validJetKVMAddr(jetkvmAddr(bad)) {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := sentence("the JetKVM didn't answer"); got != "The JetKVM didn't answer." {
		t.Errorf("sentence = %q", got)
	}
	if got := sentence("Already one."); got != "Already one." {
		t.Errorf("sentence = %q", got)
	}
}
