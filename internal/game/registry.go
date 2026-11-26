package game

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// The registry holds every game this build knows. Adapters add themselves
// from init() (internal/games/all imports each one), so the manager finds
// games without naming any of them.

type registry struct {
	mu    sync.RWMutex
	games []Game
}

// reg is replaced by tests that need an empty registry.
var reg = &registry{}

// validID is what a game id may look like. The id is also a folder name on
// every system, the start of each log file's name ("<id>-2006-…log") and,
// with "-2" after it, a second instance's id, so it is kept to lower-case
// letters and digits.
var validID = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)

// validCommand is what Discord accepts as a slash command name.
var validCommand = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Register adds a game. It is called from init() in games/<id>, so a
// mistake panics at once, in every test and before the app starts, rather
// than surfacing later as two games fighting over one folder or command.
// It panics on a nil game, an id that can't be a folder name, a duplicate
// id or slash command, an unknown stage, or a second Legacy game.
func Register(g Game) {
	reg.register(g)
}

// All returns every registered game, by Info().Order (then by id, so the
// order never depends on which package's init ran first).
func All() []Game {
	return reg.all()
}

// Get finds a game by id.
func Get(id string) (Game, bool) {
	return reg.get(id)
}

// Legacy returns the one game kept in the legacy files (Valheim), or nil
// when none is registered (as in a test that registers no games).
func Legacy() Game {
	return reg.legacy()
}

// command is the game's slash command: Info.Command, or its id when that's empty.
func command(in Info) string {
	if in.Command != "" {
		return in.Command
	}
	return in.ID
}

func (r *registry) register(g Game) {
	if g == nil {
		panic("game.Register: nil game")
	}
	in := g.Info()
	if !validID.MatchString(in.ID) {
		panic(fmt.Sprintf("game.Register: id %q must be 1-32 lower-case letters and digits, starting with a letter", in.ID))
	}
	if !validCommand.MatchString(command(in)) {
		panic(fmt.Sprintf("game.Register: %s: slash command %q must be 1-32 of a-z, 0-9, _ and -", in.ID, in.Command))
	}
	switch in.Stage {
	case Hidden, Preview, Ready:
	default:
		panic(fmt.Sprintf("game.Register: %s: unknown stage %q", in.ID, in.Stage))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, old := range r.games {
		o := old.Info()
		switch {
		case o.ID == in.ID:
			panic(fmt.Sprintf("game.Register: %s registered twice", in.ID))
		case command(o) == command(in):
			panic(fmt.Sprintf("game.Register: %s and %s both use the slash command %q", o.ID, in.ID, command(in)))
		case o.Legacy && in.Legacy:
			panic(fmt.Sprintf("game.Register: %s and %s are both the legacy game; only one can be", o.ID, in.ID))
		}
	}
	r.games = append(r.games, g)
}

func (r *registry) all() []Game {
	r.mu.RLock()
	out := slices.Clone(r.games)
	r.mu.RUnlock()
	slices.SortStableFunc(out, func(a, b Game) int {
		ai, bi := a.Info(), b.Info()
		return cmp.Or(cmp.Compare(ai.Order, bi.Order), cmp.Compare(ai.ID, bi.ID))
	})
	return out
}

func (r *registry) get(id string) (Game, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, g := range r.games {
		if g.Info().ID == id {
			return g, true
		}
	}
	return nil, false
}

func (r *registry) legacy() Game {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, g := range r.games {
		if g.Info().Legacy {
			return g
		}
	}
	return nil
}
