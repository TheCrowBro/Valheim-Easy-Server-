package manager

// Router ports (spec 6.5, stream N): when a game whose Join says friends
// need router ports becomes ready, the app asks the router to forward its
// public ports here (internal/routerports), with a 2-hour lease renewed
// every 30 minutes, and removes them when it stops; again after a takeover
// and after waking. It finds carrier-grade or double NAT and says so, and
// asks Steam whether it lists a Steam game's server. On by default
// (servers.json app.routerPorts). Valheim with crossplay on maps nothing.
//
// How it fits together. Everything that talks to the router happens in a
// "round", run by one goroutine at a time (routerRounds), so a removal and
// a mapping of the same port can never overtake each other (a restart
// removes the old run's ports before the new run's are made). The
// observer (routerObserver, called with m.mu held) only notes what is
// wanted and asks for a round:
//
//   - onReady, and onTakeover of a ready run: the run's public ports are
//     wanted (routerState.games), so a round maps them.
//   - onExit: the ended run's mappings (serverRun.mappings, only the ones
//     the app made) go on the list to remove.
//   - onTick: the first look a few seconds after the app starts (so the
//     panel and Minecraft's allow list know about carrier-grade NAT before
//     any game starts), renewals every 30 minutes, a retry when the router
//     didn't answer, and Steam's check 2 minutes after a game is ready.
//   - onWake: the router is found again shortly after waking (the network
//     may be a different one) and the ports are mapped again.
//
// A round makes a plan under m.mu, does the network work without it, and
// writes back what it found under m.mu. Status and GET /api/router only
// read what the last round wrote (spec 4.8).
//
// A port forwarded by hand to this computer (UPnP 718, ErrForwardedByHand)
// counts as open, "set on the router", and is never removed by the app.
// With NoPublicIP (the tests, and anything else that keeps the app off the
// internet) the app leaves the router alone too, so a test run never asks
// the developer's own router to forward anything.

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/routerports"
)

// What the rounds call to reach the network; the tests put stand-ins here.
var (
	findRouter    = routerports.Find
	routerGateway = platform.DefaultGateway
	routerLANFor  = lanTowards
	steamSees     = routerports.SteamSees
)

// The router clock (spec 6.5).
const (
	routerLease     = 2 * time.Hour    // asked of the router for each mapping
	routerRenew     = 30 * time.Minute // mappings are asked for again this often
	routerFirstLook = 10 * time.Second // after the app starts, the first look at the router
	routerAfterWake = 20 * time.Second // after waking, the network usually needs this long
	routerRetryMax  = 10 * time.Minute // the longest wait before asking a router that didn't answer again
	steamAfter      = 2 * time.Minute  // Steam lists a server a little while after it starts
	routerRoundMax  = 90 * time.Second // one round's network work, at most
)

// What a port's router state can be (Status, GET /api/router).
const (
	portMapping = "mapping"           // the router hasn't been asked yet
	portOpen    = "open"              // the app forwarded it (or Steam lists the server)
	portByHand  = "set on the router" // the owner forwarded it to this computer by hand
	portFailed  = "failed"            // friends outside can't reach it: the manual steps say why
	portOff     = "off"               // router ports are turned off, or the game needs none
)

// routerState is stream N's own app-wide state (Manager.router), guarded by
// m.mu: the router found, its external address, the NAT kind, the gateway
// and what Steam sees. A running game's own mappings are on its run
// (serverRun.mappings), so they end with it.
type routerState struct {
	// The rounds.
	busy      bool                  // a round is running
	again     bool                  // another round was asked for while one ran
	waiting   []chan struct{}       // closed when the next round has finished
	due       time.Time             // the next round the clock asks for; zero: none
	forget    bool                  // find the router again in the next round (after waking, a check)
	steamNow  bool                  // ask Steam in the next round, however new the games are (a check)
	firstTick time.Time             // the first sample tick, for the first look
	fails     int                   // rounds in a row in which the router didn't answer, for the retry
	remove    []routerports.Mapping // the app's mappings to take off the router in the next round
	// override is the App settings' switch as routerPortsChanged was told,
	// until servers.json says the same (so the order M3 saves and calls in
	// doesn't matter).
	override *bool

	// What the last round found.
	checked  time.Time          // when it finished; zero before the first
	found    routerports.Router // the router that answered; nil when none did
	kind     string
	gateway  net.IP // the router's address on the home network
	lan      net.IP // this computer's address there, which the router forwards to
	external net.IP // the router's own internet address
	public   string // this home's address as the internet sees it
	nat      string // routerports.NAT: "cgnat", "double" or ""

	games map[string]*routerGame // by instance: its ready run and what it wants
}

// routerGame is one ready run's router ports.
type routerGame struct {
	run     *serverRun
	need    bool   // its Join says friends outside need router ports
	manual  string // the game's own words for the manual steps (Join.Manual)
	readyAt time.Time
	ports   []*routerPort // its public ports, one per protocol
	steam   int           // Steam lists its server at the public address: 1 yes, -1 no, 0 not asked yet
}

// routerPort is one port of a game on the router.
type routerPort struct {
	role  string
	m     routerports.Mapping
	state string    // portMapping, portOpen, portByHand or portFailed
	err   string    // why it failed, for the owner
	ours  bool      // the app made it, so the app removes it
	until time.Time // when the router forgets it if it isn't renewed (ours)
}

// RouterView is GET /api/router: everything the app knows about the way in
// from the internet. The fields after Steam are additions to spec 6.2's
// list (stream N's).
type RouterView struct {
	Kind       string          `json:"kind"` // "upnp", "pcp", "natpmp", "" when none answered
	ExternalIP string          `json:"externalIP"`
	PublicIP   string          `json:"publicIP"`
	CGNAT      bool            `json:"cgnat"`
	Gateway    string          `json:"gateway"`
	LAN        []string        `json:"lan"`
	Mappings   []RouterMapping `json:"mappings"`
	Steam      []SteamSeen     `json:"steam"`

	On       bool         `json:"on"`       // the app opens router ports (App settings)
	Checking bool         `json:"checking"` // a check is running now
	Checked  *time.Time   `json:"checked"`  // when the last check finished; null before the first
	NAT      string       `json:"nat"`      // "cgnat", "double" or ""
	Problem  string       `json:"problem"`  // what stands in the way, in words for the owner; "" when nothing
	Games    []RouterGame `json:"games"`    // each running game that friends outside reach through router ports
}

// RouterMapping is one port the app forwards, or tried to.
type RouterMapping struct {
	Instance string `json:"instance"`
	Role     string `json:"role"`
	Proto    string `json:"proto"`
	Port     int    `json:"port"`
	State    string `json:"state"` // "open", "set on the router", "failed", "off", "mapping"
	Error    string `json:"error,omitempty"`
}

// SteamSeen says whether Steam lists an instance's server at the public
// address.
type SteamSeen struct {
	Instance string `json:"instance"`
	Seen     bool   `json:"seen"`
}

// RouterGame is what the router check says about one running game whose
// friends outside need router ports (an addition to spec 6.2's list).
type RouterGame struct {
	Instance string `json:"instance"`
	State    string `json:"state"`            // all its ports together, as routerStatusLocked's
	Check    string `json:"check"`            // what the app knows about reaching it from outside
	Manual   string `json:"manual,omitempty"` // the manual steps, when friends outside can't reach it yet
}

// The words for RouterGame.Check.
const (
	checkSteam   = "Steam lists this server at your internet address, so friends outside can reach it."
	checkUnknown = "Can't check from here: ask a friend outside to try."
)

// routerSkipped says the app leaves the router alone this run (NoPublicIP:
// the tests). Status and the views then say nothing about it.
func (m *Manager) routerSkipped() bool { return m.opt.NoPublicIP }

// routerOnLocked says whether the app opens router ports: the App
// settings' switch (servers.json app.routerPorts, on unless turned off).
func (m *Manager) routerOnLocked() bool {
	if m.routerSkipped() {
		return false
	}
	if o := m.router.override; o != nil {
		return *o
	}
	return m.servers.App.RouterPortsOn()
}

// Router is GET /api/router.
func (m *Manager) Router() RouterView {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &m.router
	v := RouterView{LAN: []string{}, Mappings: []RouterMapping{}, Steam: []SteamSeen{}, Games: []RouterGame{}}
	if m.routerSkipped() {
		return v
	}
	on := m.routerOnLocked()
	v.On, v.Checking, v.Kind, v.NAT, v.CGNAT = on, r.busy, r.kind, r.nat, r.nat == "cgnat"
	v.ExternalIP, v.Gateway, v.PublicIP = ipText(r.external), ipText(r.gateway), r.public
	if v.PublicIP == "" {
		v.PublicIP = m.net.public
	}
	if !r.checked.IsZero() {
		at := r.checked
		v.Checked = &at
	}
	v.Problem = m.routerProblemLocked(on)
	if r.lan != nil {
		v.LAN = append(v.LAN, r.lan.String())
	}
	for _, ip := range m.lan {
		if !slices.Contains(v.LAN, ip) {
			v.LAN = append(v.LAN, ip)
		}
	}
	now := time.Now()
	m.eachInstance(func(inst *instance) {
		g := m.routerGameLocked(inst)
		if g == nil || !g.need {
			return
		}
		for _, p := range g.ports {
			state, err := portOff, ""
			if on {
				state, err = r.effective(g, p, now)
			}
			v.Mappings = append(v.Mappings, RouterMapping{Instance: inst.id, Role: p.role, Proto: p.m.Proto,
				Port: p.m.Port, State: state, Error: err})
		}
		if g.steam != 0 {
			v.Steam = append(v.Steam, SteamSeen{Instance: inst.id, Seen: g.steam > 0})
		}
		rg := RouterGame{Instance: inst.id, State: portOff, Manual: m.routerManualLocked(inst)}
		if on {
			rg.State = r.gameState(g, now)
			switch {
			case g.steam > 0:
				rg.Check = checkSteam
			case rg.State == portOpen || rg.State == portByHand:
				rg.Check = checkUnknown
			}
		}
		v.Games = append(v.Games, rg)
	})
	return v
}

// CheckRouter runs the router discovery and the reachability checks again
// (POST /api/router/check). It waits for them, up to a little over a
// round's longest, so the panel's next look shows the answer.
func (m *Manager) CheckRouter() error {
	m.mu.Lock()
	if m.routerSkipped() {
		m.mu.Unlock()
		return nil
	}
	if !m.routerOnLocked() {
		m.mu.Unlock()
		return errors.New("Router ports are turned off in App settings. Turn them on, and the app checks the router.")
	}
	m.router.forget, m.router.steamNow = true, true
	done := m.routerWaitLocked()
	m.routerAskLocked()
	m.mu.Unlock()
	m.routerWaitFor(done)
	return nil
}

// routerStatusLocked is the instance's router state by role, for its
// public ports in Status and for Join's Router input: "open", "set on the
// router", "failed", "mapping" (being asked for), "off" (router ports
// turned off, or the game needs none). nil while it isn't running and
// ready (with router ports on), and when the app leaves the router alone.
func (m *Manager) routerStatusLocked(inst *instance) map[string]string {
	if m.routerSkipped() {
		return nil
	}
	out := map[string]string{}
	off := func() map[string]string {
		for _, p := range inst.info.Ports {
			if p.Public {
				out[p.Role] = portOff
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if !m.routerOnLocked() {
		return off()
	}
	g := m.routerGameLocked(inst)
	switch {
	case g == nil:
		return nil
	case !g.need:
		return off()
	}
	r, now := &m.router, time.Now()
	byRole := map[string][]*routerPort{}
	var roles []string
	for _, p := range g.ports {
		if byRole[p.role] == nil {
			roles = append(roles, p.role)
		}
		byRole[p.role] = append(byRole[p.role], p)
	}
	for _, role := range roles {
		out[role] = r.combine(g, byRole[role], now)
	}
	return out
}

// routerSummaryLocked is Status's top-level "router", or nil before the
// first discovery (and when the app leaves the router alone).
func (m *Manager) routerSummaryLocked() *RouterStatus {
	r := &m.router
	if m.routerSkipped() || r.checked.IsZero() {
		return nil
	}
	return &RouterStatus{Kind: r.kind, ExternalIP: ipText(r.external), CGNAT: r.nat == "cgnat",
		Problem: m.routerProblemLocked(m.routerOnLocked())}
}

// routerManualLocked is the manual steps for the instance's game Home
// (spec 6.5), "" when nothing is in the way: what to forward on the router
// by hand when the app couldn't, what carrier-grade or double NAT means.
// GET /api/router carries it in each game's "manual"; M3 may show it in the
// game's Status too.
func (m *Manager) routerManualLocked(inst *instance) string {
	if !m.routerOnLocked() {
		return ""
	}
	g := m.routerGameLocked(inst)
	if g == nil || !g.need || g.steam > 0 {
		return "" // Steam lists it: friends outside reach it, whatever the router says
	}
	r, now := &m.router, time.Now()
	if r.nat == "cgnat" {
		return routerports.CGNATText
	}
	var failed, all []routerports.Mapping
	for _, p := range g.ports {
		all = append(all, p.m)
		if state, _ := r.effective(g, p, now); state == portFailed {
			failed = append(failed, p.m)
		}
	}
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, routerports.ManualSteps(r.gateway, r.lan, failed, g.manual))
	}
	if r.nat == "double" {
		parts = append(parts, routerports.DoubleNATText(r.external, net.ParseIP(r.public), all))
	}
	return strings.Join(parts, "\n\n")
}

// routerProblemLocked is what stands in the way for every game, in words
// for the owner ("" when nothing, or before the first look).
func (m *Manager) routerProblemLocked(on bool) string {
	r := &m.router
	switch {
	case !on || r.checked.IsZero():
		return ""
	case r.found == nil && r.gateway == nil:
		return "This computer isn't connected to a router right now, so the app can't open ports for friends outside."
	case r.found == nil:
		return "The router didn't answer when the app asked it to open ports (UPnP, NAT-PMP or PCP). " +
			"Turn on UPnP in the router's settings, or forward the ports by hand."
	case r.nat == "cgnat":
		return routerports.CGNATText
	case r.nat == "double":
		return routerports.DoubleNATText(r.external, net.ParseIP(r.public), nil)
	}
	return ""
}

// routerGameLocked is the instance's router ports while its current run is
// ready, or nil.
func (m *Manager) routerGameLocked(inst *instance) *routerGame {
	g := m.router.games[inst.id]
	if g == nil || inst.run == nil || g.run != inst.run {
		return nil
	}
	return g
}

// effective is a port's state as friends outside meet it: Steam listing
// the server makes every port open; carrier-grade NAT makes every port
// fail (nothing on the router helps); a mapping whose renewals failed
// until its lease ran out is gone.
func (r *routerState) effective(g *routerGame, p *routerPort, now time.Time) (state, err string) {
	switch {
	case g.steam > 0:
		return portOpen, ""
	case r.nat == "cgnat":
		return portFailed, "Your internet provider shares one internet address between homes (carrier-grade NAT)."
	case p.state == portOpen && p.ours && now.After(p.until):
		if p.err != "" {
			return portFailed, p.err // why renewing it didn't work
		}
		return portFailed, "Its time on the router ran out."
	}
	return p.state, p.err
}

// combine is one state for several ports (a role's protocols, or a whole
// game): failed if any failed, then mapping if any is being asked for,
// "set on the router" if all were forwarded by hand, else open.
func (r *routerState) combine(g *routerGame, ports []*routerPort, now time.Time) string {
	states := make([]string, 0, len(ports))
	for _, p := range ports {
		s, _ := r.effective(g, p, now)
		states = append(states, s)
	}
	switch {
	case len(states) == 0:
		return portOff
	case slices.Contains(states, portFailed):
		return portFailed
	case slices.Contains(states, portMapping):
		return portMapping
	case !slices.ContainsFunc(states, func(s string) bool { return s != portByHand }):
		return portByHand
	}
	return portOpen
}

func (r *routerState) gameState(g *routerGame, now time.Time) string {
	return r.combine(g, g.ports, now)
}

// unmapPorts removes the instance's router mappings now (A's RemoveGame
// calls it; a stop removes them through routerObserver.onExit, which gets
// the ended run and so its serverRun.mappings). It runs without m.mu and
// waits for the router, up to a little over a round's longest.
func (m *Manager) unmapPorts(inst *instance) {
	m.mu.Lock()
	r := &m.router
	if run := inst.run; run != nil && len(run.mappings) > 0 {
		r.remove = append(r.remove, run.mappings...)
		run.mappings = nil
	}
	delete(r.games, inst.id)
	if len(r.remove) == 0 {
		m.mu.Unlock()
		return
	}
	done := m.routerWaitLocked()
	m.routerAskLocked()
	m.mu.Unlock()
	m.routerWaitFor(done)
}

// routerPortsChanged follows the App settings' "router ports" switch
// (servers.json app.routerPorts): off removes every mapping, on maps the
// ready games' ports. M3's UpdateAppSettings calls it, without m.mu,
// before or after saving servers.json; it doesn't wait for the router.
func (m *Manager) routerPortsChanged(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.routerSkipped() {
		return
	}
	m.router.override = &on
	if !on {
		m.routerOffLocked()
	}
	m.routerAskLocked()
}

// routerOffLocked puts every running game's mappings on the list to remove,
// and starts their ports again from "mapping" for when it is turned on.
func (m *Manager) routerOffLocked() {
	r := &m.router
	m.eachInstance(func(inst *instance) {
		if run := inst.run; run != nil && len(run.mappings) > 0 {
			r.remove = append(r.remove, run.mappings...)
			run.mappings = nil
		}
		if g := m.routerGameLocked(inst); g != nil {
			for _, p := range g.ports {
				*p = routerPort{role: p.role, m: p.m, state: portMapping}
			}
			g.steam = 0
		}
	})
}

// noteNAT writes what the router check found into every non-legacy
// instance's Saved.State (game.StateCGNAT "true", or the key removed), so an
// adapter's Plan knows before this run's check (Minecraft's allow list in
// "auto", 5.2). It runs without m.mu, and writes only what changed.
func (m *Manager) noteNAT(kind string) {
	want := ""
	if kind == "cgnat" {
		want = "true"
	}
	m.mu.Lock()
	var list []*instance
	m.eachInstance(func(inst *instance) {
		if !inst.legacy && inst.srv != nil {
			list = append(list, inst)
		}
	})
	m.mu.Unlock()
	for _, inst := range list {
		st := inst.srv.Store()
		if st.Get().State[game.StateCGNAT] == want {
			continue
		}
		err := st.Save(func(s *game.Saved) error {
			if want == "" {
				delete(s.State, game.StateCGNAT)
				return nil
			}
			if s.State == nil {
				s.State = game.Values{}
			}
			s.State[game.StateCGNAT] = want
			return nil
		})
		if err != nil {
			m.log.Printf("router: noting carrier-grade NAT for %s: %v", inst.id, err)
		}
	}
}

// routerObserver is stream N's observer (hooks.go): map on ready and after
// a takeover or a wake (keeping them in inst.run.mappings), unmap on exit
// (the ended run's mappings, which onExit is given). Every method runs with
// m.mu held and must not block: the router is asked in a goroutine.
type routerObserver struct{ m *Manager }

func (o routerObserver) onReady(inst *instance)                { o.m.routerReadyLocked(inst) }
func (o routerObserver) onEvent(inst *instance, ev game.Event) {}
func (o routerObserver) onExit(inst *instance, run *serverRun, exit *ExitInfo) {
	o.m.routerExitLocked(inst, run)
}
func (o routerObserver) onTick(now time.Time)           { o.m.routerTickLocked(now) }
func (o routerObserver) onJob(inst *instance, job *Job) {}

// onTakeover: a taken-over run that is already ready gets its ports again
// (renewing the earlier app's mappings); one that isn't gets them on its
// onReady.
func (o routerObserver) onTakeover(inst *instance) {
	if inst.run != nil && inst.run.ready {
		o.m.routerReadyLocked(inst)
	}
}

// onWake: after someone woke the computer, the router is found again and
// the ports mapped again, once the network is likely back. Wake packets
// heard while awake change nothing here.
func (o routerObserver) onWake(cause string) {
	if cause != wakePurpose || o.m.routerSkipped() {
		return
	}
	r := &o.m.router
	r.forget = true
	if at := time.Now().Add(routerAfterWake); r.due.IsZero() || at.Before(r.due) {
		r.due = at
	}
}

// routerReadyLocked notes a ready run's public ports, when its Join says
// friends outside need router ports, and asks for a round to map them.
func (m *Manager) routerReadyLocked(inst *instance) {
	run := inst.run
	if run == nil || inst.srv == nil || m.routerSkipped() {
		return
	}
	r := &m.router
	if g := r.games[inst.id]; g != nil && g.run == run {
		return // already noted (a takeover's onReady after its onTakeover)
	}
	rs := run.state()
	j := inst.srv.Join(game.JoinInput{Rec: &run.rec, State: &rs, Saved: inst.saved(), PublicIP: m.net.public, LAN: m.lan})
	g := &routerGame{run: run, need: j.Router, manual: j.Manual, readyAt: time.Now()}
	if g.need {
		g.ports = routerPortsLocked(inst, run)
		g.need = len(g.ports) > 0
	}
	if r.games == nil {
		r.games = map[string]*routerGame{}
	}
	r.games[inst.id] = g
	if g.need && m.routerOnLocked() {
		m.routerAskLocked()
	}
}

// routerExitLocked: the run ended, so the mappings the app made for it come
// off the router (in the next round), and its ports are no longer wanted.
func (m *Manager) routerExitLocked(inst *instance, run *serverRun) {
	r := &m.router
	if g := r.games[inst.id]; g != nil && g.run == run {
		delete(r.games, inst.id)
	}
	if len(run.mappings) > 0 {
		r.remove = append(r.remove, run.mappings...)
		run.mappings = nil
		m.routerAskLocked()
	}
}

// routerTickLocked asks for the rounds the clock wants.
func (m *Manager) routerTickLocked(now time.Time) {
	r := &m.router
	if r.firstTick.IsZero() {
		r.firstTick = now
	}
	if r.busy || !m.routerOnLocked() {
		return
	}
	switch {
	case r.checked.IsZero() && now.Sub(r.firstTick) >= routerFirstLook:
		m.routerAskLocked()
	case !r.due.IsZero() && !now.Before(r.due):
		r.due = time.Time{}
		m.routerAskLocked()
	}
}

// routerPortsLocked are a run's public ports, one per protocol: the ports
// it started with (a change in the settings waits for a restart), else the
// settings'. A tied port ("game+1") follows its role. The legacy game's run
// record has only its game port (Record.Port), as older builds write it.
func routerPortsLocked(inst *instance, run *serverRun) []*routerPort {
	ports := map[string]int{}
	for role, p := range inst.saved().Ports {
		ports[role] = p
	}
	if len(inst.info.Ports) > 0 && run.rec.Port != 0 {
		ports[inst.info.Ports[0].Role] = run.rec.Port
	}
	for role, p := range run.rec.Ports {
		if p != 0 {
			ports[role] = p
		}
	}
	for _, p := range inst.info.Ports {
		base, n, ok := strings.Cut(p.Tie, "+")
		step, err := strconv.Atoi(n)
		if ok && err == nil && run.rec.Ports[p.Role] == 0 && ports[base] != 0 {
			ports[p.Role] = ports[base] + step
		}
	}
	var out []*routerPort
	for _, p := range inst.info.Ports {
		port := ports[p.Role]
		if !p.Public || port <= 0 || port > 65535 {
			continue
		}
		for _, proto := range p.Protos {
			out = append(out, &routerPort{role: p.Role, state: portMapping,
				m: routerports.Mapping{Proto: proto, Port: port, Desc: "Easy Game Server " + inst.id + " " + p.Role}})
		}
	}
	return out
}

// --- rounds ---------------------------------------------------------------

// routerAskLocked asks for a round: now, or straight after the one running.
func (m *Manager) routerAskLocked() {
	r := &m.router
	if r.busy {
		r.again = true
		return
	}
	r.busy = true
	m.go_(m.routerRounds)
}

// routerWaitLocked is closed when the next round to start has finished.
func (m *Manager) routerWaitLocked() <-chan struct{} {
	ch := make(chan struct{})
	m.router.waiting = append(m.router.waiting, ch)
	return ch
}

// routerWaitFor waits for done, but never longer than a round may take,
// nor past the app closing.
func (m *Manager) routerWaitFor(done <-chan struct{}) {
	t := time.NewTimer(routerRoundMax + 10*time.Second)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-m.routerCtx().Done():
	}
}

// routerCtx is the app's context, which ends when it closes.
func (m *Manager) routerCtx() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// routerPlan is what a round does, made under m.mu.
type routerPlan struct {
	on      bool
	rt      routerports.Router // the router from the last round; nil: find it
	gateway net.IP
	lan     net.IP
	remove  []routerports.Mapping
	games   []planGame
	steam   bool   // ask Steam what it lists
	public  string // this home's internet address, when known and fresh
	waiters []chan struct{}
}

type planGame struct {
	id   string
	run  *serverRun
	maps []routerports.Mapping // in the order of its routerGame.ports
}

// routerResult is what a round found, written back under m.mu.
type routerResult struct {
	rt        routerports.Router
	gateway   net.IP
	lan       net.IP
	noRouter  bool // nothing answered
	external  net.IP
	public    string
	publicNew bool               // looked up in this round
	maps      map[string][]error // by instance, in plan order
	steam     []routerports.SteamServer
	steamOK   bool
	unremoved []routerports.Mapping // removals no router was there to take
}

// routerRounds runs rounds until none is wanted. Only one runs at a time.
func (m *Manager) routerRounds() {
	finished := false
	defer func() {
		if !finished { // a panic (logged by go_): let the next ask start afresh
			m.mu.Lock()
			m.router.busy, m.router.again = false, false
			m.mu.Unlock()
		}
	}()
	for {
		var p routerPlan
		func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			p = m.routerPlanLocked()
		}()
		res := m.routerWork(p)
		more, nat, natKnown := false, "", false
		func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.routerApplyLocked(p, res)
			nat, natKnown = m.router.nat, res.external != nil
			more = m.router.again && m.routerCtx().Err() == nil
			m.router.again = false
			if !more {
				m.router.busy = false
				finished = true
			}
		}()
		if natKnown {
			m.noteNAT(nat)
		}
		for _, ch := range p.waiters {
			close(ch)
		}
		m.notify()
		if !more {
			return
		}
	}
}

// routerPlanLocked takes what is wanted now: the mappings to remove, and
// the ports of every ready game that needs them.
func (m *Manager) routerPlanLocked() routerPlan {
	r := &m.router
	if r.override != nil && *r.override == m.servers.App.RouterPortsOn() {
		r.override = nil // servers.json says the same now
	}
	p := routerPlan{on: m.routerOnLocked(), waiters: r.waiting}
	r.waiting = nil
	if !p.on {
		m.routerOffLocked()
	}
	p.remove, r.remove = r.remove, nil
	if !r.forget {
		p.rt, p.gateway, p.lan = r.found, r.gateway, r.lan
	}
	r.forget = false
	if m.net.public != "" && time.Since(m.net.publicAt) < publicMaxAge {
		p.public = m.net.public
	}
	if !p.on {
		return p
	}
	now := time.Now()
	m.eachInstance(func(inst *instance) {
		g := m.routerGameLocked(inst)
		if g == nil || !g.need {
			return
		}
		pg := planGame{id: inst.id, run: g.run}
		for _, port := range g.ports {
			pg.maps = append(pg.maps, port.m)
		}
		p.games = append(p.games, pg)
		if now.Sub(g.readyAt) >= steamAfter || r.steamNow {
			p.steam = true
		}
	})
	r.steamNow = false
	return p
}

// routerWork does a round's talking to the router and to Steam, without
// m.mu.
func (m *Manager) routerWork(p routerPlan) (res routerResult) {
	res.rt, res.gateway, res.lan, res.public = p.rt, p.gateway, p.lan, p.public
	if len(p.remove) == 0 && !p.on {
		return res
	}
	ctx, cancel := context.WithTimeout(m.routerCtx(), routerRoundMax)
	defer cancel()
	fresh := false
	discover := func() {
		fresh = true
		res.gateway, res.lan, res.rt = nil, nil, nil
		if gw, err := routerGateway(); err == nil && gw.To4() != nil {
			res.gateway = gw.To4()
			res.lan = routerLANFor(res.gateway)
		}
		rt, err := findRouter(ctx, res.lan, res.gateway)
		if err != nil {
			res.rt, res.noRouter = nil, true
			return
		}
		res.rt, res.noRouter = rt, false
	}
	// The router from before must still answer (it may have restarted, or
	// this may be another network); else it is looked for again.
	if res.rt != nil {
		if ext, err := res.rt.ExternalIP(ctx); err == nil {
			res.external = ext
		} else {
			discover()
		}
	} else {
		discover()
	}
	if fresh && res.rt != nil {
		if ext, err := res.rt.ExternalIP(ctx); err == nil {
			res.external = ext
		}
	}
	// Removals first, so a game that restarted gets its ports back after the
	// old run's are gone. Without a router to ask they wait for the next
	// round: a router that only keeps mappings without a lease (UPnP 725)
	// would otherwise keep them until it restarts.
	if res.rt == nil {
		res.unremoved = p.remove
	}
	for _, mp := range p.remove {
		if res.rt == nil {
			break
		}
		if err := res.rt.Unmap(ctx, res.lan, mp); err != nil {
			m.log.Printf("router: removing %s %d: %v", mp.Proto, mp.Port, err)
		}
	}
	if !p.on {
		return res
	}
	if res.public == "" && !m.opt.NoPublicIP && ctx.Err() == nil {
		if ip := fetchPublicIP(); ip != "" {
			res.public, res.publicNew = ip, true
		}
	}
	res.maps = map[string][]error{}
	for _, g := range p.games {
		errs := make([]error, len(g.maps))
		for i, mp := range g.maps {
			if res.rt == nil {
				errs[i] = routerports.ErrNoRouter
				continue
			}
			errs[i] = res.rt.Map(ctx, res.lan, mp, routerLease)
		}
		res.maps[g.id] = errs
	}
	// PCP says the router's address only in a mapping's answer.
	if res.external == nil && res.rt != nil && len(p.games) > 0 {
		if ext, err := res.rt.ExternalIP(ctx); err == nil {
			res.external = ext
		}
	}
	if p.steam && res.public != "" && ctx.Err() == nil {
		if list, err := steamSees(ctx, res.public); err == nil {
			res.steam, res.steamOK = list, true
		} else {
			m.log.Printf("router: asking Steam: %v", err)
		}
	}
	return res
}

// routerApplyLocked writes back what a round found, and plans the next.
func (m *Manager) routerApplyLocked(p routerPlan, res routerResult) {
	r := &m.router
	now := time.Now()
	for _, mp := range res.unremoved {
		if !slices.Contains(r.remove, mp) {
			r.remove = append(r.remove, mp)
		}
	}
	if p.on || res.rt != nil {
		r.found, r.gateway, r.lan = res.rt, res.gateway, res.lan
		r.kind = ""
		if res.rt != nil {
			r.kind = res.rt.Kind()
		}
	}
	if !p.on {
		r.due = time.Time{}
		return
	}
	r.checked = now
	if res.public != "" {
		r.public = res.public
		if res.publicNew {
			m.net.public, m.net.publicAt = res.public, now
		}
	}
	// Without an answer from a router, what it said before may be stale.
	r.external, r.nat = res.external, ""
	if res.external != nil {
		r.nat = routerports.NAT(res.external, net.ParseIP(r.public))
	}
	trouble := res.noRouter
	for _, pg := range p.games {
		g := r.games[pg.id]
		inst := m.instances[pg.id]
		current := g != nil && g.run == pg.run && inst != nil && inst.run == pg.run
		var ours []routerports.Mapping
		for i, err := range res.maps[pg.id] {
			mp := pg.maps[i]
			if !current {
				// The run ended while its ports were being mapped: take back
				// what was made for it.
				if err == nil {
					r.remove = append(r.remove, mp)
					r.again = true
				}
				continue
			}
			port := g.ports[i]
			before := port.state
			switch {
			case err == nil:
				port.state, port.err, port.ours, port.until = portOpen, "", true, now.Add(routerLease)
			case errors.Is(err, routerports.ErrForwardedByHand):
				port.state, port.err, port.ours, port.until = portByHand, "", false, time.Time{}
			case port.ours && now.Before(port.until):
				// A renewal didn't work, but the router keeps the mapping
				// until its lease ends: still open; try again soon.
				port.err = portError(err)
				trouble = true
			default:
				port.state, port.err, port.ours, port.until = portFailed, portError(err), false, time.Time{}
				trouble = trouble || !errors.Is(err, routerports.ErrConflict)
			}
			if port.ours {
				ours = append(ours, mp)
			}
			if port.state != before {
				m.log.Printf("router: %s %s %s %d: %s %s", pg.id, port.role, port.m.Proto, port.m.Port, port.state, port.err)
			}
		}
		if current {
			g.run.mappings = ours
			// Not listed only counts once Steam has had time to list it.
			switch listed := res.steamOK && steamListed(res.steam, g.ports); {
			case listed:
				g.steam = 1
			case res.steamOK && now.Sub(g.readyAt) >= steamAfter:
				g.steam = -1
			}
		}
	}
	// The next round the clock asks for: renewals while a game needs ports,
	// sooner when the router didn't answer, and Steam's check.
	r.due = time.Time{}
	if trouble && len(p.games) > 0 { // a look with no game waiting for ports doesn't count
		r.fails++
	} else {
		r.fails = 0
	}
	for _, g := range r.games {
		if !g.need {
			continue
		}
		next := now.Add(routerRenew)
		if r.fails > 0 {
			next = now.Add(min(time.Minute<<min(r.fails-1, 4), routerRetryMax))
		}
		if at := g.readyAt.Add(steamAfter + time.Second); g.steam == 0 && at.After(now) && at.Before(next) {
			next = at // Steam's first look (once: a failed one waits for the next renewal)
		}
		if r.due.IsZero() || next.Before(r.due) {
			r.due = next
		}
	}
}

// steamListed says whether Steam lists a server on one of the game's ports
// (its game port, or its query address).
func steamListed(list []routerports.SteamServer, ports []*routerPort) bool {
	for _, s := range list {
		_, qport, _ := net.SplitHostPort(s.Addr)
		q, _ := strconv.Atoi(qport)
		for _, p := range ports {
			if s.GamePort == p.m.Port || q == p.m.Port {
				return true
			}
		}
	}
	return false
}

// portError is why a mapping failed, for the owner: routerports' words
// ("the router already forwards this port elsewhere: it goes to
// 192.168.1.30:2456") as a sentence.
func portError(err error) string {
	switch {
	case errors.Is(err, routerports.ErrNoRouter):
		return "The router didn't answer."
	case errors.Is(err, context.DeadlineExceeded):
		return "The router didn't answer in time."
	}
	s := err.Error()
	if s == "" {
		return "The router refused."
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// lanTowards is this computer's address on the network that reaches gw,
// the one the router forwards to. Connecting a UDP socket sends nothing.
func lanTowards(gw net.IP) net.IP {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: gw, Port: 5351})
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.To4()
	}
	return nil
}

func ipText(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}
