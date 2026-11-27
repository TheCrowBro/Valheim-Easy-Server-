package all

import (
	"slices"
	"testing"

	"valheim-easy-server/internal/game"
)

// TestRegisteredGames: this build registers the games the owner chose, with
// Valheim as the legacy game, and every other game starts as a preview
// until its real-server checks are green (spec 8.2). This replaces R2's
// TestOneGameRegistered, which allowed only Valheim.
func TestRegisteredGames(t *testing.T) {
	want := []string{"valheim", "minecraft", "terraria", "necesse", "vintagestory", "zomboid",
		"enshrouded", "corekeeper", "palworld", "satisfactory", "windrose"}
	var got []string
	for _, g := range game.All() {
		info := g.Info()
		got = append(got, info.ID)
		if info.Legacy != (info.ID == "valheim") {
			t.Errorf("%s: legacy is %v", info.ID, info.Legacy)
		}
		if info.ID != "valheim" && info.Stage == game.Ready {
			t.Errorf("%s is marked ready before its real-server checks are green", info.ID)
		}
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			t.Errorf("%s isn't registered (registered: %v)", id, got)
		}
	}
	if l := game.Legacy(); l == nil || l.Info().ID != "valheim" {
		t.Errorf("the legacy game isn't Valheim")
	}
}
