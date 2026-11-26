package zomboid

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
	// The server keeps at most 64 characters of its public name
	// (PublicName's own limit, FACTS.md).
	maxServerName = 64
	maxPassword   = 64
	// MaxPlayers takes 1 to 100 (the server's own limits, FACTS.md); 32
	// is its default, and the developers say more than 20 is
	// "inadvisable" for now.
	maxSlots    = 100
	defSlots    = 16
	minMemoryMB = 2048
	maxMemoryMB = 32768
	// The server's own .bat gives Java 16 GB; most of the owner's friends
	// play in small groups, for which 6 GB is plenty (FACTS.md).
	defMemoryMB = 6144
	maxSeed     = 32
)

// zombieLevel is one choice of the "Zombies" world setting: the sandbox's
// "Zombie Count" (Zombies, 1 to 6) and the population multiplier that the
// game's own menu sets with it (FACTS.md: the file's own comment lists
// both). Editing the file, the game changes only what it is told, so the
// app sets both, as the game's menu does.
type zombieLevel struct {
	Value, Label string
	Count        int    // SandboxVars.Zombies
	Multiplier   string // SandboxVars.ZombieConfig.PopulationMultiplier, as Lua text
}

var zombieLevels = []zombieLevel{
	{"insane", "Insane", 1, "2.5"},
	{"veryhigh", "Very high", 2, "1.6"},
	{"high", "High", 3, "1.2"},
	{"normal", "Normal", 4, "0.65"},
	{"low", "Low", 5, "0.15"},
	{"none", "None", 6, "0.0"},
}

func zombieLevelOf(v string) (zombieLevel, bool) {
	i := slices.IndexFunc(zombieLevels, func(z zombieLevel) bool { return z.Value == v })
	if i < 0 {
		return zombieLevel{}, false
	}
	return zombieLevels[i], true
}

// Schema is what the owner can set. The server settings go into the
// world's Server/<world>.ini before each start; the world's "Zombies" goes
// into its SandboxVars file; a new world's seed into its .ini once.
func (s *server) Schema() game.Schema {
	var zc []game.Choice
	for _, z := range zombieLevels {
		zc = append(zc, game.Choice{Value: z.Value, Label: z.Label})
	}
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Project Zomboid server",
			Help: "The name the game shows for the server.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defSlots),
			Min: 1, Max: maxSlots, Restart: true},
		{Key: "pvp", Scope: game.ServerScope, Kind: "bool", Label: "Players can hurt each other", Default: "true", Restart: true},
		{Key: "pauseEmpty", Scope: game.ServerScope, Kind: "bool", Label: "Pause the game while nobody is on", Default: "true",
			Help: "Time stands still in the world while the server is empty.", Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Project Zomboid as the server, and Project Zomboid on Steam updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "memoryMB", Scope: game.ServerScope, Kind: "number", Label: "Java memory (MB)", Default: strconv.Itoa(defMemoryMB),
			Help: "The most memory the server may use. Raise it for a big world or many players; the computer needs about 2 GB more than this.",
			Min:  minMemoryMB, Max: maxMemoryMB, Restart: true, Advanced: true},
		{Key: "zombies", Scope: game.WorldScope, Kind: "choice", Label: "Zombies", Default: "normal", Choices: zc,
			Help: "How many zombies the world has.", Restart: true},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "World seed", Max: maxSeed,
			Help: "Letters and numbers. Leave it empty for a random world.", Advanced: true},
	}
}

// Validate checks the settings for a start of world. Keys are the
// schema's own; the manager adds the scope.
//
// The server reads its settings file a line at a time and splits each at
// its first "=", trimming spaces (FACTS.md), so a value can't hold a line
// break or an "=", and a password can't start or end with a space.
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
	case strings.Contains(name, "="):
		errs["serverName"] = "The server name can't have an equals sign (=): Project Zomboid's settings file would cut it off there."
	}
	pw := sv.Server["password"]
	switch {
	case utf8.RuneCountInString(pw) > maxPassword:
		errs["password"] = fmt.Sprintf("The password can be at most %d characters.", maxPassword)
	case !lineSafe(pw):
		errs["password"] = "The password can't have line breaks or other control characters."
	case pw != strings.TrimSpace(pw):
		errs["password"] = "The password can't start or end with a space: Project Zomboid would drop it, and nobody could tell why theirs didn't work."
	case strings.Contains(pw, "="):
		errs["password"] = "The password can't have an equals sign (=): Project Zomboid's settings file would cut it off there."
	}
	if msg := intProblem(sv.Server, "maxPlayers", 1, maxSlots, "Most players at once"); msg != "" {
		errs["maxPlayers"] = msg
	}
	if msg := intProblem(sv.Server, "memoryMB", minMemoryMB, maxMemoryMB, "Java memory"); msg != "" {
		errs["memoryMB"] = msg
	}
	if z, ok := sv.Worlds[world]["zombies"]; ok {
		if _, known := zombieLevelOf(z); !known {
			errs["zombies"] = "Choose how many zombies the world has."
		}
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// seedProblem checks a new world's seed: the server takes a string, and
// makes its own from letters (FACTS.md), so letters and numbers are what
// the app allows.
func seedProblem(seed string) string {
	if len(seed) > maxSeed {
		return fmt.Sprintf("The world seed can be at most %d characters.", maxSeed)
	}
	for _, r := range seed {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return "The world seed can only have letters and numbers."
		}
	}
	return ""
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

// lineSafe says whether a value can be one line of the settings file:
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

// boolValue is a yes/no server setting or its default.
func (s *server) boolValue(sv game.Saved, key string) bool {
	return game.Values{key: s.value(sv, key)}.Bool(key)
}
