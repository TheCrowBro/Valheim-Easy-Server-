package platform

import (
	"strings"
	"testing"
)

const (
	goldenServer = `C:\Valheim Server\server\valheim_server.exe`
	goldenApp    = `C:\Valheim Server\ValheimEasyServer.exe`
	goldenJava   = `C:\Valheim Server\games\minecraft\runtime\bin\java.exe`
	goldenTerra  = `C:\Valheim Server\games\terraria\server\TerrariaServer.exe`
	goldenCore   = `C:\Valheim Server\games\corekeeper\server\CoreKeeperServer.exe`
)

// goldenValheim is today's firewallScript for the golden paths, byte for
// byte (as platform_identity_test.go pins it). FixFirewallAll must start
// with exactly this, so Valheim's fix never changes.
const goldenValheim = `$server = 'C:\Valheim Server\server\valheim_server.exe'
$app = 'C:\Valheim Server\ValheimEasyServer.exe'
Remove-NetFirewallRule -DisplayName 'Valheim Easy Server - *' -ErrorAction SilentlyContinue
$fw = 'advfirewall', 'firewall'
netsh $fw delete rule name=all "program=$server"
netsh $fw add rule "name=Valheim Easy Server - game (d6ca26c5)" dir=in action=allow "program=$server"
netsh $fw delete rule name=all "program=$app"
netsh $fw add rule "name=Valheim Easy Server - wake signal (385b1b89)" dir=in action=allow "program=$app" protocol=UDP localport=9 remoteip=localsubnet`

// What FixFirewallAll runs as admin on Windows: Valheim's script as it is,
// then the games' rules, named "Easy Game Server - <instance> [<protocol>]
// (<fingerprint>)". The names are what later copies look for, so they are
// pinned here, fingerprints included: a game with ports (one rule per
// protocol, scoped to the ports) and one without (AnyPort: one rule for
// the program on any port).
func TestFirewallScriptGolden(t *testing.T) {
	if got := firewallScript(goldenServer, goldenApp, true); got != goldenValheim {
		t.Fatalf("firewallScript changed:\n%s\nwant:\n%s", got, goldenValheim)
	}
	games := []GameRule{
		{Instance: "minecraft", Program: goldenJava, TCP: []int{25565}, UDP: []int{24454}},
		{Instance: "terraria", Program: goldenTerra, TCP: []int{7778, 7777, 7777}},
		{Instance: "corekeeper", Program: goldenCore, AnyPort: true},
	}
	got := firewallAllScript(goldenServer, goldenApp, true, games)
	want := goldenValheim + `
Remove-NetFirewallRule -DisplayName 'Easy Game Server - *' -ErrorAction SilentlyContinue
$g1 = 'C:\Valheim Server\games\minecraft\runtime\bin\java.exe'
netsh $fw delete rule name=all "program=$g1"
$g2 = 'C:\Valheim Server\games\terraria\server\TerrariaServer.exe'
netsh $fw delete rule name=all "program=$g2"
$g3 = 'C:\Valheim Server\games\corekeeper\server\CoreKeeperServer.exe'
netsh $fw delete rule name=all "program=$g3"
netsh $fw add rule "name=Easy Game Server - minecraft TCP (3e58b859)" dir=in action=allow "program=$g1" protocol=TCP localport=25565
netsh $fw add rule "name=Easy Game Server - minecraft UDP (5b5291c1)" dir=in action=allow "program=$g1" protocol=UDP localport=24454
netsh $fw add rule "name=Easy Game Server - terraria TCP (33b2b087)" dir=in action=allow "program=$g2" protocol=TCP localport=7777,7778
netsh $fw add rule "name=Easy Game Server - corekeeper (2e5527cc)" dir=in action=allow "program=$g3"`
	if got != want {
		t.Errorf("FixFirewallAll's script changed:\n%s\nwant:\n%s", got, want)
	}

	// Without games, the games' part only removes their old rules: a game
	// that was removed loses its rules at the next fix.
	if got := firewallAllScript(goldenServer, goldenApp, false, nil); got != firewallScript(goldenServer, goldenApp, false)+
		"\nRemove-NetFirewallRule -DisplayName 'Easy Game Server - *' -ErrorAction SilentlyContinue" {
		t.Errorf("with no games:\n%s", got)
	}
}

func TestGameRuleNames(t *testing.T) {
	r := GameRule{Instance: "minecraft", Program: goldenJava, TCP: []int{25565}}
	name := gameRules(r)[0].name
	if !strings.HasPrefix(name, fwGameRules+"minecraft TCP (") || strings.HasPrefix(name, fwRules) {
		t.Fatalf("rule name %q", name)
	}
	if strings.ContainsAny(name, `"'$`+"`") {
		t.Fatalf("the rule name %q would need quoting", name)
	}
	same := r
	same.Program = strings.ToUpper(goldenJava)
	if gameRules(same)[0].name != name {
		t.Error("Windows paths ignore case, the rule name doesn't")
	}
	moved := r
	moved.Program = `D:` + goldenJava[2:]
	if gameRules(moved)[0].name == name {
		t.Error("a rule for where the game used to be counts for where it is now")
	}
	newPort := r
	newPort.TCP = []int{25566}
	if gameRules(newPort)[0].name == name {
		t.Error("a rule for the game's old port counts for its new one")
	}
	if rules := gameRules(GameRule{Instance: "x", Program: goldenJava}); len(rules) != 0 {
		t.Errorf("a game with no ports got %v", rules)
	}
	both := GameRule{Instance: "x", Program: goldenJava, TCP: []int{1}, AnyPort: true}
	if rules := gameRules(both); len(rules) != 1 || rules[0].proto != "" {
		t.Errorf("AnyPort with ports: %+v", rules)
	}
}

// A game whose program is also Valheim's server or the app keeps the
// variable firewallScript gave it, and isn't cleaned up again (that would
// remove the allow rule just made); two games sharing a program share one
// clean-up, before any rule is added.
func TestGamesFirewallScriptSharedPrograms(t *testing.T) {
	got := gamesFirewallScript(goldenServer, goldenApp, []GameRule{
		{Instance: "a", Program: strings.ToLower(goldenServer), UDP: []int{2456}},
		{Instance: "b", Program: goldenJava, TCP: []int{25565}},
		{Instance: "c", Program: goldenJava, TCP: []int{25575}},
		{Instance: "d", Program: goldenCore}, // nothing to allow
	})
	want := `Remove-NetFirewallRule -DisplayName 'Easy Game Server - *' -ErrorAction SilentlyContinue
$g1 = 'C:\Valheim Server\games\minecraft\runtime\bin\java.exe'
netsh $fw delete rule name=all "program=$g1"
netsh $fw add rule "name=` + gameRules(GameRule{Instance: "a", Program: goldenServer, UDP: []int{2456}})[0].name + `" dir=in action=allow "program=$server" protocol=UDP localport=2456
netsh $fw add rule "name=` + gameRules(GameRule{Instance: "b", Program: goldenJava, TCP: []int{25565}})[0].name + `" dir=in action=allow "program=$g1" protocol=TCP localport=25565
netsh $fw add rule "name=` + gameRules(GameRule{Instance: "c", Program: goldenJava, TCP: []int{25575}})[0].name + `" dir=in action=allow "program=$g1" protocol=TCP localport=25575`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// With every game installed in a deep folder the script is too long for
// Windows' admin prompt command line, so it goes through the checked file.
func TestFirewallAllScriptLongPaths(t *testing.T) {
	deep := `C:\Users\Astrid Lothbrok PC\OneDrive - Contoso Corporation Limited\Documents\Games and Servers\Easy Game Server`
	var games []GameRule
	for _, id := range []string{"minecraft", "terraria", "necesse", "vintagestory", "zomboid", "enshrouded", "corekeeper", "palworld", "satisfactory", "windrose"} {
		games = append(games, GameRule{Instance: id, Program: deep + `\games\` + id + `\server\` + id + `.exe`, TCP: []int{27015}, UDP: []int{27015, 27016}})
	}
	script := firewallAllScript(deep+`\server\valheim_server.exe`, deep+`\ValheimEasyServer.exe`, true, games)
	var saved []byte
	args, err := psAdminCommand(testPowerShell, script, func(b []byte) (string, error) {
		saved = b
		return `C:\Users\Astrid Lothbrok PC\AppData\Local\Temp\ValheimEasyServer-admin-123456789.ps1`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != script {
		t.Fatal("the long script wasn't saved for the checked run")
	}
	if n := len(`"` + testPowerShell + `" ` + args); n >= shellExecuteMax {
		t.Fatalf("the command line is %d characters", n)
	}
}
