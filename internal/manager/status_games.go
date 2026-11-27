package manager

// The new panel's part of Status (spec 4.9): one GameStatus per instance,
// and the app-wide fields beside it. Made in phase 0' with every field of
// 4.9; stream M3 owns this file from then on.
//
// gameStatusLocked asks one hook per stream for that stream's part, so the
// streams fill in their parts in their own files and never edit this one:
//
//	autoStopStatusLocked  autostop.go  (A)  "autoStop"
//	askStatusLocked       ask.go       (D)  "ask"
//	routerStatusLocked    router.go    (N)  each port's "router", and Join's Router input
//	discordStatusLocked   discord.go   (D)  "discord"
//
// and Status asks routerSummaryLocked (router.go, N) for the top-level
// "router". Every hook runs with m.mu held and must answer from memory
// (spec 4.8): no disk, process or network.

import (
	"fmt"

	"valheim-easy-server/internal/game"
)

// GameStatus is one game's instance, for the new panel (spec 4.9).
type GameStatus struct {
	ID           string        `json:"id"`   // the instance
	Game         string        `json:"game"` // its game
	Name         string        `json:"name"`
	Short        string        `json:"short"`
	Letter       string        `json:"letter"`
	Color        string        `json:"color"`
	Stage        game.Stage    `json:"stage"`
	ServerName   string        `json:"serverName"`
	Starred      bool          `json:"starred"`
	Foreign      string        `json:"foreign"`   // why this build doesn't run it; "" when it does
	Disabled     bool          `json:"disabled"`  // turned off on its Settings tab (8.3)
	CanRun       game.Level    `json:"canRun"`    // "yes", or "no" with NeedsText
	NeedsText    string        `json:"needsText"` // "Needs Windows", "Needs an Intel Mac"
	State        State         `json:"state"`
	Installed    bool          `json:"installed"`
	Build        *game.Build   `json:"build,omitempty"`
	World        string        `json:"world"`        // selected
	RunningWorld string        `json:"runningWorld"` // of the running server
	Players      []game.Player `json:"players"`
	Count        int           `json:"count"`
	MaxPlayers   int           `json:"maxPlayers"`
	Presence     game.Presence `json:"presence"`
	Join         game.Join     `json:"join"`
	Ports        []PortStatus  `json:"ports"`
	Job          *Job          `json:"job"`
	LastExit     *ExitInfo     `json:"lastExit"`
	NeedsRestart bool          `json:"needsRestart"`
	MemMB        int           `json:"memMB"`  // in use by the running server (A's sample)
	CPU          float64       `json:"cpu"`    // percent of the whole computer (A's sample)
	NeedMB       int           `json:"needMB"` // Info.MemoryMB

	AutoStop *AutoStopStatus    `json:"autoStop"` // autostop.go (A); null when off or not running
	Ask      *AskStatus         `json:"ask"`      // ask.go (D); null unless the "still playing?" rule runs
	Discord  *GameDiscordStatus `json:"discord"`  // discord.go (D); null when the game doesn't post
	Reminder []string           `json:"reminder"` // what players need to do before joining
	Warnings []string           `json:"warnings"`
	// ConsoleInUse: a running console game someone plays, or may (its
	// presence isn't known or not yet proven). An automatic app update waits
	// for it; a manual one would leave it without its console until its next
	// start, which the panel says before it updates (spec 2.11).
	ConsoleInUse bool `json:"consoleInUse,omitempty"`
}

// PortStatus is one of a game's ports as it stands.
type PortStatus struct {
	Role   string   `json:"role"`
	Protos []string `json:"protos"`
	Port   int      `json:"port"`
	Public bool     `json:"public"`
	Chosen bool     `json:"chosen"`           // the owner chose it (never picked again)
	Router string   `json:"router,omitempty"` // public roles: "open", "failed", "off"… from router.go (N)
}

// SidebarStatus is which of the sidebar's lists are open (servers.json).
type SidebarStatus struct {
	ActiveOpen   bool `json:"activeOpen"`
	InactiveOpen bool `json:"inactiveOpen"`
}

// RouterStatus is the top-level "router" of Status (spec 4.9 and 6.5):
// what the app found about the router and the way in from the internet.
// router.go (N) fills it in; nil until it has.
type RouterStatus struct {
	Kind       string `json:"kind"`       // "upnp", "pcp", "natpmp", "" when none answered
	ExternalIP string `json:"externalIP"` // the router's internet address, as it says
	CGNAT      bool   `json:"cgnat"`      // carrier-grade NAT: friends outside can't reach this computer
	Problem    string `json:"problem"`    // what went wrong, in words for the owner
}

// gameView is what Status works out once for an instance, for both the
// legacy top-level fields and the instance's GameStatus, so nothing is
// asked of the adapter twice.
type gameView struct {
	saved        game.Saved
	state        State
	installed    bool
	build        game.Build
	hasBuild     bool
	runState     *game.RunState // nil when stopped
	rec          *game.Record   // nil when stopped
	join         game.Join
	needsRestart bool
}

// gameViewLocked works out an instance's view from memory. The legacy
// instance's comes from Status itself (status.go), which keeps today's
// quick look at the disk for it; every other instance's reads only the
// caches (spec 4.8), so this is M3's to finish for them.
func (m *Manager) gameViewLocked(inst *instance, lan []string) gameView {
	v := gameView{saved: inst.saved(), state: m.instStateLocked(inst), installed: m.installedLocked(inst),
		build: inst.cache.build, hasBuild: inst.cache.hasBuild && inst.cache.installed}
	if r := inst.run; r != nil {
		rs := r.state()
		v.runState, v.rec = &rs, &r.rec
		v.needsRestart = r.rec.ArgsKey != "" && r.rec.ArgsKey != inst.srv.Fingerprint(v.saved, selectedWorld(inst, v.saved))
	}
	v.join = inst.srv.Join(game.JoinInput{Rec: v.rec, State: v.runState, Saved: v.saved, PublicIP: m.net.public,
		LAN: lan, Router: m.routerStatusLocked(inst)})
	return v
}

// gameStatusLocked is one instance's GameStatus, from its view.
func (m *Manager) gameStatusLocked(inst *instance, v gameView) GameStatus {
	info := inst.info
	canRun, needs := m.canRunLocked(inst)
	gs := GameStatus{ID: inst.id, Game: info.ID, Name: info.Name, Short: info.Short, Letter: info.Letter,
		Color: info.Color, Stage: info.Stage, ServerName: v.saved.Server["serverName"], Starred: m.isStarredLocked(inst),
		Foreign: inst.foreign, Disabled: m.isDisabledLocked(inst), CanRun: canRun, NeedsText: needs,
		State: v.state, Installed: v.installed, World: v.saved.World, Players: []game.Player{},
		MaxPlayers: info.MaxPlayers, Presence: info.Presence, Join: v.join, LastExit: inst.lastExit,
		NeedsRestart: v.needsRestart, NeedMB: info.MemoryMB, Reminder: []string{}, Warnings: []string{}}
	if v.hasBuild {
		b := v.build
		gs.Build = &b
	}
	if v.runState != nil {
		gs.Players, gs.Count = v.runState.Players, v.runState.Count
	}
	if inst.job != nil {
		c := *inst.job
		gs.Job = &c
	}
	if r := inst.run; r != nil {
		gs.RunningWorld = r.rec.World
		gs.MemMB, gs.CPU = int(r.memNow>>20), r.cpuNow
		if rem := m.reminderLocked(inst, r); len(rem) > 0 {
			gs.Reminder = rem
		}
	}
	if !inst.legacy {
		gs.Warnings = m.gameWarningsLocked(inst, v)
	}
	if inst.run != nil && m.isHiddenLocked(inst) {
		// It was removed from the panel, yet its server runs: started by an
		// older version of the app after going back to it (which knows
		// nothing of hiding, spec 3.6) or outside the app, then taken over.
		// It shows, unstarred, until it stops; it can be stopped but not
		// started or restarted.
		gs.Warnings = append(gs.Warnings, inst.short()+" was removed from the panel, but its server is running, so it shows here until it stops. To keep it, add it again with Add a game.")
	}
	gs.ConsoleInUse = m.consoleInUseOfLocked(inst)
	// One hook per stream.
	router := m.routerStatusLocked(inst) // N
	for _, p := range info.Ports {
		ps := PortStatus{Role: p.Role, Protos: p.Protos, Port: v.saved.Ports[p.Role], Public: p.Public,
			Chosen: m.portChosenLocked(inst, p.Role)}
		if p.Public {
			ps.Router = router[p.Role]
		}
		gs.Ports = append(gs.Ports, ps)
	}
	if gs.Ports == nil {
		gs.Ports = []PortStatus{}
	}
	gs.AutoStop = m.autoStopStatusLocked(inst) // A
	gs.Ask = m.askStatusLocked(inst)           // D
	gs.Discord = m.discordStatusLocked(inst)   // D
	return gs
}

// gamesStatusLocked fills in Status's R3 fields: every instance's
// GameStatus, the stars and the sidebar, how many games are online and
// how many people play, the router and safe mode. legacy is the legacy
// instance's view, which Status has already worked out.
func (m *Manager) gamesStatusLocked(st *Status, lan []string, legacy gameView) {
	st.Games = []GameStatus{}
	for _, id := range m.order {
		inst := m.instances[id]
		if inst.srv == nil {
			st.Games = append(st.Games, m.foreignStatusLocked(inst))
			continue
		}
		if m.isHiddenLocked(inst) && inst.run == nil {
			// Removed from the panel (I1): only Add a game offers it again.
			// While its server runs it is shown all the same (gameStatusLocked
			// says why), so a hidden Valheim never runs unseen.
			continue
		}
		v := legacy
		if !inst.legacy {
			v = m.gameViewLocked(inst, lan)
		}
		gs := m.gameStatusLocked(inst, v)
		if gs.State == StateOnline {
			st.Online++
		}
		if gs.Count > 0 {
			st.Playing += gs.Count
		}
		st.Games = append(st.Games, gs)
	}
	st.Stars = m.starsLocked()
	st.Sidebar = SidebarStatus{ActiveOpen: m.servers.App.ActiveListOpen(), InactiveOpen: m.servers.App.InactiveListOpen()}
	st.Router = m.routerSummaryLocked() // N
	st.SafeMode = m.safeMode
}

// gameWarningsLocked are a non-legacy game's own warnings, from memory:
// settings to fix, a probe that keeps failing, a takeover still catching
// up with its log, its drive almost full.
func (m *Manager) gameWarningsLocked(inst *instance, v gameView) []string {
	out := []string{}
	if errs := inst.srv.Validate(v.saved, selectedWorld(inst, v.saved)); len(errs) > 0 {
		out = append(out, "Some settings need fixing before the server can start: "+errs.Error())
	}
	if w := m.probeWarningLocked(inst); w != "" {
		out = append(out, w)
	}
	if r := inst.run; r != nil && r.replaying {
		out = append(out, "Catching up with the server log…")
	}
	if c := inst.cache; c.freeDiskOK && c.freeDisk < 2<<30 {
		out = append(out, "The drive with the worlds is almost full. "+inst.short()+" may stop saving when it runs out of space: free up some room.")
	}
	return out
}

// foreignStatusLocked is a foreign instance as the panel shows it: its
// name and why this version can't run it, and whether its server is
// running (its run file names a live process, foreign.go).
func (m *Manager) foreignStatusLocked(inst *instance) GameStatus {
	info := inst.info
	gs := GameStatus{ID: inst.id, Game: info.ID, Name: info.Name, Short: info.Short, Letter: info.Letter, Color: info.Color,
		Stage: info.Stage, Starred: m.isStarredLocked(inst), Foreign: inst.foreign, Disabled: m.isDisabledLocked(inst),
		CanRun: game.CantRun, State: StateStopped, Players: []game.Player{}, Count: -1, MaxPlayers: info.MaxPlayers,
		Presence: game.Unknown, Join: game.Join{LAN: []string{}, Steps: []string{}}, Ports: []PortStatus{},
		Reminder: []string{}, Warnings: []string{}}
	for _, f := range m.foreign {
		if f.id == inst.id {
			gs.State = StateOnline
			gs.Warnings = append(gs.Warnings, fmt.Sprintf("%s is running, started by a newer version of the app; this version can't control it.", f.name))
		}
	}
	return gs
}
