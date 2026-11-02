package platform

// Windows settings and commands, as plain text. These are used on Windows
// only, but they're plain text, so their tests run everywhere.

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

const (
	fwGameRule = "Valheim Easy Server - game"
	fwWakeRule = "Valheim Easy Server - wake signal"
)

// fwRuleName names a firewall rule after the program it lets through, so a
// rule made for where the server or the app used to be (before the folder
// was moved, or the server folder changed in Settings) doesn't count for
// where it is now. Windows paths ignore case, so the name does too.
func fwRuleName(rule, program string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(program))))
	return rule + " (" + hex.EncodeToString(sum[:4]) + ")"
}

// firewallScript is what FixFirewall runs as admin. It first removes this
// app's rules from older versions and for old places, and any "block" rules
// Windows made for the server or the app when its firewall pop-up was
// dismissed.
func firewallScript(serverExe, appExe string, wake bool) string {
	lines := []string{
		"$server = " + psQuote(serverExe),
		"$app = " + psQuote(appExe),
		"Remove-NetFirewallRule -DisplayName " + psQuote(fwGameRule+"*") + " -ErrorAction SilentlyContinue",
		"Remove-NetFirewallRule -DisplayName " + psQuote(fwWakeRule+"*") + " -ErrorAction SilentlyContinue",
		`netsh advfirewall firewall delete rule name=all "program=$server"`,
		`netsh advfirewall firewall add rule "name=` + fwRuleName(fwGameRule, serverExe) + `" dir=in action=allow "program=$server" enable=yes profile=any`,
		`netsh advfirewall firewall delete rule name=all "program=$app"`,
	}
	if wake {
		lines = append(lines, `netsh advfirewall firewall add rule "name=`+fwRuleName(fwWakeRule, appExe)+`" dir=in action=allow "program=$app" protocol=UDP localport=9 remoteip=localsubnet enable=yes profile=any`)
	}
	return strings.Join(lines, "\n")
}

// startupEntryRuns reports whether Windows starts exe at sign-in from a Run
// entry with this command, given the entry's StartupApproved value (nil
// when there is none). The entry must run exe itself, not a copy somewhere
// the app was moved from. Task Manager and Settings → Apps → Startup switch
// an entry off by setting the first byte of its StartupApproved value to an
// odd number (03 or 07), leaving the entry itself in place.
func startupEntryRuns(command string, approved []byte, exe string) bool {
	if len(approved) > 0 && approved[0]&1 != 0 {
		return false
	}
	return exe != "" && strings.EqualFold(filepath.Clean(runCommandExe(command)), filepath.Clean(exe))
}

// runCommandExe is the program a Run entry starts: the part in quotes, or
// everything up to the first space.
func runCommandExe(command string) string {
	command = strings.TrimSpace(command)
	if rest, ok := strings.CutPrefix(command, `"`); ok {
		exe, _, _ := strings.Cut(rest, `"`)
		return exe
	}
	exe, _, _ := strings.Cut(command, " ")
	return exe
}
