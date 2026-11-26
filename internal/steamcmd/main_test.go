package steamcmd

import (
	"testing"

	"valheim-easy-server/internal/steamcmd/steamcmdtest"
)

// TestMain lets the test binary also play SteamCMD (see steamcmdtest), so
// the tests run the real installer code on every system.
func TestMain(m *testing.M) {
	steamcmdtest.Main(m)
}
