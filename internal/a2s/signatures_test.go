package a2s

import "context"

// These pin the phase 0 contract: the game streams and the fakeserver call
// these while this package is being written, so changing one needs the
// lead, and must change this file too.
var (
	_ func(context.Context, string) (Info, error)             = Query
	_ func(string, bool, func() Info) (*TestResponder, error) = NewTestResponder
	_ func(*TestResponder) error                              = (*TestResponder).Close
)
