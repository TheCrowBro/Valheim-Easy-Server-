package vintagestory

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
	maxServerName = 80  // shown in each friend's server list
	maxPassword   = 64  // the server takes longer ones, but nobody types them
	maxSlots      = 128 // the server's default is 16; far more needs a big computer
	maxSeed       = 64
)

// A world's play style decides its rules when it is made (the game's own
// presets, FACTS.md). The server keeps a world's style in the save, so
// this is asked only for a new world.
type playStyle struct {
	code, label string
	langCode    string // what the game writes beside it in serverconfig.json
	worldType   string // creative building is a flat world
}

var playStyles = []playStyle{
	{"surviveandbuild", "Standard", "preset-surviveandbuild", "standard"},
	{"exploration", "Exploration (less fighting)", "preset-exploration", "standard"},
	{"wildernesssurvival", "Wilderness survival (harder)", "preset-wildernesssurvival", "standard"},
	{"homosapiens", "Homo sapiens (no lore, very hard)", "preset-homosapiens", "standard"},
	{"creativebuilding", "Creative building (flat, unlimited blocks)", "creativebuilding", "superflat"},
}

func styleOf(code string) playStyle {
	if i := slices.IndexFunc(playStyles, func(p playStyle) bool { return p.code == code }); i >= 0 {
		return playStyles[i]
	}
	return playStyles[0]
}

// Schema is what the owner can set.
func (s *server) Schema() game.Schema {
	styles := make([]game.Choice, len(playStyles))
	for i, p := range playStyles {
		styles[i] = game.Choice{Value: p.code, Label: p.label}
	}
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Vintage Story server",
			Help: "Friends see it in their list of servers.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Min: 1, Max: maxPassword,
			Help: "Friends type it when they join. It's what keeps strangers out, so it can't be empty, and it can't have spaces.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: "16",
			Min: 1, Max: maxSlots, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Vintage Story as the server, and the game updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "playStyle", Scope: game.NewWorldScope, Kind: "choice", Label: "Play style", Default: playStyles[0].code, Choices: styles,
			Help: "The game's own presets. A world keeps its style."},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "Seed", Max: maxSeed, Advanced: true,
			Help: "Leave it empty for a random world."},
	}
}

// Validate checks the settings for a start of world. Keys are the schema's
// own; the manager adds the scope.
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
	pw := sv.Server["password"]
	switch {
	case pw == "":
		// Without a password anyone who finds the server could walk in: the
		// app turns the game's whitelist off (spec 5.5).
		errs["password"] = "Vintage Story needs a password: it's what keeps strangers out."
	case utf8.RuneCountInString(pw) > maxPassword:
		errs["password"] = fmt.Sprintf("The password can be at most %d characters.", maxPassword)
	case !lineSafe(pw):
		errs["password"] = "The password can't have line breaks or other control characters."
	case strings.ContainsFunc(pw, unicode.IsSpace):
		// The game's own rule ("/serverconfig password … Cannot use
		// spaces", FACTS.md).
		errs["password"] = "The password can't have spaces: Vintage Story doesn't allow them."
	}
	if raw, ok := sv.Server["maxPlayers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 1 || n > maxSlots {
			errs["maxPlayers"] = fmt.Sprintf("Most players at once must be a number from 1 to %d.", maxSlots)
		}
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// newWorldProblems checks the new-world values.
func newWorldProblems(nw game.Values) game.FieldErrors {
	errs := game.FieldErrors{}
	if code, ok := nw["playStyle"]; ok && code != "" && !slices.ContainsFunc(playStyles, func(p playStyle) bool { return p.code == code }) {
		errs["playStyle"] = "Choose one of the play styles."
	}
	if seed := nw["seed"]; utf8.RuneCountInString(seed) > maxSeed || !lineSafe(seed) {
		errs["seed"] = fmt.Sprintf("The seed can be at most %d characters, without line breaks.", maxSeed)
	}
	return errs
}

// lineSafe says whether a value is valid text without line breaks or other
// control characters.
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

func port(sv game.Saved) int {
	if p := sv.Ports["game"]; p > 0 {
		return p
	}
	return defaultPort
}
