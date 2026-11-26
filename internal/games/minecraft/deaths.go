package minecraft

import (
	_ "embed"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// deathsJSON is every "death.*" entry of Minecraft 26.3's English language
// file (assets/minecraft/lang/en_us.json inside the server jar), copied
// as it is: 106 messages. The server writes a death to its log in these
// words, with the player's name where %1$s is (FACTS.md).
//
//go:embed deaths.json
var deathsJSON []byte

// deathPattern is one death message as two regular expressions with the
// dead player's name as the group: a greedy one and a lazy one, because a
// killer's name (a named mob) may itself contain words of the message.
type deathPattern struct {
	key          string
	greedy, lazy *regexp.Regexp
}

// tooLong is the message Minecraft sends instead of a death message that
// is too long, with a plain copy of it after the colon.
const tooLong = "death.attack.message_too_long"

var (
	deathPatterns []deathPattern
	tooLongPrefix string
	reArg         = regexp.MustCompile(`%(?:(\d+)\$)?s`)
)

func init() {
	var msgs map[string]string
	if err := json.Unmarshal(deathsJSON, &msgs); err != nil {
		panic("minecraft: deaths.json: " + err.Error())
	}
	// In a fixed order, so the same line always reads the same way.
	for _, key := range slices.Sorted(maps.Keys(msgs)) {
		msg := msgs[key]
		if key == tooLong {
			tooLongPrefix, _, _ = strings.Cut(msg, "%s")
			continue
		}
		g, l, ok := deathRegexps(msg)
		if !ok {
			continue // a piece of another message ("Intentional Game Design")
		}
		deathPatterns = append(deathPatterns, deathPattern{key: key, greedy: g, lazy: l})
	}
}

// deathRegexps turns "%1$s was slain by %2$s" into ^(.+) was slain by .+$.
// It says false for a text without the player's name.
func deathRegexps(msg string) (greedy, lazy *regexp.Regexp, ok bool) {
	var g, l strings.Builder
	g.WriteString("^")
	l.WriteString("^")
	last := 0
	for _, m := range reArg.FindAllStringSubmatchIndex(msg, -1) {
		lit := regexp.QuoteMeta(msg[last:m[0]])
		g.WriteString(lit)
		l.WriteString(lit)
		if m[2] >= 0 && msg[m[2]:m[3]] == "1" {
			ok = true
			g.WriteString("(.+)")
			l.WriteString("(.+?)")
		} else {
			g.WriteString(".+")
			l.WriteString(".+?")
		}
		last = m[1]
	}
	if !ok {
		return nil, nil, false
	}
	lit := regexp.QuoteMeta(msg[last:])
	g.WriteString(lit + "$")
	l.WriteString(lit + "$")
	return regexp.MustCompile(g.String()), regexp.MustCompile(l.String()), true
}

// deathOf says whose death a message tells of, among the players on
// (isOn), or "" when it isn't a death message of one of them.
func deathOf(msg string, isOn func(name string) bool) string {
	if rest, ok := strings.CutPrefix(msg, tooLongPrefix); ok && tooLongPrefix != "" {
		if who := deathOf(rest, isOn); who != "" {
			return who
		}
		// The plain copy may be cut short: its first word is the player.
		if name, _, _ := strings.Cut(rest, " "); isOn(name) {
			return name
		}
		return ""
	}
	for _, p := range deathPatterns {
		for _, re := range []*regexp.Regexp{p.greedy, p.lazy} {
			if m := re.FindStringSubmatch(msg); m != nil && isOn(m[1]) {
				return m[1]
			}
		}
	}
	return ""
}
