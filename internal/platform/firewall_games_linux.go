//go:build !windows && !darwin

package platform

// gameFirewallReady is always true outside Windows and macOS (nothing to
// set up here), as FirewallReady is.
func gameFirewallReady(GameRule) bool { return true }

// fixFirewallAll isn't done here, as FixFirewall isn't.
func fixFirewallAll(string, string, bool, []GameRule) error { return errNotHere }
