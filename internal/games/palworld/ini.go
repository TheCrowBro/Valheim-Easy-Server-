package palworld

import (
	"strings"
)

// Palworld keeps its settings in Unreal Engine ini files (FACTS.md):
//
//   - PalWorldSettings.ini has one section, [/Script/Pal.PalGameWorldSettings],
//     whose single line OptionSettings=(Key=Value,Key="text",…) holds every
//     game and server option. Values can be quoted text (which may hold
//     commas and brackets), numbers, True/False, or a bracketed list
//     ("CrossplayPlatforms=(Steam,Xbox,PS5,Mac)"), so the line can't be split
//     on commas blindly.
//   - GameUserSettings.ini has [/Script/Pal.PalGameLocalSettings] with
//     DedicatedServerName=<the world's 32-character id>, which says which
//     world the server loads.
//
// The app changes only the values it manages and keeps every other line,
// key and value as it found them, so options the owner set by hand in the
// file survive. It keeps the file's own line endings (the shipped default
// file uses Windows' CRLF).

const (
	worldSection = "[/Script/Pal.PalGameWorldSettings]"
	localSection = "[/Script/Pal.PalGameLocalSettings]"
	optionKey    = "OptionSettings"
	worldKey     = "DedicatedServerName"
)

// option is one Key=Value of the OptionSettings line, as written in the
// file (raw keeps an entry without "=" exactly as it was).
type option struct {
	key, value string
	raw        string
}

// options is the parsed OptionSettings line, in the file's order.
type options []option

// parseOptions reads the inside of OptionSettings=( … ). Commas split
// entries only outside quotes and outside brackets.
func parseOptions(inner string) options {
	var out options
	for _, part := range splitTop(inner) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			out = append(out, option{raw: part})
			continue
		}
		out = append(out, option{key: strings.TrimSpace(k), value: v})
	}
	return out
}

// splitTop splits s at commas that are outside quotes and brackets. A
// backslash inside quotes keeps the next character as it is, as Unreal's
// quoted strings do.
func splitTop(s string) []string {
	var parts []string
	depth, start := 0, 0
	quoted, escaped := false, false
	for i, r := range s {
		switch {
		case escaped:
			escaped = false
		case quoted && r == '\\':
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			if depth > 0 {
				depth--
			}
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// get returns the value of key (Unreal matches option names without regard
// to case).
func (o options) get(key string) (string, bool) {
	for _, e := range o {
		if e.raw == "" && strings.EqualFold(e.key, key) {
			return e.value, true
		}
	}
	return "", false
}

// set gives key a value, in place when it is there (keeping the file's
// spelling of the key) or at the end when it isn't.
func (o options) set(key, value string) options {
	for i, e := range o {
		if e.raw == "" && strings.EqualFold(e.key, key) {
			o[i].value = value
			return o
		}
	}
	return append(o, option{key: key, value: value})
}

func (o options) String() string {
	parts := make([]string, len(o))
	for i, e := range o {
		if e.raw != "" {
			parts[i] = e.raw
		} else {
			parts[i] = e.key + "=" + e.value
		}
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// iniFile is an ini file kept line by line, so writing it back changes only
// the lines that were edited.
type iniFile struct {
	lines []string
	eol   string // "\r\n" or "\n", as the file had it
}

func parseINI(text string) *iniFile {
	f := &iniFile{eol: "\n"}
	if strings.Contains(text, "\r\n") {
		f.eol = "\r\n"
	}
	text = strings.TrimPrefix(text, "\uFEFF") // a byte order mark, which Windows editors may add
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if text != "" {
		f.lines = strings.Split(text, "\n")
	}
	return f
}

func (f *iniFile) String() string {
	if len(f.lines) == 0 {
		return ""
	}
	return strings.Join(f.lines, f.eol) + f.eol
}

// find returns the index of the first line that sets key (in any section)
// and its value, or -1.
func (f *iniFile) find(key string) (int, string) {
	for i, l := range f.lines {
		k, v, ok := strings.Cut(l, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return i, v
		}
	}
	return -1, ""
}

// setKey sets key=value: on the line that has it, else as the first line
// of section (which is added at the end when the file has none).
func (f *iniFile) setKey(section, key, value string) {
	if i, _ := f.find(key); i >= 0 {
		f.lines[i] = key + "=" + value
		return
	}
	for i, l := range f.lines {
		if strings.EqualFold(strings.TrimSpace(l), section) {
			f.lines = append(f.lines[:i+1], append([]string{key + "=" + value}, f.lines[i+1:]...)...)
			return
		}
	}
	f.lines = append(f.lines, section, key+"="+value)
}

// optionsLine reads the OptionSettings line. ok is false when the file has
// no such line or it isn't bracketed: an empty file, or one cut short.
func (f *iniFile) optionsLine() (options, bool) {
	i, v := f.find(optionKey)
	if i < 0 {
		return nil, false
	}
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "(") || !strings.HasSuffix(v, ")") {
		return nil, false
	}
	return parseOptions(v[1 : len(v)-1]), true
}

func (f *iniFile) setOptions(o options) {
	f.setKey(worldSection, optionKey, o.String())
}

// quote writes text as an Unreal quoted string. Validate refuses quotes,
// backslashes and line breaks in what the owner types, so nothing needs
// escaping; the replacements are a second guard for values from elsewhere.
func quote(s string) string {
	s = strings.NewReplacer(`\`, "", `"`, "", "\r", "", "\n", "").Replace(s)
	return `"` + s + `"`
}

// unquote reads a quoted value back ("" when it isn't quoted text).
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`)
	}
	return v
}

// boolText is how Unreal writes a bool in these files.
func boolText(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
