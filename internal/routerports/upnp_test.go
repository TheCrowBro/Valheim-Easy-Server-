package routerports

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// findIGD finds the stand-in through SSDP, as the app finds a real router.
func findIGD(t *testing.T, g *igd) *upnpRouter {
	t.Helper()
	f := testFinder(g.addr(), silentUDP(t).Port)
	r, err := f.find(ctx(t), loopback, loopback)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	u, ok := r.(*upnpRouter)
	if !ok || r.Kind() != "upnp" {
		t.Fatalf("found %T (%s), want the UPnP router", r, r.Kind())
	}
	return u
}

var valheimGame = Mapping{Proto: "udp", Port: 2456, Desc: "Easy Game Server valheim game"}

// Map, renew and unmap on an IGD version 1 router, as for Valheim with
// crossplay off.
func TestUPnPMapRenewUnmap(t *testing.T) {
	g := newIGD(t)
	r := findIGD(t, g)
	ip, err := r.ExternalIP(ctx(t))
	if err != nil || ip.String() != "203.0.113.7" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	e := g.entry("UDP 2456")
	if e == nil || e.client != "127.0.0.1" || e.port != 2456 || e.lease != 7200 || e.desc != valheimGame.Desc {
		t.Fatalf("entry after map = %+v", e)
	}
	// Renewing is asking again: the same entry, the lease started over.
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if n := g.count("AddPortMapping"); n != 2 {
		t.Fatalf("AddPortMapping called %d times, want 2", n)
	}
	if err := r.Unmap(ctx(t), loopback, valheimGame); err != nil {
		t.Fatalf("unmap: %v", err)
	}
	if e := g.entry("UDP 2456"); e != nil {
		t.Fatalf("entry after unmap = %+v", e)
	}
	// Already gone (UPnP error 714) is no error.
	if err := r.Unmap(ctx(t), loopback, valheimGame); err != nil {
		t.Fatalf("second unmap: %v", err)
	}
}

// IGD version 2's service is preferred, and a DSL router's PPP service
// works like the others.
func TestUPnPServicesInOrder(t *testing.T) {
	g := newIGD(t, "urn:schemas-upnp-org:service:WANIPConnection:1", "urn:schemas-upnp-org:service:WANIPConnection:2")
	if r := findIGD(t, g); r.service != "urn:schemas-upnp-org:service:WANIPConnection:2" || r.version() != 2 {
		t.Fatalf("chose %s", r.service)
	}
	ppp := newIGD(t, "urn:schemas-upnp-org:service:WANPPPConnection:1")
	r := findIGD(t, ppp)
	if r.service != "urn:schemas-upnp-org:service:WANPPPConnection:1" {
		t.Fatalf("chose %s", r.service)
	}
	m := Mapping{Proto: "tcp", Port: 25565, Desc: "Easy Game Server minecraft game"}
	if err := r.Map(ctx(t), loopback, m, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	if ppp.entry("TCP 25565") == nil {
		t.Fatal("no entry on the PPP service")
	}
}

// A DSL router often lists an unused WANIPConnection next to the PPP
// connection that carries its traffic, and the unused one says its
// internet address is 0.0.0.0. The app must forward ports on the PPP one:
// on the other they would say "open" and reach nobody. When every service
// says 0.0.0.0 (the line is down), the first one is still the router's,
// in the usual order, so it forwards once the line is up.
func TestUPnPPrefersConnectedService(t *testing.T) {
	const ip1, ppp = "urn:schemas-upnp-org:service:WANIPConnection:1", "urn:schemas-upnp-org:service:WANPPPConnection:1"
	g := newIGD(t, ip1, ppp)
	g.with(func() { g.externalOf = map[string]string{ip1: "0.0.0.0", ppp: "203.0.113.9"} })
	r := findIGD(t, g)
	if r.service != ppp {
		t.Fatalf("chose %s, want the connected PPP service", r.service)
	}
	if ip, err := r.ExternalIP(ctx(t)); err != nil || ip.String() != "203.0.113.9" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}

	down := newIGD(t, ip1, ppp)
	down.with(func() { down.externalOf = map[string]string{ip1: "0.0.0.0", ppp: "0.0.0.0"} })
	if r := findIGD(t, down); r.service != ip1 {
		t.Fatalf("with the line down, chose %s, want %s", r.service, ip1)
	}

	// A service whose answer isn't an address at all doesn't count, so the
	// one that said 0.0.0.0 is the fallback.
	garbled := newIGD(t, ip1, ppp)
	garbled.with(func() { garbled.externalOf = map[string]string{ip1: "not an address", ppp: "0.0.0.0"} })
	if r := findIGD(t, garbled); r.service != ppp {
		t.Fatalf("chose %s, want the one that answered 0.0.0.0", r.service)
	}
}

// Connected tells a real internet address from a router's "none".
func TestConnected(t *testing.T) {
	for ip, want := range map[string]bool{"203.0.113.9": true, "100.64.0.1": true, "192.168.0.2": true, "0.0.0.0": false, "": false, "::": false} {
		if got := Connected(net.ParseIP(ip)); got != want {
			t.Errorf("Connected(%q) = %v, want %v", ip, got, want)
		}
	}
}

// A router that only keeps mappings until it restarts (725) gets one
// without a lease.
func TestUPnPPermanentLeasesOnly(t *testing.T) {
	g := newIGD(t)
	g.with(func() { g.permanentOnly = true })
	r := findIGD(t, g)
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	if e := g.entry("UDP 2456"); e == nil || e.lease != 0 {
		t.Fatalf("entry = %+v", e)
	}
}

// Spec 6.5: the owner forwarded the port by hand to this computer, so the
// router refuses the mapping (718), but the port counts as open.
func TestUPnPConflictForwardedByHand(t *testing.T) {
	g := newIGD(t)
	g.with(func() {
		g.entries["UDP 2456"] = &igdEntry{client: "127.0.0.1", port: 2456, desc: "Valheim", static: true}
	})
	r := findIGD(t, g)
	err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour)
	if !errors.Is(err, ErrForwardedByHand) {
		t.Fatalf("map over a forward to this computer: %v, want ErrForwardedByHand", err)
	}
	if e := g.entry("UDP 2456"); e == nil || e.desc != "Valheim" || !e.static {
		t.Fatalf("the owner's forward was changed: %+v", e)
	}
}

// A port forwarded to another computer, or to another port here, is a
// conflict: it doesn't count as open.
func TestUPnPConflictElsewhere(t *testing.T) {
	for _, e := range []*igdEntry{
		{client: "192.168.1.30", port: 2456, desc: "Valheim", static: true},
		{client: "127.0.0.1", port: 2460, desc: "Valheim", static: true},
	} {
		g := newIGD(t)
		g.with(func() { g.entries["UDP 2456"] = e })
		r := findIGD(t, g)
		err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour)
		if !errors.Is(err, ErrConflict) || errors.Is(err, ErrForwardedByHand) {
			t.Fatalf("map over a forward to %s:%d: %v, want ErrConflict", e.client, e.port, err)
		}
	}
}

// The app's own mapping from before, which this router won't renew in
// place, is made again.
func TestUPnPOwnEarlierMapping(t *testing.T) {
	g := newIGD(t)
	g.with(func() {
		g.entries["UDP 2456"] = &igdEntry{client: "127.0.0.1", port: 2456, desc: valheimGame.Desc, lease: 30, static: true}
	})
	r := findIGD(t, g)
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	if e := g.entry("UDP 2456"); e == nil || e.lease != 7200 || e.static {
		t.Fatalf("entry = %+v", e)
	}
}

// An IGD version 2 router that refuses AddPortMapping is asked with
// AddAnyPortMapping, which only counts when it keeps the port.
func TestUPnPAddAnyPortMapping(t *testing.T) {
	g := newIGD(t, "urn:schemas-upnp-org:service:WANIPConnection:2")
	g.with(func() { g.refuseAdd = 606 })
	r := findIGD(t, g)
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	if e := g.entry("UDP 2456"); e == nil || e.lease != 7200 {
		t.Fatalf("entry = %+v", e)
	}
	// Taken by another computer: the router offers 2457, which is taken
	// back.
	other := Mapping{Proto: "tcp", Port: 7777, Desc: "Easy Game Server satisfactory game"}
	g.with(func() { g.entries["TCP 7777"] = &igdEntry{client: "192.168.1.30", port: 7777, desc: "theirs"} })
	err := r.Map(ctx(t), loopback, other, 2*time.Hour)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "7778") {
		t.Fatalf("map = %v, want ErrConflict offering 7778", err)
	}
	if e := g.entry("TCP 7778"); e != nil {
		t.Fatalf("the other port was kept: %+v", e)
	}
}

// An answer whose description is on another device than the one that
// answered is ignored, so nothing on the network can send the app
// elsewhere; and a description's control URL must be on the router too.
func TestUPnPOnlyTheDeviceThatAnswered(t *testing.T) {
	answer := "HTTP/1.1 200 OK\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\nLOCATION: http://192.0.2.1:5000/rootDesc.xml\r\n\r\n"
	if _, ok := parseSSDP([]byte(answer), loopback); ok {
		t.Fatal("an answer pointing at another address was taken")
	}
	if a, ok := parseSSDP([]byte(answer), net.IPv4(192, 0, 2, 1)); !ok || a.location.Port() != "5000" {
		t.Fatalf("a good answer = %+v, %v", a, ok)
	}
	if _, ok := parseSSDP([]byte("HTTP/1.1 200 OK\r\nLOCATION: https://192.0.2.1/x\r\n\r\n"), net.IPv4(192, 0, 2, 1)); ok {
		t.Fatal("a description over HTTPS isn't UPnP's")
	}

	g := newIGD(t)
	loc, _ := url.Parse(g.http.URL + "/rootDesc.xml")
	f := testFinder(g.addr(), 0)
	if got := f.services(ctx(t), loc); len(got) != 1 || !strings.HasPrefix(got[0].control, g.http.URL+"/ctl/") {
		t.Fatalf("services = %+v", got)
	}
}

func TestSOAPValues(t *testing.T) {
	v := soapValues([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:GetSpecificPortMappingEntryResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewInternalPort>2456</NewInternalPort><NewInternalClient>192.168.1.20</NewInternalClient><NewEnabled>1</NewEnabled>
<NewPortMappingDescription>Fish &amp; chips</NewPortMappingDescription><NewLeaseDuration/></u:GetSpecificPortMappingEntryResponse>
</s:Body></s:Envelope>`))
	want := map[string]string{"NewInternalPort": "2456", "NewInternalClient": "192.168.1.20", "NewEnabled": "1",
		"NewPortMappingDescription": "Fish & chips", "NewLeaseDuration": ""}
	for k, w := range want {
		if got, ok := v[k]; !ok || got != w {
			t.Errorf("%s = %q (%v), want %q", k, got, ok, w)
		}
	}
	if _, ok := v["GetSpecificPortMappingEntryResponse"]; ok {
		t.Error("a container element was read as a value")
	}
}

// The search also goes straight to the gateway, so a router is found even
// when no answer to the multicast search gets through (a firewall on this
// computer holding back answers to a multicast).
func TestUPnPUnicastSearch(t *testing.T) {
	g := newIGD(t)
	f := testFinder(silentUDP(t).String(), silentUDP(t).Port)
	f.ssdpUnicast = g.ssdp.LocalAddr().(*net.UDPAddr).Port
	r, err := f.find(ctx(t), loopback, loopback)
	if err != nil || r.Kind() != "upnp" {
		t.Fatalf("find: %v, %v", r, err)
	}
	// Without the gateway's address there is nowhere to send it.
	if _, err := f.find(ctx(t), loopback, nil); !errors.Is(err, ErrNoRouter) {
		t.Fatalf("without a gateway: %v", err)
	}
}
