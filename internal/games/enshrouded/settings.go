package enshrouded

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
	// maxServerName: friends read the name in Enshrouded's server list;
	// the server itself sets no limit the app knows of.
	maxServerName = 60
	// The password friends type in Enshrouded's join box. The server
	// takes any password; these are the app's own limits: long enough not
	// to be guessed in a few tries, short enough to type.
	minPassword = 5
	maxPassword = 32
	// The server takes 1 to 16 players (slotCount; Keen's documentation).
	maxSlots = 16
	defSlots = 16
)

// presets are the server's difficulty presets (gameSettingsPreset), and
// "custom", which keeps whatever the owner put in enshrouded_server.json's
// gameSettings by hand (the server reads gameSettings only with "Custom").
var presets = []game.Choice{
	{Value: "default", Label: "Default: the game as it was made"},
	{Value: "relaxed", Label: "Relaxed: fewer enemies, more resources"},
	{Value: "hard", Label: "Hard: more enemies, and fiercer"},
	{Value: "survival", Label: "Survival: hunger and fiercer enemies"},
	{Value: "custom", Label: "Custom: the gameSettings in enshrouded_server.json"},
}

// presetName is a preset as the server's file spells it.
func presetName(v string) string {
	switch v {
	case "relaxed":
		return "Relaxed"
	case "hard":
		return "Hard"
	case "survival":
		return "Survival"
	case "custom":
		return "Custom"
	}
	return "Default"
}

// Schema is what the owner can set.
//
// The server name is what friends see in Enshrouded's list of servers.
// The password is the one user group's (Friends). The difficulty is the
// world's: the server reads it from its settings file at every start, so
// each world keeps its own.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Enshrouded server",
			Help: "Friends see this name in Enshrouded's list of servers.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password",
			Help: fmt.Sprintf("Friends type this when they join (%d to %d characters).", minPassword, maxPassword),
			Min:  minPassword, Max: maxPassword, Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defSlots),
			Help: "Each player on uses more of the computer: Keen recommends a fast computer for 16.", Min: 1, Max: maxSlots, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Enshrouded as the server, and Enshrouded on Steam updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "textChat", Scope: game.ServerScope, Kind: "bool", Label: "Text chat", Default: "true", Restart: true, Advanced: true,
			Help: "Players can type messages to each other in the game."},
		{Key: "voiceChat", Scope: game.ServerScope, Kind: "bool", Label: "Voice chat", Default: "false", Restart: true, Advanced: true,
			Help: "Players can talk to each other with a microphone in the game."},
		{Key: "difficulty", Scope: game.WorldScope, Kind: "choice", Label: "Difficulty", Default: "default", Choices: presets, Restart: true,
			Help: "Can be changed between games; it applies from the next start."},
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
	pw := s.value(sv, "password")
	switch n := utf8.RuneCountInString(pw); {
	case strings.TrimSpace(pw) == "":
		errs["password"] = "Give the server a password: friends type it to join."
	case n < minPassword || n > maxPassword:
		errs["password"] = fmt.Sprintf("The password must be %d to %d characters.", minPassword, maxPassword)
	case !lineSafe(pw):
		errs["password"] = "The password can't have line breaks or other control characters."
	}
	if raw, ok := sv.Server["maxPlayers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 1 || n > maxSlots {
			errs["maxPlayers"] = fmt.Sprintf("Most players at once must be a number from 1 to %d.", maxSlots)
		}
	}
	if d := s.worldValue(sv, world, "difficulty"); !slices.ContainsFunc(presets, func(c game.Choice) bool { return c.Value == d }) {
		errs["difficulty"] = "Choose one of the difficulties."
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// lineSafe says whether a value can go into the server's settings file
// and be typed in the game: valid text without line breaks or other
// control characters.
func lineSafe(v string) bool {
	return utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl)
}

// value is a server setting or its schema default.
func (s *server) value(sv game.Saved, key string) string {
	if v, ok := sv.Server[key]; ok {
		return v
	}
	return s.def(game.ServerScope, key)
}

// worldValue is a world's setting or its schema default.
func (s *server) worldValue(sv game.Saved, world, key string) string {
	if v, ok := sv.Worlds[world][key]; ok {
		return v
	}
	return s.def(game.WorldScope, key)
}

func (s *server) def(scope game.Scope, key string) string {
	schema := s.Schema()
	i := slices.IndexFunc(schema, func(f game.Field) bool { return f.Key == key && f.Scope == scope })
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

// boolValue is a server setting read as on or off, with its default.
func (s *server) boolValue(sv game.Saved, key string) bool {
	return game.Values{key: s.value(sv, key)}.Bool(key)
}
