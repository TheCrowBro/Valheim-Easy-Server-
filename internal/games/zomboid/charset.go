package zomboid

import (
	"fmt"
	"slices"
	"strings"
)

// Java on Windows gets its command line in the computer's old "ANSI"
// character set (its code page, such as Windows-1252 for English), not as
// Unicode, and a character the set lacks arrives as "?" (FACTS.md; the
// Necesse stream found the same for its Java). The world name and the
// passwords the app puts on the command line are plain ASCII, which every
// code page has, but the saves folder (-cachedir) is inside the app's data
// folder, whose path holds the Windows user's name. With a character the
// code page lacks, the server would make its data folder somewhere else
// and the app would never find the world. So Plan refuses such a folder.
// On Windows set to use UTF-8 for every language, every character gets
// through.

// argLacks lists, once each, the characters of v that wouldn't reach the
// server's Java as they are.
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

// folderProblem is Plan's message for a folder the server is given on its
// command line whose path has such characters, or "" when it has none.
func folderProblem(path string) string {
	bad := argLacks(path)
	if len(bad) == 0 {
		return ""
	}
	that := "that character"
	if len(bad) > 1 {
		that = "those characters"
	}
	q := make([]string, 0, min(len(bad), 5))
	for _, r := range bad[:min(len(bad), 5)] {
		q = append(q, fmt.Sprintf("%q", string(r)))
	}
	return fmt.Sprintf("Project Zomboid can't run from the app's folder on this computer: its path (%s) has %s, and Windows gives the server's Java its folders in an older character set without %s, so the server would keep its world in the wrong place.",
		path, strings.Join(q, ", "), that)
}
