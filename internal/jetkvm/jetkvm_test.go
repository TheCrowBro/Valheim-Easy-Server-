package jetkvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/jetkvm/fakekvm"
	"valheim-easy-server/internal/waker"
)

func testKey(t *testing.T) ssh.Signer {
	t.Helper()
	pemKey, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParsePrivateKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestKey(t *testing.T) {
	key := testKey(t)
	line := PublicKey(key)
	if !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " valheim-easy-server") {
		t.Fatalf("public key = %q", line)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || !bytes.Equal(parsed.Marshal(), key.PublicKey().Marshal()) {
		t.Fatalf("the public key doesn't read back: %v", err)
	}
}

func TestWaker(t *testing.T) {
	bin, err := Waker()
	if errors.Is(err, ErrNoWaker) {
		t.Skip("built without the waker")
	}
	if err != nil || !bytes.HasPrefix(bin, []byte("\x7fELF")) || bin[18] != 40 { // EM_ARM
		t.Fatalf("the waker isn't an ARM program: %v", err)
	}
}

func TestInstall(t *testing.T) {
	key := testKey(t)
	kvm := fakekvm.New(t, key.PublicKey())
	target := Target{Addr: kvm.Addr, Key: key, Base: kvm.Base, Run: kvm.Run, Arch: "-"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F", Query: "192.168.1.20:2457", Name: "Viking's Night"}

	// The stand-in waker says nothing for a moment as it starts, and that
	// it's in Discord a little before /valheim is set up in the Discord
	// server it's in: Install waits for both.
	st, hostKey, err := Install(ctx, target, []byte(fakekvm.Waker), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Bot != "Valheim Bot" || st.Servers != 1 || !bytes.Equal(hostKey, kvm.HostKey.Marshal()) {
		t.Fatalf("status %+v, host key %x", st, hostKey)
	}
	dir := filepath.Join(kvm.Base, "valheim-waker")
	var saved waker.Config
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if json.Unmarshal(data, &saved) != nil || saved.Token != "secret" || saved.Name != "Viking's Night" {
		t.Fatalf("settings = %s", data)
	}
	if fi, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(kvm.Base, "init.d", "S99valheim-waker")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("start script: %v", err)
	}
	pid, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid"))

	// Starting it again leaves it be.
	target.HostKey = hostKey
	c, _, err := target.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.run(q(target.script())+" start", nil); err != nil {
		t.Fatal(out, err)
	}
	c.close()
	if again, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid")); len(pid) == 0 || !bytes.Equal(again, pid) {
		t.Fatalf("started twice: %s then %s", pid, again)
	}

	// A token Discord doesn't take: the running copy and its settings stay.
	bad := cfg
	bad.Token = "bad"
	if _, _, err := Install(ctx, target, []byte(fakekvm.Waker), bad); err == nil || !strings.Contains(err.Error(), "Discord doesn’t accept the bot token") {
		t.Fatalf("bad token: %v", err)
	}
	if st, err := Status(ctx, target); err != nil || !st.Connected {
		t.Fatalf("after a failed install: %+v %v", st, err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "config.json"))
	if !strings.Contains(string(data), `"token": "secret"`) {
		t.Fatalf("settings replaced: %s", data)
	}

	// Installing again replaces the running copy.
	if st, _, err := Install(ctx, target, []byte(fakekvm.Waker), cfg); err != nil || !st.Connected || st.Servers != 1 {
		t.Fatalf("reinstall: %+v %v", st, err)
	}
	if again, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid")); bytes.Equal(again, pid) {
		t.Fatal("the old copy kept running")
	}

	// Another device, or the JetKVM reset: its host key changed.
	other := target
	other.HostKey = []byte("someone else")
	if _, err := Status(ctx, other); !errors.Is(err, ErrHostKey) {
		t.Fatalf("host key: %v", err)
	}

	if err := Remove(ctx, target); err != nil {
		t.Fatal(err)
	}
	var stopped *ErrStopped
	if _, err := Status(ctx, target); !errors.As(err, &stopped) {
		t.Fatalf("after removing: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the waker's folder is still there")
	}
}

// An R3 app's settings, with a command per starred game, reach the JetKVM
// as they were made, and so does an empty list (nothing starred), which
// must stay a list: without it the waker would set up /valheim as for an
// older app (spec 6.4).
func TestInstallWithGames(t *testing.T) {
	key := testKey(t)
	kvm := fakekvm.New(t, key.PublicKey())
	target := Target{Addr: kvm.Addr, Key: key, Base: kvm.Base, Run: kvm.Run, Arch: "-"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	games := []waker.GameCommand{
		{ID: "valheim", Command: "valheim", Description: "Wake the server computer and start Valheim", Name: "Viking's Night", Channel: "111", Query: "192.168.1.20:2457"},
		{ID: "minecraft", Command: "minecraft", Description: "Wake the server computer and start Minecraft", Name: "Friends' Minecraft", Channel: "222"},
	}
	cfg := waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F", Guild: "g1", Games: &games}
	path := filepath.Join(kvm.Base, "valheim-waker", "config.json")
	for _, c := range []struct {
		what  string
		games []waker.GameCommand
		has   string
	}{
		{"two starred games", games, `"command": "minecraft"`},
		{"nothing starred", []waker.GameCommand{}, `"games": []`},
	} {
		cfg.Games = &c.games
		st, hostKey, err := Install(ctx, target, []byte(fakekvm.Waker), cfg)
		if err != nil || !st.Connected {
			t.Fatalf("%s: status %+v, %v", c.what, st, err)
		}
		target.HostKey = hostKey
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), c.has) || strings.Contains(string(data), `"query": ""`) {
			t.Fatalf("%s: the waker's settings = %s", c.what, data)
		}
		got, err := waker.LoadConfig(path)
		if err != nil || got.Games == nil || len(*got.Games) != len(c.games) || got.Guild != "g1" {
			t.Fatalf("%s: read back as %+v (%v)", c.what, got, err)
		}
		for i, g := range c.games {
			if (*got.Games)[i] != g {
				t.Errorf("%s: command %d = %+v, want %+v", c.what, i, (*got.Games)[i], g)
			}
		}
	}
}

// A bot that isn't in a Discord server yet: Install waits a few seconds
// for one, then says so, for the app to show how to add it.
func TestInstallInNoServer(t *testing.T) {
	key := testKey(t)
	kvm := fakekvm.New(t, key.PublicKey())
	target := Target{Addr: kvm.Addr, Key: key, Base: kvm.Base, Run: kvm.Run, Arch: "-"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lonely := strings.ReplaceAll(fakekvm.Waker, `"servers": 1`, `"servers": 0`)
	start := time.Now()
	st, _, err := Install(ctx, target, []byte(lonely), waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F"})
	if err != nil || !st.Connected || st.Servers != 0 {
		t.Fatalf("status %+v, %v", st, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("waited %v for a Discord server", d)
	}
}

func TestStoppedSaysWhy(t *testing.T) {
	key := testKey(t)
	kvm := fakekvm.New(t, key.PublicKey())
	target := Target{Addr: kvm.Addr, Key: key, Base: kvm.Base, Run: kvm.Run, Arch: "-"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := Install(ctx, target, []byte(fakekvm.Waker), waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F"}); err != nil {
		t.Fatal(err)
	}
	// It dies (say, the token was reset since).
	pid, _ := os.ReadFile(filepath.Join(kvm.Run, "valheim-waker.pid"))
	os.WriteFile(filepath.Join(kvm.Run, "valheim-waker.pid"), []byte("999999"), 0o644)
	defer os.WriteFile(filepath.Join(kvm.Run, "valheim-waker.pid"), pid, 0o644) // so it's stopped at the end
	f, _ := os.OpenFile(filepath.Join(kvm.Run, "valheim-waker.log"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("Discord doesn’t accept the bot token.\n")
	f.Close()
	var stopped *ErrStopped
	if _, err := Status(ctx, target); !errors.As(err, &stopped) || !strings.Contains(stopped.Log, "Discord doesn’t accept the bot token.") {
		t.Fatalf("status = %v", err)
	}
	// Just after the JetKVM restarts there's no log yet: it's stopped all
	// the same, so the app starts it again.
	os.Remove(filepath.Join(kvm.Run, "valheim-waker.log"))
	if _, err := Status(ctx, target); !errors.As(err, &stopped) || stopped.Log != "" {
		t.Fatalf("status without a log = %v", err)
	}
	// The start script from older versions of the app fails then, but it's
	// still stopped, so the app starts it again and updates it.
	initScript := filepath.Join(kvm.Base, "init.d", "S99valheim-waker")
	data, _ := os.ReadFile(initScript)
	older := strings.Replace(string(data), "\texit 0\n\t;;", "\t;;", 1)
	if older == string(data) {
		t.Fatal("couldn’t make the older start script")
	}
	os.WriteFile(initScript, []byte(older), 0o755)
	if _, err := Status(ctx, target); !errors.As(err, &stopped) || stopped.Log != "" {
		t.Fatalf("status from the older start script = %v", err)
	}
}

func TestNotLetIn(t *testing.T) {
	key := testKey(t)
	kvm := fakekvm.New(t, nil)
	ctx := context.Background()
	if _, err := Status(ctx, Target{Addr: kvm.Addr, Key: key}); !errors.Is(err, ErrNotLetIn) {
		t.Fatalf("err = %v", err)
	}
	// Developer Mode off: nothing takes SSH connections.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := Status(ctx, Target{Addr: addr, Key: key}); !errors.Is(err, ErrNoSSH) {
		t.Fatalf("err = %v", err)
	}
}
