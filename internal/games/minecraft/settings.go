package minecraft

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"valheim-easy-server/internal/game"
)

// Setting keys (spec 5.2). The allow list's three ways:
const (
	allowAuto = "auto" // on once the internet can reach this computer (the owner's decision)
	allowOn   = "on"
	allowOff  = "off"
)

// Defaults, used when a setting is missing so a hand-edited servers.json
// still starts the server the way the panel shows it.
const (
	defName       = "Minecraft server"
	defMaxPlayers = 10
	defView       = 10
	defMemoryMB   = 2048
	defDifficulty = "normal"
	defGamemode   = "survival"
	defLevelType  = "normal"
	maxNameLen    = 59 // what fits on one line of the server list
)

var (
	difficulties = []game.Choice{{Value: "peaceful", Label: "Peaceful"}, {Value: "easy", Label: "Easy"}, {Value: "normal", Label: "Normal"}, {Value: "hard", Label: "Hard"}}
	gamemodes    = []game.Choice{{Value: "survival", Label: "Survival"}, {Value: "creative", Label: "Creative"}, {Value: "adventure", Label: "Adventure"}}
	levelTypes   = []game.Choice{{Value: "normal", Label: "Normal"}, {Value: "flat", Label: "Flat"}, {Value: "large_biomes", Label: "Large biomes"}, {Value: "amplified", Label: "Amplified"}}
	allowModes   = []game.Choice{
		{Value: allowAuto, Label: "On once friends outside can reach the server"},
		{Value: allowOn, Label: "Always on"},
		{Value: allowOff, Label: "Off: anyone with the address can join"},
	}
)

// Schema is what the panel's form shows. Everything the server reads at
// start is marked Restart; Fingerprint covers exactly those.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: defName, Max: maxNameLen,
			Help: "Shown under the server in friends' server list.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: strconv.Itoa(defMaxPlayers), Min: 1, Max: 100, Restart: true},
		{Key: "allowList", Scope: game.ServerScope, Kind: "choice", Label: "Allow list", Default: allowAuto, Choices: allowModes, Restart: true,
			Help: "Only players on the list can join. Add friends on the Players tab, or click Allow when someone is turned away."},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update Minecraft when the server starts", Default: "true",
			Help: "Friends' Minecraft updates itself to the newest version, so the server should too."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "viewDistance", Scope: game.ServerScope, Kind: "number", Label: "View distance (chunks)", Default: strconv.Itoa(defView), Min: 3, Max: 32, Restart: true, Advanced: true,
			Help: "How far players see. Lower it if the server struggles."},
		{Key: "memoryMB", Scope: game.ServerScope, Kind: "number", Label: "Memory for the server (MB)", Default: strconv.Itoa(defMemoryMB), Min: 1024, Max: 32768, Restart: true, Advanced: true,
			Help: "The most memory Java may use for the server."},
		{Key: "difficulty", Scope: game.WorldScope, Kind: "choice", Label: "Difficulty", Default: defDifficulty, Choices: difficulties, Restart: true},
		{Key: "gamemode", Scope: game.WorldScope, Kind: "choice", Label: "Game mode", Default: defGamemode, Choices: gamemodes, Restart: true,
			Help: "For players joining this world for the first time."},
		{Key: "seed", Scope: game.NewWorldScope, Kind: "text", Label: "Seed", Max: 64, Advanced: true,
			Help: "Leave it empty for a random world."},
		{Key: "levelType", Scope: game.NewWorldScope, Kind: "choice", Label: "World type", Default: defLevelType, Choices: levelTypes, Advanced: true},
		{Key: "hardcore", Scope: game.NewWorldScope, Kind: "bool", Label: "Hardcore (one life; the world is lost when you die)", Default: "false", Advanced: true},
	}
}

func choiceOK(cs []game.Choice, v string) bool {
	return slices.ContainsFunc(cs, func(c game.Choice) bool { return c.Value == v })
}

// value is a setting, or def when it's missing or empty.
func value(v game.Values, key, def string) string {
	if s := strings.TrimSpace(v[key]); s != "" {
		return s
	}
	return def
}

// Validate checks the settings the way the panel shows its errors: by
// setting key.
func (s *server) Validate(sv game.Saved, world string) game.FieldErrors {
	errs := game.FieldErrors{}
	name := strings.TrimSpace(sv.Server["serverName"])
	switch {
	case name == "":
		errs["serverName"] = "Give the server a name."
	case utf8.RuneCountInString(name) > maxNameLen:
		errs["serverName"] = "The server name can be at most " + strconv.Itoa(maxNameLen) + " characters."
	case strings.ContainsFunc(name, unicode.IsControl):
		errs["serverName"] = "The server name can't have line breaks or tabs."
	}
	for _, f := range []struct {
		key      string
		min, max int
		msg      string
	}{
		{"maxPlayers", 1, 100, "Most players must be 1 to 100."},
		{"viewDistance", 3, 32, "The view distance must be 3 to 32 chunks."},
		{"memoryMB", 1024, 32768, "Give the server 1024 to 32768 MB."},
	} {
		if v, ok := sv.Server[f.key]; ok && strings.TrimSpace(v) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < f.min || n > f.max {
				errs[f.key] = f.msg
			}
		}
	}
	if v := sv.Server["allowList"]; v != "" && !choiceOK(allowModes, v) {
		errs["allowList"] = "Choose how the allow list works."
	}
	w := sv.Worlds[world]
	if v := w["difficulty"]; v != "" && !choiceOK(difficulties, v) {
		errs["difficulty"] = "Choose peaceful, easy, normal or hard."
	}
	if v := w["gamemode"]; v != "" && !choiceOK(gamemodes, v) {
		errs["gamemode"] = "Choose survival, creative or adventure."
	}
	if p := s.worlds.NameProblem(world); p != "" {
		errs["world"] = p
	}
	return errs
}

// settings are the values a start uses, with the defaults filled in.
type settings struct {
	name, allowList, difficulty, gamemode string
	maxPlayers, view, memoryMB            int
	port, rconPort                        int
}

func readSettings(sv game.Saved, world string) settings {
	w := sv.Worlds[world]
	return settings{
		name:       value(sv.Server, "serverName", defName),
		allowList:  value(sv.Server, "allowList", allowAuto),
		difficulty: value(w, "difficulty", defDifficulty),
		gamemode:   value(w, "gamemode", defGamemode),
		maxPlayers: sv.Server.Int("maxPlayers", defMaxPlayers),
		view:       sv.Server.Int("viewDistance", defView),
		memoryMB:   sv.Server.Int("memoryMB", defMemoryMB),
		port:       sv.Ports[roleGame],
		rconPort:   sv.Ports[roleRCON],
	}
}

// Fingerprint covers everything the server reads when it starts: the
// command line and the server.properties keys the app sets (but not the
// RCON password, which is new every run). When it differs from the running
// server's, the panel says "restart to apply". The allow list counts by its
// setting, not by whether "auto" has turned it on yet: the app turns it on
// live, which needs no restart.
func (s *server) Fingerprint(sv game.Saved, world string) string {
	c := readSettings(sv, world)
	h := sha256.New()
	for _, v := range []string{
		world, c.name, c.allowList, c.difficulty, c.gamemode,
		strconv.Itoa(c.maxPlayers), strconv.Itoa(c.view), strconv.Itoa(c.memoryMB),
		strconv.Itoa(c.port), strconv.Itoa(c.rconPort),
	} {
		io.WriteString(h, v+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
