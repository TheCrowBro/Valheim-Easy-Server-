package platform

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// absProgram is a full path to a program on this system, for rules that
// must pass checkGameRule.
func absProgram(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\Easy Game Server test\` + name + ".exe"
	}
	return "/opt/easy game server test/" + name
}

// A rule that would break out of its quotes, or names no real program or
// port, is refused before anything is run, on every system.
func TestGameRuleChecks(t *testing.T) {
	good := GameRule{Instance: "minecraft-2", Program: absProgram("java"), TCP: []int{25565}, UDP: []int{1, 65535}}
	if err := checkGameRule(good); err != nil {
		t.Fatalf("a good rule: %v", err)
	}
	for name, bad := range map[string]GameRule{
		"no id":           {Program: absProgram("java"), TCP: []int{1}},
		"quote in the id": {Instance: `x" dir=out`, Program: absProgram("java"), TCP: []int{1}},
		"space in the id": {Instance: "mine craft", Program: absProgram("java"), TCP: []int{1}},
		"capitals":        {Instance: "Minecraft", Program: absProgram("java"), TCP: []int{1}},
		"no program":      {Instance: "x", TCP: []int{1}},
		"relative":        {Instance: "x", Program: filepath.Join("server", "java"), TCP: []int{1}},
		"port 0":          {Instance: "x", Program: absProgram("java"), TCP: []int{0}},
		"port too big":    {Instance: "x", Program: absProgram("java"), UDP: []int{65536}},
	} {
		if checkGameRule(bad) == nil {
			t.Errorf("%s: accepted", name)
		}
		if GameFirewallReady(bad) {
			t.Errorf("%s: ready", name)
		}
		if err := FixFirewallAll(absProgram("valheim_server"), absProgram("app"), false, []GameRule{good, bad}); err == nil || errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: FixFirewallAll gave %v", name, err)
		}
	}
	// A game that needs no rule (no public ports, not AnyPort) is ready.
	if !GameFirewallReady(GameRule{Instance: "x", Program: absProgram("x")}) {
		t.Error("a game needing no rule isn't ready")
	}
}

// There is nothing to set up on Linux, as with Valheim's rule.
func TestGameFirewallLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	r := GameRule{Instance: "minecraft", Program: absProgram("java"), TCP: []int{25565}}
	if !GameFirewallReady(r) {
		t.Error("not ready on Linux")
	}
	err := FixFirewallAll(absProgram("valheim_server"), absProgram("app"), true, []GameRule{r})
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("FixFirewallAll = %v", err)
	}
	if err.Error() != ErrUnsupported.Error() {
		t.Errorf("says %q", err)
	}
}

// PowerShell reads a bare comma in a command's arguments as a list, so
// netsh would get "localport=7777" and "7778" as two arguments and make no
// rule. Every comma on a netsh line must sit inside quotes.
func TestNetshLinesQuoteCommas(t *testing.T) {
	script := firewallAllScript(goldenServer, goldenApp, true, []GameRule{
		{Instance: "satisfactory", Program: goldenJava, TCP: []int{8888, 7777}, UDP: []int{7777, 15000, 15777}},
		{Instance: "corekeeper", Program: goldenCore, AnyPort: true},
	})
	lines := 0
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, "netsh ") {
			continue
		}
		lines++
		quoted := false
		for _, c := range line {
			switch {
			case c == '"':
				quoted = !quoted
			case c == ',' && !quoted:
				t.Errorf("a bare comma in %s", line)
			}
		}
	}
	if !strings.Contains(script, `"localport=7777,8888"`) || !strings.Contains(script, `"localport=7777,15000,15777"`) || lines != 9 {
		t.Errorf("not the script expected:\n%s", script)
	}
}

// loggedAdminScript wraps an admin script so that, while it runs hidden
// as admin, it leaves a record a test can read in the file log: a first
// line saying it started (with PowerShell's version and whether it really
// has admin rights), everything the script printed (netsh says "Ok." or
// why it refused a line), every error PowerShell recorded, even those the
// script keeps quiet with -ErrorAction SilentlyContinue, and a last line
// saying it finished. The script runs inside unchanged. Only tests use
// it, through testWrapFirewallAll.
func loggedAdminScript(script, log string) string {
	out := " | Out-File -LiteralPath $egsLog -Encoding utf8 -Append -Width 500"
	return strings.Join([]string{
		"$egsLog = " + psQuote(log),
		`"started: PowerShell $($PSVersionTable.PSVersion), admin $(([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))"` + out,
		"try {",
		"& {",
		script,
		"} *>&1" + out,
		"} finally {",
		"$Error" + out,
		"'finished'" + out,
		"}",
	}, "\n")
}

// The wrapper keeps the script whole and writes to the file it was given,
// even when the file's path has spaces and a quote.
func TestLoggedAdminScript(t *testing.T) {
	script := firewallAllScript(goldenServer, goldenApp, true, nil)
	got := loggedAdminScript(script, `C:\Users\Ann O'Neil\AppData\Local\Temp\fw test\script.log`)
	if !strings.HasPrefix(got, `$egsLog = 'C:\Users\Ann O''Neil\AppData\Local\Temp\fw test\script.log'`+"\n") {
		t.Errorf("the log's path isn't set first:\n%s", got)
	}
	if !strings.Contains(got, "\n& {\n"+script+"\n} *>&1 | Out-File -LiteralPath $egsLog ") {
		t.Errorf("the script isn't inside whole:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n'finished' | Out-File -LiteralPath $egsLog -Encoding utf8 -Append -Width 500\n}") {
		t.Errorf("it doesn't end by saying it finished:\n%s", got)
	}
	if strings.Count(got, "{") != strings.Count(got, "}") {
		t.Errorf("unbalanced braces:\n%s", got)
	}
}

// For Valheim's server and the app the macOS command is exactly the one
// FixFirewall runs; the games' programs follow the same way.
func TestMacFirewallCommand(t *testing.T) {
	const tool = "/usr/libexec/ApplicationFirewall/socketfilterfw"
	server := "/Users/me/Library/Application Support/ValheimEasyServer/server/valheim_server.x86_64"
	app := "/Applications/Valheim Easy Server.app"
	// As desktop_darwin.go's FixFirewall builds it.
	var today []string
	for _, p := range []string{server, app} {
		today = append(today, tool+" --add "+shellQuote(p), tool+" --unblockapp "+shellQuote(p))
	}
	if got := macFirewallCommand(tool, []string{server, app}); got != strings.Join(today, "; ") {
		t.Errorf("Valheim's part changed:\n%s", got)
	}
	java := "/Users/me/Library/Application Support/ValheimEasyServer/games/minecraft/runtime/jdk/Contents/Home/bin/java"
	want := strings.Join(today, "; ") + "; " + tool + " --add '" + java + "'; " + tool + " --unblockapp '" + java + "'"
	if got := macFirewallCommand(tool, []string{server, app, java}); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
