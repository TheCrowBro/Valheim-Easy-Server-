//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OpenAppWindow opens the control panel in the default browser.
func OpenAppWindow(url string) error { return OpenURL(url) }

// launchLabel names the app's login item for launchd.
const launchLabel = "com.thecrowbro.valheimeasyserver"

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
}

// LaunchOnLogin reports whether the app starts when the user logs in, from
// where it is now (it may have been moved since).
func LaunchOnLogin() (bool, error) {
	p, err := launchAgentPath()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return strings.Contains(string(b), "<string>"+xmlEscape(exe)+"</string>"), nil
}

// SetLaunchOnLogin adds the app to what macOS starts when the user logs in,
// or takes it off.
func SetLaunchOnLogin(on bool, exe, args string) error {
	p, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(launchAgentPlist(launchLabel, exe, strings.Fields(args))), 0o644)
}

const socketfilterfw = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// firewallOn reports whether the macOS firewall is on. It's off unless
// someone turned it on in System Settings.
func firewallOn() bool {
	out, err := exec.Command(socketfilterfw, "--getglobalstate").Output()
	return err == nil && parseFirewallOn(string(out))
}

func firewallAllows(path string) bool {
	out, err := exec.Command(socketfilterfw, "--listapps").Output()
	if err != nil {
		return false
	}
	listed, allowed := parseFirewallApp(string(out), path)
	return listed && allowed
}

// appPath is how the firewall knows an app: by its .app bundle, when it's
// in one.
func appPath(exe string) string {
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		return exe[:i+len(".app")]
	}
	return exe
}

// FirewallReady reports whether friends can reach the server through the
// macOS firewall.
func FirewallReady(serverExe string) bool { return !firewallOn() || firewallAllows(serverExe) }

// WakeRuleReady reports whether the app may hear Wake-on-LAN signals
// through the macOS firewall.
func WakeRuleReady(appExe string) bool { return !firewallOn() || firewallAllows(appPath(appExe)) }

// FixFirewall lets the server (and, with wake on, the app) through the
// macOS firewall. macOS asks for an administrator's password on the
// screen; this returns before that is answered.
func FixFirewall(serverExe, appExe string, wake bool) error {
	if !firewallOn() {
		return nil
	}
	programs := []string{serverExe}
	if wake {
		programs = append(programs, appPath(appExe))
	}
	var cmds []string
	for _, p := range programs {
		cmds = append(cmds, socketfilterfw+" --add "+shellQuote(p), socketfilterfw+" --unblockapp "+shellQuote(p))
	}
	return runAdmin(strings.Join(cmds, "; "), "Valheim Easy Server wants to let friends through the macOS firewall.")
}

// runAdmin runs a shell command as an administrator: macOS asks for the
// password on the screen. It returns without waiting for the answer.
func runAdmin(command, prompt string) error {
	script := "do shell script " + appleScriptString(command) + " with administrator privileges with prompt " + appleScriptString(prompt)
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// ShowError shows a message on the screen; the app has no window of its own.
func ShowError(title, msg string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
	exec.Command("/usr/bin/osascript", "-e", "on run argv",
		"-e", "display alert (item 1 of argv) message (item 2 of argv) as critical",
		"-e", "end run", title, msg).Run()
}

// SteamPath returns where the Steam client keeps its data, or "".
func SteamPath() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "Library", "Application Support", "Steam")
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p
	}
	return ""
}

// Confirm has no dialog of its own here (it's only used by the Windows
// tray menu); it always says yes.
func Confirm(title, msg string) bool { return true }
