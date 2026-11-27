package routerports

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// findAt finds the router at the stand-in's port 5351, with no UPnP on the
// network.
func findAt(t *testing.T, s *pmpServer) Router {
	t.Helper()
	f := testFinder(silentUDP(t).String(), s.port())
	r, err := f.find(ctx(t), loopback, loopback)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	return r
}

var minecraftGame = Mapping{Proto: "tcp", Port: 25565, Desc: "Easy Game Server minecraft game"}

// A NAT-PMP router (it answers PCP with "unsupported version"): map, renew,
// unmap.
func TestNATPMPMapRenewUnmap(t *testing.T) {
	s := newPMPServer(t, true, false)
	r := findAt(t, s)
	if r.Kind() != "natpmp" {
		t.Fatalf("found %s, want natpmp", r.Kind())
	}
	ip, err := r.ExternalIP(ctx(t))
	if err != nil || ip.String() != "198.51.100.4" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	if err := r.Map(ctx(t), loopback, minecraftGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	if e := s.entry("tcp 25565"); e == nil || e.external != 25565 || e.lifetime != 7200 {
		t.Fatalf("entry = %+v", e)
	}
	if err := r.Map(ctx(t), loopback, minecraftGame, 2*time.Hour); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if e := s.entry("tcp 25565"); e == nil || e.external != 25565 {
		t.Fatalf("entry after renewing = %+v", e)
	}
	if err := r.Unmap(ctx(t), loopback, minecraftGame); err != nil {
		t.Fatalf("unmap: %v", err)
	}
	if e := s.entry("tcp 25565"); e != nil {
		t.Fatalf("entry after unmap = %+v", e)
	}
}

// When the port is taken, a NAT-PMP router gives another, which doesn't
// count and is taken back.
func TestNATPMPConflict(t *testing.T) {
	s := newPMPServer(t, true, false)
	s.with(func() { s.taken["udp 2456"] = true })
	r := findAt(t, s)
	err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "2457") {
		t.Fatalf("map = %v, want ErrConflict offering 2457", err)
	}
	if e := s.entry("udp 2456"); e != nil {
		t.Fatalf("the other port was kept: %+v", e)
	}
}

// A PCP router: map, renew and unmap with the same nonce, and its internet
// address from the MAP answer.
func TestPCPMapRenewUnmap(t *testing.T) {
	s := newPMPServer(t, false, true)
	r := findAt(t, s)
	if r.Kind() != "pcp" {
		t.Fatalf("found %s, want pcp", r.Kind())
	}
	// Before any mapping, PCP has no way to ask for the address, and this
	// router doesn't speak NAT-PMP.
	if _, err := r.ExternalIP(ctx(t)); err == nil {
		t.Fatal("an address before any mapping")
	}
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err != nil {
		t.Fatalf("map: %v", err)
	}
	e := s.entry("udp 2456")
	if e == nil || e.external != 2456 || e.lifetime != 7200 {
		t.Fatalf("entry = %+v", e)
	}
	ip, err := r.ExternalIP(ctx(t))
	if err != nil || ip.String() != "198.51.100.4" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	// A copy of the app that took over (a new Router) renews and removes
	// what the earlier one made: the nonce is the mapping's own.
	r2 := findAt(t, s)
	if err := r2.Map(ctx(t), loopback, valheimGame, time.Hour); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if e := s.entry("udp 2456"); e == nil || e.lifetime != 3600 {
		t.Fatalf("entry after renewing = %+v", e)
	}
	if err := r2.Unmap(ctx(t), loopback, valheimGame); err != nil {
		t.Fatalf("unmap: %v", err)
	}
	if e := s.entry("udp 2456"); e != nil {
		t.Fatalf("entry after unmap = %+v", e)
	}
}

// A PCP router that also speaks NAT-PMP says its address before any
// mapping; a taken port is a conflict; and another computer's mapping
// can't be changed (the router checks the nonce).
func TestPCPDetails(t *testing.T) {
	s := newPMPServer(t, true, true)
	r := findAt(t, s)
	if r.Kind() != "pcp" {
		t.Fatalf("found %s, want pcp (it comes before NAT-PMP)", r.Kind())
	}
	if ip, err := r.ExternalIP(ctx(t)); err != nil || ip.String() != "198.51.100.4" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	s.with(func() { s.taken["tcp 25565"] = true })
	if err := r.Map(ctx(t), loopback, minecraftGame, 2*time.Hour); !errors.Is(err, ErrConflict) {
		t.Fatalf("map over a taken port = %v, want ErrConflict", err)
	}
	if e := s.entry("tcp 25565"); e != nil {
		t.Fatalf("the other port was kept: %+v", e)
	}
	s.with(func() { s.entries["udp 2456"] = &pmpEntry{external: 2456, lifetime: 100, nonce: "someone else"} })
	if err := r.Map(ctx(t), loopback, valheimGame, 2*time.Hour); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("map over another's = %v", err)
	}
}

// Find asks UPnP first, then PCP, then NAT-PMP, and says ErrNoRouter
// quickly when nothing answers.
func TestFindOrder(t *testing.T) {
	g := newIGD(t)
	s := newPMPServer(t, true, true)
	f := testFinder(g.addr(), s.port())
	r, err := f.find(ctx(t), loopback, loopback)
	if err != nil || r.Kind() != "upnp" {
		t.Fatalf("with all three: %v, %v", r, err)
	}
	if s.requestCount() != 0 {
		t.Fatal("PCP or NAT-PMP was asked though UPnP answered")
	}

	f = testFinder(silentUDP(t).String(), silentUDP(t).Port)
	start := time.Now()
	r, err = f.find(ctx(t), loopback, loopback)
	if !errors.Is(err, ErrNoRouter) || r != nil {
		t.Fatalf("with none: %v, %v", r, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("finding nothing took %s", took)
	}

	// Without the gateway's address only UPnP can be tried.
	s2 := newPMPServer(t, true, true)
	f = testFinder(silentUDP(t).String(), s2.port())
	if _, err := f.find(ctx(t), loopback, nil); !errors.Is(err, ErrNoRouter) {
		t.Fatalf("without a gateway: %v", err)
	}
}

// A request is sent again when no answer comes (UDP can lose either).
func TestPMPResends(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make(chan int, 1)
	go func() {
		buf := make([]byte, 64)
		for n := 1; ; n++ {
			_, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n == 1 {
				continue // lost
			}
			c.WriteToUDP([]byte{0, 128, 0, 0, 0, 0, 0, 1, 198, 51, 100, 4}, from)
			got <- n
			return
		}
	}()
	r := &natpmpRouter{addr: c.LocalAddr().(*net.UDPAddr), try: 50 * time.Millisecond, tries: 3}
	ip, err := r.ExternalIP(ctx(t))
	if err != nil || ip.String() != "198.51.100.4" {
		t.Fatalf("external IP = %v, %v", ip, err)
	}
	if n := <-got; n != 2 {
		t.Fatalf("answered request %d", n)
	}
}
