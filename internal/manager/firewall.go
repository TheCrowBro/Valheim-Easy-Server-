package manager

// Firewall rules for every game (spec 2.12, stream A). R3's fix makes the
// legacy game's rules exactly as today and every other installed game's
// "Easy Game Server - <instance> …" rules in the same admin prompt
// (platform.FixFirewallAll). It stays a synchronous call, not a job (4.2).
//
// Phase 0' moved FixFirewall here unchanged from status.go, so stream A
// owns it, and made the other signatures with empty bodies.

import (
	"valheim-easy-server/internal/platform"
)

// FixFirewall adds the firewall rules (asks for admin permission).
func (m *Manager) FixFirewall() error {
	m.mu.Lock()
	exe, wake := m.legacy().srv.Program(), m.settings.DiscordWake
	m.mu.Unlock()
	err := platform.FixFirewall(exe, m.opt.AppExe, wake)
	m.recheckSoon()
	return err
}

// gameRulesLocked are the firewall rules of every non-legacy instance
// whose server is installed (a rule for a program that isn't there yet
// can't be made, so it would keep offering the fix): its public ports by
// protocol, or AnyPort for a game with none (2.12).
func (m *Manager) gameRulesLocked() []platform.GameRule {
	return nil
}

// checkGameFirewalls refreshes each non-legacy instance's instCache.firewall
// with platform.GameFirewallReady. The checks loop (M3) calls it every
// minute, without m.mu.
func (m *Manager) checkGameFirewalls() {}
