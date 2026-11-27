package necesse

import (
	"fmt"
	"slices"
	"strings"
)

// Java on Windows gets its command line in the computer's old "ANSI"
// character set (its code page, such as Windows-1252 for English), not as
// Unicode: its launcher converts the line into that set before it reads
// it (FACTS.md), and a character the set lacks arrives as "?". A world name
// would then name a different file, a password would be one nobody can
// type, and the server name would show "?" to friends. So the settings
// refuse such characters, and Plan refuses a data folder whose path has
// them. On a Mac, and on Windows set to use UTF-8 for every language,
// every character gets through.

// argLacks lists, once each, the characters of v that wouldn't reach the
// server's Java as they are. Plain ASCII reaches it on every code page.
func argLacks(v string) []rune {
	var out []rune
	for _, r := range v {
		if r < 0x80 || slices.Contains(out, r) {
			continue
		}
		if !argRuneFits(r) {
			out = append(out, r)
		}
	}
	return out
}

// codePageProblem is the settings' message for a value with characters
// that wouldn't reach Java as they are, or "" when it has none. what
// starts the sentence ("The password"); then says what would go wrong.
func codePageProblem(what, v, then string) string {
	bad := argLacks(v)
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("%s can't have %s on this computer: Windows gives Necesse's Java its settings in an older character set without %s, so %s.",
		what, quoteRunes(bad), thatOrThose(bad), then)
}

// folderProblem is Plan's message for a folder the server is given on its
// command line whose path has such characters, or "" when it has none.
func folderProblem(path string) string {
	bad := argLacks(path)
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("Necesse can't run from the app's folder on this computer: its path (%s) has %s, and Windows gives Necesse's Java its folders in an older character set without %s, so the server would look for its files in the wrong place.",
		path, quoteRunes(bad), thatOrThose(bad))
}

// quoteRunes lists characters for a message: "é", "ł" and "🏡" (at most
// five, then "…").
func quoteRunes(rs []rune) string {
	more := len(rs) > 5
	if more {
		rs = rs[:5]
	}
	q := make([]string, len(rs))
	for i, r := range rs {
		q[i] = fmt.Sprintf("%q", string(r))
	}
	switch {
	case more:
		return strings.Join(q, ", ") + " and others"
	case len(q) == 1:
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func thatOrThose(rs []rune) string {
	if len(rs) == 1 {
		return "that character"
	}
	return "those characters"
}
