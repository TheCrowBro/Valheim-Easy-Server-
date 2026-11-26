//go:build windows

package platform

// gameFirewallReady checks for each of the game's rules by name. The name
// carries a fingerprint of the program and the ports (gameRuleName), so
// finding the name is enough, and it works in any language: netsh
// translates what it prints, but not whether it found the rule.
func gameFirewallReady(r GameRule) bool {
	for _, rule := range gameRules(r) {
		if !ruleExists(rule.name) {
			return false
		}
	}
	return true
}

// testWrapFirewallAll lets TestFixFirewallAllReal wrap the admin script in
// one that writes what it printed, and PowerShell's errors, to a file the
// test can read. The script runs hidden, as admin, in a process the app
// can't see into, so without this a failure on CI leaves no trace. The
// app never sets it, so it runs firewallAllScript exactly as it is.
var testWrapFirewallAll func(script string) string

// fixFirewallAll runs firewallScript's lines unchanged, then the games',
// as one script, so Windows asks for admin permission once.
func fixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error {
	script := firewallAllScript(serverExe, appExe, wake, games)
	if testWrapFirewallAll != nil {
		script = testWrapFirewallAll(script)
	}
	return runElevatedPowerShell(script)
}
