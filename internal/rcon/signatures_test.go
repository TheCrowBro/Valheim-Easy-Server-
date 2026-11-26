package rcon

import "context"

// These pin the phase 0 contract: the game streams and the fakeserver call
// these while this package is being written, so changing one needs the
// lead, and must change this file too.
var (
	_ func(context.Context, string, string) (*Client, error)         = Dial
	_ func(*Client, context.Context, string) (string, error)         = (*Client).Exec
	_ func(*Client) error                                            = (*Client).Close
	_ func(context.Context, string, string, string) (string, error)  = Exec
	_ func(string, string, func(string) string) (*TestServer, error) = NewTestServer
	_ func(*TestServer) error                                        = (*TestServer).Close
)
