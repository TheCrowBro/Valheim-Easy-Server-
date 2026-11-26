package satisfactory

import (
	"testing"

	"valheim-easy-server/internal/game/gametest"
)

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// satisfactory personality).
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}
