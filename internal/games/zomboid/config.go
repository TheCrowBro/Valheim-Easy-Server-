package zomboid

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// The world's two settings files, edited before each start and only then:
// the server reads them when it starts and rewrites them whole from what
// it read (FACTS.md), so an edit while it runs would be lost or undone.
//
// Server/<world>.ini holds "Key=Value" lines with "# …" comments. The app
// changes the lines of the keys it sets and keeps every other line, so a
// setting the owner changed by hand stays. A new world has no file yet:
// the app writes only its own keys, and the server fills in its defaults
// for the rest at its first start (FACTS.md).
//
// Server/<world>_SandboxVars.lua is a Lua table, SandboxVars = { … }, with
// nested tables such as ZombieConfig = { … }. The app sets two values in
// it the same way; a new world gets a file with just those, which the
// server likewise completes.

// iniFile and sandboxFile are where the world's settings are, under the
// saves folder (the server's -cachedir).
func iniFile(saves, world string) string { return filepath.Join(saves, "Server", world+".ini") }
func sandboxFile(saves, world string) string {
	return filepath.Join(saves, "Server", world+"_SandboxVars.lua")
}

// setting is one "Key=Value" line the app writes.
type setting struct{ Key, Value string }

// newline is what a new settings file's lines end with: the server writes
// CRLF on Windows (FACTS.md), and an edited file keeps what it has.
func newline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// lineEnd is the line ending text already uses, or nl when it has none.
func lineEnd(text, nl string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\n") {
		return "\n"
	}
	return nl
}

// iniSet sets keys in the text of a settings file. A key's line is the one
// the server would read for it: trimmed, it starts with "Key=". Every such
// line is replaced, so a key written twice can't keep an old value; keys
// the file doesn't have are added at its end, in order. Every other line,
// comments included, stays as it is. nl ends new lines in a file that has
// none yet.
func iniSet(text string, set []setting, nl string) string {
	nl = lineEnd(text, nl)
	lines := strings.Split(text, nl)
	// A file the server wrote has no newline after its last line; one
	// that ends with one keeps it.
	trailing := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailing {
		lines = lines[:len(lines)-1]
	}
	done := map[string]bool{}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, _, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		for _, st := range set {
			if key == st.Key {
				lines[i] = st.Key + "=" + st.Value
				done[st.Key] = true
			}
		}
	}
	for _, st := range set {
		if !done[st.Key] {
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, st.Key+"="+st.Value)
			done[st.Key] = true
		}
	}
	out := strings.Join(lines, nl)
	if trailing {
		out += nl
	}
	return out
}

// iniGet reads one key's value the way the server does: the last line for
// it wins, cut at its first "=" after the key, trimmed.
func iniGet(text, key string) (string, bool) {
	var val string
	found := false
	for l := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if ok && k == key {
			v, _, _ = strings.Cut(v, "=")
			val, found = strings.TrimSpace(v), true
		}
	}
	return val, found
}

// sandboxSettings are the SandboxVars values the app sets for a world.
type sandboxSettings struct {
	Zombies    int    // Zombies, at the top level
	Multiplier string // ZombieConfig.PopulationMultiplier
}

var (
	reSandboxOpen = regexp.MustCompile(`^\s*SandboxVars\s*=\s*\{`)
	reLuaKey      = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?),?\s*$`)
)

// errSandboxShape says a SandboxVars file doesn't look like one the server
// writes, so the app leaves it alone rather than guess.
var errSandboxShape = errors.New("the world's SandboxVars file isn't laid out the way Project Zomboid writes it, so the app left it as it was")

// sandboxSet sets the app's values in the text of a SandboxVars file, or
// makes a new file with only them when text is empty. A value's line is
// found by its key at the right depth of the table (Zombies directly in
// SandboxVars, PopulationMultiplier in its ZombieConfig table), so a key of
// the same name elsewhere is never touched; a value the file lacks is added
// where the server would read it. Every other line stays.
func sandboxSet(text string, v sandboxSettings, nl string) (string, error) {
	zombies := "Zombies = " + strconv.Itoa(v.Zombies) + ","
	multiplier := "PopulationMultiplier = " + v.Multiplier + ","
	if strings.TrimSpace(text) == "" {
		return strings.Join([]string{
			"SandboxVars = {",
			"    VERSION = 6,",
			"    " + zombies,
			"    ZombieConfig = {",
			"        " + multiplier,
			"    },",
			"}",
			"",
		}, nl), nil
	}
	nl = lineEnd(text, nl)
	lines := strings.Split(text, nl)

	open := -1    // the "SandboxVars = {" line
	closing := -1 // the "}" that ends it
	version := -1 // its "VERSION = …" line
	zombie := -1  // its "Zombies = …" line
	zcOpen := -1  // its "ZombieConfig = {" line
	zcClose := -1 // the "}," that ends ZombieConfig
	zcMult := -1  // ZombieConfig's "PopulationMultiplier = …" line
	depth := 0    // braces open before the line
	inZC := false // inside ZombieConfig
	for i, l := range lines {
		code := luaCode(l)
		if open < 0 {
			if reSandboxOpen.MatchString(code) {
				open = i
				if depth = braces(code); depth != 1 {
					// The whole table on one line: not as the server
					// writes it.
					return text, errSandboxShape
				}
			}
			continue
		}
		if m := reLuaKey.FindStringSubmatch(code); m != nil {
			switch {
			case depth == 1 && m[2] == "VERSION":
				version = i
			case depth == 1 && m[2] == "Zombies":
				zombie = i
			case depth == 1 && m[2] == "ZombieConfig" && strings.HasPrefix(strings.TrimSpace(m[3]), "{"):
				zcOpen, inZC = i, true
			case depth == 2 && inZC && m[2] == "PopulationMultiplier":
				zcMult = i
			}
		}
		depth += braces(code)
		if inZC && depth == 1 {
			zcClose, inZC = i, false
		}
		if depth == 0 {
			closing = i
			break
		}
	}
	if open < 0 || closing < 0 || (zcOpen >= 0 && zcClose < 0) {
		return text, errSandboxShape
	}

	indent := func(i int, def string) string {
		if m := reLuaKey.FindStringSubmatch(luaCode(lines[i])); m != nil {
			return m[1]
		}
		return def
	}
	// Lines are replaced in place; new ones are inserted from the bottom
	// up, so the line numbers found above stay right.
	type insert struct {
		at   int // before this line
		text []string
	}
	var inserts []insert
	switch {
	case zcMult >= 0:
		lines[zcMult] = indent(zcMult, "        ") + multiplier
	case zcOpen >= 0:
		inserts = append(inserts, insert{zcOpen + 1, []string{indent(zcOpen, "    ") + "    " + multiplier}})
	default:
		inserts = append(inserts, insert{closing, []string{"    ZombieConfig = {", "        " + multiplier, "    },"}})
	}
	switch {
	case zombie >= 0:
		lines[zombie] = indent(zombie, "    ") + zombies
	case version >= 0:
		inserts = append(inserts, insert{version + 1, []string{indent(version, "    ") + zombies}})
	default:
		inserts = append(inserts, insert{open + 1, []string{"    " + zombies}})
	}
	// Apply the inserts from the last position to the first.
	sort.Slice(inserts, func(a, b int) bool { return inserts[a].at > inserts[b].at })
	for _, in := range inserts {
		lines = append(lines[:in.at], append(append([]string(nil), in.text...), lines[in.at:]...)...)
	}
	return strings.Join(lines, nl), nil
}

// luaCode is a line without its "--" comment (outside a string).
func luaCode(l string) string {
	in := false
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '"' && (i == 0 || l[i-1] != '\\'):
			in = !in
		case !in && l[i] == '-' && i+1 < len(l) && l[i+1] == '-':
			return l[:i]
		}
	}
	return l
}

// braces is how many tables a line opens less how many it closes,
// outside strings.
func braces(code string) int {
	n, in := 0, false
	for i := 0; i < len(code); i++ {
		switch c := code[i]; {
		case c == '"' && (i == 0 || code[i-1] != '\\'):
			in = !in
		case !in && c == '{':
			n++
		case !in && c == '}':
			n--
		}
	}
	return n
}

// writeFile writes a settings file whole or not at all: a new file next to
// it, then renamed over it, so a crash never leaves half a file for the
// server to read.
func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".egs-tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// readText reads a file, "" when there is none.
func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}
