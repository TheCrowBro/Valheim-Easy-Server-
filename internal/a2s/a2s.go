// Package a2s asks a Steam game server about itself with the A2S_INFO
// query: whether it is up, and how many players are on. Games whose log
// doesn't name players (Enshrouded) use it for their count, and others use
// it to cross-check the log. It also has a responder for tests and for
// cmd/fakeserver's stand-ins (responder.go). (The JetKVM waker keeps its
// own small A2S query.)
//
// The query is one UDP packet: four 0xFF bytes, 'T' and the text "Source
// Engine Query". Since the end of 2020 Steam servers first answer with a
// challenge ('A' and four bytes), which the client sends back on the end
// of the same query; the real answer ('I') then follows. UDP can lose a
// packet, so a query that gets no answer is sent again every half second
// until the time is up.
package a2s

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Info is what a server says about itself in its A2S_INFO answer.
type Info struct {
	Name       string
	Map        string
	Folder     string
	Game       string
	AppID      int
	Players    int
	MaxPlayers int
	Bots       int
	Version    string
}

// The first bytes of every packet: a whole packet, or one piece of an
// answer split into several.
const (
	headerWhole = 0xFFFFFFFF
	headerSplit = 0xFFFFFFFE
)

// The packet kinds, the byte after the header.
const (
	kindQuery     = 'T' // A2S_INFO
	kindChallenge = 'A' // S2C_CHALLENGE: send the query again with these four bytes on the end
	kindInfo      = 'I' // the A2S_INFO answer
)

// queryText follows the header and 'T' in every A2S_INFO query.
const queryText = "Source Engine Query\x00"

// maxChallenges is how many challenges Query answers before it gives up on
// a server that keeps asking. Real servers ask once; a query sent again
// after a lost packet can bring one more.
const maxChallenges = 4

var (
	// defaultTimeout bounds a query whose ctx has no deadline (the
	// promise in Query's comment). It is a variable so tests can shorten it.
	defaultTimeout = 2 * time.Second

	// resendEvery is how long Query waits for an answer before sending the
	// query again, in case UDP lost it (or the answer) on the way.
	resendEvery = 500 * time.Millisecond
)

var (
	errNotSteam = errors.New("the program on that port doesn't answer like a Steam game server")
	errSplit    = errors.New("the game server sent its answer in several pieces, which this app doesn't put back together")
	errChasing  = errors.New("the game server kept asking for a new challenge")
)

// Query asks the server at addr ("host:port", its query port) about
// itself, answering the challenge newer servers send first. It gives up
// at ctx's deadline, or after 2 seconds when ctx has none; running out of
// time gives an error wrapping context.DeadlineExceeded, and a cancelled
// ctx one wrapping context.Canceled.
func Query(ctx context.Context, addr string) (Info, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return Info{}, failure(ctx, addr, err)
	}
	defer conn.Close()
	release := interrupt(ctx, conn)
	defer release()

	req := request(nil)
	var resendAt time.Time
	send := func() error {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		// ctx may have ended just before the line above replaced the past
		// deadline its end sets (see interrupt), so look again.
		if err := ctx.Err(); err != nil {
			return err
		}
		resendAt = time.Now().Add(resendEvery)
		_, err := conn.Write(req)
		return err
	}
	if err := send(); err != nil {
		return Info{}, failure(ctx, addr, err)
	}

	buf := make([]byte, 64<<10) // the largest UDP packet, so no answer is cut short
	challenges := 0
	for {
		wait := deadline
		if resendAt.Before(wait) {
			wait = resendAt
		}
		if err := conn.SetReadDeadline(wait); err != nil {
			return Info{}, failure(ctx, addr, err)
		}
		if err := ctx.Err(); err != nil { // as in send
			return Info{}, failure(ctx, addr, err)
		}
		n, err := conn.Read(buf)
		if err != nil {
			if wait.Before(deadline) && ctx.Err() == nil && errors.Is(err, os.ErrDeadlineExceeded) {
				if err := send(); err != nil { // nothing came back: ask again
					return Info{}, failure(ctx, addr, err)
				}
				continue
			}
			return Info{}, failure(ctx, addr, err)
		}

		b := buf[:n]
		if len(b) >= 4 && binary.LittleEndian.Uint32(b) == headerSplit {
			return Info{}, failure(ctx, addr, errSplit)
		}
		if len(b) < 5 || binary.LittleEndian.Uint32(b) != headerWhole {
			return Info{}, failure(ctx, addr, errNotSteam)
		}
		switch b[4] {
		case kindChallenge:
			if len(b) < 9 {
				return Info{}, failure(ctx, addr, errNotSteam)
			}
			if challenges++; challenges > maxChallenges {
				return Info{}, failure(ctx, addr, errChasing)
			}
			req = request(b[5:9])
			if err := send(); err != nil {
				return Info{}, failure(ctx, addr, err)
			}
		case kindInfo:
			info, err := parseInfo(b[5:])
			if err != nil {
				return Info{}, failure(ctx, addr, err)
			}
			return info, nil
		default:
			return Info{}, failure(ctx, addr, errNotSteam)
		}
	}
}

// request makes an A2S_INFO query, with the server's challenge on the end
// when it has sent one.
func request(challenge []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, headerWhole)
	b = append(b, kindQuery)
	b = append(b, queryText...)
	return append(b, challenge...)
}

// failure turns what went wrong into the error a caller sees.
func failure(ctx context.Context, addr string, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("stopped waiting for the game server at %s: %w", addr, context.Canceled)
	case ctx.Err() != nil, errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		// The connection's deadline is ctx's, and it can notice a moment
		// before ctx does: either way it is ctx's deadline that passed.
		return fmt.Errorf("the game server at %s didn't answer the query in time: %w", addr, context.DeadlineExceeded)
	default:
		return fmt.Errorf("couldn't query the game server at %s: %w", addr, err)
	}
}

// interrupt makes ctx's end wake a read or write on conn that is waiting,
// by moving the connection's deadline into the past (a cancelled ctx has
// no deadline of its own to wake it). The function it returns unhooks that
// and, if ctx has already ended, waits until the move is done.
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

// parseInfo reads an A2S_INFO answer after its header and 'I': the
// protocol version, the name, map, folder and game, the app id, the
// players, the most players, the bots, four one-byte flags (server type,
// system, password, VAC), the version, then optional extra data flagged
// by one byte. Old servers end after the version.
func parseInfo(b []byte) (Info, error) {
	r := reader{b: b}
	var in Info
	r.byte() // protocol version
	in.Name = r.text()
	in.Map = r.text()
	in.Folder = r.text()
	in.Game = r.text()
	shortID := r.u16()
	in.AppID = int(shortID)
	in.Players = int(r.byte())
	in.MaxPlayers = int(r.byte())
	in.Bots = int(r.byte())
	r.skip(4) // server type, system, password, VAC
	if shortID == 2400 {
		r.skip(3) // The Ship adds its game mode, witnesses and duration here
	}
	in.Version = r.text()
	if r.bad {
		return Info{}, fmt.Errorf("the answer ends too early: %w", errNotSteam)
	}
	if len(r.b) == 0 {
		return in, nil
	}

	// The extra data, in this order when its bit is set. Only the game id
	// matters: the app id field above has two bytes, too few for newer
	// games (Enshrouded is 1203620), whose full id is the low 24 bits of
	// the game id. If the extra data is cut short, what came before stands.
	flags := r.byte()
	if flags&0x80 != 0 {
		r.u16() // game port
	}
	if flags&0x10 != 0 {
		r.skip(8) // the server's Steam id
	}
	if flags&0x40 != 0 {
		r.u16()  // spectator port
		r.text() // spectator name
	}
	if flags&0x20 != 0 {
		r.text() // keywords
	}
	if flags&0x01 != 0 {
		if id := r.u64(); !r.bad {
			in.AppID = int(id & 0xFFFFFF)
		}
	}
	return in, nil
}

// reader takes the fields of an answer off its front. Reading past the
// end sets bad and gives zeros, so a parser checks once at the end.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) take(n int) []byte {
	if r.bad || len(r.b) < n {
		r.bad = true
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) byte() byte  { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u64() uint64 { return binary.LittleEndian.Uint64(r.take(8)) }
func (r *reader) skip(n int)  { r.take(n) }

// text reads a string ending in a zero byte.
func (r *reader) text() string {
	i := bytes.IndexByte(r.b, 0)
	if r.bad || i < 0 {
		r.bad = true
		return ""
	}
	s := string(r.b[:i])
	r.b = r.b[i+1:]
	return s
}
