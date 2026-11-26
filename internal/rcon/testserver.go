package rcon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// TestServer is a small RCON server for tests and the stand-in servers.
//
// It behaves like the real servers as far as a client can tell: it answers
// a login the way Source servers and Project Zomboid do (an empty reply,
// then the login answer, id -1 when the password is wrong, and the
// connection stays open), answers a command sent before a good login with
// -1 as Minecraft does, splits long replies into packets of 4096 bytes,
// and echoes an empty reply packet (the client's end marker) followed by a
// second packet, as Source servers do.
type TestServer struct {
	Addr string // where it listens, "host:port"

	ln       net.Listener
	password string
	handle   func(cmd string) string
	run      sync.Mutex // held while handle runs: one command at a time

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup // the accept loop and every connection's loop
}

// NewTestServer listens on addr ("127.0.0.1:0" for any free port), accepts
// password, and answers each command with handle's reply (split into
// packets the way real servers split long replies).
//
// handle runs for one command at a time, as a real server's main loop
// runs them, even with several clients connected. Its reply can't hold a
// zero byte (the protocol ends text there). handle must not call Close:
// Close waits for a command in progress to finish. A nil handle answers
// every command with an empty reply.
func NewTestServer(addr, password string, handle func(cmd string) string) (*TestServer, error) {
	if handle == nil {
		handle = func(string) string { return "" }
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("the test RCON server couldn't listen on %s: %w", addr, err)
	}
	s := &TestServer{
		Addr:     ln.Addr().String(),
		ln:       ln,
		password: password,
		handle:   handle,
		conns:    map[net.Conn]struct{}{},
	}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Close stops listening and closes every connection. It waits for a
// command in progress to finish. Closing it again does nothing.
func (s *TestServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	err := s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *TestServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(10 * time.Millisecond) // out of file handles, say: wait rather than spin
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1) // under mu, so never after Close has started waiting
		s.mu.Unlock()
		go s.serve(conn)
	}
}

// serve answers one connection's packets until it closes.
func (s *TestServer) serve(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	r := bufio.NewReader(conn)
	loggedIn := false
	for {
		p, err := readPacket(r)
		if err != nil {
			return // closed, or not an RCON client: real servers hang up too
		}
		var out []byte
		switch p.typ {
		case typeAuth:
			loggedIn = string(p.body) == s.password
			answer := p.id
			if !loggedIn {
				answer = -1
			}
			out = append(encode(p.id, typeResponse, ""), encode(answer, typeAuthAnswer, "")...)
		case typeExec:
			if !loggedIn {
				out = encode(-1, typeAuthAnswer, "")
				break
			}
			s.run.Lock()
			reply := s.handle(string(p.body))
			s.run.Unlock()
			out = split(p.id, reply)
		case typeResponse:
			out = append(encode(p.id, typeResponse, ""), encode(p.id, typeResponse, "\x00\x01\x00\x00")...)
		default:
			out = encode(p.id, typeResponse, fmt.Sprintf("Unknown request %x", p.typ)) // Minecraft's answer
		}
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// readPacket reads one whole packet from r.
func readPacket(r io.Reader) (packet, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return packet{}, err
	}
	size := int64(int32(binary.LittleEndian.Uint32(head[:])))
	if !validSize(size) {
		return packet{}, errNotRCON
	}
	b := make([]byte, 4+size)
	copy(b, head[:])
	if _, err := io.ReadFull(r, b[4:]); err != nil {
		return packet{}, err
	}
	p, _, err := decode(b)
	return p, err
}

// split cuts a reply into packets of at most splitAt bytes, as real
// servers do. An empty reply is still one packet.
func split(id int32, reply string) []byte {
	var out []byte
	for {
		n := min(len(reply), splitAt)
		out = append(out, encode(id, typeResponse, reply[:n])...)
		reply = reply[n:]
		if reply == "" {
			return out
		}
	}
}
