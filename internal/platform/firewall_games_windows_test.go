//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestFixFirewallAllReal runs FixFirewallAll for real, with a fake
// Valheim, app and two fake games, and reads the rules back from Windows
// Firewall. It needs admin rights without a prompt (the CI runner, which
// sets EGS_CI_ADMIN=1), and it changes this computer's firewall: like the
// app's own fix, it first removes every "Valheim Easy Server - *" and
// "Easy Game Server - *" rule. Never run it on the owner's computer.
//
// The script runs hidden, as admin, where the test can't see it, so the
// test has it write what it printed and PowerShell's errors to a log
// (testWrapFirewallAll), and shows that log, the script and the state of
// Windows Firewall when a rule is missing.
func TestFixFirewallAllReal(t *testing.T) {
	needCIAdmin(t, `FixFirewallAll makes Valheim's and the games' firewall rules for real (it first removes every "Valheim Easy Server - *" and "Easy Game Server - *" rule)`)
	// The folders by their long names, as the app's own are: on the CI
	// runner the temp folder is under C:\Users\RUNNER~1, an 8.3 short name.
	base := longPath(t, t.TempDir())
	dir := filepath.Join(base, "Easy Game Server CI")
	server := filepath.Join(dir, "server", "valheim_server.exe")
	app := filepath.Join(dir, "ValheimEasyServer.exe")
	games := []GameRule{
		{Instance: "egsci", Program: filepath.Join(dir, "games", "egsci", "server.exe"), TCP: []int{27015}, UDP: []int{27016, 27015}},
		{Instance: "egsci-any", Program: filepath.Join(dir, "games", "egsci-any", "server.exe"), AnyPort: true},
	}
	// Real files, as the app and an installed game always are when the app
	// fixes the firewall.
	for _, p := range []string{server, app, games[0].Program, games[1].Program} {
		writeFakeProgram(t, p)
	}
	// Two more rules only report what Windows does with them, for later
	// decisions: one for a program that isn't there (a game added but not
	// installed yet), one naming its program by an 8.3 short path. Neither
	// fails the test.
	type probe struct {
		about string
		rule  GameRule
	}
	probes := []probe{{"a program that isn't there", GameRule{Instance: "egsci-missing", Program: filepath.Join(dir, "games", "egsci-missing", "server.exe"), AnyPort: true}}}
	shortProgram := filepath.Join(dir, "games", "egsci-short", "server.exe")
	writeFakeProgram(t, shortProgram)
	if short, err := convertPath(shortProgram, windows.GetShortPathName); err == nil && !strings.EqualFold(short, shortProgram) {
		probes = append(probes, probe{"a program named by its 8.3 short path (" + short + ")", GameRule{Instance: "egsci-short", Program: short, AnyPort: true}})
	} else {
		t.Logf("no 8.3 short path to try for %s (%v)", shortProgram, err)
	}
	all := append([]GameRule(nil), games...)
	for _, p := range probes {
		all = append(all, p.rule)
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
		for _, p := range probes {
			quoted = append(quoted, psQuote(gameRules(p.rule)[0].name))
		}
		// Rules that were never made aren't found; that's fine here.
		powershell(t, "Remove-NetFirewallRule -DisplayName "+strings.Join(quoted, ",")+" -ErrorAction SilentlyContinue; exit 0")
	})

	for _, g := range games {
		if GameFirewallReady(g) {
			t.Fatalf("%s is ready before the fix", g.Instance)
		}
	}
	logFile := filepath.Join(base, "admin script.log")
	testWrapFirewallAll = func(script string) string { return loggedAdminScript(script, logFile) }
	t.Cleanup(func() { testWrapFirewallAll = nil })
	if err := FixFirewallAll(server, app, true, all); err != nil {
		t.Fatal(err)
	}
	// FixFirewallAll returns once the elevated PowerShell has been asked
	// for, as FixFirewall does; the script says in its log when it's done.
	deadline := time.Now().Add(90 * time.Second)
	for !strings.Contains(readAdminLog(logFile), "\nfinished") && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	got, readErrs := firewallRules(t, names)
	for _, e := range readErrs {
		t.Errorf("reading the rules back: %s", e)
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
	// The log also has netsh's own words for a probe's rule it refused.
	showLog := t.Failed()
	for _, p := range probes {
		made := GameFirewallReady(p.rule)
		t.Logf("for %s, netsh made the rule: %v", p.about, made)
		showLog = showLog || !made
	}

	if showLog {
		if log := readAdminLog(logFile); log == "" {
			t.Log("the admin script left no log within 90 s: the elevated PowerShell never ran it, or stopped before its first line (TestRunElevatedPowerShellReal and TestAdminCommandWindowsPowerShell say which)")
		} else {
			t.Logf("what the admin script printed, then the errors PowerShell recorded (newest first):\n%s", log)
		}
		t.Logf("the admin script (run inside loggedAdminScript):\n%s", firewallAllScript(server, app, true, all))
	}
	if t.Failed() {
		t.Logf("Windows Firewall:\n%s", powershell(t, "Get-Service MpsSvc, BFE | ForEach-Object { '{0} service: {1}' -f $_.Name, $_.Status };"+
			" Get-NetFirewallProfile | ForEach-Object { '{0} profile on: {1}' -f $_.Name, $_.Enabled }"))
	}
}

// readAdminLog gives what loggedAdminScript wrote so far ("" before it
// starts). Windows PowerShell 5.1 writes UTF-8 with a byte order mark.
func readAdminLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(b), "\ufeff", ""), "\r\n", "\n")
}

// writeFakeProgram puts a copy of cmd.exe at path, so a rule's program is
// a real program, as the app's and an installed game's always are.
func writeFakeProgram(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(system32("cmd.exe"))
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, b, 0o755)
	}
	if err != nil {
		t.Fatalf("couldn't make the fake program %s: %v", path, err)
	}
}

// longPath gives p by its long name: no 8.3 short names in it.
func longPath(t *testing.T, p string) string {
	t.Helper()
	long, err := convertPath(p, windows.GetLongPathName)
	if err != nil {
		t.Fatalf("the long name of %s: %v", p, err)
	}
	return long
}

// convertPath runs GetLongPathName or GetShortPathName on p, which must
// exist.
func convertPath(p string, conv func(from, to *uint16, n uint32) (uint32, error)) (string, error) {
	from, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := conv(from, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) >= len(buf) {
		return "", fmt.Errorf("Windows gave a length of %d", n)
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// needCIAdmin skips a test that runs something as admin for real, unless
// EGS_CI_ADMIN=1 says this computer gives admin rights without a prompt.
// Only the Windows CI runner should set it, and only on the step that
// runs this package's tests with -v, so the "All tests" step doesn't run
// these twice. why says what the test does, for the skip message.
//
// On GitHub Actions the skip also puts a warning on the run's summary
// page. Without it a run that never checked the real firewall looks just
// as green as one that did, and spec 9.3 accepts this package's additions
// only once TestFixFirewallAllReal has passed on the Windows runner.
func needCIAdmin(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("EGS_CI_ADMIN") == "1" {
		return
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		// "::warning ...::" is a GitHub Actions command, read from the
		// start of a line of the step's output; t.Log indents its lines,
		// so this prints directly. go test shows it only with -v (or when
		// the package fails), so the "All tests" step stays quiet.
		fmt.Printf("\n::warning title=%s did not run::EGS_CI_ADMIN is not 1 on this step, so this run did not check that %s\n", t.Name(), why)
	}
	t.Skipf("set EGS_CI_ADMIN=1 only on a computer where admin rights need no prompt (the CI runner): %s", why)
}

// firewallRules reads the named rules back as "program|protocol|ports".
// A name that isn't found is simply missing from the map. Any other error
// (Windows Firewall not answering, say) comes back in errs, so the test
// says so rather than only reporting the rules as missing.
func firewallRules(t *testing.T, names []string) (rules map[string]string, errs []string) {
	t.Helper()
	var quoted []string
	for _, n := range names {
		quoted = append(quoted, psQuote(n))
	}
	out := powershell(t, "Get-NetFirewallRule -DisplayName "+strings.Join(quoted, ",")+" -ErrorAction SilentlyContinue -ErrorVariable readErrors | ForEach-Object {"+
		" $a = $_ | Get-NetFirewallApplicationFilter; $p = $_ | Get-NetFirewallPortFilter;"+
		" 'RULE|{0}|{1}|{2}|{3}' -f $_.DisplayName, $a.Program, $p.Protocol, ($p.LocalPort -join ',') };"+
		" $readErrors | Where-Object { $_.CategoryInfo.Category -ne 'ObjectNotFound' } | ForEach-Object { 'ERROR|' + $_ }; exit 0")
	rules = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if e, ok := strings.CutPrefix(line, "ERROR|"); ok {
			errs = append(errs, e)
		} else if parts := strings.Split(line, "|"); len(parts) == 5 && parts[0] == "RULE" {
			rules[parts[1]] = strings.ToLower(parts[2]) + "|" + parts[3] + "|" + parts[4]
		}
	}
	return rules, errs
}

// powershell runs a script in Windows PowerShell, as the app's admin
// scripts are, and gives what it printed.
func powershell(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command(system32(`WindowsPowerShell\v1.0\powershell.exe`), "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("powershell: %v\n%s", err, out)
	}
	return string(out)
}
