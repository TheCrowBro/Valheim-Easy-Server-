package platform

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// psQuote makes s a PowerShell string literal. PowerShell also takes the
// curly quotes ‘ ’ ‚ ‛ as quote marks, so those are doubled too.
func psQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// psEncode gives a script in the form PowerShell's -EncodedCommand takes
// (base64 of its UTF-16LE text). Scripts that need admin rights go this
// way, in the command line, rather than as a file someone else could change
// before it runs.
func psEncode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}
