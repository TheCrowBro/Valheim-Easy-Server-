package manager

// Firewall rules for every game (spec 2.12, stream A). R3's fix makes the
// legacy game's rules exactly as today and every other installed game's
// "Easy Game Server - <instance> …" rules in the same admin prompt
// (platform.FixFirewallAll), so the owner answers one prompt however many
// games there are. It stays a synchronous call, not a job (4.2).
//
// Windows Firewall only makes a rule for a program file that is really
// there, named by its long path: a rule for a game that isn't installed
// yet, or for a path with an 8.3 short name in it (C:\Users\RUNNER~1),
// isn't made (stream P's notes), and the check would then offer the fix
// for ever. So the rules list only games whose program exists, by the
// path Windows itself gives the file.

import (
	"os"
	"path/filepath"
	"runtime"

	"valheim-easy-server/internal/platform"
)

// FixFirewall adds the firewall rules (asks for admin permission): the
// legacy game's and the app's exactly as before, then every installed
// game's. Rules of games that were removed go at the same time, so
// removing a game needs no admin prompt of its own.
func (m *Manager) FixFirewall() error {
	m.mu.Lock()
	exe, wake := m.legacy().srv.Program(), m.settings.DiscordWake
	rules := m.gameRulesLocked()
	m.mu.Unlock()
	err := platform.FixFirewallAll(exe, m.opt.AppExe, wake, realPrograms(rules))
	m.recheckSoon() // the checks loop looks at the games' rules again with the legacy game's
	return err
}

// gameRulesLocked are the firewall rules of every non-legacy instance this
// build runs: its public ports by protocol, or AnyPort for a game with
// none (2.12; its server listens on ports it picks itself, and a
// program-wide rule keeps Windows' pop-up from asking). Whether its program
// is really there is looked at without the lock (realPrograms), so the
// fix and the check leave out a game that isn't installed yet.
func (m *Manager) gameRulesLocked() []platform.GameRule {
	var rules []platform.GameRule
	m.eachInstance(func(inst *instance) {
		if inst.legacy || inst.srv == nil {
			return
		}
		rules = append(rules, m.gameRuleLocked(inst))
	})
	return rules
}

// gameRuleLocked is one instance's rule (see gameRulesLocked).
func (m *Manager) gameRuleLocked(inst *instance) platform.GameRule {
	r := platform.GameRule{Instance: inst.id, Program: inst.srv.Program()}
	ports := inst.saved().Ports
	public := false
	for _, role := range inst.info.Ports {
		if !role.Public {
			continue // RCON and REST stay off the firewall (2.5)
		}
		public = true
		port := ports[role.Role]
		if port <= 0 {
			continue
		}
		for _, proto := range role.Protos {
			switch proto {
			case "tcp":
				r.TCP = append(r.TCP, port)
			case "udp":
				r.UDP = append(r.UDP, port)
			}
		}
	}
	r.AnyPort = !public
	return r
}

// realPrograms keeps the rules whose program is there, each by the path
// the system gives the file (on Windows its long name, no 8.3 short
// names), and leaves the others out. It looks at the disk, so it runs
// without m.mu.
func realPrograms(rules []platform.GameRule) []platform.GameRule {
	var out []platform.GameRule
	for _, r := range rules {
		if p, ok := realProgram(r.Program); ok {
			r.Program = p
			out = append(out, r)
		}
	}
	return out
}

// realProgram is the program's path as the system names the file, and
// whether it is a file that is there. On Windows EvalSymlinks also turns
// 8.3 short names into long ones (it asks Windows for each part's real
// name), which is the path Windows Firewall matches; elsewhere the path is
// kept as it is, as Valheim's always was.
func realProgram(p string) (string, bool) {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", false
	}
	if runtime.GOOS == "windows" {
		long, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", false
		}
		if abs, err := filepath.Abs(long); err == nil {
			long = abs
		}
		return long, true
	}
	return p, true
}

// checkGameFirewalls refreshes each non-legacy instance's instCache.firewall
// with platform.GameFirewallReady. The checks loop (M3) calls it every
// minute, without m.mu.
func (m *Manager) checkGameFirewalls() {
	var insts []*instance
	m.mu.Lock()
	m.eachInstance(func(inst *instance) {
		if !inst.legacy && inst.srv != nil {
			insts = append(insts, inst)
		}
	})
	m.mu.Unlock()
	for _, inst := range insts {
		m.checkGameFirewall(inst)
	}
}

// checkGameFirewall refreshes one instance's instCache.firewall and says
// whether its rules are in place. A game that isn't installed has none (a
// rule for it can't be made yet). It runs without m.mu.
func (m *Manager) checkGameFirewall(inst *instance) bool {
	m.mu.Lock()
	rule := m.gameRuleLocked(inst)
	m.mu.Unlock()
	ready := false
	if rs := realPrograms([]platform.GameRule{rule}); len(rs) == 1 {
		ready = gameFirewallReady(rs[0])
	}
	m.mu.Lock()
	inst.cache.firewall = ready
	m.mu.Unlock()
	return ready
}

// gameFirewallReady is platform.GameFirewallReady; tests replace it.
var gameFirewallReady = platform.GameFirewallReady
