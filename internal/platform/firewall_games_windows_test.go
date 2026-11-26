//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestFixFirewallAllReal runs FixFirewallAll for real, with a fake
// Valheim, app and two fake games, and reads the rules back from Windows
// Firewall. It needs admin rights without a prompt (the CI runner, which
// sets EGS_CI_ADMIN=1), and it changes this computer's firewall: like the
// app's own fix, it first removes every "Valheim Easy Server - *" and
// "Easy Game Server - *" rule. Never run it on the owner's computer.
func TestFixFirewallAllReal(t *testing.T) {
	if os.Getenv("EGS_CI_ADMIN") != "1" {
		t.Skip("set EGS_CI_ADMIN=1 on a CI runner with admin rights (this replaces the app's firewall rules)")
	}
	dir := filepath.Join(t.TempDir(), "Easy Game Server CI")
	server := filepath.Join(dir, "server", "valheim_server.exe")
	app := filepath.Join(dir, "ValheimEasyServer.exe")
	games := []GameRule{
		{Instance: "egsci", Program: filepath.Join(dir, "games", "egsci", "server.exe"), TCP: []int{27015}, UDP: []int{27016, 27015}},
		{Instance: "egsci-any", Program: filepath.Join(dir, "games", "egsci-any", "server.exe"), AnyPort: true},
	}
	// What Windows should have afterwards: rule name → "program|protocol|ports".
	want := map[string]string{
		fwRuleName(fwGameRule, server): strings.ToLower(server) + "|Any|Any",
		fwRuleName(fwWakeRule, app):    strings.ToLower(app) + "|UDP|9",
	}
	for _, g := range games {
		for _, r := range gameRules(g) {
			proto, ports := r.proto, r.ports
			if proto == "" {
				proto, ports = "Any", "Any"
			}
			want[r.name] = strings.ToLower(g.Program) + "|" + proto + "|" + ports
		}
	}
	var names []string
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Cleanup(func() {
		var quoted []string
		for _, n := range names {
			quoted = append(quoted, psQuote(n))
		}
		powershell(t, "Remove-NetFirewallRule -DisplayName "+strings.Join(quoted, ",")+" -ErrorAction SilentlyContinue")
	})

	for _, g := range games {
		if GameFirewallReady(g) {
			t.Fatalf("%s is ready before the fix", g.Instance)
		}
	}
	if err := FixFirewallAll(server, app, true, games); err != nil {
		t.Fatal(err)
	}
	// FixFirewallAll returns once the elevated PowerShell has started, as
	// FixFirewall does, so the rules appear a moment later.
	deadline := time.Now().Add(90 * time.Second)
	var got map[string]string
	for {
		got = firewallRules(t, names)
		if len(got) == len(want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	for _, name := range names {
		if got[name] != want[name] {
			t.Errorf("rule %q: got %q, want %q", name, got[name], want[name])
		}
	}
	for _, g := range games {
		if !GameFirewallReady(g) {
			t.Errorf("%s isn't ready after the fix", g.Instance)
		}
	}
	if !FirewallReady(server) || !WakeRuleReady(app) {
		t.Error("Valheim's rules aren't ready after the fix")
	}
	moved := games[0]
	moved.TCP = []int{27020}
	if GameFirewallReady(moved) {
		t.Error("a rule for the game's old port counts for its new one")
	}
}

// firewallRules reads the named rules back as "program|protocol|ports".
func firewallRules(t *testing.T, names []string) map[string]string {
	t.Helper()
	var quoted []string
	for _, n := range names {
		quoted = append(quoted, psQuote(n))
	}
	out := powershell(t, "Get-NetFirewallRule -DisplayName "+strings.Join(quoted, ",")+" -ErrorAction SilentlyContinue | ForEach-Object {"+
		" $a = $_ | Get-NetFirewallApplicationFilter; $p = $_ | Get-NetFirewallPortFilter;"+
		" '{0}|{1}|{2}|{3}' -f $_.DisplayName, $a.Program, $p.Protocol, ($p.LocalPort -join ',') }")
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) == 4 {
			got[parts[0]] = strings.ToLower(parts[1]) + "|" + parts[2] + "|" + parts[3]
		}
	}
	return got
}

func powershell(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command(system32(`WindowsPowerShell\v1.0\powershell.exe`), "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("powershell: %v\n%s", err, out)
	}
	return string(out)
}
