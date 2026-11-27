// Package routerports asks the home router to forward a game's ports to
// this computer, so friends outside the home can join (spec 6.5), and finds
// out what it can about whether they can reach it. It speaks UPnP IGD,
// PCP and NAT-PMP, the three ways home routers offer for this. Friends
// always connect straight to this computer (invariant I10), and nothing
// outside the home is asked except Steam's own list of servers (no
// third-party port checker, which would learn the owner's address).
//
// Only the standard library is used: the three protocols are small, and a
// package for each would be three more things an installed copy depends on.
//
// The files:
//
//	routerports.go  the types, Find, NAT
//	ssdp.go         finding UPnP routers on the home network (SSDP)
//	upnp.go         UPnP IGD v1 and v2: the device description and SOAP calls
//	natpmp.go       NAT-PMP (RFC 6886)
//	pcp.go          PCP (RFC 6887)
//	steam.go        SteamSees: does Steam list a server at the public address
//	manual.go       the words for the owner when the app can't open the ports
package routerports

import (
	"context"
	"errors"
	"net"
	"net/http"
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
	// (the app asks for 2 hours and renews every 30 minutes). Asking again
	// for a mapping that is already there renews it.
	Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error
	// Unmap stops forwarding it. A mapping that is already gone is no
	// error.
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
//
// lan is this computer's address on the home network (the one the router
// forwards to) and gateway the router's; either may be nil when unknown,
// which leaves only UPnP (it finds the router by asking the whole
// network). It takes a few seconds when nothing answers, never longer than
// ctx allows.
func Find(ctx context.Context, lan, gateway net.IP) (Router, error) {
	return defaultFinder.find(ctx, lan, gateway)
}

// finder holds where Find looks, so the tests can point it at stand-in
// routers on this computer instead of the real network.
type finder struct {
	ssdp        string        // where SSDP searches go: the UPnP multicast group
	ssdpUnicast int           // the gateway's SSDP port, for the search sent straight to it; 0: none
	pmpPort     int           // the PCP and NAT-PMP port on the gateway
	wait        time.Duration // how long to listen for answers to a search
	try         time.Duration // the first wait for a PCP or NAT-PMP answer; doubled each try
	tries       int           // how many times a PCP or NAT-PMP request is sent
	client      *http.Client  // for UPnP's description and SOAP calls
}

var defaultFinder = finder{
	ssdp:        "239.255.255.250:1900",
	ssdpUnicast: 1900,
	pmpPort:     5351,
	wait:        2 * time.Second,
	try:         250 * time.Millisecond,
	tries:       4, // 250 ms + 500 ms + 1 s + 2 s: RFC 6886 allows more, but a home router answers at once or not at all
	client:      lanClient(),
}

// lanClient is the HTTP client for talking to the router. It never goes
// through a proxy (the router is on the home network, and a proxy set for
// the internet can't reach it), and gives up quickly: a router answers in
// well under a second.
func lanClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		},
		// A router's description and control URLs are on the router itself;
		// following a redirect elsewhere would let any device on the network
		// send the app anywhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (f finder) find(ctx context.Context, lan, gateway net.IP) (Router, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	lan, gateway = lan.To4(), gateway.To4()
	if r := f.findUPnP(ctx, lan, gateway); r != nil {
		return r, nil
	}
	if gateway == nil {
		return nil, ErrNoRouter
	}
	if r := f.findPCP(ctx, lan, gateway); r != nil {
		return r, nil
	}
	if r := f.findNATPMP(ctx, gateway); r != nil {
		return r, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrNoRouter
}

// cgnatNet is the shared address space internet providers use for
// carrier-grade NAT (RFC 6598).
var cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// NAT reports "cgnat" when the router's external address is in
// 100.64.0.0/10 (carrier-grade NAT: the internet can't reach this
// computer), "double" when it is a private address or isn't the public
// address (another router in front), and "" otherwise or when it can't
// tell.
//
// public is this home's address as the internet sees it (nil when it
// couldn't be looked up). Only IPv4 is compared: the router forwards IPv4,
// and an IPv6 public address says nothing about it.
func NAT(external, public net.IP) string {
	ext := external.To4()
	if ext == nil || ext.IsUnspecified() {
		return "" // a router not connected to the internet yet says 0.0.0.0
	}
	if cgnatNet.Contains(ext) {
		return "cgnat"
	}
	if ext.IsPrivate() || ext.IsLoopback() || ext.IsLinkLocalUnicast() {
		return "double"
	}
	if pub := public.To4(); pub != nil && !pub.Equal(ext) {
		return "double"
	}
	return ""
}
