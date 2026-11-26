package worldstore

import (
	"testing"

	"valheim-easy-server/internal/game"
)

// This pins the phase 0 contract (spec 2.8): the game streams call New
// while this package is being written, so changing it needs the lead, and
// must change this file too.
var _ func(Format, string, string) game.Worlds = New

func TestSignatures(t *testing.T) {
	if New(Format{}, "", "") == nil {
		t.Error("New must return a game.Worlds")
	}
}
