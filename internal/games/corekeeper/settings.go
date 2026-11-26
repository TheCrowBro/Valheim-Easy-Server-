package corekeeper

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"valheim-easy-server/internal/game"
)

// Limits the panel checks before the server sees a value.
const (
	// maxServerName: the server keeps any length (a 63-character name with
	// emoji went through whole, FACTS.md); friends see it in a menu, so
	// the app keeps it short enough to read there.
	maxServerName = 60
	// maxSlots: the server takes any count from 1 (its own message: "Max
	// number players needs to be at least 1"); the publisher warns that
	// more than the default 8 needs more of the computer. 64 is far past
	// what a few friends need.
	maxSlots   = 64
	defSlots   = 8
	maxSeedLen = 64
)

// The world modes a new world can have, as the server's -worldmode takes
// them (its ARGUMENTS.txt). The mode is chosen once: the publisher says
// changing between creative and the other modes after the world has been
// made isn't supported.
var worldModes = []game.Choice{
	{Value: "normal", Label: "Normal: balanced monsters"},
	{Value: "hard", Label: "Hard: tougher monsters, better rewards"},
	{Value: "casual", Label: "Casual: gentler monsters"},
	{Value: "creative", Label: "Creative: build freely, no monsters"},
}

// modeArg is a world mode as the server's command line spells it.
func modeArg(mode string) string {
	switch mode {
	case "hard":
		return "Hard"
	case "casual":
		return "Casual"
	case "creative":
		return "Creative"
	}
	return "Normal"
}

// Schema is what the owner can set.
//
// The server name is what friends see in Core Keeper (the server's
// -worldname: "the name that connecting clients will see"). The world's
// own name is the app's, for the Worlds page. The mode and the seed only
// matter when a world is made: the world keeps them after that.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Core Keeper server",
			Help: "Friends see this name in Core Keeper when they join.", Max: maxServerName, Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defSlots),
			Help: "Core Keeper's makers say more than 8 needs a faster computer.", Min: 1, Max: maxSlots, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Core Keeper as the server, and Core Keeper on Steam updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "mode", Scope: game.NewWorldScope, Kind: "choice", Label: "World mode", Default: "normal", Choices: worldModes,
			Help: "Chosen once, when the world is made."},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "World seed", Max: maxSeedLen, Advanced: true,
			Help: "Any words or numbers. The same seed makes the same world. Leave it empty for a random one."},
	}
}

// Validate checks the settings for a start of world. Keys are the
// schema's own; the manager adds the scope.
func (s *server) Validate(sv game.Saved, world string) game.FieldErrors {
	errs := game.FieldErrors{}
	name := s.value(sv, "serverName")
	switch {
	case strings.TrimSpace(name) == "":
		errs["serverName"] = "Give the server a name."
	case utf8.RuneCountInString(name) > maxServerName:
		errs["serverName"] = fmt.Sprintf("The server name can be at most %d characters.", maxServerName)
	case !lineSafe(name):
		errs["serverName"] = "The server name can't have line breaks or other control characters."
	}
	if msg := intProblem(sv.Server, "maxPlayers", 1, maxSlots, "Most players at once"); msg != "" {
		errs["maxPlayers"] = msg
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// validateNewWorld checks the values asked only when a world is made.
func validateNewWorld(v game.Values) game.FieldErrors {
	errs := game.FieldErrors{}
	if m, ok := v["mode"]; ok && m != "" && !slices.ContainsFunc(worldModes, func(c game.Choice) bool { return c.Value == m }) {
		errs["mode"] = "Choose one of the world modes."
	}
	seed := v["seed"]
	switch {
	case utf8.RuneCountInString(seed) > maxSeedLen:
		errs["seed"] = fmt.Sprintf("The seed can be at most %d characters.", maxSeedLen)
	case !lineSafe(seed):
		errs["seed"] = "The seed can't have line breaks or other control characters."
	}
	return errs
}

// intProblem checks a whole-number setting, when it is set.
func intProblem(v game.Values, key string, lo, hi int, label string) string {
	raw, ok := v[key]
	if !ok {
		return ""
	}
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < lo || n > hi {
		return fmt.Sprintf("%s must be a number from %d to %d.", label, lo, hi)
	}
	return ""
}

// lineSafe says whether a value can be given as one command-line value:
// valid text without line breaks or other control characters.
func lineSafe(v string) bool {
	return utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl)
}

// value is a server setting or its schema default.
func (s *server) value(sv game.Saved, key string) string {
	if v, ok := sv.Server[key]; ok {
		return v
	}
	schema := s.Schema()
	i := slices.IndexFunc(schema, func(f game.Field) bool { return f.Key == key && f.Scope == game.ServerScope })
	if i < 0 {
		return ""
	}
	return schema[i].Default
}

// slots is the player limit to give the server.
func (s *server) slots(sv game.Saved) int {
	n := sv.Server.Int("maxPlayers", defSlots)
	if n < 1 || n > maxSlots {
		return defSlots
	}
	return n
}
