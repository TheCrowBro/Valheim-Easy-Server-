// Package rcon is a client for the Source RCON protocol, which Minecraft and
// Project Zomboid servers answer: the app uses it to count players, manage
// the allow list and ask the server to save and stop. It also has a small
// RCON server for tests and for cmd/fakeserver's stand-ins.
//
// Phase 0 stub: the spec names this package but not its functions, so the
// signatures below are the lead's phase 0 choice, the contract the game
// streams and the fakeserver build against; stream RC fills in the bodies.
// Until then every call fails with errors.ErrUnsupported, and no shipped
// code calls any of it.
package rcon

import (
	"context"
	"errors"
)

// ErrAuth says the server refused the password.
var ErrAuth = errors.New("the server refused the RCON password")

// Client is one logged-in RCON connection. It is not safe for use by
// several goroutines at once.
type Client struct{}

// Dial connects to addr ("host:port") and logs in with password. ctx
// bounds the connection and the login.
func Dial(ctx context.Context, addr, password string) (*Client, error) {
	return nil, errors.ErrUnsupported
}

// Exec sends one command and returns the server's whole reply, joined
// from as many packets as the server split it into.
func (c *Client) Exec(ctx context.Context, cmd string) (string, error) {
	return "", errors.ErrUnsupported
}

// Close ends the connection.
func (c *Client) Close() error {
	return errors.ErrUnsupported
}

// Exec connects, logs in, runs one command and disconnects: what a probe
// or a stop step needs.
func Exec(ctx context.Context, addr, password, cmd string) (string, error) {
	return "", errors.ErrUnsupported
}

// TestServer is a small RCON server for tests and the stand-in servers.
type TestServer struct {
	Addr string // where it listens, "host:port"
}

// NewTestServer listens on addr ("127.0.0.1:0" for any free port), accepts
// password, and answers each command with handle's reply (split into
// packets the way real servers split long replies).
func NewTestServer(addr, password string, handle func(cmd string) string) (*TestServer, error) {
	return nil, errors.ErrUnsupported
}

// Close stops listening and closes every connection.
func (s *TestServer) Close() error {
	return errors.ErrUnsupported
}
