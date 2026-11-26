package platform

// The names on this computer that copies of the app already installed
// depend on (docs/dev/multigame/spec.md 1.2): the single-instance lock, the
// login item, the admin scripts and the firewall rules. A copy that comes
// back after a rollback, or the one that replaces it, finds them by these
// names, so they never change, even when the name people see does. Each is
// compared with its literal, on every system: renaming one fails here.

import (
	"strings"
	"testing"
)

func TestIdentity(t *testing.T) {
	for _, c := range []struct{ what, got, want string }{
		{"single-instance lock in the data folder", instanceLockFile, ".instance.lock"},
		{"single-instance lock names (Local\\…-<id> on Windows, …-<id>.lock in the temp folder)", instanceLockPrefix, "ValheimEasyServer-"},
		{"Windows Run value (start at sign-in)", runValue, "ValheimEasyServer"},
		{"macOS LaunchAgent label (start at login)", launchLabel, "com.thecrowbro.valheimeasyserver"},
		{"admin scripts in the temp folder", adminScripts, "ValheimEasyServer-admin-*.ps1"},
		{"firewall rules", fwRules, "Valheim Easy Server - "},
		{"the game's firewall rule (also its name from before rules carried a fingerprint)", fwGameRule, "Valheim Easy Server - game"},
		{"the wake signal's firewall rule (also its name from before rules carried a fingerprint)", fwWakeRule, "Valheim Easy Server - wake signal"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}

	// A rule's name carries the first 8 hex digits of the SHA-256 of the
	// program's path in lower case, so the rule made by any copy for the
	// same program is found again.
	for _, c := range []struct{ rule, program, want string }{
		{fwGameRule, `C:\Valheim Server\server\valheim_server.exe`, "Valheim Easy Server - game (d6ca26c5)"},
		{fwGameRule, `c:\valheim server\SERVER\valheim_server.exe`, "Valheim Easy Server - game (d6ca26c5)"},
		{fwWakeRule, `C:\Valheim Server\ValheimEasyServer.exe`, "Valheim Easy Server - wake signal (385b1b89)"},
	} {
		if got := fwRuleName(c.rule, c.program); got != c.want {
			t.Errorf("fwRuleName(%q, %q) = %q, want %q", c.rule, c.program, got, c.want)
		}
	}

	// What Allow runs as admin, which removes every rule an older copy made
	// (by the prefix) and adds this program's.
	got := firewallScript(`C:\Valheim Server\server\valheim_server.exe`, `C:\Valheim Server\ValheimEasyServer.exe`, true)
	want := `$server = 'C:\Valheim Server\server\valheim_server.exe'
$app = 'C:\Valheim Server\ValheimEasyServer.exe'
Remove-NetFirewallRule -DisplayName 'Valheim Easy Server - *' -ErrorAction SilentlyContinue
$fw = 'advfirewall', 'firewall'
netsh $fw delete rule name=all "program=$server"
netsh $fw add rule "name=Valheim Easy Server - game (d6ca26c5)" dir=in action=allow "program=$server"
netsh $fw delete rule name=all "program=$app"
netsh $fw add rule "name=Valheim Easy Server - wake signal (385b1b89)" dir=in action=allow "program=$app" protocol=UDP localport=9 remoteip=localsubnet`
	if got != want {
		t.Errorf("firewall script changed:\n%s\nwant:\n%s", got, want)
	}

	// The login item macOS keeps is named by the label.
	plist := launchAgentPlist(launchLabel, "/Applications/Valheim Easy Server.app/Contents/MacOS/ValheimEasyServer", []string{"--background"})
	if !strings.Contains(plist, "<key>Label</key>\n\t<string>com.thecrowbro.valheimeasyserver</string>") {
		t.Errorf("the login item isn't labelled com.thecrowbro.valheimeasyserver:\n%s", plist)
	}
}
