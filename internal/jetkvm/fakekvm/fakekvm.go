// Package fakekvm is a stand-in JetKVM for tests: an SSH server that lets in
// one key and runs commands with this computer's sh, in a temporary folder
// that stands in for the JetKVM's /userdata and /tmp.
package fakekvm

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Waker stands in for the waker program: it checks the bot token ("bad"
// isn't accepted), and runs until it's stopped, saying it's in Discord.
const Waker = `#!/bin/sh
while [ $# -gt 0 ]; do
	case "$1" in
	-config) config=$2; shift ;;
	-status) status=$2; shift ;;
	-check) check=1 ;;
	esac
	shift
done
if grep -q '"token": "bad"' "$config"; then
	echo "Discord doesn’t accept the bot token." >&2
	exit 1
fi
if [ -n "$check" ]; then
	echo "ok Valheim Bot"
	exit 0
fi
echo "fake waker started"
echo '{"version": "build-7", "connected": true, "bot": "Valheim Bot", "servers": 1, "updated": "2026-11-01T12:00:00Z"}' >"$status"
trap 'exit 0' TERM
while :; do sleep 0.1; done
`

// Server is the stand-in JetKVM.
type Server struct {
	Addr      string
	HostKey   ssh.PublicKey
	Base, Run string // its /userdata and /tmp

	ln  net.Listener
	mu  sync.Mutex
	key ssh.PublicKey // the one let in
	wg  sync.WaitGroup
}

// New starts a stand-in JetKVM that lets in key. It's stopped, along with
// a waker running on it, when the test ends.
func New(t testing.TB, key ssh.PublicKey) *Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in JetKVM needs sh")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &Server{HostKey: signer.PublicKey(), Base: filepath.Join(dir, "userdata"), Run: filepath.Join(dir, "tmp"), key: key}
	os.MkdirAll(s.Base, 0o755)
	os.MkdirAll(s.Run, 0o755)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.key == nil || string(k.Marshal()) != string(s.key.Marshal()) {
				return nil, errors.New("not this key")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)
	s.ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Addr = s.ln.Addr().String()
	go s.serve(cfg)
	t.Cleanup(func() {
		s.ln.Close()
		// Stop a waker left running.
		script := filepath.Join(s.Base, "init.d", "S99valheim-waker")
		if _, err := os.Stat(script); err == nil {
			exec.Command("sh", script, "stop").Run()
		}
		s.wg.Wait()
	})
	return s
}

// Allow changes the key it lets in; nil for none.
func (s *Server) Allow(key ssh.PublicKey) {
	s.mu.Lock()
	s.key = key
	s.mu.Unlock()
}

func (s *Server) serve(cfg *ssh.ServerConfig) {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer nc.Close()
			_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
			if err != nil {
				return
			}
			go ssh.DiscardRequests(reqs)
			for nch := range chans {
				if nch.ChannelType() != "session" {
					nch.Reject(ssh.UnknownChannelType, "sessions only")
					continue
				}
				ch, reqs, err := nch.Accept()
				if err != nil {
					continue
				}
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.session(ch, reqs)
				}()
			}
		}()
	}
}

// session runs one command, the way the JetKVM's SSH server does.
func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if ssh.Unmarshal(req.Payload, &payload) != nil {
			req.Reply(false, nil)
			return
		}
		req.Reply(true, nil)
		cmd := exec.Command("sh", "-c", payload.Command)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		ch.CloseWrite()
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
		io.Copy(io.Discard, ch) // until the client hangs up
		return
	}
}
