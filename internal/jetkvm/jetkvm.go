// Package jetkvm puts the Discord waker on a JetKVM, the IP KVM next to the
// server computer, and checks on it. The JetKVM's Developer Mode lets the
// app in over SSH with the app's own key; the waker goes in /userdata, which
// the JetKVM keeps through its own updates, and starts at boot from
// /userdata/init.d.
package jetkvm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/waker"
)

//go:embed dist
var dist embed.FS

var (
	// ErrNoWaker means this build of the app doesn't carry the waker.
	ErrNoWaker = errors.New("this copy of the app was built without the JetKVM waker. Install a release from GitHub")
	// ErrHostKey means the JetKVM isn't the one the waker was installed on,
	// or it was reset.
	ErrHostKey = errors.New("the JetKVM at this address doesn’t identify itself the way it did before. That happens after it’s reset or its system is updated; it could also be another device")
	// ErrNotLetIn means the JetKVM refused the app's key.
	ErrNotLetIn = errors.New("the JetKVM didn’t let the app in. In the JetKVM’s settings, under Advanced, turn on Developer Mode, paste the app’s key into SSH Public Key and click Update SSH Key")
	// ErrNoSSH means the JetKVM is there but not taking SSH connections.
	ErrNoSSH = errors.New("the JetKVM isn’t taking connections from the app. In the JetKVM’s settings, under Advanced, turn on Developer Mode")
)

const wakerFile = "dist/valheim-waker-armv7.gz"

// HasWaker reports whether this build carries the waker. Release builds
// do; the waker is built for the JetKVM first and put in dist.
func HasWaker() bool {
	_, err := dist.Open(wakerFile)
	return err == nil
}

// Waker is the waker program for the JetKVM, if this build carries it.
func Waker() ([]byte, error) {
	gz, err := dist.ReadFile(wakerFile)
	if err != nil {
		return nil, ErrNoWaker
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(r, 64<<20))
}

// NewKey makes the app's key for the JetKVM, as PEM to keep.
func NewKey() ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "valheim-easy-server")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// PublicKey is the key's public half, the line to paste into the JetKVM.
func PublicKey(key ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))) + " valheim-easy-server"
}

// Target is a JetKVM to talk to.
type Target struct {
	Addr    string     // on the home network; port 22 unless it says
	Key     ssh.Signer // the app's key
	HostKey []byte     // the JetKVM's own key from last time; nil to accept and learn it

	// For tests: where the waker lives and keeps its status (/userdata and
	// /tmp), and what `uname -m` must start with ("-" for anything).
	Base, Run, Arch string
}

func (t Target) paths() (base, run string) {
	base, run = t.Base, t.Run
	if base == "" {
		base = "/userdata"
	}
	if run == "" {
		run = "/tmp"
	}
	return base, run
}

// script is where the waker's start script goes.
func (t Target) script() string {
	base, _ := t.paths()
	return base + "/init.d/S99valheim-waker"
}

// q quotes a word for the JetKVM's shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// conn is an SSH connection to the JetKVM.
type conn struct {
	c    *ssh.Client
	stop func() bool
}

func (c *conn) close() {
	c.stop()
	c.c.Close()
}

// dial logs in to the JetKVM as root. It returns the JetKVM's host key.
func (t Target) dial(ctx context.Context) (*conn, []byte, error) {
	addr := t.Addr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "22")
	}
	var seen []byte
	cfg := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(t.Key)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = key.Marshal()
			if t.HostKey != nil && !bytes.Equal(seen, t.HostKey) {
				return ErrHostKey
			}
			return nil
		},
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		if strings.Contains(err.Error(), "refused") {
			return nil, nil, ErrNoSSH
		}
		return nil, nil, fmt.Errorf("couldn’t reach the JetKVM at %s. Check its address (the JetKVM shows it on its little screen) and that it’s on the same network as this computer", t.Addr)
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() }) // a cancelled request hangs up
	nc.SetDeadline(time.Now().Add(15 * time.Second))
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		stop()
		nc.Close()
		switch {
		case errors.Is(err, ErrHostKey):
			return nil, nil, ErrHostKey
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, nil, ErrNotLetIn
		case ctx.Err() != nil:
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("couldn’t log in to the JetKVM: %w", err)
	}
	nc.SetDeadline(time.Time{})
	return &conn{c: ssh.NewClient(sc, chans, reqs), stop: stop}, seen, nil
}

// run runs a command on the JetKVM, with stdin, and returns its output.
func (c *conn) run(cmd string, stdin []byte) (string, error) {
	s, err := c.c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	if stdin != nil {
		s.Stdin = bytes.NewReader(stdin)
	}
	out, err := s.CombinedOutput(cmd)
	return strings.TrimSpace(string(out)), err
}

// script starts and stops the waker. The JetKVM runs it with start when it
// boots (and stop when it shuts down); the app also asks it for status.
// It's for BusyBox's sh, returns at once, and logs to memory, which the
// JetKVM clears when it restarts, sparing its storage.
func script(base, run string) string {
	return fmt.Sprintf(`#!/bin/sh
# Valheim Easy Server's Discord waker: wakes the server computer when
# someone types /valheim in Discord. The app put it here.
dir=%s
pid=%s
log=%s
status=%s
running() {
	p=$(cat "$pid" 2>/dev/null) && [ -n "$p" ] && grep -q valheim-waker "/proc/$p/cmdline" 2>/dev/null
}
case "$1" in
start)
	running && exit 0
	if [ -f "$log" ] && [ "$(wc -c <"$log")" -gt 262144 ]; then mv -f "$log" "$log.old"; fi
	(trap '' HUP; exec "$dir/valheim-waker" -config "$dir/config.json" -status "$status") </dev/null >>"$log" 2>&1 &
	echo $! >"$pid"
	;;
stop)
	if running; then
		kill "$p"
		i=0
		while running && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
	fi
	rm -f "$pid" "$status"
	;;
status)
	if running; then cat "$status" 2>/dev/null; else echo stopped; tail -n 5 "$log" 2>/dev/null; fi
	;;
esac
`, q(base+"/valheim-waker"), q(run+"/valheim-waker.pid"), q(run+"/valheim-waker.log"), q(run+"/valheim-waker.json"))
}

// Install puts the waker on the JetKVM with its settings, makes it start
// with the JetKVM, and starts it, after checking that it can use the bot
// token. It waits a little for the waker to get into Discord, and returns
// its status then, and the JetKVM's host key, to check next time.
func Install(ctx context.Context, t Target, binary []byte, cfg waker.Config) (waker.Status, []byte, error) {
	var st waker.Status
	c, hostKey, err := t.dial(ctx)
	if err != nil {
		return st, nil, err
	}
	defer c.close()
	base, run := t.paths()
	dir, initScript := base+"/valheim-waker", t.script()
	arch := t.Arch
	if arch == "" {
		arch = "armv7"
	}
	if arch != "-" {
		if out, err := c.run("uname -m", nil); err != nil || !strings.HasPrefix(out, arch) {
			return st, nil, fmt.Errorf("%s doesn’t look like a JetKVM (it says it’s %q), so the waker wouldn’t run on it", t.Addr, out)
		}
	}
	settings, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return st, nil, err
	}
	// The new copy and settings are tried out (which checks the bot token,
	// and that the JetKVM can reach Discord) before they replace the old.
	newBin, newCfg := dir+"/valheim-waker.new", dir+"/config.json.new"
	steps := []struct {
		cmd   string
		stdin []byte
	}{
		{"mkdir -p " + q(dir) + " " + q(base+"/init.d"), nil},
		{"cat >" + q(newBin) + " && chmod 755 " + q(newBin), binary},
		{"umask 077 && cat >" + q(newCfg), settings},
	}
	for _, s := range steps {
		if out, err := c.run(s.cmd, s.stdin); err != nil {
			c.run("rm -f "+q(newBin)+" "+q(newCfg), nil)
			if ctx.Err() != nil {
				return st, nil, ctx.Err()
			}
			return st, nil, fmt.Errorf("couldn’t copy the waker to the JetKVM: %s", orErr(out, err))
		}
	}
	out, err := c.run(q(newBin)+" -check -config "+q(newCfg), nil)
	if err != nil || !checked(out) {
		c.run("rm -f "+q(newBin)+" "+q(newCfg), nil)
		if ctx.Err() != nil {
			return st, nil, ctx.Err()
		}
		return st, nil, fmt.Errorf("the waker on the JetKVM couldn’t get into Discord: %s", orErr(out, err))
	}
	if out, err := c.run("cat >"+q(initScript)+" && chmod 755 "+q(initScript), []byte(script(base, run))); err != nil {
		return st, nil, fmt.Errorf("couldn’t set up the waker to start with the JetKVM: %s", orErr(out, err))
	}
	swap := q(initScript) + " stop; mv -f " + q(newCfg) + " " + q(dir+"/config.json") + " && mv -f " + q(newBin) + " " + q(dir+"/valheim-waker") + " && " + q(initScript) + " start"
	if out, err := c.run(swap, nil); err != nil {
		return st, nil, fmt.Errorf("couldn’t start the waker: %s", orErr(out, err))
	}
	// Give it a moment to get into Discord.
	for i := 0; ; i++ {
		st, err = c.status(initScript)
		if err != nil || st.Connected || st.Error != "" || i >= 30 {
			break
		}
		select {
		case <-ctx.Done():
			return st, nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err != nil {
		return st, nil, err
	}
	return st, hostKey, nil
}

// checked reports whether the waker's -check said "ok <the bot's name>",
// on a line of its own among anything else it said.
func checked(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ok ") {
			return true
		}
	}
	return false
}

func orErr(out string, err error) string {
	if out != "" {
		return out
	}
	return fmt.Sprint(err)
}

// ErrStopped is the waker not running on the JetKVM.
type ErrStopped struct {
	Log string // the end of its log
}

func (e *ErrStopped) Error() string {
	msg := "the waker isn’t running on the JetKVM"
	if e.Log != "" {
		msg += ". It last said: " + e.Log
	}
	return msg
}

func (c *conn) status(initScript string) (waker.Status, error) {
	var st waker.Status
	out, err := c.run("if [ -x "+q(initScript)+" ]; then "+q(initScript)+" status; else echo stopped; fi", nil)
	if err != nil {
		return st, fmt.Errorf("couldn’t ask the JetKVM about the waker: %s", orErr(out, err))
	}
	if rest, ok := strings.CutPrefix(out, "stopped"); ok {
		return st, &ErrStopped{Log: strings.TrimSpace(rest)}
	}
	if out == "" {
		return st, nil // started, but not saying anything yet
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return st, fmt.Errorf("the waker’s status didn’t make sense: %w", err)
	}
	return st, nil
}

// Status reads what the waker on the JetKVM is up to.
func Status(ctx context.Context, t Target) (waker.Status, error) {
	c, _, err := t.dial(ctx)
	if err != nil {
		return waker.Status{}, err
	}
	defer c.close()
	return c.status(t.script())
}

// Remove stops the waker and deletes it from the JetKVM.
func Remove(ctx context.Context, t Target) error {
	c, _, err := t.dial(ctx)
	if err != nil {
		return err
	}
	defer c.close()
	base, run := t.paths()
	initScript := t.script()
	cmd := "if [ -x " + q(initScript) + " ]; then " + q(initScript) + " stop; fi; rm -rf " + q(base+"/valheim-waker") + " " + q(initScript) +
		" " + q(run+"/valheim-waker.log") + " " + q(run+"/valheim-waker.log.old")
	if out, err := c.run(cmd, nil); err != nil {
		return fmt.Errorf("couldn’t remove the waker from the JetKVM: %s", orErr(out, err))
	}
	return nil
}
