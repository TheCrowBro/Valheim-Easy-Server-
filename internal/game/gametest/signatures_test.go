package gametest

import (
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// These pin the phase 0 contract (spec 7.1): every game stream writes its
// conformance test against them, so changing one needs the lead, and must
// change this file too.
var (
	_ func(testing.TB) string                                  = BuildFakeserver
	_ func(*testing.T, game.Game, Options) *Session            = Start
	_ func(*Session, string, string, time.Duration) game.Event = (*Session).WaitEvent
	_ func(*Session) game.RunState                             = (*Session).State
	_ func(*Session)                                           = (*Session).Takeover
	_ func(*Session) time.Time                                 = (*Session).Stop
	_ func(*testing.T, game.Game, Options)                     = Conformance
)
