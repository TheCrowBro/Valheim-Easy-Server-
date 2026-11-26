package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"valheim-easy-server/internal/a2s"
	"valheim-easy-server/internal/rcon"
)

// The ports. A stand-in really opens the ports its command line names, so
// the app's port checks, its "port in use" errors and two games started on
// the same port behave as they would with the real servers. By default it
// listens on 127.0.0.1 only (Config.Host).

// addr is host:port on the stand-in's listening address.
func (s *Server) addr(port int) string {
	host := s.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ListenTCP opens a TCP port, accepting connections and holding them open
// until the client or the server closes them, as a game port does. An
// error means another program has the port; the personality says so in
// its game's words and exits.
func (s *Server) ListenTCP(port int) error {
	l, err := net.Listen("tcp", s.addr(port))
	if err != nil {
		return err
	}
	s.closeOnExit(l)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(io.Discard, c)
				c.Close()
			}()
		}
	}()
	return nil
}

// ListenUDP opens a UDP port and drops whatever arrives on it.
func (s *Server) ListenUDP(port int) error {
	c, err := net.ListenPacket("udp", s.addr(port))
	if err != nil {
		return err
	}
	s.closeOnExit(c)
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := c.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return nil
}

// RCON answers the Source RCON protocol on a TCP port with password, as
// Minecraft and Project Zomboid do. handle gets each command and returns
// the reply; it runs on the server's goroutine. A command that stops the
// server should answer and queue the stop for after (s.post, as the
// demo's "stop" does): stopping inside handle ends the program before the
// reply goes out, and Exit then waits its short while (closeWait) for this
// command to finish, which it never will.
func (s *Server) RCON(port int, password string, handle func(cmd string) string) error {
	srv, err := rcon.NewTestServer(s.addr(port), password, func(cmd string) string {
		var reply string
		s.Do(func() { reply = handle(cmd) })
		return reply
	})
	if err != nil {
		return err
	}
	s.closeOnExit(closerFunc(srv.Close))
	return nil
}

// A2S answers Steam's A2S_INFO query on a UDP port (with a challenge
// first, as newer servers ask, when challenge is set). info gives the
// current answer; it runs on the server's goroutine.
func (s *Server) A2S(port int, challenge bool, info func() a2s.Info) error {
	r, err := a2s.NewTestResponder(s.addr(port), challenge, func() a2s.Info {
		var in a2s.Info
		s.Do(func() { in = info() })
		return in
	})
	if err != nil {
		return err
	}
	s.closeOnExit(closerFunc(r.Close))
	return nil
}

// HTTP serves a JSON API on a TCP port, as Palworld's REST API and
// Satisfactory's HTTPS API do; with useTLS it is HTTPS with a certificate
// made up for this run (the app must accept it, as it must accept the real
// servers' self-signed ones). Each request runs on the server's goroutine.
func (s *Server) HTTP(port int, useTLS bool, handle func(w http.ResponseWriter, r *http.Request)) error {
	l, err := net.Listen("tcp", s.addr(port))
	if err != nil {
		return err
	}
	if useTLS {
		cert, err := selfSigned()
		if err != nil {
			l.Close()
			return err
		}
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Do(func() { handle(w, r) }) }),
		ReadHeaderTimeout: 10 * time.Second,
		// Its complaints (a client that won't speak TLS) aren't the
		// game's lines, and would land in the game's log.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	s.closeOnExit(srv)
	go srv.Serve(l)
	return nil
}

// WriteJSON answers an HTTP request with status and v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

var (
	certOnce sync.Once
	cert     tls.Certificate
	certErr  error
)

// selfSigned makes one certificate per run for 127.0.0.1 and localhost.
func selfSigned() (tls.Certificate, error) {
	certOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			certErr = err
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: "fakeserver"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(30 * 24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{"localhost"},
			IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			certErr = err
			return
		}
		cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	return cert, certErr
}
