package platform

// The text RunElevatedWait gives macOS's osascript, and what it reads
// back. Used on macOS only, but plain text, so the tests run everywhere.

import (
	"strconv"
	"strings"
)

// macElevatePrompt is what macOS shows above the password field. It names
// no app, so it stays right whatever the app is called.
const macElevatePrompt = "Setting up a game server needs an administrator's permission."

// macElevatedScript is the AppleScript that runs exe with args as an
// administrator and prints its exit code. `do shell script` fails on any
// exit code but 0, which would hide the code, so the shell prints it
// instead; the program's own output is dropped, so the code is all that
// comes back.
func macElevatedScript(exe string, args []string) string {
	words := []string{shellQuote(exe)}
	for _, a := range args {
		words = append(words, shellQuote(a))
	}
	command := strings.Join(words, " ") + " >/dev/null 2>&1; echo $?"
	return "do shell script " + appleScriptString(command) +
		" with administrator privileges with prompt " + appleScriptString(macElevatePrompt)
}

// parseElevatedExit reads the exit code macElevatedScript prints.
// AppleScript may end lines with \r.
func parseElevatedExit(out string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	return n, err == nil
}

// macUserCanceled says whether osascript failed because someone clicked
// Cancel at the password prompt (AppleScript's error -128).
func macUserCanceled(stderr string) bool {
	return strings.Contains(stderr, "(-128)")
}
