package game

import (
	"maps"
	"strconv"
	"strings"
)

// Values are settings by key. Every value is a string: "true", "25565", "normal".
//
// Keeping every value a string means one generic form in the panel can draw
// any game's settings, and servers.json can hold a setting a newer build
// added without this build having to understand it.
type Values map[string]string

// Int reads key as a whole number. A missing or unreadable value gives def,
// so a hand-edited or damaged setting falls back to the game's default
// instead of stopping the server from starting.
func (v Values) Int(key string, def int) int {
	s, ok := v[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// Bool reads key as on or off. The app always writes "true" or "false";
// reading also accepts what strconv.ParseBool does ("1", "TRUE", …) so a
// hand-edited file means what it looks like. Anything else, or a missing
// key, is off.
func (v Values) Bool(key string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v[key]))
	return err == nil && b
}

// Clone returns a copy that can be changed without changing v. A nil v
// stays nil, so "no settings yet" survives a copy.
func (v Values) Clone() Values {
	return maps.Clone(v)
}

// Saved is everything an instance's Store keeps.
type Saved struct {
	Server Values            // server scope
	Worlds map[string]Values // world scope, by world name
	World  string            // the selected world
	Ports  map[string]int    // by role
	State  Values            // the adapter's own bookkeeping (Palworld world ids, Satisfactory claimed…)
}

// Clone returns a deep copy, which is what Store.Get must hand out: the
// manager reads it with its lock held and may keep it, so it must never
// share maps with what the store changes later.
func (s Saved) Clone() Saved {
	c := Saved{
		Server: s.Server.Clone(),
		World:  s.World,
		Ports:  maps.Clone(s.Ports),
		State:  s.State.Clone(),
	}
	if s.Worlds != nil {
		c.Worlds = make(map[string]Values, len(s.Worlds))
		for name, v := range s.Worlds {
			c.Worlds[name] = v.Clone()
		}
	}
	return c
}
