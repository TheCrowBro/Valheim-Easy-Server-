package manager

// What the manager shares with copies of the app from before and after it
// (docs/dev/multigame/spec.md 1.2): the salt behind the password hash in
// lastplayed.json. A copy that comes back after a rollback, or the one
// that replaces it, compares its own hash with the one in the file, so the
// salt never changes. It's compared with its literal: changing it fails
// here.

import "testing"

func TestIdentity(t *testing.T) {
	if reminderSalt != "valheim-easy-server:" {
		t.Errorf("the reminder salt = %q, want valheim-easy-server:", reminderSalt)
	}
	// The hashes today's app writes into lastplayed.json.
	for pw, want := range map[string]string{
		"raven-fjord-4821": "c876de2de4744f10",
		"frost":            "735cd0aaf3b09b1b",
	} {
		if got := hashPassword(pw); got != want {
			t.Errorf("hashPassword(%q) = %q, want %q", pw, got, want)
		}
	}
}
