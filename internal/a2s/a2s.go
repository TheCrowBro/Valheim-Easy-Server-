// Package a2s asks a Steam game server about itself with the A2S_INFO
// query: whether it is up, and how many players are on. Games whose log
// doesn't name players (Enshrouded) use it for their count, and others use
// it to cross-check the log. It also has a responder for tests and for
// cmd/fakeserver's stand-ins.
//
// Phase 0 stub: the spec names this package but not its functions, so the
// signatures below are the lead's phase 0 choice, the contract the game
// streams and the fakeserver build against; stream RC fills in the bodies.
// Until then every call fails with errors.ErrUnsupported, and no shipped
// code calls any of it. (The JetKVM waker keeps its own small A2S query.)
package a2s

import (
	"context"
	"errors"
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

// Query asks the server at addr ("host:port", its query port) about
// itself, answering the challenge newer servers send first. It gives up
// at ctx's deadline, or after 2 seconds when ctx has none.
func Query(ctx context.Context, addr string) (Info, error) {
	return Info{}, errors.ErrUnsupported
}

// TestResponder answers A2S_INFO for tests and the stand-in servers.
type TestResponder struct {
	Addr string // where it listens, "host:port"
}

// NewTestResponder listens on UDP addr ("127.0.0.1:0" for any free port)
// and answers each query with info's current answer. With challenge set it
// first asks for a challenge, as newer servers do.
func NewTestResponder(addr string, challenge bool, info func() Info) (*TestResponder, error) {
	return nil, errors.ErrUnsupported
}

// Close stops answering.
func (r *TestResponder) Close() error {
	return errors.ErrUnsupported
}
