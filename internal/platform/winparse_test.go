package platform

import (
	"strings"
	"testing"
)

// A firewall rule only counts for the program it was made for: after the
// folder moves, the check finds no rule and the Allow button comes back.
func TestFirewallRuleFollowsThePath(t *testing.T) {
	server := `C:\Valheim Server\server\valheim_server.exe`
	moved := `D:\Valheim Server\server\valheim_server.exe`
	name := fwRuleName(fwGameRule, server)
	if !strings.HasPrefix(name, fwGameRule+" (") || name == fwGameRule {
		t.Fatalf("rule name %q", name)
	}
	if fwRuleName(fwGameRule, moved) == name {
		t.Fatal("a rule for the old place counts for the new one")
	}
	if fwRuleName(fwGameRule, strings.ToUpper(server)) != name {
		t.Fatal("Windows paths ignore case, the rule name doesn't")
	}
	if strings.ContainsAny(name, `"'$`+"`") {
		t.Fatalf("the rule name %q would need quoting", name)
	}

	// FixFirewall makes exactly the rules FirewallReady and WakeRuleReady
	// look for, and clears out rules for old places first.
	app := `C:\Valheim Server\ValheimEasyServer.exe`
	script := firewallScript(server, app, true)
	for _, want := range []string{
		`"name=` + name + `"`,
		`"name=` + fwRuleName(fwWakeRule, app) + `"`,
		"Remove-NetFirewallRule -DisplayName '" + fwRules + "*'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script has no %s:\n%s", want, script)
		}
	}
	for _, rule := range []string{fwGameRule, fwWakeRule} {
		if !strings.HasPrefix(rule, fwRules) {
			t.Errorf("rules for old places named %q aren't removed", rule)
		}
	}
	removed := strings.Index(script, "Remove-NetFirewallRule")
	added := strings.Index(script, "add rule")
	if removed < 0 || added < removed {
		t.Error("old rules are removed after the new ones are added")
	}
	if strings.Contains(firewallScript(server, app, false), fwWakeRule+" (") {
		t.Error("a wake rule without wake on")
	}
}

// Windows only takes a command line of up to shellExecuteMax characters
// when it asks for admin permission, so Allow must still work when the app
// is somewhere with a long path and Discord wake is on.
func TestFirewallScriptFitsLongPaths(t *testing.T) {
	powershell := `"C:\WINDOWS\system32\WindowsPowerShell\v1.0\powershell.exe" `
	user := `C:\Users\Astrid Lothbrok PC` // a long user name: Windows allows 20 characters
	for _, c := range []struct{ where, server, app string }{
		{"the recommended folder",
			`C:\Valheim Server\server\valheim_server.exe`,
			`C:\Valheim Server\ValheimEasyServer.exe`},
		{"Downloads",
			user + `\Downloads\Valheim Easy Server\server\valheim_server.exe`,
			user + `\Downloads\ValheimEasyServer.exe`},
		{"unzipped on a OneDrive desktop",
			user + `\OneDrive - Personal\Desktop\ValheimEasyServer-windows\server\valheim_server.exe`,
			user + `\OneDrive - Personal\Desktop\ValheimEasyServer-windows\ValheimEasyServer.exe`},
		{"run from inside the zip",
			user + `\AppData\Roaming\Valheim Easy Server\server\valheim_server.exe`,
			user + `\AppData\Local\Temp\Temp1_ValheimEasyServer-windows.zip\ValheimEasyServer.exe`},
	} {
		n := len(powershell + psAdminArgs(firewallScript(c.server, c.app, true)))
		t.Logf("%s: %d of %d characters", c.where, n, shellExecuteMax)
		if n >= shellExecuteMax {
			t.Errorf("%s: the command line is %d characters, over Windows' limit of %d", c.where, n, shellExecuteMax)
		}
	}
}

func TestStartupEntryRuns(t *testing.T) {
	exe := `C:\Valheim Server\ValheimEasyServer.exe`
	entry := `"C:\Valheim Server\ValheimEasyServer.exe" --background`
	for _, c := range []struct {
		name     string
		command  string
		approved []byte
		want     bool
	}{
		{"as written", entry, nil, true},
		{"other case", `"c:\valheim server\valheimeasyserver.exe" --background`, nil, true},
		{"another copy, not quoted", `C:\Games\ValheimEasyServer.exe --background`, nil, false},
		{"moved since", `"C:\Users\me\Downloads\ValheimEasyServer.exe" --background`, nil, false},
		{"switched on in Task Manager", entry, []byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, true},
		{"switched off in Task Manager", entry, []byte{3, 0, 0, 0, 0x10, 0x20, 0, 0, 0, 0, 0, 0}, false},
		{"switched off in Settings", entry, []byte{7, 0, 0, 0}, false},
		{"empty mark", entry, []byte{}, true},
	} {
		if got := startupEntryRuns(c.command, c.approved, exe); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if !startupEntryRuns(`C:\Games\ValheimEasyServer.exe --background`, nil, `C:\Games\ValheimEasyServer.exe`) {
		t.Error("an entry without quotes isn't read")
	}
	if startupEntryRuns(entry, nil, "") {
		t.Error("runs with no program to compare with")
	}
}
