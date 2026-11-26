package necesse

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
	maxServerName = 100 // the welcome message (MOTD) friends see when they join
	maxPassword   = 64  // the server takes longer ones, but nobody types them
	maxSlots      = 250 // the server's own limit (its server.cfg says 1 to 250)
	minMemoryMB   = 1024
	maxMemoryMB   = 16384
	defMemoryMB   = 2048
)

// Schema is what the owner can set. Necesse makes a new world from its
// name alone: the dedicated server has no seed or world options on its
// command line (FACTS.md), so there are no new-world fields.
func (s *server) Schema() game.Schema {
	return game.Schema{
		{Key: "serverName", Scope: game.ServerScope, Kind: "text", Label: "Server name", Default: "Necesse server",
			Help: "Friends see it as the welcome message when they join.", Max: maxServerName, Restart: true, Add: true},
		{Key: "password", Scope: game.ServerScope, Kind: "password", Label: "Password", Max: maxPassword,
			Help: "Friends type it when they join. Leave it empty for no password.", Restart: true, Add: true},
		{Key: "maxPlayers", Scope: game.ServerScope, Kind: "number", Label: "Most players at once", Default: "10",
			Min: 1, Max: maxSlots, Restart: true},
		{Key: "updateOnStart", Scope: game.ServerScope, Kind: "bool", Label: "Update the server when it starts", Default: "true",
			Help: "Friends need the same version of Necesse as the server, and Necesse on Steam updates itself."},
		{Key: "autoRestart", Scope: game.ServerScope, Kind: "bool", Label: "Start it again if it crashes", Default: "true"},
		{Key: "memoryMB", Scope: game.ServerScope, Kind: "number", Label: "Java memory (MB)", Default: strconv.Itoa(defMemoryMB),
			Help: "The most memory the server may use. Raise it only if a big world runs out.",
			Min:  minMemoryMB, Max: maxMemoryMB, Restart: true, Advanced: true},
	}
}

// Validate checks the settings for a start of world. Keys are the
// schema's own; the manager adds the scope.
//
// The server reads every value from its command line, and takes a value
// that starts with "-" for the next option instead (FACTS.md: a password
// of "-abc" left the server with no password at all). So no value may
// start with a dash. On Windows, Java gets that command line in an older
// character set, so no value may have a character it lacks (charset.go).
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
	case strings.HasPrefix(name, "-"):
		errs["serverName"] = "The server name can't start with a dash (-)."
	default:
		if p := codePageProblem("The server name", name, `friends would see "?" instead`); p != "" {
			errs["serverName"] = p
		}
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
	case strings.HasPrefix(pw, "-"):
		errs["password"] = "The password can't start with a dash (-): the server would ignore it and let anyone in."
	default:
		if p := codePageProblem("The password", pw, `the server would get "?" instead and turn friends away`); p != "" {
			errs["password"] = p
		}
	}
	if msg := intProblem(sv.Server, "maxPlayers", 1, maxSlots, "Most players at once"); msg != "" {
		errs["maxPlayers"] = msg
	}
	if msg := intProblem(sv.Server, "memoryMB", minMemoryMB, maxMemoryMB, "Java memory"); msg != "" {
		errs["memoryMB"] = msg
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
