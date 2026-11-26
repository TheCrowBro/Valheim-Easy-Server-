//go:build !windows

package necesse

// argRuneFits says whether a character reaches the server's Java as it is
// on its command line. Off Windows, Java reads its arguments as UTF-8, so
// every character does. Tests replace it to act as Windows.
var argRuneFits = func(r rune) bool { return true }

// codePage names the character set Java gets its arguments in, for the
// real-server check's notes.
func codePage() string { return "UTF-8 (not Windows)" }
