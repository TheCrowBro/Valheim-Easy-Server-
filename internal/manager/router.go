package manager

// Router ports (spec 6.5, stream N): when a game whose Join says friends
// need router ports becomes ready, the app asks the router to forward its
// public ports here (internal/routerports), with a 2-hour lease renewed
// every 30 minutes, and removes them when it stops; again after a takeover
// and after waking. It finds carrier-grade or double NAT and says so, and
// asks Steam whether it lists a Steam game's server. On by default
// (servers.json app.routerPorts). Valheim with crossplay on maps nothing.
//
// Phase 0' made this file with its signatures and empty bodies; stream N
// fills them in. routerObserver is N's observer (hooks.go).

import (
	"time"

	"valheim-easy-server/internal/game"
)

// routerState is stream N's own app-wide state (Manager.router), guarded by
// m.mu: the router found, its external address, the NAT kind, the gateway,
// each instance's mappings and what Steam sees. N adds its fields here.
type routerState struct{}

// RouterView is GET /api/router: everything the app knows about the way in
// from the internet.
type RouterView struct {
	Kind       string          `json:"kind"` // "upnp", "pcp", "natpmp", "" when none answered
	ExternalIP string          `json:"externalIP"`
	PublicIP   string          `json:"publicIP"`
	CGNAT      bool            `json:"cgnat"`
	Gateway    string          `json:"gateway"`
	LAN        []string        `json:"lan"`
	Mappings   []RouterMapping `json:"mappings"`
	Steam      []SteamSeen     `json:"steam"`
}

// RouterMapping is one port the app forwards, or tried to.
type RouterMapping struct {
	Instance string `json:"instance"`
	Role     string `json:"role"`
	Proto    string `json:"proto"`
	Port     int    `json:"port"`
	State    string `json:"state"` // "open", "set on the router", "failed", "off"
	Error    string `json:"error,omitempty"`
}

// SteamSeen says whether Steam lists an instance's server at the public
// address.
type SteamSeen struct {
	Instance string `json:"instance"`
	Seen     bool   `json:"seen"`
}

// Router is GET /api/router.
func (m *Manager) Router() RouterView {
	return RouterView{LAN: []string{}, Mappings: []RouterMapping{}, Steam: []SteamSeen{}}
}

// CheckRouter runs the router discovery and the reachability checks again
// (POST /api/router/check).
func (m *Manager) CheckRouter() error {
	return errNotYet
}

// routerStatusLocked is the instance's router state by role, for its
// public ports in Status and for Join's Router input: "open", "failed",
// "off" (router ports turned off, or the game needs none). nil until N
// has something to say.
func (m *Manager) routerStatusLocked(inst *instance) map[string]string {
	return nil
}

// routerSummaryLocked is Status's top-level "router", or nil before the
// first discovery.
func (m *Manager) routerSummaryLocked() *RouterStatus {
	return nil
}

// unmapPorts removes the instance's router mappings now (A's RemoveGame
// calls it; a stop removes them through routerObserver.onExit). It runs
// without m.mu.
func (m *Manager) unmapPorts(inst *instance) {}

// routerPortsChanged follows the App settings' "router ports" switch
// (servers.json app.routerPorts): off removes every mapping, on maps the
// ready games' ports. M3's UpdateAppSettings calls it, without m.mu.
func (m *Manager) routerPortsChanged(on bool) {}

// noteNAT writes what the router check found into every non-legacy
// instance's Saved.State (game.StateCGNAT "true", or the key removed), so an
// adapter's Plan knows before this run's check (Minecraft's allow list in
// "auto", 5.2). It runs without m.mu.
func (m *Manager) noteNAT(kind string) {}

// routerObserver is stream N's observer (hooks.go): map on ready and after
// a takeover or a wake, unmap on exit. Every method runs with m.mu held and
// must not block: the router is asked in a goroutine.
type routerObserver struct{ m *Manager }

func (o routerObserver) onReady(inst *instance)                           {}
func (o routerObserver) onEvent(inst *instance, ev game.Event)            {}
func (o routerObserver) onExit(inst *instance, exit *ExitInfo, auto bool) {}
func (o routerObserver) onTick(now time.Time)                             {}
func (o routerObserver) onJob(inst *instance, job *Job)                   {}
func (o routerObserver) onTakeover(inst *instance)                        {}
func (o routerObserver) onWake(cause string)                              {}
