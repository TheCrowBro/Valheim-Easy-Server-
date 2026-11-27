package minecraft

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// properties is a Java .properties file (server.properties, eula.txt) kept
// line by line, so writing it back changes only the keys the app sets:
// comments, the order and every key the owner or a newer Minecraft added
// stay as they were.
type properties struct {
	entries []propEntry
}

// propEntry is one logical line: a comment or blank line (key ""), or a
// key and its value. raw is the text as it was in the file (a value that
// continues over several lines keeps them).
type propEntry struct {
	raw   string
	key   string
	value string
}

// parseProperties reads the file the way java.util.Properties does: "#" and
// "!" start comments; the key ends at the first unescaped "=", ":" or
// space; a line ending in an odd number of backslashes goes on to the next.
func parseProperties(text string) *properties {
	p := &properties{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // the newline at the end of the file
	}
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		trimmed := strings.TrimLeft(raw, " \t\f")
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '!' {
			p.entries = append(p.entries, propEntry{raw: raw})
			continue
		}
		logical := trimmed
		for continues(logical) && i+1 < len(lines) {
			i++
			raw += "\n" + lines[i]
			logical = logical[:len(logical)-1] + strings.TrimLeft(lines[i], " \t\f")
		}
		key, value := splitProperty(logical)
		p.entries = append(p.entries, propEntry{raw: raw, key: key, value: value})
	}
	return p
}

// continues says whether a line ends in an odd number of backslashes.
func continues(line string) bool {
	n := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// splitProperty splits one logical line into its key and value, both
// unescaped.
func splitProperty(line string) (key, value string) {
	end := len(line)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' {
			i++
			continue
		}
		if c == '=' || c == ':' || c == ' ' || c == '\t' || c == '\f' {
			end = i
			break
		}
	}
	rest := strings.TrimLeft(line[end:], " \t\f")
	if rest != "" && (rest[0] == '=' || rest[0] == ':') {
		rest = strings.TrimLeft(rest[1:], " \t\f")
	}
	return unescapeProperty(line[:end]), unescapeProperty(rest)
}

func unescapeProperty(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	var units []uint16 // \uXXXX escapes, joined so surrogate pairs decode
	flush := func() {
		if len(units) > 0 {
			b.WriteString(string(utf16.Decode(units)))
			units = nil
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			flush()
			b.WriteByte(s[i])
			continue
		}
		i++
		if s[i] == 'u' && i+4 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+5], 16, 16); err == nil {
				units = append(units, uint16(n))
				i += 4
				continue
			}
		}
		flush()
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		default:
			b.WriteByte(s[i])
		}
	}
	flush()
	return b.String()
}

// escapeProperty writes a key or value as java.util.Properties.store does,
// with everything outside printable ASCII as \uXXXX, which every Java reads
// the same whatever the file's encoding (Minecraft itself writes
// "minecraft\:normal").
func escapeProperty(s string, isKey bool) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == ' ' && (i == 0 || isKey):
			b.WriteString(`\ `)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '=' || r == ':' || r == '#' || r == '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04X`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Get returns a key's value (the last one, as Java reads a repeated key).
func (p *properties) Get(key string) (string, bool) {
	for i := len(p.entries) - 1; i >= 0; i-- {
		if e := p.entries[i]; e.key == key {
			return e.value, true
		}
	}
	return "", false
}

// Set gives key a value: on its own line where the key already is (any
// repeats of it are dropped), else on a new line at the end.
func (p *properties) Set(key, value string) {
	line := escapeProperty(key, true) + "=" + escapeProperty(value, false)
	found := false
	out := p.entries[:0]
	for _, e := range p.entries {
		if e.key == key {
			if found {
				continue
			}
			found = true
			e = propEntry{raw: line, key: key, value: value}
		}
		out = append(out, e)
	}
	p.entries = out
	if !found {
		p.entries = append(p.entries, propEntry{raw: line, key: key, value: value})
	}
}

// String is the file's text, with "\n" line ends (Java reads either).
func (p *properties) String() string {
	var b strings.Builder
	for _, e := range p.entries {
		b.WriteString(e.raw)
		b.WriteByte('\n')
	}
	return b.String()
}
