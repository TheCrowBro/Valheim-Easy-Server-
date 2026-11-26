package platform

// The Windows firewall rules for games other than Valheim, as the text of
// the script FixFirewallAll runs as admin. Used on Windows only, but plain
// text, so the tests run everywhere.

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// fwGameRules starts the names of the games' firewall rules. It is not
// fwRules: builds before this one remove every rule named "Valheim Easy
// Server - *" when they fix the firewall, so after a rollback the games'
// rules would go each time. Copies that run games depend on it, so it
// never changes.
const fwGameRules = "Easy Game Server - "

// gameFwRule is one firewall rule a game needs.
type gameFwRule struct {
	name  string
	proto string // "TCP" or "UDP"; "" for a program-wide rule
	ports string // "25565" or "7777,7778"; "" for a program-wide rule
}

// gameRules lists the rules r needs: one per protocol that has ports, or,
// with AnyPort, one for the program on any port.
func gameRules(r GameRule) []gameFwRule {
	if r.AnyPort {
		return []gameFwRule{{name: gameRuleName(r.Instance, r.Program, "", "")}}
	}
	var rules []gameFwRule
	for _, p := range []struct {
		proto string
		ports []int
	}{{"TCP", r.TCP}, {"UDP", r.UDP}} {
		if ports := portList(p.ports); ports != "" {
			rules = append(rules, gameFwRule{name: gameRuleName(r.Instance, r.Program, p.proto, ports), proto: p.proto, ports: ports})
		}
	}
	return rules
}

// gameRuleName names a game's rule: the prefix, the instance, the
// protocol and a fingerprint. The fingerprint covers the program's path
// (in lower case, as Windows paths ignore case) and the rule's ports, so
// a rule made for where the game used to be, or for the ports it used to
// have, doesn't count for now, and the check offers to fix it. A
// program-wide rule's fingerprint covers only the path, as Valheim's do.
func gameRuleName(instance, program, proto, ports string) string {
	key := strings.ToLower(filepath.Clean(program))
	label := fwGameRules + instance
	if proto != "" {
		key += "\n" + proto + " " + ports
		label += " " + proto
	}
	sum := sha256.Sum256([]byte(key))
	return label + " (" + hex.EncodeToString(sum[:4]) + ")"
}

// portList is ports sorted, without repeats, as netsh takes them.
func portList(ports []int) string {
	ps := slices.Clone(ports)
	slices.Sort(ps)
	ps = slices.Compact(ps)
	words := make([]string, len(ps))
	for i, p := range ps {
		words[i] = strconv.Itoa(p)
	}
	return strings.Join(words, ",")
}

// gamesFirewallScript is the games' part of what FixFirewallAll runs as
// admin, after firewallScript (which sets $server, $app and $fw). It
// removes every game rule first, so rules for games that were removed, or
// for old ports, go too; then any "block" rules Windows made for a game's
// program when its firewall pop-up was dismissed; then it adds the rules.
// All the removals come before all the additions, so two rules for one
// program can't undo each other, and a program that is Valheim's server or
// the app keeps the variable (and the clean-up) firewallScript gave it.
func gamesFirewallScript(serverExe, appExe string, games []GameRule) string {
	lines := []string{"Remove-NetFirewallRule -DisplayName " + psQuote(fwGameRules+"*") + " -ErrorAction SilentlyContinue"}
	vars := map[string]string{
		strings.ToLower(filepath.Clean(serverExe)): "$server",
		strings.ToLower(filepath.Clean(appExe)):    "$app",
	}
	var adds []string
	n := 0
	for _, g := range games {
		rules := gameRules(g)
		if len(rules) == 0 {
			continue
		}
		key := strings.ToLower(filepath.Clean(g.Program))
		v, ok := vars[key]
		if !ok {
			n++
			v = "$g" + strconv.Itoa(n)
			vars[key] = v
			lines = append(lines,
				v+" = "+psQuote(g.Program),
				`netsh $fw delete rule name=all "program=`+v+`"`)
		}
		for _, r := range rules {
			add := `netsh $fw add rule "name=` + r.name + `" dir=in action=allow "program=` + v + `"`
			if r.proto != "" {
				add += " protocol=" + r.proto + " localport=" + r.ports
			}
			adds = append(adds, add)
		}
	}
	return strings.Join(append(lines, adds...), "\n")
}

// firewallAllScript is what FixFirewallAll runs as admin on Windows:
// today's script for Valheim and the app, unchanged, then the games'.
func firewallAllScript(serverExe, appExe string, wake bool, games []GameRule) string {
	return firewallScript(serverExe, appExe, wake) + "\n" + gamesFirewallScript(serverExe, appExe, games)
}
