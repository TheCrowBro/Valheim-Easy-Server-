//go:build darwin

package platform

import "slices"

// macGamesFirewallPrompt is what macOS shows above the password field when
// the games' programs are let through too. Only the panel of R3 on, which
// is called Easy Game Server, asks for that.
const macGamesFirewallPrompt = "Easy Game Server wants to let friends through the macOS firewall to reach your game servers."

// gameFirewallReady checks the macOS firewall, which knows programs, not
// ports: either it is off, or it lets the game's program accept
// connections and isn't blocking everything.
func gameFirewallReady(r GameRule) bool {
	on, blockAll := firewallState()
	return !on || !blockAll && firewallAllows(appPath(r.Program))
}

// fixFirewallAll lets Valheim's server, the app (with wake on) and the
// games' programs through the macOS firewall with one password prompt.
// With no games it is exactly FixFirewall.
func fixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error {
	var extra []string
	for _, g := range games {
		if !needsNothing(g) {
			extra = append(extra, appPath(g.Program))
		}
	}
	if len(extra) == 0 {
		return FixFirewall(serverExe, appExe, wake)
	}
	on, blockAll := firewallState()
	if !on {
		return nil
	}
	if blockAll {
		return errBlockAll
	}
	programs := []string{serverExe}
	if wake {
		programs = append(programs, appPath(appExe))
	}
	for _, p := range extra {
		if !slices.Contains(programs, p) {
			programs = append(programs, p)
		}
	}
	return runAdmin(macFirewallCommand(socketfilterfw, programs), macGamesFirewallPrompt)
}
