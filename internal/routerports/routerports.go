// Package routerports asks the home router to forward a game's ports to
// this computer, so friends outside the home can join (spec 6.5), and finds
// out what it can about whether they can reach it. It speaks UPnP IGD,
// PCP and NAT-PMP, the three ways home routers offer for this. Friends
// always connect straight to this computer (invariant I10), and nothing
// outside the home is asked except Steam's own list of servers (no
// third-party port checker, which would learn the owner's address).
//
// Phase 0' made the types and signatures so the manager can be written
// against them; the bodies are stubs that answer errors.ErrUnsupported
// (or "don't know") until stream N fills them in. Stream N owns this
// package.
package routerports

import (
	"context"
	"errors"
	"net"
	"time"
)

// Mapping is one port forwarded on the router.
type Mapping struct {
	Proto string // "tcp", "udp"
	Port  int    // the same inside and outside
	Desc  string // shown in the router's list: "Easy Game Server minecraft game"
}

// Router is a home router that forwards ports when asked.
type Router interface {
	Kind() string // "upnp", "pcp", "natpmp"
	// ExternalIP is the router's own internet address, as it says.
	ExternalIP(ctx context.Context) (net.IP, error)
	// Map forwards m.Port on the router to the same port at lan, for lease
	// (the app asks for 2 hours and renews every 30 minutes).
	Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error
	// Unmap stops forwarding it.
	Unmap(ctx context.Context, lan net.IP, m Mapping) error
}

// ErrForwardedByHand says the router refused a mapping because a forward
// the owner set up by hand already sends that port to this computer (UPnP
// error 718, with the existing entry pointing at lan and the same port):
// the port counts as open, "set on the router", and no manual steps are
// shown (spec 6.5).
var ErrForwardedByHand = errors.New("the router already forwards this port to this computer")

// ErrConflict says the router refused a mapping because the port is
// already forwarded somewhere else (another computer, or another port).
var ErrConflict = errors.New("the router already forwards this port elsewhere")

// ErrNoRouter says no router answered UPnP, PCP or NAT-PMP.
var ErrNoRouter = errors.New("no router answered")

// Find tries UPnP IGD (SSDP; WANIPConnection:2, :1, WANPPPConnection:1),
// then PCP, then NAT-PMP (both at gateway:5351), and returns the first
// router that answers, or ErrNoRouter.
func Find(ctx context.Context, lan, gateway net.IP) (Router, error) {
	return nil, errors.ErrUnsupported
}

// NAT reports "cgnat" when the router's external address is in
// 100.64.0.0/10 (carrier-grade NAT: the internet can't reach this
// computer), "double" when it is a private address or isn't the public
// address (another router in front), and "" otherwise or when it can't
// tell.
func NAT(external, public net.IP) string {
	return ""
}

// SteamServer is one game server Steam knows at an address, from
// ISteamApps/GetServersAtAddress.
type SteamServer struct {
	Addr     string `json:"addr"`     // "203.0.113.25:2457": its query address
	GamePort int    `json:"gameport"` // the port players join on
	AppID    int    `json:"appid"`    // the game's Steam app id (892970 is Valheim)
	GameDir  string `json:"gamedir"`  // "valheim"
	SteamID  string `json:"steamid"`
	Secure   bool   `json:"secure"`
	LAN      bool   `json:"lan"`
}

// SteamSees asks Steam which game servers it knows at publicIP
// (api.steampowered.com/ISteamApps/GetServersAtAddress/v1/?addr=IP; no key
// needed). A game that registers with Steam is reachable from the internet
// when its server is listed.
func SteamSees(ctx context.Context, publicIP string) ([]SteamServer, error) {
	return nil, errors.ErrUnsupported
}
