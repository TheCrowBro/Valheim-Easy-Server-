package satisfactory

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
	// The server's list and the game's Server Manager show the name; the
	// server sets no limit we know of, so this keeps it readable.
	maxServerName = 64
	maxPassword   = 64
	// Four is the game's own limit; the wiki warns that more can make
	// the server slower and less stable, and 16 GB of memory is advised
	// past four (FACTS.md).
	defaultPlayers = 4
	maxSlots       = 16
)

// The new-world choice of where the players start. The values are the
// names the game shows, which is what the API's StartingLocation takes
// (FACTS.md); "random" is sent as an empty name, which the API reads as
// "pick one at random".
const randomStart = "random"

var startChoices = []game.Choice{
	{Value: "Grass Fields", Label: "Grass Fields (easiest)"},
	{Value: "Rocky Desert", Label: "Rocky Desert"},
	{Value: "Northern Forest", Label: "Northern Forest"},
	{Value: "Dune Desert", Label: "Dune Desert (hardest)"},
	{Value: randomStart, Label: "Let the game choose"},
}

// Schema is what the owner can set. The server's name and the friends'
// password are given to the server through its API right after each
// start, so a change applies at the next start (Restart). The admin
// password isn't part of a start at all: the app sets it up itself, and it
// is shown here so the owner can manage the server from the game's Server
// Manager too.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Satisfactory server",
			Help: "Friends see it in the game's Server Manager.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defaultPlayers),
			Min: 1, Max: maxSlots, Restart: true,
			Help: "Satisfactory's own limit is 4. More players need more memory (about 16 GB) and can slow the server down."},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends can only join a Satisfactory server of the same version as their game, and the game updates itself. Leave this on."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "adminPassword", Scope: game.ServerScope, Kind: "password", Label: "Admin password", Max: maxPassword, Advanced: true,
			Help: "The app makes one when it first sets the server up. Type it in Satisfactory's Server Manager to manage the server from the game. If you set this server up yourself before, type the admin password you chose."},
		{Key: "startingLocation", Scope: game.NewWorldScope, Kind: "choice", Label: "Where the players start", Default: "Grass Fields",
			Help: "Only asked when the world is made.", Choices: startChoices},
	}
}

// Validate checks the settings for a start of world. Keys are the schema's
// own; the manager adds the scope.
func (s *server) Validate(sv game.Saved, world string) game.FieldErrors {
	errs := game.FieldErrors{}
	name := serverName(sv)
	switch {
	case strings.TrimSpace(name) == "":
		errs["serverName"] = "Give the server a name."
	case utf8.RuneCountInString(name) > maxServerName:
		errs["serverName"] = fmt.Sprintf("The server name can be at most %d characters.", maxServerName)
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl):
		errs["serverName"] = "The server name can't have line breaks or other control characters."
	}
	pw, admin := sv.Server["password"], sv.Server["adminPassword"]
	if p := passwordProblem("The password", pw); p != "" {
		errs["password"] = p
	}
	if p := passwordProblem("The admin password", admin); p != "" {
		errs["adminPassword"] = p
	} else if admin != "" && admin == pw {
		// The server refuses a password that is the other one's
		// (password_in_use), and friends would be admins with it.
		errs["adminPassword"] = "The admin password must be different from the friends' password."
	}
	if raw, ok := sv.Server["maxPlayers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 1 || n > maxSlots {
			errs["maxPlayers"] = fmt.Sprintf("Most players at once must be a number from 1 to %d.", maxSlots)
		}
	}
	// The name rules are for a world the app is about to make: a world
	// already there is started by the name it has.
	if world != "" && !s.worlds.Exists(world) {
		if p := s.worlds.NameProblem(world); p != "" {
			errs["world"] = p
		}
	}
	return errs
}

// passwordProblem says why a password can't be used, or "".
func passwordProblem(what, pw string) string {
	switch {
	case utf8.RuneCountInString(pw) > maxPassword:
		return fmt.Sprintf("%s can be at most %d characters.", what, maxPassword)
	case pw != strings.TrimSpace(pw):
		// The server would keep the space, and nobody could tell why
		// their password didn't work.
		return what + " can't start or end with a space."
	case !utf8.ValidString(pw) || strings.ContainsFunc(pw, unicode.IsControl):
		return what + " can't have line breaks or other control characters."
	}
	return ""
}

// serverName is the server name setting or its default.
func serverName(sv game.Saved) string {
	if v, ok := sv.Server["serverName"]; ok {
		return v
	}
	return "Satisfactory server"
}

// players is how many may be on at once, kept within the server's range.
func players(sv game.Saved) int {
	n := sv.Server.Int("maxPlayers", defaultPlayers)
	return min(max(n, 1), maxSlots)
}

// startingLocation is the API's name for a new world's start ("" for one
// the game picks).
func startingLocation(v game.Values) string {
	loc := v["startingLocation"]
	if loc == "" {
		loc = "Grass Fields"
	}
	if loc == randomStart || !slices.ContainsFunc(startChoices, func(c game.Choice) bool { return c.Value == loc }) {
		return ""
	}
	return loc
}

func itoa(n int) string { return strconv.Itoa(n) }
