package waker

import (
	"bytes"
	"context"
	"errors"
	"net"
	"time"
)

// Info is what a game server says about itself.
type Info struct {
	Name    string
	Players int
}

var errNotServer = errors.New("not a game server's answer")

// Query asks a Steam game server about itself (A2S_INFO). Valheim answers on
// its game port + 1 while it's running. It gives up after two seconds unless
// ctx ends sooner.
func Query(ctx context.Context, addr string) (Info, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return Info{}, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	conn.SetDeadline(deadline)
	req := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'T'}, "Source Engine Query\x00"...)
	buf := make([]byte, 1400)
	for range 2 { // again with the challenge, if the server asks for one
		if _, err := conn.Write(req); err != nil {
			return Info{}, err
		}
		n, err := conn.Read(buf)
		if err != nil {
			return Info{}, err
		}
		b := buf[:n]
		if len(b) < 5 || !bytes.Equal(b[:4], []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
			return Info{}, errNotServer
		}
		switch b[4] {
		case 'A': // a challenge
			if len(b) < 9 {
				return Info{}, errNotServer
			}
			req = append(req[:25:25], b[5:9]...)
		case 'I':
			return parseInfo(b[5:])
		default:
			return Info{}, errNotServer
		}
	}
	return Info{}, errors.New("the game server kept asking for a challenge")
}

// parseInfo reads the start of an A2S_INFO answer: the protocol, then the
// name, map, folder and game, the app ID, and the number of players.
func parseInfo(b []byte) (Info, error) {
	if len(b) < 1 {
		return Info{}, errNotServer
	}
	b = b[1:]
	var s [4]string
	for i := range s {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			return Info{}, errNotServer
		}
		s[i], b = string(b[:j]), b[j+1:]
	}
	if len(b) < 3 {
		return Info{}, errNotServer
	}
	return Info{Name: s[0], Players: int(b[2])}, nil
}
