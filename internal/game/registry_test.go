package game

import (
	"strings"
	"testing"
)

type testGame struct{ info Info }

func (g testGame) Info() Info         { return g.info }
func (g testGame) New(env Env) Server { return nil }

func newTestGame(id string, order int) testGame {
	return testGame{Info{ID: id, Order: order, Stage: Ready}}
}

// emptyRegistry gives the test a registry of its own, so registering test
// games never leaks into other tests.
func emptyRegistry(t *testing.T) {
	t.Helper()
	old := reg
	reg = &registry{}
	t.Cleanup(func() { reg = old })
}

func ids(games []Game) string {
	var s []string
	for _, g := range games {
		s = append(s, g.Info().ID)
	}
	return strings.Join(s, ",")
}

func TestRegistryOrderAndLookup(t *testing.T) {
	emptyRegistry(t)
	if len(All()) != 0 || Legacy() != nil {
		t.Fatal("a new registry isn't empty")
	}
	if _, ok := Get("valheim"); ok {
		t.Fatal("found a game in an empty registry")
	}
	v := newTestGame("valheim", 1)
	v.info.Legacy = true
	Register(newTestGame("terraria", 3))
	Register(newTestGame("minecraft", 2))
	Register(v)
	Register(newTestGame("necesse", 3)) // same Order as terraria: by id

	if got := ids(All()); got != "valheim,minecraft,necesse,terraria" {
		t.Errorf("All() = %s", got)
	}
	if g, ok := Get("minecraft"); !ok || g.Info().ID != "minecraft" {
		t.Errorf("Get(minecraft) = %v, %v", g, ok)
	}
	if _, ok := Get("Minecraft"); ok {
		t.Error("Get matched an id in another case")
	}
	if l := Legacy(); l == nil || l.Info().ID != "valheim" {
		t.Errorf("Legacy() = %v", l)
	}

	// All returns a copy: changing it changes nothing.
	all := All()
	all[0] = newTestGame("intruder", 0)
	if got := ids(All()); got != "valheim,minecraft,necesse,terraria" {
		t.Errorf("All() after changing a copy = %s", got)
	}
}

func TestRegisterRefuses(t *testing.T) {
	legacy := func(id string) testGame {
		g := newTestGame(id, 0)
		g.info.Legacy = true
		return g
	}
	withCommand := func(id, cmd string) testGame {
		g := newTestGame(id, 0)
		g.info.Command = cmd
		return g
	}
	withStage := func(id string, s Stage) testGame {
		g := newTestGame(id, 0)
		g.info.Stage = s
		return g
	}
	for _, c := range []struct {
		name   string
		before []Game
		g      Game
		say    string
	}{
		{"nil", nil, nil, "nil game"},
		{"empty id", nil, newTestGame("", 0), "id"},
		{"id with a dash", nil, newTestGame("minecraft-2", 0), "id"},
		{"id in capitals", nil, newTestGame("Minecraft", 0), "id"},
		{"id with a slash", nil, newTestGame("a/b", 0), "id"},
		{"id starting with a digit", nil, newTestGame("7days", 0), "id"},
		{"duplicate id", []Game{newTestGame("minecraft", 1)}, newTestGame("minecraft", 2), "twice"},
		{"second legacy", []Game{legacy("valheim")}, legacy("other"), "legacy"},
		{"bad command", nil, withCommand("zomboid", "Zomboid!"), "slash command"},
		{"command too long", nil, withCommand("zomboid", strings.Repeat("z", 33)), "slash command"},
		{"same command", []Game{withCommand("valheim", "play")}, withCommand("minecraft", "play"), "slash command"},
		{"command same as another id", []Game{newTestGame("valheim", 1)}, withCommand("other", "valheim"), "slash command"},
		{"no stage", nil, withStage("terraria", ""), "stage"},
		{"unknown stage", nil, withStage("terraria", "beta"), "stage"},
	} {
		t.Run(c.name, func(t *testing.T) {
			emptyRegistry(t)
			for _, g := range c.before {
				Register(g)
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("registered without a panic")
				}
				if msg, _ := r.(string); !strings.Contains(msg, c.say) {
					t.Errorf("panic %q doesn't mention %q", r, c.say)
				}
				if got := len(All()); got != len(c.before) {
					t.Errorf("the refused game was kept: %d games", got)
				}
			}()
			Register(c.g)
		})
	}
}

func TestRegisterAccepts(t *testing.T) {
	emptyRegistry(t)
	Register(newTestGame("vintagestory", 1))            // the id is the command
	Register(withCommandGame("zomboid", "pz_server-1")) // its own command
	h := newTestGame("hiddengame", 3)
	h.info.Stage = Hidden
	Register(h)
	p := newTestGame("previewgame", 4)
	p.info.Stage = Preview
	Register(p)
	if got := len(All()); got != 4 {
		t.Errorf("All() has %d games, want 4", got)
	}
	if Legacy() != nil {
		t.Error("a legacy game without one being registered")
	}
}

func withCommandGame(id, cmd string) testGame {
	g := newTestGame(id, 2)
	g.info.Command = cmd
	return g
}
