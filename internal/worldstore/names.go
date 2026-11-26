package worldstore

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// defaultMaxName caps names when a game sets no limit of its own: the
// world's name ends up in folder and file names under the app's data
// folder, and Windows struggles with very long paths.
const defaultMaxName = 60

// reservedNames are names Windows keeps for devices: a file called "CON"
// or "con.txt" can't be made there.
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"COM¹": true, "COM²": true, "COM³": true, "LPT¹": true, "LPT²": true, "LPT³": true,
	"CONIN$": true, "CONOUT$": true,
}

func (s *store) maxName() int {
	if s.f.MaxName > 0 {
		return s.f.MaxName
	}
	return defaultMaxName
}

// NameProblem says what is wrong with a name for a new or renamed world,
// or "" when it can be used. These are the rules every game shares, because
// a world's name becomes a file or folder name on Windows and macOS; a game
// adds its own by wrapping the store.
func (s *store) NameProblem(name string) string {
	max := s.maxName()
	switch {
	case strings.TrimSpace(name) == "":
		return "Give the world a name."
	case name != strings.TrimSpace(name):
		return "The world name can't start or end with a space."
	case !utf8.ValidString(name):
		return "The world name has characters that can't be used in a file name."
	case utf8.RuneCountInString(name) > max:
		return fmt.Sprintf("The world name can be at most %d characters.", max)
	case strings.ContainsAny(name, `\/:*?"<>|`) || strings.ContainsFunc(name, unicode.IsControl):
		return `The world name can't contain any of these: \ / : * ? " < > |`
	case strings.HasPrefix(name, ".") || strings.HasSuffix(name, "."):
		return "The world name can't start or end with a dot."
	case strings.HasPrefix(name, "-"):
		// Every game's server is given the world's name on its command
		// line, where a leading dash reads as an option.
		return "The world name can't start with a dash (-)."
	case reservedNames[strings.ToUpper(strings.TrimRight(strings.SplitN(name, ".", 2)[0], " "))]:
		return "That name is reserved by Windows. Pick another."
	}
	return ""
}

// freeName finds a name like "Forest 2" that no world uses yet, or "" when
// the name can't be made into one (the owner then picks a name).
func (s *store) freeName(name string, taken map[string]bool) string {
	name = strings.TrimLeft(name, " .-") // a world name can't start with these
	if name == "" {
		return ""
	}
	for n := 2; n < 1000; n++ {
		suffix := " " + strconv.Itoa(n)
		base := []rune(name)
		if max := s.maxName() - len(suffix); len(base) > max {
			if max < 1 {
				return ""
			}
			base = base[:max]
		}
		try := strings.TrimSpace(string(base)) + suffix
		if !taken[strings.ToLower(try)] && s.NameProblem(try) == "" && !s.Exists(try) {
			return try
		}
	}
	return ""
}
