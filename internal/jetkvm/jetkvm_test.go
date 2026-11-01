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

	st, hostKey, err := Install(ctx, target, []byte(fakekvm.Waker), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.Bot != "Valheim Bot" || !bytes.Equal(hostKey, kvm.HostKey.Marshal()) {
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
	if st, _, err := Install(ctx, target, []byte(fakekvm.Waker), cfg); err != nil || !st.Connected {
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
