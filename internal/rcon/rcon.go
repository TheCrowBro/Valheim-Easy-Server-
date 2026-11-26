// Package rcon is a client for the Source RCON protocol, which Minecraft and
// Project Zomboid servers answer: the app uses it to count players, manage
// the allow list and ask the server to save and stop. It also has a small
// RCON server for tests and for cmd/fakeserver's stand-ins (testserver.go).
//
// The protocol runs over TCP. Every message is a packet: its length, a
// request id and a type (each a 4-byte little-endian number), then a text
// ending in two zero bytes. The client logs in with the password, then
// sends commands; the server answers each command with one or more packets
// carrying the command's id, because real servers split a long reply into
// pieces of 4096 bytes.
//
// Nothing in a reply says how many pieces it has, so once the first piece
// has come the client sends an empty packet of another type as an end
// marker. Servers answer packets in order, so the marker's answer comes
// after the rest of the reply (Source servers and Project Zomboid echo it,
// Minecraft answers "Unknown request 0"). A server that ignores the marker
// is given a short quiet time instead: when nothing more comes, the reply
// is whole.
//
// Time limits come from ctx. When ctx has no deadline, each call gives up
// after 10 seconds, so a server that stops answering can't hold the caller
// forever (pass a longer deadline for a slow command, such as a big world's
// save). A call that runs out of time returns an error wrapping
// context.DeadlineExceeded; one whose ctx is cancelled, context.Canceled.
package rcon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// ErrAuth says the server refused the password.
var ErrAuth = errors.New("the server refused the RCON password")

// Packet types. The same number 2 means "run this command" from the client
// and "here is your login answer" from the server.
const (
	typeResponse   = 0 // SERVERDATA_RESPONSE_VALUE: a piece of a reply (and the client's end marker)
	typeExec       = 2 // SERVERDATA_EXECCOMMAND: run a command
	typeAuthAnswer = 2 // SERVERDATA_AUTH_RESPONSE: the login answer; id -1 means refused
	typeAuth       = 3 // SERVERDATA_AUTH: log in with the password
)

const (
	// headerSize is a packet's length, id and type fields; every packet
	// also ends with two zero bytes.
	headerSize = 12

	// maxText is the longest command or password the client sends.
	// Minecraft reads each request with a single read into a 1460-byte
	// buffer and drops the connection when the packet doesn't fit, so a
	// whole packet (14 bytes of fields and zeros, plus the text) has to
	// stay within 1460 bytes.
	maxText = 1460 - headerSize - 2

	// splitAt is how much text real servers put in one reply packet
	// (Source servers and Minecraft both cut at 4096 bytes); the test
	// server splits long replies the same way.
	splitAt = 4096

	// maxPacket bounds the length a packet may claim and maxReply a whole
	// reply, so a program on the port that isn't an RCON server can't make
	// the app allocate gigabytes. Real replies are a few kilobytes.
	maxPacket = 1 << 20
	maxReply  = 8 << 20
)

var (
	// defaultTimeout bounds a call whose ctx has no deadline. It is a
	// variable so the tests can shorten it.
	defaultTimeout = 10 * time.Second

	// quietWait is how long Exec waits for more of a reply when the server
	// doesn't answer the end marker: the rest of a split reply is already
	// on its way by then, so a short wait is enough.
	quietWait = 500 * time.Millisecond
)

// errNotRCON says the other end sent something that can't be an RCON
// packet: whatever listens on that port isn't an RCON server.
var errNotRCON = errors.New("the program on that port doesn't answer like an RCON server")

// Client is one logged-in RCON connection. It is not safe for use by
// several goroutines at once.
//
// When a command fails part way (a timeout, a cancel, a broken
// connection, ErrAuth), the connection is closed, because a late reply
// could still be on its way and the packets might no longer line up; every
// later call then fails, so Dial again to carry on. A command refused
// before it is sent (a zero byte in it, or a ctx that has already ended)
// leaves the connection as it was.
type Client struct {
	conn    net.Conn
	addr    string
	in      []byte // bytes read from conn that don't make a whole packet yet
	scratch []byte // what each read fills
	lastID  int32
	shut    bool  // conn is closed
	err     error // why the Client can't be used any more; nil while it can
}

// Dial connects to addr ("host:port") and logs in with password. ctx
// bounds the connection and the login (10 seconds when it has no
// deadline). A refused password gives ErrAuth.
func Dial(ctx context.Context, addr, password string) (*Client, error) {
	if err := checkText("password", password); err != nil {
		return nil, err
	}
	ctx, cancel := withDefault(ctx)
	defer cancel()
	c := &Client{addr: addr, scratch: make([]byte, 16<<10)}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctx.Err() == nil && !timedOut(err) {
			return nil, fmt.Errorf("couldn't connect to the RCON port at %s: %w", addr, err)
		}
		return nil, c.failure(ctx, err)
	}
	c.conn = conn
	release := interrupt(ctx, conn)
	defer release()
	if err := c.login(ctx, password); err != nil {
		conn.Close()
		return nil, c.failure(ctx, err)
	}
	return c, nil
}

// login sends the password and waits for the server's answer.
func (c *Client) login(ctx context.Context, password string) error {
	deadline, _ := ctx.Deadline()
	id := c.newID()
	if err := c.write(ctx, deadline, id, typeAuth, password); err != nil {
		return err
	}
	for {
		p, err := c.read(ctx, deadline)
		if err != nil {
			return err
		}
		if p.typ != typeAuthAnswer {
			continue // Source servers and Zomboid send an empty reply before the login answer
		}
		switch p.id {
		case id:
			return nil
		case -1:
			return ErrAuth
		}
	}
}

// Exec sends one command and returns the server's whole reply, joined
// from as many packets as the server split it into. ctx bounds the command
// (10 seconds when it has no deadline).
//
// If the server closes the connection once it has started answering, as
// Minecraft's stop and Zomboid's quit can, Exec returns what came with no
// error, and the Client is closed. ErrAuth means the server says this
// connection isn't logged in.
func (c *Client) Exec(ctx context.Context, cmd string) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	if err := checkText("command", cmd); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", c.failure(ctx, err) // nothing was sent, so the connection is still good
	}
	ctx, cancel := withDefault(ctx)
	defer cancel()
	release := interrupt(ctx, c.conn)
	defer release()
	reply, gone, err := c.exec(ctx, cmd)
	if err != nil {
		// A late reply, or the rest of one, may still be on its way, so
		// the packets can't be trusted to line up any more.
		err = c.failure(ctx, err)
		c.close(fmt.Errorf("the RCON connection to %s was closed after an earlier error (%v), so it can't be used again", c.addr, err))
		return "", err
	}
	if gone {
		c.close(fmt.Errorf("the RCON server at %s closed the connection", c.addr))
	}
	return reply, nil
}

// exec sends cmd and reads its reply. gone is true when the server closed
// the connection after it had started answering.
func (c *Client) exec(ctx context.Context, cmd string) (reply string, gone bool, err error) {
	deadline, _ := ctx.Deadline()
	id := c.newID()
	if err := c.write(ctx, deadline, id, typeExec, cmd); err != nil {
		return "", false, err
	}

	// Wait for the first piece before sending the end marker. Two reasons:
	// a server that hands commands to its main loop (Project Zomboid does)
	// can answer a marker sent with the command before the command's
	// reply; and Minecraft reads one packet per read, dropping the
	// connection when two arrive together.
	var out []byte
	for {
		p, err := c.read(ctx, deadline)
		if err != nil {
			return "", false, err
		}
		if refused(p) {
			return "", false, ErrAuth
		}
		if p.id == id {
			out = p.body
			break
		}
		// Anything else is left over from an earlier command (the second
		// packet Source servers send for an end marker): skip it.
	}

	end := c.newID()
	if err := c.write(ctx, deadline, end, typeResponse, ""); err != nil {
		if broken(ctx, err) {
			return string(out), true, nil
		}
		return "", false, err
	}
	for {
		wait := time.Now().Add(quietWait)
		if deadline.Before(wait) {
			wait = deadline
		}
		p, err := c.read(ctx, wait)
		switch {
		case err == nil:
		case wait.Before(deadline) && ctx.Err() == nil && timedOut(err):
			return string(out), false, nil // the server ignores the marker, and nothing more came
		case broken(ctx, err):
			return string(out), true, nil
		default:
			return "", false, err
		}
		switch {
		case p.id == end:
			return string(out), false, nil
		case p.id == id:
			if len(out)+len(p.body) > maxReply {
				return "", false, fmt.Errorf("the reply is longer than %d MB, which no real server sends: %w", maxReply>>20, errNotRCON)
			}
			out = append(out, p.body...)
		case refused(p):
			return "", false, ErrAuth
		}
	}
}

// broken says err is the connection itself failing (closed or reset by
// the server), rather than time running out or the server sending
// something that isn't RCON.
func broken(ctx context.Context, err error) bool {
	return ctx.Err() == nil && !timedOut(err) && !errors.Is(err, errNotRCON)
}

// timedOut says err is a deadline passing: a connection's own (reads and
// writes) or a context's (connecting).
func timedOut(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
}

// refused says p is the server's "you aren't logged in" answer.
func refused(p packet) bool { return p.id == -1 && p.typ == typeAuthAnswer }

// Close ends the connection. Closing it again does nothing.
func (c *Client) Close() error {
	if c.shut {
		return nil
	}
	c.shut = true
	if c.err == nil {
		c.err = fmt.Errorf("the RCON connection to %s is closed", c.addr)
	}
	return c.conn.Close()
}

// close closes the connection after a failure, keeping why for later calls.
func (c *Client) close(why error) {
	if c.err == nil {
		c.err = why
	}
	if !c.shut {
		c.shut = true
		c.conn.Close()
	}
}

// Exec connects, logs in, runs one command and disconnects: what a probe
// or a stop step needs. ctx bounds all of it (10 seconds when it has no
// deadline).
func Exec(ctx context.Context, addr, password, cmd string) (string, error) {
	if err := checkText("command", cmd); err != nil {
		return "", err
	}
	ctx, cancel := withDefault(ctx)
	defer cancel()
	c, err := Dial(ctx, addr, password)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.Exec(ctx, cmd)
}

// newID gives the next request id. Ids stay positive: -1 is the server's
// "refused", and 0 is what some servers put on packets they send unasked.
func (c *Client) newID() int32 {
	c.lastID++
	if c.lastID <= 0 {
		c.lastID = 1
	}
	return c.lastID
}

// write sends one packet, giving up at deadline or when ctx ends.
func (c *Client) write(ctx context.Context, deadline time.Time, id, typ int32, text string) error {
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	// ctx may have ended just before the line above replaced the past
	// deadline its end sets (see interrupt), so look again.
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.conn.Write(encode(id, typ, text))
	return err
}

// read gives the next packet, waiting until deadline at most, or until ctx
// ends. Bytes that came before a timeout are kept for the next read, so a
// timeout never breaks the packets apart.
func (c *Client) read(ctx context.Context, deadline time.Time) (packet, error) {
	for {
		if p, ok, err := c.next(); ok || err != nil {
			return p, err
		}
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			return packet{}, err
		}
		if err := ctx.Err(); err != nil { // as in write
			return packet{}, err
		}
		n, err := c.conn.Read(c.scratch)
		c.in = append(c.in, c.scratch[:n]...)
		if err != nil {
			if p, ok, perr := c.next(); ok && perr == nil {
				return p, nil
			}
			return packet{}, err
		}
	}
}

// next takes the first whole packet off the bytes read so far.
func (c *Client) next() (packet, bool, error) {
	p, n, err := decode(c.in)
	if n == 0 || err != nil {
		return packet{}, false, err
	}
	c.in = append(c.in[:0], c.in[n:]...)
	return p, true, nil
}

// failure turns what went wrong while talking to the server into the error
// a caller sees.
func (c *Client) failure(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrAuth):
		return ErrAuth
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("stopped waiting for the RCON server at %s: %w", c.addr, context.Canceled)
	case ctx.Err() != nil, timedOut(err):
		// The connection's deadline is ctx's, and it can notice a moment
		// before ctx does: either way it is ctx's deadline that passed.
		return fmt.Errorf("the RCON server at %s didn't answer in time: %w", c.addr, context.DeadlineExceeded)
	case errors.Is(err, io.EOF):
		return fmt.Errorf("the RCON server at %s closed the connection: %w", c.addr, err)
	default:
		return fmt.Errorf("RCON with the server at %s failed: %w", c.addr, err)
	}
}

// withDefault gives ctx the default time limit when it has no deadline.
func withDefault(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultTimeout)
}

// interrupt makes ctx's end wake a read or write on conn that is waiting,
// by moving the connection's deadline into the past (a cancelled ctx has
// no deadline of its own to wake it). The function it returns unhooks that
// and, if ctx has already ended, waits until the move is done, so it can't
// land on a later call's deadlines.
func interrupt(ctx context.Context, conn net.Conn) (release func()) {
	moved := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Unix(1, 0))
		close(moved)
	})
	return func() {
		if !stop() {
			<-moved
		}
	}
}

// checkText refuses a command or password no server could read: one with
// a zero byte (it ends the text early) or one too long for Minecraft.
func checkText(what, s string) error {
	if strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("an RCON %s can't contain a zero byte", what)
	}
	if len(s) > maxText {
		return fmt.Errorf("an RCON %s can be at most %d bytes long; this one is %d", what, maxText, len(s))
	}
	return nil
}

// validSize says a packet's length field could be a real one: at least
// the id, the type and the two closing zeros, and within maxPacket.
func validSize(size int64) bool { return size >= headerSize-4+2 && size <= maxPacket }

// packet is one RCON packet.
type packet struct {
	id   int32
	typ  int32
	body []byte // the text, without its closing zero bytes
}

// encode makes the bytes of one packet.
func encode(id, typ int32, text string) []byte {
	b := make([]byte, headerSize, headerSize+len(text)+2)
	binary.LittleEndian.PutUint32(b[0:], uint32(headerSize-4+len(text)+2))
	binary.LittleEndian.PutUint32(b[4:], uint32(id))
	binary.LittleEndian.PutUint32(b[8:], uint32(typ))
	b = append(b, text...)
	return append(b, 0, 0)
}

// decode reads the first packet in b. n is how many bytes it took, 0 when
// b doesn't hold a whole packet yet. The text ends at its first zero byte,
// so a server that leaves out the second closing zero is still read.
func decode(b []byte) (p packet, n int, err error) {
	if len(b) < 4 {
		return packet{}, 0, nil
	}
	size := int64(int32(binary.LittleEndian.Uint32(b)))
	if !validSize(size) {
		return packet{}, 0, errNotRCON
	}
	if int64(len(b)) < 4+size {
		return packet{}, 0, nil
	}
	body := b[headerSize : 4+size]
	if i := bytes.IndexByte(body, 0); i >= 0 {
		body = body[:i]
	}
	return packet{
		id:   int32(binary.LittleEndian.Uint32(b[4:])),
		typ:  int32(binary.LittleEndian.Uint32(b[8:])),
		body: bytes.Clone(body),
	}, int(4 + size), nil
}
