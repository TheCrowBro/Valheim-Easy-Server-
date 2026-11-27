//go:build !windows

package zomboid

// argRuneFits says whether a character reaches the server's Java as it is
// on its command line. Off Windows, Java reads its arguments as UTF-8, so
// every character does. Tests replace it to act as Windows.
var argRuneFits = func(r rune) bool { return true }
