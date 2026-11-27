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
// Ports never move by themselves because friends type them in, and the
// firewall rules and router mappings name them: a port that moved silently
// would leave friends knocking on the old one.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// How the app asks the system about ports and the programs holding them.
// Replaced in tests, so they never depend on what else runs on the
// computer.
var (
	portInUse = platform.PortInUse
	procName  = platform.ProcName
)

// portSearch is how far above its default a role's port is looked for.
const portSearch = 100

// pickPortsLocked picks a port for every role of info, keeping the ones in
// keep (roles the owner chose). Tied roles ("game+1") follow their base.
// It asks the system's port tables for each candidate with m.mu held,
// which is quick on Windows and Linux but runs lsof on a Mac; the app's
// own callers (Add a game, Pick free ports) use pickPorts instead, which
// asks without the lock.
func (m *Manager) pickPortsLocked(info game.Info, keep map[string]int) (map[string]int, error) {
	return pickPortsWith(info, keep, m.reservedPortsLocked(), "", portInUse)
}

// pickPorts picks ports for the instance self ("" for a game being added)
// without holding m.mu while the system is asked, then checks that nobody
// reserved one of them meanwhile. life.portsMu keeps two picks from
// choosing the same free port at the same moment; the caller saves the
// result before it lets go of unlock.
func (m *Manager) pickPorts(self string, info game.Info, keep map[string]int) (ports map[string]int, unlock func(), err error) {
	m.life.portsMu.Lock()
	m.mu.Lock()
	reserved := m.reservedPortsLocked()
	m.mu.Unlock()
	ports, err = pickPortsWith(info, keep, reserved, self, portInUse)
	if err != nil {
		m.life.portsMu.Unlock()
		return nil, nil, err
	}
	return ports, m.life.portsMu.Unlock, nil
}

// pickPortsWith is the picking itself: for each role without a tie, the
// first port from its default up that no other instance reserves, no
// other program holds on any of the role's protocols, and that leaves the
// ports of the roles tied to it free too. reserved maps a port to the
// instance that has it; self's own reservations don't count, so picking
// again for an instance can give it the ports it had.
func pickPortsWith(info game.Info, keep map[string]int, reserved map[int]string, self string, inUse func(proto string, port int) (bool, int)) (map[string]int, error) {
	out := map[string]int{}
	taken := map[int]bool{} // given to a role in this pick
	for _, r := range info.Ports {
		if p := keep[r.Role]; p > 0 {
			out[r.Role] = p
			taken[p] = true
		}
	}
	free := func(port int, protos []string) bool {
		if port <= 0 || port > 65535 || taken[port] {
			return false
		}
		if owner := reserved[port]; owner != "" && owner != self {
			return false
		}
		for _, proto := range protos {
			if busy, _ := inUse(proto, port); busy {
				return false
			}
		}
		return true
	}
	for _, r := range info.Ports {
		if r.Tie != "" || out[r.Role] != 0 || r.Default <= 0 {
			continue
		}
		ties := tiedTo(info, r.Role, keep)
		found := 0
		for p := r.Default; p <= r.Default+portSearch && found == 0; p++ {
			if !free(p, r.Protos) {
				continue
			}
			ok := true
			for _, t := range ties {
				ok = ok && free(p+t.offset, t.role.Protos)
			}
			if ok {
				found = p
			}
		}
		if found == 0 {
			return nil, fmt.Errorf("No free port was found for %s's %s (%d to %d): other programs or games use them all. Choose one in its Settings.",
				info.Short, strings.ToLower(r.Label), r.Default, r.Default+portSearch)
		}
		out[r.Role], taken[found] = found, true
		for _, t := range ties {
			out[t.role.Role], taken[found+t.offset] = found+t.offset, true
		}
	}
	return withTiedPorts(info, out), nil
}

// tie is a role whose port is always another role's plus offset.
type tie struct {
	role   game.PortRole
	offset int
}

// parseTie reads a PortRole.Tie such as "game+1".
func parseTie(s string) (base string, offset int, ok bool) {
	base, n, found := strings.Cut(s, "+")
	if !found || base == "" {
		return "", 0, false
	}
	offset, err := strconv.Atoi(n)
	return base, offset, err == nil && offset > 0
}

// tiedTo lists the roles tied to base that the owner didn't choose.
func tiedTo(info game.Info, base string, keep map[string]int) []tie {
	var out []tie
	for _, r := range info.Ports {
		if b, off, ok := parseTie(r.Tie); ok && b == base && keep[r.Role] == 0 {
			out = append(out, tie{role: r, offset: off})
		}
	}
	return out
}

// withTiedPorts sets every tied role's port from its base's, so a change
// of the base (picked, or typed on the Settings tab) carries the tied
// ports with it. It returns a new map.
func withTiedPorts(info game.Info, ports map[string]int) map[string]int {
	out := make(map[string]int, len(ports))
	for k, v := range ports {
		out[k] = v
	}
	for _, r := range info.Ports {
		if base, off, ok := parseTie(r.Tie); ok && out[base] > 0 {
			out[r.Role] = out[base] + off
		}
	}
	return out
}

// reservedPortsLocked is every port an instance reserves, with the
// instance that does: what servers.json lists for each instance (games
// this build doesn't run included, so their ports stay theirs), and each
// instance's own settings, which for the legacy game are settings.json's
// port and port+1.
func (m *Manager) reservedPortsLocked() map[int]string {
	out := map[int]string{}
	add := func(id string, ports map[string]int) {
		for _, p := range ports {
			if p > 0 && out[p] == "" {
				out[p] = id
			}
		}
	}
	m.eachInstance(func(inst *instance) {
		if inst.srv != nil {
			add(inst.id, inst.saved().Ports)
		}
	})
	for _, in := range m.servers.Instances {
		if !in.Legacy {
			add(in.ID, in.Ports)
		}
	}
	return out
}

// checkPorts is the start's port check for a non-legacy instance: nil, or
// an *ActionError with Action "pick_ports" ("Port 7777 (TCP) is being used
// by another program: <name>"; for a port the owner chose, Action
// "settings" and "change it in Settings", because the app never picks
// another for it). M3's doStart calls it inside the start job, after Plan
// and before the launch (spec 4.3), never for the legacy instance. It runs
// without m.mu: it reads the system's port tables.
func (m *Manager) checkPorts(inst *instance) error {
	m.mu.Lock()
	saved := inst.saved()
	chosen := map[string]bool{}
	for _, r := range inst.info.Ports {
		chosen[r.Role] = m.portChosenLocked(inst, r.Role)
	}
	games := map[int]string{} // the running games' processes, to name them
	m.eachInstance(func(i *instance) {
		if i.run != nil && i.run.proc != nil {
			games[i.run.proc.PID()] = i.short()
		}
	})
	m.mu.Unlock()
	for _, r := range inst.info.Ports {
		port := saved.Ports[r.Role]
		if port <= 0 {
			return &ActionError{Msg: fmt.Sprintf("%s has no %s yet. Pick free ports for it.", inst.short(), strings.ToLower(r.Label)), Action: "pick_ports"}
		}
		for _, proto := range r.Protos {
			busy, pid := portInUse(proto, port)
			if !busy {
				continue
			}
			msg := fmt.Sprintf("Port %d (%s) is being used by another program", port, strings.ToUpper(proto))
			switch name := games[pid]; {
			case pid > 0 && name != "":
				msg = fmt.Sprintf("Port %d (%s) is being used by the %s server", port, strings.ToUpper(proto), name)
			case pid > 0 && procName(pid) != "":
				msg += ": " + procName(pid)
			}
			if chosen[r.Role] {
				return &ActionError{Msg: msg + ". You chose this port: change it in Settings.", Action: "settings"}
			}
			return &ActionError{Msg: msg + ".", Action: "pick_ports"}
		}
	}
	return nil
}

// checkChosenPortsLocked checks ports the owner typed on the Settings tab,
// by role; the errors are keyed by role. It looks only at what the app
// knows (other instances' ports, the game's roles), not at the system's
// port tables, which the start checks (checkPorts). A tied role can't be
// typed on its own: it follows its base (withTiedPorts).
func (m *Manager) checkChosenPortsLocked(inst *instance, ports map[string]int) game.FieldErrors {
	errs := game.FieldErrors{}
	roles := map[string]game.PortRole{}
	for _, r := range inst.info.Ports {
		roles[r.Role] = r
	}
	reserved := m.reservedPortsLocked()
	owner := func(id string) string {
		if other := m.instances[id]; other != nil {
			return other.short()
		}
		return id
	}
	for role, p := range ports {
		r, ok := roles[role]
		switch {
		case !ok:
			errs[role] = fmt.Sprintf("%s has no port called %q.", inst.short(), role)
		case r.Tie != "":
			base, off, _ := parseTie(r.Tie)
			errs[role] = fmt.Sprintf("This port is always the %s plus %d, so it changes with that one.", strings.ToLower(roles[base].Label), off)
		case p < 1024 || p > 65535:
			errs[role] = "Choose a port from 1024 to 65535."
		case reserved[p] != "" && reserved[p] != inst.id:
			errs[role] = fmt.Sprintf("Port %d is %s's. Choose another one.", p, owner(reserved[p]))
		}
	}
	// A typed base carries its tied ports with it, and those must be free
	// of other instances too.
	for _, r := range inst.info.Ports {
		base, off, ok := parseTie(r.Tie)
		bp, typed := ports[base]
		if !ok || !typed || errs[base] != "" {
			continue
		}
		if o := reserved[bp+off]; o != "" && o != inst.id {
			errs[base] = fmt.Sprintf("Port %d, which the %s takes with it, is %s's. Choose another one.", bp+off, strings.ToLower(r.Label), owner(o))
		}
	}
	// The instance's ports after the change, tied ones included, must not
	// share a number between roles.
	after := inst.saved().Ports
	if after == nil {
		after = map[string]int{}
	}
	for role, p := range ports {
		if _, bad := errs[role]; !bad {
			after[role] = p
		}
	}
	after = withTiedPorts(inst.info, after)
	byPort := map[int][]string{}
	for role, p := range after {
		byPort[p] = append(byPort[p], role)
	}
	for p, rs := range byPort {
		if p <= 0 {
			continue
		}
		sort.Strings(rs)
		for _, role := range rs {
			if _, typed := ports[role]; !typed || errs[role] != "" {
				continue
			}
			if len(rs) > 1 {
				errs[role] = fmt.Sprintf("Port %d is also this game's %s.", p, strings.ToLower(roles[otherRole(rs, role)].Label))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// otherRole is the first role in rs that isn't role.
func otherRole(rs []string, role string) string {
	for _, r := range rs {
		if r != role {
			return r
		}
	}
	return role
}

// PickPorts picks new free ports for the roles the owner didn't choose
// (POST /api/games/{id}/ports/pick), then updates the firewall rules and
// router mappings. It answers the new ports by role. The game must be
// stopped: its ports are what friends and the router know it by while it
// runs, and the router mappings follow at its next start.
func (m *Manager) PickPorts(id string) (map[string]int, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return nil, err
	}
	if inst.legacy {
		return nil, &ActionError{Msg: fmt.Sprintf("%s's port is set on its Settings tab.", inst.short()), Action: "settings"}
	}
	if inst.srv == nil {
		return nil, fmt.Errorf("This version of the app can't change %s's ports.", inst.short())
	}
	m.mu.Lock()
	running := inst.run != nil
	busy := m.busyJobLocked(inst)
	keep := map[string]int{}
	cur := inst.saved().Ports
	for _, r := range inst.info.Ports {
		if m.portChosenLocked(inst, r.Role) && cur[r.Role] > 0 {
			keep[r.Role] = cur[r.Role]
		}
	}
	wasReady := inst.cache.firewall
	m.mu.Unlock()
	switch {
	case running:
		return nil, fmt.Errorf("Stop the %s server before picking new ports for it.", inst.short())
	case busy != nil:
		return nil, &BusyError{Title: lowerFirst(busy.Title)}
	}
	ports, unlock, err := m.pickPorts(inst.id, inst.info, keep)
	if err != nil {
		return nil, err
	}
	err = inst.srv.Store().Save(func(s *game.Saved) error {
		s.Ports = ports
		return nil
	})
	unlock()
	if err != nil {
		return nil, err
	}
	m.addGameActivity(inst, "info", "", fmt.Sprintf("New ports for %s: %s.", inst.short(), portsText(inst.info, ports)))
	// Windows' rules name the ports, so rules that were in place no longer
	// match: make them again, with one admin prompt, as the owner is at the
	// panel now. (macOS allows programs, not ports, so nothing changes
	// there.) Router mappings are made at the next start, with these ports.
	if wasReady && !m.checkGameFirewall(inst) {
		go func() {
			if err := m.FixFirewall(); err != nil && !errors.Is(err, platform.ErrUnsupported) {
				m.log.Printf("firewall after picking ports: %v", err)
			}
		}()
	}
	m.notify()
	return ports, nil
}

// portsText lists ports by role for people: "game port 7778, query port
// 7779".
func portsText(info game.Info, ports map[string]int) string {
	var parts []string
	for _, r := range info.Ports {
		if p := ports[r.Role]; p > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(r.Label), p))
		}
	}
	return strings.Join(parts, ", ")
}
