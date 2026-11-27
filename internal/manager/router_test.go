package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/routerports"
)

// realRouterNet is what router.go reaches the network with, kept before
// init puts the stand-ins in (a package's variables are all set before any
// of its init functions run), so useRealRouter can put back exactly that.
var realRouterNet = struct {
	find    func(context.Context, net.IP, net.IP) (routerports.Router, error)
	gateway func() (net.IP, error)
	lanFor  func(net.IP) net.IP
	steam   func(context.Context, string) ([]routerports.SteamServer, error)
}{findRouter, routerGateway, routerLANFor, steamSees}

// Every test in this package stays off the real network's router, whatever
// its options: no router answers, and Steam is never asked. The router
// tests below put their own stand-ins in.
func init() {
	findRouter = func(context.Context, net.IP, net.IP) (routerports.Router, error) {
		return nil, routerports.ErrNoRouter
	}
	routerGateway = func() (net.IP, error) { return testGateway, nil }
	routerLANFor = func(net.IP) net.IP { return testLAN }
	steamSees = func(context.Context, string) ([]routerports.SteamServer, error) {
		return nil, errors.New("no Steam in the tests")
	}
}

var (
	testGateway = net.IPv4(192, 168, 1, 1).To4()
	testLAN     = net.IPv4(192, 168, 1, 20).To4()
)

// fakeRouter is a router that does what it's asked and writes it down.
type fakeRouter struct {
	kind string

	mu       sync.Mutex
	external net.IP
	maps     map[string]time.Duration // "udp 2456" → lease
	refuse   map[string]error         // what Map answers for "udp 2456"
	calls    []string                 // "map udp 2456", "unmap udp 2456"
}

func newFakeRouter(external string) *fakeRouter {
	return &fakeRouter{kind: "upnp", external: net.ParseIP(external).To4(), maps: map[string]time.Duration{}, refuse: map[string]error{}}
}

func key(proto string, port int) string { return fmt.Sprintf("%s %d", proto, port) }

func (f *fakeRouter) Kind() string { return f.kind }

func (f *fakeRouter) ExternalIP(context.Context) (net.IP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.external, nil
}

func (f *fakeRouter) Map(_ context.Context, lan net.IP, m routerports.Mapping, lease time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(m.Proto, m.Port)
	f.calls = append(f.calls, "map "+k)
	if !lan.Equal(testLAN) || !strings.HasPrefix(m.Desc, "Easy Game Server ") {
		return fmt.Errorf("asked for %s at %s (%q)", k, lan, m.Desc)
	}
	if err := f.refuse[k]; err != nil {
		return err
	}
	f.maps[k] = lease
	return nil
}

func (f *fakeRouter) Unmap(_ context.Context, lan net.IP, m routerports.Mapping) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(m.Proto, m.Port)
	f.calls = append(f.calls, "unmap "+k)
	delete(f.maps, k)
	return nil
}

func (f *fakeRouter) mapped(proto string, port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.maps[key(proto, port)]
	return ok
}

func (f *fakeRouter) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeRouter) with(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

// routerTest is a manager whose router is fr (nil: none answers) and whose
// public address is 203.0.113.7, with Valheim's crossplay off so it needs
// router ports. finds counts the router lookups; answer changes which
// router a lookup finds.
type routerTest struct {
	*testEnv
	fr     *fakeRouter
	port   int
	finds  *int
	mu     *sync.Mutex
	answer func(*fakeRouter)
}

func routerSetup(t *testing.T, fr *fakeRouter) *routerTest {
	t.Helper()
	ip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("203.0.113.7")) }))
	t.Cleanup(ip.Close)
	oldIP, oldFind := publicIPURL, findRouter
	publicIPURL = ip.URL
	rt := &routerTest{fr: fr, finds: new(int), mu: &sync.Mutex{}}
	answers := fr
	rt.answer = func(f *fakeRouter) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		answers = f
	}
	findRouter = func(_ context.Context, lan, gw net.IP) (routerports.Router, error) {
		rt.mu.Lock()
		*rt.finds++
		found := answers
		rt.mu.Unlock()
		if !lan.Equal(testLAN) || !gw.Equal(testGateway) {
			return nil, fmt.Errorf("looked for the router at %s from %s", gw, lan)
		}
		if found == nil {
			return nil, routerports.ErrNoRouter
		}
		return found, nil
	}
	t.Cleanup(func() { publicIPURL, findRouter = oldIP, oldFind })
	rt.testEnv = setup(t, func(o *Options) { o.NoPublicIP = false })
	s := rt.m.Settings()
	s.Crossplay = false
	if err := rt.m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rt.port = s.Port
	return rt
}

func (rt *routerTest) findCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return *rt.finds
}

// start installs and starts Valheim and waits until it is online.
func (rt *routerTest) start(t *testing.T) {
	t.Helper()
	install(t, rt.m)
	if err := rt.m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return rt.m.Status().State == StateOnline })
}

// ports is the legacy game's router state by role, as Status shows it.
func ports(m *Manager) map[string]string {
	out := map[string]string{}
	for _, g := range m.Status().Games {
		if g.ID == m.legacyID {
			for _, p := range g.Ports {
				out[p.Role] = p.Router
			}
		}
	}
	return out
}

func mappingsOf(m *Manager) []routerports.Mapping {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.legacy().run; r != nil {
		return append([]routerports.Mapping(nil), r.mappings...)
	}
	return nil
}

// Spec 6.5 and the owner's decision: Valheim with crossplay off maps its
// game and query ports when it is ready, for 2 hours, renews them, and
// removes them when it stops.
func TestRouterPortsValheimCrossplayOff(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the router ports", 10*time.Second, func() bool { return fr.mapped("udp", port) && fr.mapped("udp", port+1) })
	fr.with(func() {
		if fr.maps[key("udp", port)] != 2*time.Hour || fr.maps[key("udp", port+1)] != 2*time.Hour {
			t.Errorf("leases = %v", fr.maps)
		}
	})
	waitFor(t, "the ports open in Status", 5*time.Second, func() bool {
		p := ports(m)
		return p["game"] == "open" && p["query"] == "open"
	})
	if got := mappingsOf(m); len(got) != 2 || got[0].Desc != "Easy Game Server "+m.legacyID+" game" {
		t.Fatalf("run's mappings = %+v", got)
	}
	st := m.Status()
	if st.Router == nil || st.Router.Kind != "upnp" || st.Router.ExternalIP != "203.0.113.7" || st.Router.CGNAT || st.Router.Problem != "" {
		t.Fatalf("Status router = %+v", st.Router)
	}
	v := m.Router()
	if v.Kind != "upnp" || v.Gateway != "192.168.1.1" || v.PublicIP != "203.0.113.7" || !v.On || v.NAT != "" ||
		len(v.LAN) == 0 || v.LAN[0] != "192.168.1.20" || len(v.Mappings) != 2 || v.Checked == nil {
		t.Fatalf("router view = %+v", v)
	}
	if len(v.Games) != 1 || v.Games[0].State != "open" || v.Games[0].Check != checkUnknown || v.Games[0].Manual != "" {
		t.Fatalf("router view's games = %+v", v.Games)
	}

	// The clock renews them every 30 minutes; here, now.
	m.mu.Lock()
	m.router.due = time.Now()
	m.mu.Unlock()
	waitFor(t, "a renewal", 10*time.Second, func() bool { return fr.count(key("map udp", port)) >= 2 })

	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the ports removed", 20*time.Second, func() bool { return !fr.mapped("udp", port) && !fr.mapped("udp", port+1) })
	if v := m.Router(); len(v.Mappings) != 0 || len(v.Games) != 0 {
		t.Fatalf("after stopping: %+v", v)
	}
	if p := ports(m); p["game"] != "" {
		t.Fatalf("a stopped game's router state = %v", p)
	}
}

// With crossplay on (join code), nothing changes: no router ports.
func TestRouterPortsCrossplayOn(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	s := rt.m.Settings()
	s.Crossplay = true
	if err := rt.m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rt.start(t)
	waitFor(t, "the ports off in Status", 5*time.Second, func() bool {
		p := ports(rt.m)
		return p["game"] == "off" && p["query"] == "off"
	})
	time.Sleep(time.Second)
	fr.with(func() {
		if len(fr.calls) != 0 {
			t.Fatalf("the router was asked: %v", fr.calls)
		}
	})
	if v := rt.m.Router(); len(v.Mappings) != 0 || len(v.Games) != 0 {
		t.Fatalf("router view = %+v", v)
	}
}

// Spec 6.5: a port the owner forwarded by hand to this computer counts as
// open, "set on the router", shows no manual steps, and the app never
// removes it.
func TestRouterPortForwardedByHand(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	fr.with(func() { fr.refuse[key("udp", port)] = routerports.ErrForwardedByHand })
	rt.start(t)
	waitFor(t, "the router answer", 10*time.Second, func() bool {
		p := ports(m)
		return p["game"] == "set on the router" && p["query"] == "open"
	})
	if got := mappingsOf(m); len(got) != 1 || got[0].Port != port+1 {
		t.Fatalf("run's mappings = %+v (the hand-made one isn't the app's)", got)
	}
	v := m.Router()
	if len(v.Games) != 1 || v.Games[0].Manual != "" || v.Games[0].State != "open" || v.Games[0].Check != checkUnknown {
		t.Fatalf("router view's games = %+v", v.Games)
	}
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the app's port removed", 20*time.Second, func() bool { return !fr.mapped("udp", port+1) })
	if n := fr.count(key("unmap udp", port)); n != 0 {
		t.Fatalf("the owner's forward was removed %d times", n)
	}
}

// When no router answers, the game's ports fail and the manual steps name
// them, with the router's and this computer's addresses; the app tries
// again within a few minutes.
func TestRouterNoRouterManualSteps(t *testing.T) {
	rt := routerSetup(t, nil)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the ports failed", 10*time.Second, func() bool {
		p := ports(m)
		return p["game"] == "failed" && p["query"] == "failed"
	})
	v := m.Router()
	want := fmt.Sprintf("Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), "+
		"and forward UDP %d and UDP %d to 192.168.1.20 (this computer). If you already forwarded these ports, you can ignore this.", port, port+1)
	if len(v.Games) != 1 || v.Games[0].Manual != want || v.Games[0].State != "failed" || v.Games[0].Check != "" {
		t.Fatalf("router view's games = %+v\nwant manual %q", v.Games, want)
	}
	if v.Kind != "" || !strings.Contains(v.Problem, "didn't answer") || v.Mappings[0].Error != "The router didn't answer." {
		t.Fatalf("router view = %+v", v)
	}
	if st := m.Status(); st.Router == nil || st.Router.Kind != "" || st.Router.Problem != v.Problem {
		t.Fatalf("Status router = %+v", st.Router)
	}
	m.mu.Lock()
	due := m.router.due
	m.mu.Unlock()
	if wait := time.Until(due); wait <= 0 || wait > time.Minute+time.Second {
		t.Fatalf("next try in %s, want within a minute", wait)
	}
	// The minecraft-style Join problem text comes from the "failed" state.
	m.mu.Lock()
	router := m.routerStatusLocked(m.legacy())
	m.mu.Unlock()
	if router["game"] != "failed" {
		t.Fatalf("Join's router input = %v", router)
	}
}

// Carrier-grade NAT is found from the router's external address: every
// port fails whatever the router did, and the manual steps are the
// explanation.
func TestRouterCGNAT(t *testing.T) {
	fr := newFakeRouter("100.64.3.4")
	rt := routerSetup(t, fr)
	m := rt.m
	rt.start(t)
	waitFor(t, "carrier-grade NAT", 10*time.Second, func() bool {
		st := m.Status()
		return st.Router != nil && st.Router.CGNAT
	})
	if p := ports(m); p["game"] != "failed" || p["query"] != "failed" {
		t.Fatalf("ports = %v", p)
	}
	v := m.Router()
	if !v.CGNAT || v.NAT != "cgnat" || v.Problem != routerports.CGNATText || len(v.Games) != 1 || v.Games[0].Manual != routerports.CGNATText {
		t.Fatalf("router view = %+v", v)
	}
}

// Double NAT: the router's address is private, so the manual steps say to
// forward the ports on the box in front of it too.
func TestRouterDoubleNAT(t *testing.T) {
	fr := newFakeRouter("192.168.0.2")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "double NAT", 10*time.Second, func() bool { return m.Router().NAT == "double" })
	v := m.Router()
	want := routerports.DoubleNATText(net.ParseIP("192.168.0.2"), net.ParseIP("203.0.113.7"),
		[]routerports.Mapping{{Proto: "udp", Port: port}, {Proto: "udp", Port: port + 1}})
	if len(v.Games) != 1 || v.Games[0].Manual != want || v.Problem == "" || v.CGNAT {
		t.Fatalf("router view = %+v\nwant manual %q", v, want)
	}
	if !strings.Contains(want, fmt.Sprintf("forward UDP %d and UDP %d to 192.168.0.2 (your router)", port, port+1)) {
		t.Fatalf("double NAT text = %q", want)
	}
}

// Spec 6.5: Steam listing the server at the public address counts as open,
// even when the router refused, and no manual steps are shown.
func TestRouterSteamSees(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	fr.with(func() {
		fr.refuse[key("udp", port)] = routerports.ErrConflict
		fr.refuse[key("udp", port+1)] = routerports.ErrConflict
	})
	var asked string
	var askedMu sync.Mutex
	old := steamSees
	steamSees = func(_ context.Context, ip string) ([]routerports.SteamServer, error) {
		askedMu.Lock()
		asked = ip
		askedMu.Unlock()
		return []routerports.SteamServer{{Addr: fmt.Sprintf("203.0.113.7:%d", port+1), GamePort: port, AppID: 892970}}, nil
	}
	t.Cleanup(func() { steamSees = old })
	rt.start(t)
	waitFor(t, "the ports refused", 10*time.Second, func() bool { return ports(m)["game"] == "failed" })
	if v := m.Router(); len(v.Games) != 1 || !strings.Contains(v.Games[0].Manual, fmt.Sprintf("forward UDP %d and UDP %d", port, port+1)) {
		t.Fatalf("router view's games = %+v", v.Games)
	}
	// Steam lists a server a while after it starts; the clock asks then.
	m.mu.Lock()
	m.router.games[m.legacyID].readyAt = time.Now().Add(-3 * time.Minute)
	m.router.due = time.Now()
	m.mu.Unlock()
	waitFor(t, "Steam's answer", 10*time.Second, func() bool { return len(m.Router().Steam) == 1 })
	v := m.Router()
	if !v.Steam[0].Seen || v.Games[0].Manual != "" || v.Games[0].Check != checkSteam || v.Games[0].State != "open" {
		t.Fatalf("router view = %+v", v)
	}
	if p := ports(m); p["game"] != "open" || p["query"] != "open" {
		t.Fatalf("ports = %v", p)
	}
	askedMu.Lock()
	defer askedMu.Unlock()
	if asked != "203.0.113.7" {
		t.Fatalf("Steam was asked about %q", asked)
	}
}

// The App settings' switch: off removes every mapping and says "off"; on
// maps the ready games' ports again.
func TestRouterPortsTurnedOff(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the router ports", 10*time.Second, func() bool { return fr.mapped("udp", port) && fr.mapped("udp", port+1) })
	m.routerPortsChanged(false)
	waitFor(t, "the ports removed", 10*time.Second, func() bool { return !fr.mapped("udp", port) && !fr.mapped("udp", port+1) })
	if p := ports(m); p["game"] != "off" || p["query"] != "off" {
		t.Fatalf("ports = %v", p)
	}
	if v := m.Router(); v.On || v.Games[0].State != "off" || v.Mappings[0].State != "off" || v.Games[0].Manual != "" {
		t.Fatalf("router view = %+v", v)
	}
	if err := m.CheckRouter(); err == nil {
		t.Fatal("a check while it is off")
	}
	if got := mappingsOf(m); len(got) != 0 {
		t.Fatalf("run's mappings = %+v", got)
	}
	m.routerPortsChanged(true)
	waitFor(t, "the ports again", 10*time.Second, func() bool { return fr.mapped("udp", port) && fr.mapped("udp", port+1) })
	waitFor(t, "open again", 5*time.Second, func() bool { return ports(m)["game"] == "open" })
}

// After waking, and on the panel's check, the router is found again and
// the ports mapped again.
func TestRouterAfterWakeAndCheck(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the router ports", 10*time.Second, func() bool { return fr.mapped("udp", port) })
	finds, maps := rt.findCount(), fr.count(key("map udp", port))
	// The router restarted while the computer slept and forgot everything.
	fr.with(func() { clear(fr.maps) })
	m.mu.Lock()
	m.observeLocked(func(o observer) { o.onWake(wakePurpose) })
	due := m.router.due
	m.router.due = time.Now() // rather than in 20 seconds
	m.mu.Unlock()
	if wait := time.Until(due); wait <= 0 || wait > routerAfterWake {
		t.Fatalf("after waking, the router is asked in %s", wait)
	}
	waitFor(t, "mapped again", 10*time.Second, func() bool { return fr.mapped("udp", port) && fr.mapped("udp", port+1) })
	if rt.findCount() <= finds || fr.count(key("map udp", port)) <= maps {
		t.Fatal("the router wasn't found again")
	}
	finds = rt.findCount()
	if err := m.CheckRouter(); err != nil {
		t.Fatal(err)
	}
	if rt.findCount() != finds+1 {
		t.Fatalf("CheckRouter looked for the router %d times", rt.findCount()-finds)
	}
}

// A restart removes the old run's ports before the new run's are mapped,
// so the ports end up open; a takeover after the app restarts maps them
// again for the new app.
func TestRouterRestartAndTakeover(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the router ports", 10*time.Second, func() bool { return fr.mapped("udp", port) })
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restart", 30*time.Second, func() bool {
		st := m.Status()
		return st.State == StateOnline && st.Job != nil && st.Job.Kind == "restart" && st.Job.Finished != nil
	})
	waitFor(t, "mapped after the restart", 10*time.Second, func() bool {
		return fr.count(key("unmap udp", port)) >= 1 && fr.mapped("udp", port) && fr.mapped("udp", port+1)
	})
	fr.with(func() {
		u := slices.Index(fr.calls, key("unmap udp", port))
		if !slices.Contains(fr.calls[u:], key("map udp", port)) {
			t.Fatalf("the old run's removal came after the new run's mapping: %v", fr.calls)
		}
	})

	// The app restarts (an update): the server keeps running, and the new
	// app maps its ports again once it has caught up.
	maps := fr.count(key("map udp", port))
	m.Close()
	m2 := newTestManager(t, rt.data, rt.testEnv, func(o *Options) { o.NoPublicIP = false })
	m2.Start()
	rt.m = m2
	waitFor(t, "the takeover", 20*time.Second, func() bool { return m2.Status().State == StateOnline })
	waitFor(t, "mapped by the new app", 10*time.Second, func() bool {
		return fr.count(key("map udp", port)) > maps && len(mappingsOf(m2)) == 2
	})
	if !fr.mapped("udp", port) || !fr.mapped("udp", port+1) {
		t.Fatal("the ports aren't mapped after the takeover")
	}
}

// unmapPorts (Remove a game) takes a game's mappings off the router and
// waits for it.
func TestRouterUnmapPorts(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	rt := routerSetup(t, fr)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the router ports", 10*time.Second, func() bool { return fr.mapped("udp", port) && fr.mapped("udp", port+1) })
	m.unmapPorts(m.legacy())
	if fr.mapped("udp", port) || fr.mapped("udp", port+1) {
		t.Fatal("unmapPorts returned before the router removed them")
	}
	if got := mappingsOf(m); len(got) != 0 {
		t.Fatalf("run's mappings = %+v", got)
	}
}

// storeServer is a non-legacy game's server for noteNAT's test: only its
// Store is used.
type storeServer struct {
	game.Server
	st *testStore
}

func (s storeServer) Store() game.Store { return s.st }

type testStore struct {
	mu    sync.Mutex
	saved game.Saved
	saves int
}

func (s *testStore) Get() game.Saved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved.Clone()
}

func (s *testStore) Save(change func(*game.Saved) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.saved.Clone()
	if err := change(&c); err != nil {
		return err
	}
	s.saved, s.saves = c, s.saves+1
	return nil
}

// noteNAT writes State["cgnat"] for every non-legacy instance (Minecraft's
// allow list reads it, 5.2), only when it changes, and never touches the
// legacy game's settings.
func TestNoteNAT(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	st := &testStore{saved: game.Saved{State: game.Values{"allowListOn": "true"}}}
	inst := &instance{id: "other", srv: storeServer{st: st}}
	m.mu.Lock()
	m.instances[inst.id] = inst
	m.order = append(m.order, inst.id)
	m.mu.Unlock()
	before := m.Settings()

	m.noteNAT("cgnat")
	if got := st.Get().State; got[game.StateCGNAT] != "true" || got["allowListOn"] != "true" {
		t.Fatalf("after cgnat: %v", got)
	}
	m.noteNAT("cgnat")
	m.noteNAT("cgnat")
	if st.saves != 1 {
		t.Fatalf("saved %d times for the same answer", st.saves)
	}
	m.noteNAT("double")
	if got := st.Get().State; got[game.StateCGNAT] != "" || len(got) != 1 {
		t.Fatalf("after double: %v", got)
	}
	m.noteNAT("")
	if st.saves != 2 {
		t.Fatalf("saved %d times", st.saves)
	}
	if after := m.Settings(); after.ServerName != before.ServerName || after.Port != before.Port {
		t.Fatal("the legacy game's settings changed")
	}
}

// With NoPublicIP (the other tests, and the web tests) the app leaves the
// router alone, and Status says nothing about it.
func TestRouterLeftAloneWithoutPublicIP(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	old := findRouter
	findRouter = func(context.Context, net.IP, net.IP) (routerports.Router, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, routerports.ErrNoRouter
	}
	t.Cleanup(func() { findRouter = old })
	env := setup(t)
	m := env.m
	s := m.Settings()
	s.Crossplay = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	time.Sleep(time.Second)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("the router was looked for %d times", calls)
	}
	if st := m.Status(); st.Router != nil || ports(m)["game"] != "" {
		t.Fatalf("Status says %+v, %v", st.Router, ports(m))
	}
	if err := m.CheckRouter(); err != nil {
		t.Fatal(err)
	}
	if v := m.Router(); v.On || len(v.Games) != 0 {
		t.Fatalf("router view = %+v", v)
	}
}

// round runs one round now, as the clock would, and waits for it.
func round(m *Manager) {
	m.mu.Lock()
	done := m.routerWaitLocked()
	m.routerAskLocked()
	m.mu.Unlock()
	m.routerWaitFor(done)
}

// A router that says its internet address is 0.0.0.0 isn't connected: it
// was the best that answered, or its line has gone down. So each round
// looks for the router again, and once another service is connected (a
// DSL router's PPP one) the ports go there; while nothing else answers,
// the one that does is kept. A router that says a real address isn't
// looked for again.
func TestRouterLooksAgainWhenNotConnected(t *testing.T) {
	dead := newFakeRouter("0.0.0.0")
	rt := routerSetup(t, dead)
	m, port := rt.m, rt.port
	rt.start(t)
	waitFor(t, "the ports on the router that answered", 10*time.Second, func() bool {
		return dead.mapped("udp", port) && dead.mapped("udp", port+1)
	})
	if st := m.Status(); st.Router == nil || st.Router.Kind != "upnp" || st.Router.ExternalIP != "" {
		t.Fatalf("Status router = %+v (0.0.0.0 is no address)", st.Router)
	}

	// Nothing else answers: the same router is kept, and asked again.
	rt.answer(nil)
	finds, maps := rt.findCount(), dead.count(key("map udp", port))
	round(m)
	if rt.findCount() <= finds || dead.count(key("map udp", port)) <= maps {
		t.Fatalf("looked %d more times, mapped %d more times", rt.findCount()-finds, dead.count(key("map udp", port))-maps)
	}
	if p := ports(m); p["game"] != "open" || p["query"] != "open" {
		t.Fatalf("ports = %v", p)
	}
	if v := m.Router(); v.Kind != "upnp" || v.Problem != "" || v.ExternalIP != "" {
		t.Fatalf("router view = %+v", v)
	}

	// The connected one answers now: the ports go there.
	live := newFakeRouter("203.0.113.7")
	rt.answer(live)
	round(m)
	if !live.mapped("udp", port) || !live.mapped("udp", port+1) {
		t.Fatal("the ports aren't on the connected router")
	}
	if st := m.Status(); st.Router == nil || st.Router.ExternalIP != "203.0.113.7" {
		t.Fatalf("Status router = %+v", st.Router)
	}

	// It says a real address, so the next round asks it without looking.
	finds, maps = rt.findCount(), live.count(key("map udp", port))
	round(m)
	if rt.findCount() != finds || live.count(key("map udp", port)) <= maps {
		t.Fatalf("looked %d more times, mapped %d more times", rt.findCount()-finds, live.count(key("map udp", port))-maps)
	}
}

// routerDespiteNoPublicIP (for the real-server check) uses the router with
// NoPublicIP, which the tests' managers have; the public address is still
// never looked up.
func TestRouterDespiteNoPublicIP(t *testing.T) {
	fr := newFakeRouter("203.0.113.7")
	oldFind, oldOn := findRouter, routerDespiteNoPublicIP
	findRouter = func(context.Context, net.IP, net.IP) (routerports.Router, error) { return fr, nil }
	routerDespiteNoPublicIP = true
	t.Cleanup(func() { findRouter, routerDespiteNoPublicIP = oldFind, oldOn }) // after the manager closes (setup's cleanup runs first)
	asked := false
	var mu sync.Mutex
	ip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = true
		mu.Unlock()
		w.Write([]byte("203.0.113.7"))
	}))
	t.Cleanup(ip.Close)
	oldIP := publicIPURL
	publicIPURL = ip.URL
	t.Cleanup(func() { publicIPURL = oldIP })
	env := setup(t) // NoPublicIP, as every test manager
	m := env.m
	s := m.Settings()
	s.Crossplay = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if err := m.CheckRouter(); err != nil {
		t.Fatal(err)
	}
	if p := ports(m); p["game"] != "open" || p["query"] != "open" {
		t.Fatalf("ports = %v", p)
	}
	if !fr.mapped("udp", s.Port) || !fr.mapped("udp", s.Port+1) {
		t.Fatal("not mapped")
	}
	if v := m.Router(); !v.On || v.Kind != "upnp" || len(v.Games) != 1 || v.Games[0].State != "open" {
		t.Fatalf("router view = %+v", v)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked {
		t.Fatal("the public address was looked up with NoPublicIP")
	}
}

// useRealRouter is for TestRealServer's part with crossplay off (spec 5.1:
// it "maps router ports when the runner's network has a router that
// answers", skipped otherwise), whose manager has NoPublicIP like every
// test's. Call it before New: it turns routerDespiteNoPublicIP on and puts
// back the real network in place of this file's stand-ins (init), until
// the test ends. Both are needed: NoPublicIP alone keeps the router alone,
// and without NoPublicIP every test here still finds no router, because
// init's stand-ins answer for it. Nothing else may use it: it asks the
// real router of whoever runs the test to forward ports.
func useRealRouter(t *testing.T) {
	t.Helper()
	oldFind, oldGateway, oldLAN, oldSteam, oldOn := findRouter, routerGateway, routerLANFor, steamSees, routerDespiteNoPublicIP
	n := realRouterNet
	findRouter, routerGateway, routerLANFor, steamSees = n.find, n.gateway, n.lanFor, n.steam
	routerDespiteNoPublicIP = true
	t.Cleanup(func() {
		findRouter, routerGateway, routerLANFor, steamSees, routerDespiteNoPublicIP = oldFind, oldGateway, oldLAN, oldSteam, oldOn
	})
}

// useRealRouter puts back the app's own network functions, not init's
// stand-ins, and takes them away again when the test ends (so a real-server
// run can't leave the developer's router in reach of the tests after it).
func TestUseRealRouter(t *testing.T) {
	same := func(a, b any) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }
	reaches := func() bool {
		return same(findRouter, routerports.Find) && same(routerGateway, platform.DefaultGateway) &&
			same(routerLANFor, lanTowards) && same(steamSees, routerports.SteamSees)
	}
	if reaches() || routerDespiteNoPublicIP {
		t.Fatal("the tests reach the real router")
	}
	t.Run("on", func(t *testing.T) {
		useRealRouter(t)
		if !reaches() || !routerDespiteNoPublicIP {
			t.Fatal("useRealRouter didn't put back the real network")
		}
	})
	if reaches() || routerDespiteNoPublicIP {
		t.Fatal("the real network stayed after the test")
	}
}

// checkRealRouter is TestRealServer's router check, while Valheim runs
// with crossplay off (after useRealRouter): it asks the router again and,
// when one answers, wants every port the game needs open (forwarded by
// the app, or already set on the router to this computer). When none
// answers (GitHub's runners have no router that does) it says so and
// doesn't fail: the check is skipped. It says whether a router answered.
func checkRealRouter(t *testing.T, m *Manager) bool {
	t.Helper()
	if err := m.CheckRouter(); err != nil {
		t.Errorf("router check: %v", err)
		return false
	}
	v := m.Router()
	if v.Kind == "" {
		t.Logf("no router answered (gateway %q, %s): the router ports check is skipped", v.Gateway, v.Problem)
		return false
	}
	t.Logf("router: %s, internet address %q, NAT %q", v.Kind, v.ExternalIP, v.NAT)
	n := 0
	for _, mp := range v.Mappings {
		if mp.Instance != m.legacyID {
			continue
		}
		n++
		t.Logf("  %s %s %d: %s %s", mp.Role, mp.Proto, mp.Port, mp.State, mp.Error)
		if mp.State != portOpen && mp.State != portByHand {
			t.Errorf("router port %s %d is %s: %s", mp.Proto, mp.Port, mp.State, mp.Error)
		}
	}
	if n != 2 {
		t.Errorf("%d router ports for Valheim, want its game and query ports: %+v", n, v.Mappings)
	}
	return true
}
