package platform

// Firewall rules for games other than Valheim (spec 2.12). The rules
// themselves are made per system: firewall_games_windows.go (with the
// script in winparse_games.go), firewall_games_darwin.go and
// firewall_games_linux.go.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// GameFirewallReady says whether r's rules are in place, so friends can
// reach the game through this computer's firewall. A rule that can't be
// made (see FixFirewallAll) is never ready; one that needs nothing (no
// ports and not AnyPort) always is.
func GameFirewallReady(r GameRule) bool {
	if checkGameRule(r) != nil {
		return false
	}
	if needsNothing(r) {
		return true
	}
	return gameFirewallReady(r)
}

// FixFirewallAll runs today's Valheim fix (on Windows, firewallScript,
// byte for byte) followed by the games' rules, behind one admin prompt.
// Like FixFirewall it returns before the prompt is answered. On Windows a
// long script goes through the checked temp file (psAdminCommand). The
// games' rules are all made again each time, and rules for games that are
// no longer listed are removed, so removing a game needs no admin prompt
// of its own.
func FixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error {
	for _, g := range games {
		if err := checkGameRule(g); err != nil {
			return err
		}
	}
	return fixFirewallAll(serverExe, appExe, wake, games)
}

// instanceName is what an instance id may look like in a rule's name.
// Ids are like "minecraft" or "minecraft-2"; anything else could break out
// of the quotes the name sits in, so it is refused.
var instanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// checkGameRule says why a rule can't be made, in words for the owner.
func checkGameRule(r GameRule) error {
	if !instanceName.MatchString(r.Instance) {
		return fmt.Errorf("can't make a firewall rule for the game %q: its id may only have small letters, digits, - and _", r.Instance)
	}
	if r.Program == "" || !filepath.IsAbs(r.Program) {
		return fmt.Errorf("can't make a firewall rule for %s: the game's program %q isn't a full path", r.Instance, r.Program)
	}
	for _, ports := range [][]int{r.TCP, r.UDP} {
		for _, p := range ports {
			if p <= 0 || p > 65535 {
				return fmt.Errorf("can't make a firewall rule for %s: %d isn't a port", r.Instance, p)
			}
		}
	}
	return nil
}

// needsNothing says whether r asks for no rule at all.
func needsNothing(r GameRule) bool {
	return !r.AnyPort && len(r.TCP) == 0 && len(r.UDP) == 0
}

// macFirewallCommand is the shell command that lets programs through the
// macOS firewall, as an administrator runs it: each is added to the
// firewall's list and allowed to accept connections. For Valheim's server
// and the app it is exactly what FixFirewall runs.
func macFirewallCommand(socketfilterfw string, programs []string) string {
	var cmds []string
	for _, p := range programs {
		cmds = append(cmds, socketfilterfw+" --add "+shellQuote(p), socketfilterfw+" --unblockapp "+shellQuote(p))
	}
	return strings.Join(cmds, "; ")
}
