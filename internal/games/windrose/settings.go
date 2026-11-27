package windrose

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
	// maxServerName and maxPassword: the publisher gives no limit; both
	// are only shown in the game's menus and typed by friends, so the app
	// keeps them short enough for that.
	maxServerName = 64
	maxPassword   = 64
	// maxPlayers: the publisher sizes servers for 2 to 10 players
	// (DedicatedServer.md's hardware table), and the server's own default
	// is 4.
	maxPlayers = 10
	defPlayers = 4
)

// regions are the connection-service regions the server takes in
// UserSelectedRegion (DedicatedServer.md); empty lets the server choose
// the closest itself, which the publisher recommends unless one is slow.
var regions = []game.Choice{
	{Value: "", Label: "The closest one (chosen by the server)"},
	{Value: "EU", Label: "Europe and North America (EU)"},
	{Value: "SEA", Label: "South-East Asia (SEA)"},
	{Value: "CIS", Label: "CIS"},
}

// Schema is what the owner can set. Everything goes into
// ServerDescription.json, which the server reads when it starts, so every
// game setting applies after a restart.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Windrose server",
			Help: "Friends see this name in Windrose when they connect.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they connect. Leave it empty so the invite code is all they need.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defPlayers),
			Help: "Windrose's makers say a server needs about 12 GB of memory for 4 players and 16 GB for 10.", Min: 1, Max: maxPlayers, Restart: true},
		{Key: "region", Scope: game.ServerScope, Kind: "choice", Label: "Connection region", Choices: regions, Advanced: true, Restart: true,
			Help: "Where Windrose's online service connects friends to the server. Leave it on the closest one unless connections are slow."},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Windrose as the server, and the game updates itself on their computers."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
	}
}

// Validate checks the settings for a start of world ("" is the selected
// one). Keys are the schema's own; the manager adds the scope.
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
		errs["password"] = "The password can't start or end with a space: friends would never guess it."
	}
	if msg := intProblem(sv.Server, "maxPlayers", 1, maxPlayers, "Most players at once"); msg != "" {
		errs["maxPlayers"] = msg
	}
	if r, ok := sv.Server["region"]; ok && !slices.ContainsFunc(regions, func(c game.Choice) bool { return c.Value == r }) {
		errs["region"] = "Choose one of the regions."
	}
	if world == "" {
		world = sv.World
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
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

// lineSafe says whether a value is valid text without line breaks or
// other control characters.
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

// players is the player limit to give the server.
func (s *server) players(sv game.Saved) int {
	n := sv.Server.Int("maxPlayers", defPlayers)
	if n < 1 || n > maxPlayers {
		return defPlayers
	}
	return n
}

// region is the connection region to give the server ("" = the closest).
func (s *server) region(sv game.Saved) string {
	r := sv.Server["region"]
	if slices.ContainsFunc(regions, func(c game.Choice) bool { return c.Value == r }) {
		return r
	}
	return ""
}
