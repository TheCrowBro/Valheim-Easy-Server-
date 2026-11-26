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

// fixFirewallAll runs firewallScript's lines unchanged, then the games',
// as one script, so Windows asks for admin permission once.
func fixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error {
	return runElevatedPowerShell(firewallAllScript(serverExe, appExe, wake, games))
}
