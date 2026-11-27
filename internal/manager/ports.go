package manager

// Ports as role specs (spec 2.5, stream A): an instance's ports are picked
// once, when the game is added, from each role's default up to default+100,
// avoiding every port another instance reserves (servers.json, and the
// legacy game's settings.json port and port+1) and every port another
// program holds. They never move after that: a clash at start fails with
// ActionError{Action: "pick_ports"}, and the owner's "Pick free ports"
// picks new ones for the roles the owner didn't choose. The legacy game
// keeps its own rule (its port from settings.json, checked inside its
// Plan).
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in.

import (
	"valheim-easy-server/internal/game"
)

// pickPortsLocked picks a port for every role of info, keeping the ones in
// keep (roles the owner chose). Tied roles ("game+1") follow their base.
func (m *Manager) pickPortsLocked(info game.Info, keep map[string]int) (map[string]int, error) {
	return nil, errNotYet
}

// reservedPortsLocked is every port an instance reserves, with the
// instance that does.
func (m *Manager) reservedPortsLocked() map[int]string {
	return nil
}

// checkPorts is the start's port check for a non-legacy instance: nil, or
// an *ActionError with Action "pick_ports" ("Port 7777 (TCP) is being used
// by another program: <name>"; for a chosen port, "change it in
// Settings"). M3's doStart calls it inside the start job, after Plan and
// before the launch (spec 4.3), never for the legacy instance. It runs
// without m.mu: it reads the system's port tables.
func (m *Manager) checkPorts(inst *instance) error {
	return nil
}

// checkChosenPortsLocked checks ports the owner typed on the Settings tab,
// by role; the errors are keyed by role.
func (m *Manager) checkChosenPortsLocked(inst *instance, ports map[string]int) game.FieldErrors {
	return nil
}

// PickPorts picks new free ports for the roles the owner didn't choose
// (POST /api/games/{id}/ports/pick), then updates the firewall rules and
// router mappings. It answers the new ports by role.
func (m *Manager) PickPorts(id string) (map[string]int, error) {
	if _, err := m.instanceByID(id); err != nil {
		return nil, err
	}
	return nil, errNotYet
}
