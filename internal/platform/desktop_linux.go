//go:build !windows && !darwin

package platform

import (
	"fmt"
	"os"
)

// OpenAppWindow opens the control panel in the default browser.
func OpenAppWindow(url string) error { return OpenURL(url) }

// LaunchOnLogin is only implemented on Windows.
func LaunchOnLogin() (bool, error) { return false, nil }

// SetLaunchOnLogin is only implemented on Windows.
func SetLaunchOnLogin(bool, string, string) error { return ErrUnsupported }

// FirewallReady is always true outside Windows (nothing to set up here).
func FirewallReady(string) bool { return true }

// WakeRuleReady is always true outside Windows.
func WakeRuleReady(string) bool { return true }

// FixFirewall is only implemented on Windows.
func FixFirewall(string, string, bool) error { return ErrUnsupported }

// ShowError prints the message (there is no message box here).
func ShowError(title, msg string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg) }

// SteamPath returns where the Steam client keeps its data, or "".
func SteamPath() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{home + "/.steam/steam", home + "/.local/share/Steam"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// Confirm has no dialog outside Windows; it always says yes.
func Confirm(title, msg string) bool { return true }
