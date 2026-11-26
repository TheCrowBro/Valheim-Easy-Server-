package a2s

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"runtime"
	"strings"
	"sync"
)

// TestResponder answers A2S_INFO for tests and the stand-in servers.
//
// It answers the way Steam servers do as far as Query can tell: with
// challenge set, a query without the right challenge gets a challenge
// back, and only a query carrying it gets the answer. The answer carries
// the full app id in its extra data (as Steam servers do for app ids above
// 65535), and players, most players and bots are capped at 255, the most
// their one-byte fields hold. Packets that aren't A2S_INFO queries get no
// answer.
type TestResponder struct {
	Addr string // where it listens, "host:port"

	conn      net.PacketConn
	info      func() Info
	challenge []byte // nil: answers at once
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// NewTestResponder listens on UDP addr ("127.0.0.1:0" for any free port)
// and answers each query with info's current answer. With challenge set it
// first asks for a challenge, as newer servers do.
//
// info is called for one query at a time and must not call Close (Close
// waits for the answer in progress). A nil info answers with an empty
// Info.
func NewTestResponder(addr string, challenge bool, info func() Info) (*TestResponder, error) {
	if info == nil {
		info = func() Info { return Info{} }
	}
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("the test A2S responder couldn't listen on %s: %w", addr, err)
	}
	r := &TestResponder{
		Addr: conn.LocalAddr().String(),
		conn: conn,
		info: info,
		done: make(chan struct{}),
	}
	if challenge {
		r.challenge = newChallenge()
	}
	go r.serve()
	return r, nil
}

// Close stops answering. Closing it again does nothing.
func (r *TestResponder) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.conn.Close()
		<-r.done
	})
	return r.closeErr
}

func (r *TestResponder) serve() {
	defer close(r.done)
	buf := make([]byte, 2048)
	for {
		n, from, err := r.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue // one bad packet (or Windows reporting that an earlier answer bounced): carry on
		}
		got, ok := parseRequest(buf[:n])
		if !ok {
			continue
		}
		var out []byte
		if r.challenge != nil && !bytes.Equal(got, r.challenge) {
			out = binary.LittleEndian.AppendUint32(nil, headerWhole)
			out = append(out, kindChallenge)
			out = append(out, r.challenge...)
		} else {
			out = encodeInfo(r.info())
		}
		r.conn.WriteTo(out, from) // a lost answer is the client's to retry, as with a real server
	}
}

// parseRequest checks b is an A2S_INFO query and gives the challenge on
// its end (nil when it has none).
func parseRequest(b []byte) (challenge []byte, ok bool) {
	head := request(nil)
	if !bytes.HasPrefix(b, head) {
		return nil, false
	}
	rest := b[len(head):]
	if len(rest) >= 4 {
		return rest[:4], true
	}
	return nil, true
}

// newChallenge picks the responder's challenge. 0xFFFFFFFF is what some
// clients send to mean "no challenge yet", so it is never picked.
func newChallenge() []byte {
	for {
		if v := rand.Uint32(); v != headerWhole {
			return binary.LittleEndian.AppendUint32(nil, v)
		}
	}
}

// encodeInfo makes an A2S_INFO answer, the reverse of parseInfo.
func encodeInfo(in Info) []byte {
	b := binary.LittleEndian.AppendUint32(nil, headerWhole)
	b = append(b, kindInfo, 17) // 17: the protocol version Source servers send
	b = appendText(b, in.Name)
	b = appendText(b, in.Map)
	b = appendText(b, in.Folder)
	b = appendText(b, in.Game)
	shortID := 0
	if in.AppID > 0 && in.AppID <= 0xFFFF {
		shortID = in.AppID
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(shortID))
	b = append(b, oneByte(in.Players), oneByte(in.MaxPlayers), oneByte(in.Bots))
	b = append(b, 'd', system(), 0, 0) // a dedicated server, this system, no password, no VAC
	if shortID == 2400 {
		b = append(b, 0, 0, 0) // The Ship's extra fields
	}
	b = appendText(b, in.Version)
	if in.AppID > 0 {
		b = append(b, 0x01) // extra data: the game id, whose low 24 bits are the app id
		b = binary.LittleEndian.AppendUint64(b, uint64(in.AppID)&0xFFFFFF)
	}
	return b
}

// appendText adds s and its closing zero byte; a zero byte inside s would
// end it early, so those are dropped.
func appendText(b []byte, s string) []byte {
	b = append(b, strings.ReplaceAll(s, "\x00", "")...)
	return append(b, 0)
}

// oneByte caps n to what a one-byte field holds.
func oneByte(n int) byte { return byte(min(max(n, 0), 255)) }

// system is the answer's system letter: w for Windows, m for macOS, l for
// Linux.
func system() byte {
	switch runtime.GOOS {
	case "windows":
		return 'w'
	case "darwin":
		return 'm'
	}
	return 'l'
}
