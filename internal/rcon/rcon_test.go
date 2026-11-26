package rcon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slack is how much longer than its limit a call may take before a test
// calls it hung: CI machines can be slow, but not this slow.
const slack = 5 * time.Second

// startServer starts a TestServer that closes when the test ends.
func startServer(t *testing.T, password string, handle func(string) string) *TestServer {
	t.Helper()
	s, err := NewTestServer("127.0.0.1:0", password, handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// dial logs in to addr or ends the test.
func dial(t *testing.T, addr, password string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	c, err := Dial(ctx, addr, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// run sends cmd on c with a generous limit and ends the test on an error.
func run(t *testing.T, c *Client, cmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	reply, err := c.Exec(ctx, cmd)
	if err != nil {
		t.Fatalf("Exec(%q): %v", cmd, err)
	}
	return reply
}

// rawServer accepts connections on a free local port and runs serve for
// each, until the test ends: the stand-in for a real server's quirks.
func rawServer(t *testing.T, serve func(conn net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		closed bool
		conns  = map[net.Conn]bool{}
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				c.Close()
				return
			}
			conns[c] = true
			wg.Add(1)
			mu.Unlock()
			go func() {
				defer wg.Done()
				defer c.Close()
				serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		ln.Close()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// longReply is longer than two packets, with a two-byte letter cut in
// half where the first packet ends, as Minecraft cuts its replies.
var longReply = strings.Repeat("x", splitAt-1) + "é" + strings.Repeat("0123456789", 500) + "end"

func TestExec(t *testing.T) {
	s := startServer(t, "secret", func(cmd string) string {
		switch cmd {
		case "list":
			return "There are 0 of a max of 10 players online: "
		case "say hi":
			return ""
		}
		return "Unknown command: " + cmd
	})
	c := dial(t, s.Addr, "secret")
	for _, tc := range []struct{ cmd, want string }{
		{"list", "There are 0 of a max of 10 players online: "},
		{"say hi", ""},
		{"jump", "Unknown command: jump"},
		{"list", "There are 0 of a max of 10 players online: "},
	} {
		if got := run(t, c, tc.cmd); got != tc.want {
			t.Errorf("Exec(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}

	// A ctx that has already ended sends nothing, so the connection stays
	// usable.
	ended, end := context.WithCancel(t.Context())
	end()
	if _, err := c.Exec(ended, "list"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec with an ended ctx = %v; want Canceled", err)
	}
	if got := run(t, c, "jump"); got != "Unknown command: jump" {
		t.Errorf("after the ended ctx, Exec = %q", got)
	}

	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	got, err := Exec(ctx, s.Addr, "secret", "list")
	if err != nil || got != "There are 0 of a max of 10 players online: " {
		t.Errorf("one-shot Exec = %q, %v", got, err)
	}
}

func TestMultiPacketReply(t *testing.T) {
	s := startServer(t, "pw", func(cmd string) string {
		if cmd == "long" {
			return longReply
		}
		return "short"
	})
	c := dial(t, s.Addr, "pw")
	// Between the long replies, a short one: the end marker's second
	// packet from each command must be skipped, not taken as a reply.
	for _, cmd := range []string{"long", "short", "long", "long", "short"} {
		want := "short"
		if cmd == "long" {
			want = longReply
		}
		if got := run(t, c, cmd); got != want {
			t.Fatalf("Exec(%q) gave %d bytes, want %d (starts %q)", cmd, len(got), len(want), got[:min(len(got), 20)])
		}
	}
}

// TestServerSplitsLikeRealServers reads TestServer's answers packet by
// packet: logins as Source servers answer them, long replies cut at 4096
// bytes, an empty reply still one packet, and a command before logging in
// refused with -1.
func TestServerSplitsLikeRealServers(t *testing.T) {
	s := startServer(t, "pw", func(cmd string) string {
		if cmd == "long" {
			return longReply
		}
		return ""
	})
	conn, err := net.Dial("tcp", s.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(slack))
	send := func(id, typ int32, text string) {
		t.Helper()
		if _, err := conn.Write(encode(id, typ, text)); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(id, typ int32, text string) {
		t.Helper()
		p, err := readPacket(conn)
		if err != nil {
			t.Fatal(err)
		}
		if p.id != id || p.typ != typ || string(p.body) != text {
			t.Fatalf("got packet id %d type %d text %q; want id %d type %d text %q", p.id, p.typ, p.body, id, typ, text)
		}
	}

	send(1, typeExec, "long")
	expect(-1, typeAuthAnswer, "")

	send(2, typeAuth, "wrong")
	expect(2, typeResponse, "")
	expect(-1, typeAuthAnswer, "")

	send(3, typeAuth, "pw")
	expect(3, typeResponse, "")
	expect(3, typeAuthAnswer, "")

	send(4, typeExec, "long")
	b := []byte(longReply)
	expect(4, typeResponse, string(b[:splitAt]))
	expect(4, typeResponse, string(b[splitAt:2*splitAt]))
	expect(4, typeResponse, string(b[2*splitAt:]))

	send(5, typeExec, "nothing")
	expect(5, typeResponse, "")
}

func TestBadPassword(t *testing.T) {
	var calls atomic.Int32
	s := startServer(t, "right", func(string) string { calls.Add(1); return "ok" })
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()

	c, err := Dial(ctx, s.Addr, "wrong")
	if !errors.Is(err, ErrAuth) || c != nil {
		t.Fatalf("Dial with a wrong password = %v, %v; want ErrAuth", c, err)
	}
	if _, err := Exec(ctx, s.Addr, "wrong", "list"); !errors.Is(err, ErrAuth) {
		t.Fatalf("one-shot Exec with a wrong password: %v; want ErrAuth", err)
	}
	if _, err := Exec(ctx, s.Addr, "", "list"); !errors.Is(err, ErrAuth) {
		t.Fatalf("one-shot Exec with no password: %v; want ErrAuth", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a command ran %d times without a login", n)
	}
	if got, err := Exec(ctx, s.Addr, "right", "list"); err != nil || got != "ok" {
		t.Fatalf("with the right password: %q, %v", got, err)
	}
}

// minecraftLike imitates Minecraft's RCON as a client sees it: it reads
// each request with one read of at most 1460 bytes and hangs up when that
// read isn't exactly one packet; it answers a login with the login answer
// alone; it cuts long replies at 4096 bytes, writing each piece on its
// own; and it answers any other packet type with "Unknown request <type>".
// sentTogether is set when the client sent two packets without waiting.
func minecraftLike(t *testing.T, password string, handle func(string) string) (addr string, sentTogether *atomic.Bool) {
	sentTogether = new(atomic.Bool)
	addr = rawServer(t, func(conn net.Conn) {
		buf := make([]byte, 1460)
		loggedIn := false
		for {
			n, err := conn.Read(buf)
			if err != nil || n < 10 {
				return
			}
			if size := int(binary.LittleEndian.Uint32(buf)); size != n-4 {
				sentTogether.Store(true)
				return
			}
			p, _, err := decode(buf[:n])
			if err != nil {
				return
			}
			switch p.typ {
			case typeAuth:
				loggedIn = len(p.body) > 0 && string(p.body) == password
				if loggedIn {
					conn.Write(encode(p.id, typeAuthAnswer, ""))
				} else {
					conn.Write(encode(-1, typeAuthAnswer, ""))
				}
			case typeExec:
				if !loggedIn {
					conn.Write(encode(-1, typeAuthAnswer, ""))
					continue
				}
				reply := handle(string(p.body))
				for {
					k := min(len(reply), splitAt)
					conn.Write(encode(p.id, typeResponse, reply[:k]))
					if reply = reply[k:]; reply == "" {
						break
					}
				}
			default:
				conn.Write(encode(p.id, typeResponse, fmt.Sprintf("Unknown request %x", p.typ)))
			}
		}
	})
	return addr, sentTogether
}

func TestMinecraftStyle(t *testing.T) {
	addr, sentTogether := minecraftLike(t, "pw", func(cmd string) string {
		switch cmd {
		case "list":
			return "There are 1 of a max of 20 players online: Alex"
		case "help":
			return longReply
		}
		return ""
	})
	c := dial(t, addr, "pw")
	for _, cmd := range []string{"list", "help", "list", "whitelist on", "help"} {
		want := map[string]string{"list": "There are 1 of a max of 20 players online: Alex", "help": longReply}[cmd]
		if got := run(t, c, cmd); got != want {
			t.Fatalf("Exec(%q) gave %d bytes, want %d", cmd, len(got), len(want))
		}
	}
	if sentTogether.Load() {
		t.Fatal("the client sent two packets together, which makes Minecraft hang up")
	}

	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := Dial(ctx, addr, "wrong"); !errors.Is(err, ErrAuth) {
		t.Fatalf("Minecraft-style refusal: %v; want ErrAuth", err)
	}
}

// TestServerIgnoringEndMarker: a server that never answers the end marker
// still gives whole replies, split or not, after the quiet time.
func TestServerIgnoringEndMarker(t *testing.T) {
	addr := rawServer(t, func(conn net.Conn) {
		for {
			p, err := readPacket(conn)
			if err != nil {
				return
			}
			switch p.typ {
			case typeAuth:
				conn.Write(append(encode(p.id, typeResponse, ""), encode(p.id, typeAuthAnswer, "")...))
			case typeExec:
				if string(p.body) == "long" {
					conn.Write(split(p.id, longReply))
				} else {
					conn.Write(split(p.id, "short"))
				}
			}
		}
	})
	c := dial(t, addr, "pw")
	for _, cmd := range []string{"long", "short", "long"} {
		start := time.Now()
		got := run(t, c, cmd)
		want := map[string]string{"long": longReply, "short": "short"}[cmd]
		if got != want {
			t.Fatalf("Exec(%q) gave %d bytes, want %d", cmd, len(got), len(want))
		}
		if d := time.Since(start); d < quietWait*4/5 { // a little under, so a coarse system clock can't fail it
			t.Errorf("Exec(%q) returned after %v, before the quiet time of %v", cmd, d, quietWait)
		}
	}
}

// TestRefusedCommand: a server that answers a command with "not logged in"
// gives ErrAuth, and the Client can't be used after it.
func TestRefusedCommand(t *testing.T) {
	addr := rawServer(t, func(conn net.Conn) {
		for {
			p, err := readPacket(conn)
			if err != nil {
				return
			}
			if p.typ == typeAuth {
				conn.Write(encode(p.id, typeAuthAnswer, ""))
			} else {
				conn.Write(encode(-1, typeAuthAnswer, ""))
			}
		}
	})
	c := dial(t, addr, "pw")
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := c.Exec(ctx, "list"); !errors.Is(err, ErrAuth) {
		t.Fatalf("Exec = %v; want ErrAuth", err)
	}
	if _, err := c.Exec(ctx, "list"); err == nil || errors.Is(err, ErrAuth) {
		t.Fatalf("Exec after a failure = %v; want the closed-connection error", err)
	}
}

// TestServerHangsUpAfterAnswering: Minecraft's stop and Zomboid's quit
// can close the connection right after answering. That's the answer, not
// a failure; the Client is closed after it.
func TestServerHangsUpAfterAnswering(t *testing.T) {
	addr := rawServer(t, func(conn net.Conn) {
		for {
			p, err := readPacket(conn)
			if err != nil {
				return
			}
			switch {
			case p.typ == typeAuth:
				conn.Write(encode(p.id, typeAuthAnswer, ""))
			case string(p.body) == "stop":
				conn.Write(encode(p.id, typeResponse, "Stopping the server"))
				return
			default:
				conn.Write(encode(p.id, typeResponse, ""))
			}
		}
	})
	c := dial(t, addr, "pw")
	if got := run(t, c, "stop"); got != "Stopping the server" {
		t.Fatalf("Exec(stop) = %q", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := c.Exec(ctx, "list"); err == nil {
		t.Fatal("Exec after the server hung up worked")
	}
}

// TestServerHangsUpBeforeAnswering: a connection that closes before any
// answer is an error, at login and at a command.
func TestServerHangsUpBeforeAnswering(t *testing.T) {
	silentAt := rawServer(t, func(conn net.Conn) { readPacket(conn) })
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := Dial(ctx, silentAt, "pw"); err == nil || errors.Is(err, ErrAuth) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial to a server that hangs up = %v", err)
	}

	addr := rawServer(t, func(conn net.Conn) {
		p, err := readPacket(conn)
		if err != nil {
			return
		}
		conn.Write(encode(p.id, typeAuthAnswer, ""))
		readPacket(conn)
	})
	c := dial(t, addr, "pw")
	if _, err := c.Exec(ctx, "quit"); err == nil || errors.Is(err, ErrAuth) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec on a server that hangs up = %v", err)
	}
}

// TestNotAnRCONServer: something else on the port fails at once, not at
// the deadline, and never makes the client allocate what it claims.
func TestNotAnRCONServer(t *testing.T) {
	addr := rawServer(t, func(conn net.Conn) {
		readPacket(conn)
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		readPacket(conn) // keep the connection open
	})
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	start := time.Now()
	_, err := Dial(ctx, addr, "pw")
	if !errors.Is(err, errNotRCON) {
		t.Fatalf("Dial to a web server = %v; want errNotRCON", err)
	}
	if d := time.Since(start); d > slack/2 {
		t.Errorf("took %v to notice", d)
	}
}

// silentServer accepts connections and reads, but never answers.
func silentServer(t *testing.T) string {
	return rawServer(t, func(conn net.Conn) {
		for {
			if _, err := readPacket(conn); err != nil {
				return
			}
		}
	})
}

func TestDialTimeouts(t *testing.T) {
	addr := silentServer(t)

	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := Dial(ctx, addr, "pw")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial past its deadline = %v; want DeadlineExceeded", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > slack {
		t.Errorf("Dial gave up after %v; want about 200ms", d)
	}

	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := Dial(ctx, addr, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial cancelled = %v; want Canceled", err)
	}
	if d := time.Since(start); d > slack {
		t.Errorf("Dial took %v to notice it was cancelled", d)
	}

	if _, err := Dial(ctx, addr, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial with an ended ctx = %v; want Canceled", err)
	}
}

func TestExecTimeouts(t *testing.T) {
	unblock := make(chan struct{})
	s := startServer(t, "pw", func(cmd string) string {
		if cmd == "slow" {
			<-unblock
		}
		return "done"
	})
	// Close waits for a command in progress, so let it finish first
	// (cleanups run last first).
	t.Cleanup(func() { close(unblock) })

	c := dial(t, s.Addr, "pw")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Exec(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec past its deadline = %v; want DeadlineExceeded", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > slack {
		t.Errorf("Exec gave up after %v; want about 200ms", d)
	}
	ctx, cancel = context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := c.Exec(ctx, "fast"); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec after a timeout = %v; want the closed-connection error", err)
	}

	// The server runs one command at a time and the slow one still holds
	// it, so every command now waits: good for the cancel test.
	c = dial(t, s.Addr, "pw")
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := c.Exec(ctx, "fast"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec cancelled = %v; want Canceled", err)
	}
	if d := time.Since(start); d > slack {
		t.Errorf("Exec took %v to notice it was cancelled", d)
	}

	ctx, cancel = context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := Exec(ctx, s.Addr, "pw", "fast"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("one-shot Exec past its deadline = %v; want DeadlineExceeded", err)
	}
}

// TestDefaultTimeout: with no deadline on ctx, calls still give up.
func TestDefaultTimeout(t *testing.T) {
	if defaultTimeout != 10*time.Second {
		t.Errorf("defaultTimeout = %v; the package doc promises 10 seconds", defaultTimeout)
	}
	old := defaultTimeout
	defaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { defaultTimeout = old })

	addr := silentServer(t)
	start := time.Now()
	if _, err := Dial(context.Background(), addr, "pw"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial with no deadline = %v; want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > slack {
		t.Errorf("Dial gave up after %v", d)
	}

	unblock := make(chan struct{})
	s := startServer(t, "pw", func(cmd string) string {
		if cmd == "slow" {
			<-unblock
		}
		return "done"
	})
	t.Cleanup(func() { close(unblock) })
	c := dial(t, s.Addr, "pw")
	if _, err := c.Exec(context.Background(), "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec with no deadline = %v; want DeadlineExceeded", err)
	}
}

func TestNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	_, err = Dial(ctx, addr, "pw")
	if err == nil || errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "couldn't connect") {
		t.Fatalf("Dial with nothing listening = %v", err)
	}
}

// TestTextLimits: a command or password no server could read is refused
// before anything is sent; the longest allowed one goes through whole.
func TestTextLimits(t *testing.T) {
	var calls atomic.Int32
	s := startServer(t, "pw", func(cmd string) string { calls.Add(1); return cmd })
	c := dial(t, s.Addr, "pw")
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()

	if _, err := c.Exec(ctx, "say a\x00b"); err == nil {
		t.Error("a command with a zero byte was sent")
	}
	if _, err := c.Exec(ctx, strings.Repeat("a", maxText+1)); err == nil {
		t.Error("a command too long for Minecraft was sent")
	}
	if _, err := Dial(ctx, s.Addr, "p\x00w"); err == nil || errors.Is(err, ErrAuth) {
		t.Errorf("a password with a zero byte: %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d refused commands reached the server", n)
	}
	// Refusing them didn't spoil the connection.
	longest := strings.Repeat("b", maxText)
	if got := run(t, c, longest); got != longest {
		t.Fatalf("the longest command came back as %d bytes", len(got))
	}
	if len(encode(1, typeExec, longest)) > 1460 {
		t.Fatal("the longest command's packet doesn't fit Minecraft's 1460-byte read")
	}
}

// TestManyClients: several clients at once, each getting its own replies,
// while handle runs for one command at a time.
func TestManyClients(t *testing.T) {
	var inside, overlaps atomic.Int32
	s := startServer(t, "pw", func(cmd string) string {
		if inside.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer inside.Add(-1)
		if strings.HasSuffix(cmd, "/long") {
			return longReply + cmd
		}
		return "echo " + cmd
	})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), slack)
			defer cancel()
			c, err := Dial(ctx, s.Addr, "pw")
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			for j := range 20 {
				cmd := fmt.Sprintf("c%d-%d", i, j)
				want := "echo " + cmd
				if j%5 == 0 {
					cmd += "/long"
					want = longReply + cmd
				}
				got, err := c.Exec(ctx, cmd)
				if err != nil || got != want {
					errs <- fmt.Errorf("client %d, %q: got %d bytes, %v", i, cmd, len(got), err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := overlaps.Load(); n != 0 {
		t.Errorf("handle ran for two commands at once %d times", n)
	}
}

func TestClose(t *testing.T) {
	s := startServer(t, "pw", func(string) string { return "ok" })
	c := dial(t, s.Addr, "pw")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	if _, err := c.Exec(ctx, "list"); err == nil {
		t.Fatal("Exec after Close worked")
	}

	c = dial(t, s.Addr, "pw")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second TestServer.Close: %v", err)
	}
	if _, err := c.Exec(ctx, "list"); err == nil {
		t.Fatal("Exec on a connection the server closed worked")
	}
	if _, err := Dial(ctx, s.Addr, "pw"); err == nil {
		t.Fatal("Dial after the server closed worked")
	}
}

func TestPacketRoundTrip(t *testing.T) {
	b := encode(7, typeExec, "list")
	if want := []byte{14, 0, 0, 0, 7, 0, 0, 0, 2, 0, 0, 0, 'l', 'i', 's', 't', 0, 0}; !bytes.Equal(b, want) {
		t.Fatalf("encode = %v, want %v", b, want)
	}
	for n := range len(b) {
		if _, k, err := decode(b[:n]); k != 0 || err != nil {
			t.Fatalf("decode of %d of %d bytes = %d, %v; want \"not yet\"", n, len(b), k, err)
		}
	}
	p, n, err := decode(append(b, 1, 2, 3))
	if err != nil || n != len(b) || p.id != 7 || p.typ != typeExec || string(p.body) != "list" {
		t.Fatalf("decode = %+v, %d, %v", p, n, err)
	}
	// One closing zero instead of two, as a careless server might send.
	short := []byte{11, 0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 'o', 'k', 0}
	if p, _, err := decode(short); err != nil || string(p.body) != "ok" {
		t.Fatalf("decode with one closing zero = %+v, %v", p, err)
	}
	for _, size := range []int32{-1, 0, 9, maxPacket + 1} {
		var head [4]byte
		binary.LittleEndian.PutUint32(head[:], uint32(size))
		if _, _, err := decode(head[:]); !errors.Is(err, errNotRCON) {
			t.Errorf("a length of %d: %v; want errNotRCON", size, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(encode(1, typeResponse, "hello"))
	f.Add([]byte{10, 0, 0, 0, 255, 255, 255, 255, 2, 0, 0, 0, 0, 0})
	f.Add([]byte("HTTP/1.1 200 OK"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, n, err := decode(b)
		if err != nil || n == 0 {
			return
		}
		if n > len(b) || bytes.IndexByte(p.body, 0) >= 0 {
			t.Fatalf("decode(%v) = %+v, %d", b, p, n)
		}
	})
}
