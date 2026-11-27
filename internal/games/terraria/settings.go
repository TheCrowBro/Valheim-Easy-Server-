package terraria

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
	maxServerName = 64  // the welcome message (motd) friends see when they join
	maxPassword   = 64  // the server takes longer ones, but nobody types them
	maxPlayers    = 255 // the server's own limit (its serverconfig.txt says 1 to 255)
	maxSeed       = 40  // the real server keeps only the first 40 characters (FACTS.md)
)

// The new-world choices and what serverconfig.txt calls them.
var (
	sizes = []game.Choice{
		{Value: "small", Label: "Small"},
		{Value: "medium", Label: "Medium"},
		{Value: "large", Label: "Large"},
	}
	sizeCodes = map[string]string{"small": "1", "medium": "2", "large": "3"}

	difficulties = []game.Choice{
		{Value: "classic", Label: "Classic"},
		{Value: "expert", Label: "Expert"},
		{Value: "master", Label: "Master"},
		{Value: "journey", Label: "Journey"},
	}
	difficultyCodes = map[string]string{"classic": "0", "expert": "1", "master": "2", "journey": "3"}
)

// Schema is what the owner can set. A world's size, difficulty and seed
// are fixed when Terraria makes it, so they are asked only for a new world
// (spec 2.9). There is no "world evil" choice: the dedicated server has no
// setting for it when it makes a world by itself, and picks one at random
// (FACTS.md).
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Terraria server",
			Help: "Friends see it as the welcome message when they join.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: "8",
			Min: 1, Max: maxPlayers, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Terraria as the server, and Terraria on Steam updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "size", Scope: game.NewWorldScope, Kind: "choice", Label: "World size", Default: "medium", Choices: sizes, Add: true,
			Help: "Bigger worlds take longer to make and need more memory."},
		{Key: "difficulty", Scope: game.NewWorldScope, Kind: "choice", Label: "Difficulty", Default: "classic", Choices: difficulties, Add: true},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "Seed", Max: maxSeed, Advanced: true,
			Help: "Leave it empty for a random world. The same seed makes the same world."},
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
	pw := sv.Server["password"]
	switch {
	case utf8.RuneCountInString(pw) > maxPassword:
		errs["password"] = fmt.Sprintf("The password can be at most %d characters.", maxPassword)
	case !lineSafe(pw):
		errs["password"] = "The password can't have line breaks or other control characters."
	case pw != strings.TrimSpace(pw):
		// The server would keep the space as part of the password, and
		// nobody could tell why theirs didn't work.
		errs["password"] = "The password can't start or end with a space."
	}
	if v, ok := sv.Server["maxPlayers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < 1 || n > maxPlayers {
			errs["maxPlayers"] = fmt.Sprintf("Most players at once must be a number from 1 to %d.", maxPlayers)
		}
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// validateNewWorld checks the values for making a world.
func validateNewWorld(v game.Values) game.FieldErrors {
	errs := game.FieldErrors{}
	if size := v["size"]; size != "" && sizeCodes[size] == "" {
		errs["size"] = "Choose small, medium or large."
	}
	if d := v["difficulty"]; d != "" && difficultyCodes[d] == "" {
		errs["difficulty"] = "Choose classic, expert, master or journey."
	}
	seed := v["seed"]
	switch {
	case utf8.RuneCountInString(seed) > maxSeed:
		errs["seed"] = fmt.Sprintf("The seed can be at most %d characters: Terraria ignores the rest.", maxSeed)
	case !lineSafe(seed):
		errs["seed"] = "The seed can't have line breaks or other control characters."
	}
	return errs
}

// lineSafe says whether a value can go on one line of serverconfig.txt,
// which ends a value at the line's end.
func lineSafe(v string) bool {
	return utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl)
}

// value is a setting or its schema default.
func (s *server) value(sv game.Saved, key string) string {
	if v, ok := sv.Server[key]; ok {
		return v
	}
	i := slices.IndexFunc(s.Schema(), func(f game.Field) bool { return f.Key == key && f.Scope == game.ServerScope })
	if i < 0 {
		return ""
	}
	return s.Schema()[i].Default
}
