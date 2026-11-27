package manager

// Removing a game (spec 6.2, stream A): a job in that game's own slot
// ("remove") that stops it, backs each world up into its backups, removes
// its router mappings, deletes its server, runtime and logs, and drops it
// from servers.json and the stars. Its backups, players and records stay
// in games/<id>/ and show again if it is added back; its firewall rules go
// at the next fix. Removing the legacy game only hides it (I1): stopped,
// unstarred, no command, never started by anything, files untouched.
//
// Phase 0' made this file with its signature and an empty body; stream A
// fills it in.

// RemoveGame removes (or, for the legacy game, hides) a game and answers
// the job's id (POST /api/games/{id}/remove).
func (m *Manager) RemoveGame(id string) (int, error) {
	if _, err := m.instanceByID(id); err != nil {
		return 0, err
	}
	return 0, errNotYet
}
