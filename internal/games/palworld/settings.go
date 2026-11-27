package palworld

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
	// The server's list and the game's join screen show the name; the
	// server sets no limit we know of, so this keeps it readable.
	maxServerName = 64
	maxPassword   = 64
	// The server's own default and the most its docs mention (-players=32,
	// FACTS.md).
	maxSlots = 32
)

// worldOption is a world setting the app writes into OptionSettings when
// that world runs. Palworld keeps these options for the whole server, so
// each world's own choice is written again before each of its starts.
type worldOption struct {
	field game.Field
	ini   string                // the OptionSettings key (FACTS.md)
	write func(v string) string // the value as the file writes it
}

// rate choices are multipliers; the file writes them as 1.000000.
func rateChoices(values ...string) []game.Choice {
	out := make([]game.Choice, 0, len(values))
	for _, v := range values {
		label := v + "×"
		if v == "1" {
			label = "1× (normal)"
		}
		out = append(out, game.Choice{Value: v, Label: label})
	}
	return out
}

func rateText(v string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f <= 0 {
		f = 1
	}
	return strconv.FormatFloat(f, 'f', 6, 64)
}

func plainText(v string) string { return v }

// worldOptions are the world settings, with the keys checked against the
// 1.0 server's DefaultPalWorldSettings.ini and its documentation
// (FACTS.md). Every one applies at the next start, so each is Restart.
var worldOptions = []worldOption{
	{ini: "DeathPenalty", write: plainText, field: game.Field{Key: "deathPenalty", Kind: "choice", Label: "When a player dies",
		Default: "Item", Help: "What a player drops when they die.",
		Choices: []game.Choice{
			{Value: "None", Label: "Keep everything"},
			{Value: "Item", Label: "Drop items, keep equipment"},
			{Value: "ItemAndEquipment", Label: "Drop items and equipment"},
			{Value: "All", Label: "Drop items, equipment and the Pals in the team"},
		}}},
	{ini: "ExpRate", write: rateText, field: game.Field{Key: "expRate", Kind: "choice", Label: "Experience",
		Default: "1", Help: "How fast players and Pals level up.", Choices: rateChoices("0.5", "1", "1.5", "2", "3", "5")}},
	{ini: "PalCaptureRate", write: rateText, field: game.Field{Key: "palCaptureRate", Kind: "choice", Label: "Catching Pals",
		Default: "1", Help: "How easily Pals are caught.", Choices: rateChoices("0.5", "1", "1.5", "2", "3")}},
	{ini: "PalSpawnNumRate", write: rateText, field: game.Field{Key: "palSpawnNumRate", Kind: "choice", Label: "Wild Pals",
		Default: "1", Help: "How many wild Pals appear. More makes the server work harder.",
		Choices: rateChoices("0.5", "1", "1.5", "2", "3"), Advanced: true}},
	{ini: "DayTimeSpeedRate", write: rateText, field: game.Field{Key: "dayTimeSpeedRate", Kind: "choice", Label: "Day speed",
		Default: "1", Help: "Higher makes days pass faster.", Choices: rateChoices("0.5", "1", "2", "3"), Advanced: true}},
	{ini: "NightTimeSpeedRate", write: rateText, field: game.Field{Key: "nightTimeSpeedRate", Kind: "choice", Label: "Night speed",
		Default: "1", Help: "Higher makes nights pass faster.", Choices: rateChoices("0.5", "1", "2", "3"), Advanced: true}},
}

// Schema is what the owner can set. A new world needs nothing but its name:
// the server makes it from the settings when it first starts it.
func (s *server) Schema() game.Schema {
	sc := game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Palworld server",
			Help: "Friends see it when they join.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(maxSlots),
			Min: 1, Max: maxSlots, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends can only join a Palworld server of exactly their game's version, and Palworld on Steam updates itself. Leave this on."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
	}
	for _, o := range worldOptions {
		f := o.field
		f.Scope, f.Restart = game.WorldScope, true
		sc = append(sc, f)
	}
	return sc
}

// Validate checks the settings for a start of world. Keys are the schema's
// own; the manager adds the scope.
//
// The server name and password go into PalWorldSettings.ini as quoted text
// on one line (ini.go), so a quote, a backslash or a line break would end
// the text early and spoil every option after it.
func (s *server) Validate(sv game.Saved, world string) game.FieldErrors {
	errs := game.FieldErrors{}
	name := s.value(sv, "serverName")
	switch {
	case strings.TrimSpace(name) == "":
		errs["serverName"] = "Give the server a name."
	case utf8.RuneCountInString(name) > maxServerName:
		errs["serverName"] = fmt.Sprintf("The server name can be at most %d characters.", maxServerName)
	default:
		if p := textProblem("The server name", name); p != "" {
			errs["serverName"] = p
		}
	}
	pw := sv.Server["password"]
	switch {
	case utf8.RuneCountInString(pw) > maxPassword:
		errs["password"] = fmt.Sprintf("The password can be at most %d characters.", maxPassword)
	case pw != strings.TrimSpace(pw):
		// The server would keep the space, and nobody could tell why
		// their password didn't work.
		errs["password"] = "The password can't start or end with a space."
	default:
		if p := textProblem("The password", pw); p != "" {
			errs["password"] = p
		}
	}
	if raw, ok := sv.Server["maxPlayers"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 1 || n > maxSlots {
			errs["maxPlayers"] = fmt.Sprintf("Most players at once must be a number from 1 to %d.", maxSlots)
		}
	}
	wv := sv.Worlds[world]
	for _, o := range worldOptions {
		v, ok := wv[o.field.Key]
		if ok && !slices.ContainsFunc(o.field.Choices, func(c game.Choice) bool { return c.Value == v }) {
			errs[o.field.Key] = fmt.Sprintf("%q isn't one of the choices for %s.", v, strings.ToLower(o.field.Label))
		}
	}
	// The name rules are for a world the app is about to make: a world
	// already there (one copied in by hand is listed under its id) is
	// started by the name it has.
	if world != "" && !s.worlds.Exists(world) {
		if p := s.worlds.NameProblem(world); p != "" {
			errs["world"] = p
		}
	}
	return errs
}

// textProblem says why a value can't be quoted text in the settings file.
func textProblem(what, v string) string {
	switch {
	case !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl):
		return what + " can't have line breaks or other control characters."
	case strings.ContainsAny(v, `"\`):
		return what + ` can't contain " or \ (Palworld's settings file can't hold them).`
	}
	return ""
}

// value is a server setting or its schema default.
func (s *server) value(sv game.Saved, key string) string {
	if v, ok := sv.Server[key]; ok {
		return v
	}
	return schemaDefault(game.ServerScope, key)
}

// worldValue is a world setting or its schema default.
func worldValue(sv game.Saved, world, key string) string {
	if v, ok := sv.Worlds[world][key]; ok {
		return v
	}
	return schemaDefault(game.WorldScope, key)
}

func schemaDefault(scope game.Scope, key string) string {
	for _, o := range worldOptions {
		if scope == game.WorldScope && o.field.Key == key {
			return o.field.Default
		}
	}
	if scope == game.ServerScope {
		for _, f := range (&server{}).Schema() {
			if f.Scope == scope && f.Key == key {
				return f.Default
			}
		}
	}
	return ""
}

// players is how many may be on at once, kept within the server's range.
func players(sv game.Saved) int {
	n := sv.Server.Int("maxPlayers", maxSlots)
	return min(max(n, 1), maxSlots)
}
