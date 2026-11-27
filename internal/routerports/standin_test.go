package routerports

// Stand-in routers for the tests: a UPnP IGD (an SSDP responder, a device
// description and a SOAP control point) and a NAT-PMP/PCP server, all on
// this computer's loopback address, each keeping a table of mappings the
// tests can look at. They follow the protocols' documents where the app
// relies on them, and refuse the way real routers do.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var loopback = net.IPv4(127, 0, 0, 1).To4()

// testFinder looks for routers at the stand-ins, and waits only briefly
// for answers that won't come.
func testFinder(ssdp string, pmpPort int) finder {
	return finder{ssdp: ssdp, pmpPort: pmpPort, wait: 400 * time.Millisecond, try: 50 * time.Millisecond, tries: 3, client: lanClient()}
}

// silentUDP is a UDP address where something listens and never answers,
// like a network where no router speaks the protocol. (A closed port would
// answer with an ICMP error, which isn't what a home network does.)
func silentUDP(t *testing.T) *net.UDPAddr {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.LocalAddr().(*net.UDPAddr)
}

// --- UPnP IGD -----------------------------------------------------------

type igdEntry struct {
	client string
	port   int // internal
	desc   string
	lease  int
	// static: made on the router's own page. The stand-in, like many real
	// routers, refuses a UPnP mapping over it (718) even for the computer
	// it points at.
	static bool
}

// igd is a stand-in UPnP Internet Gateway Device.
type igd struct {
	t        *testing.T
	ssdp     *net.UDPConn
	http     *httptest.Server
	services []string // the connection services its description lists

	mu            sync.Mutex
	external      string
	entries       map[string]*igdEntry // by "UDP 2456"
	calls         []string             // the SOAP actions, in order
	permanentOnly bool                 // refuses leases other than 0 (725)
	refuseAdd     int                  // refuses AddPortMapping with this code (an IGD v2 quirk)
}

func newIGD(t *testing.T, services ...string) *igd {
	t.Helper()
	if len(services) == 0 {
		services = []string{"urn:schemas-upnp-org:service:WANIPConnection:1"}
	}
	g := &igd{t: t, services: services, external: "203.0.113.7", entries: map[string]*igdEntry{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/rootDesc.xml", g.description)
	mux.HandleFunc("/ctl/", g.control)
	g.http = httptest.NewServer(mux)
	t.Cleanup(g.http.Close)
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	g.ssdp = c
	t.Cleanup(func() { c.Close() })
	go g.answerSearches()
	return g
}

// addr is where SSDP searches go to reach the stand-in.
func (g *igd) addr() string { return g.ssdp.LocalAddr().String() }

func (g *igd) answerSearches() {
	buf := make([]byte, 2048)
	for {
		n, from, err := g.ssdp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, "M-SEARCH * HTTP/1.1\r\n") || !strings.Contains(msg, `MAN: "ssdp:discover"`) {
			continue
		}
		st := ""
		for _, line := range strings.Split(msg, "\r\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, "ST") {
				st = strings.TrimSpace(v)
			}
		}
		if !strings.Contains(st, "InternetGatewayDevice") && !strings.Contains(st, "WANIPConnection") {
			continue
		}
		answer := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nST: " + st + "\r\n" +
			"USN: uuid:standin::" + st + "\r\nEXT:\r\nSERVER: StandIn/1.0 UPnP/1.1 MiniUPnPd/2.3\r\n" +
			"LOCATION: " + g.http.URL + "/rootDesc.xml\r\n\r\n"
		g.ssdp.WriteToUDP([]byte(answer), from)
	}
}

func (g *igd) description(w http.ResponseWriter, r *http.Request) {
	var svc strings.Builder
	for i, s := range g.services {
		fmt.Fprintf(&svc, "<service><serviceType>%s</serviceType><serviceId>urn:upnp-org:serviceId:Conn%d</serviceId>"+
			"<controlURL>/ctl/%d</controlURL><eventSubURL>/evt/%d</eventSubURL><SCPDURL>/scpd%d.xml</SCPDURL></service>", s, i, i, i, i)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
<specVersion><major>1</major><minor>0</minor></specVersion>
<device>
<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
<friendlyName>Stand-in router</friendlyName>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:Layer3Forwarding:1</serviceType><controlURL>/ctl/L3F</controlURL></service></serviceList>
<deviceList><device>
<deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:WANCommonInterfaceConfig:1</serviceType><controlURL>/ctl/CmnIfCfg</controlURL></service></serviceList>
<deviceList><device>
<deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
<serviceList>%s</serviceList>
</device></deviceList>
</device></deviceList>
</device>
</root>`, svc.String())
}

// fault answers a SOAP call with a UPnP error, as the UPnP documents say:
// HTTP 500 with the code inside.
func fault(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>
<s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring>
<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError></detail>
</s:Fault></s:Body></s:Envelope>`, code, desc)
}

func reply(w http.ResponseWriter, service, action string, values ...string) {
	var b strings.Builder
	for i := 0; i+1 < len(values); i += 2 {
		fmt.Fprintf(&b, "<%s>%s</%s>", values[i], values[i+1], values[i])
	}
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>
<u:%sResponse xmlns:u="%s">%s</u:%sResponse></s:Body></s:Envelope>`, action, service, b.String(), action)
}

func (g *igd) control(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/ctl/"))
	if err != nil || i >= len(g.services) || r.Method != http.MethodPost {
		http.Error(w, "no such service", http.StatusNotFound)
		return
	}
	service := g.services[i]
	soapAction := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	svcType, action, ok := strings.Cut(soapAction, "#")
	if !ok || svcType != service || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/xml") {
		fault(w, 401, "Invalid Action")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if !bytes.Contains(body, []byte(`<u:`+action+` xmlns:u="`+service+`">`)) {
		fault(w, 401, "Invalid Action")
		return
	}
	v := soapValues(body)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, action)
	key := v["NewProtocol"] + " " + v["NewExternalPort"]
	switch action {
	case "GetExternalIPAddress":
		reply(w, service, action, "NewExternalIPAddress", g.external)
	case "AddPortMapping", "AddAnyPortMapping":
		if action == "AddAnyPortMapping" && !strings.HasSuffix(service, ":2") {
			fault(w, 401, "Invalid Action")
			return
		}
		if action == "AddPortMapping" && g.refuseAdd != 0 {
			fault(w, g.refuseAdd, "Refused")
			return
		}
		lease, _ := strconv.Atoi(v["NewLeaseDuration"])
		port, _ := strconv.Atoi(v["NewInternalPort"])
		if v["NewProtocol"] != "TCP" && v["NewProtocol"] != "UDP" || port == 0 || net.ParseIP(v["NewInternalClient"]) == nil {
			fault(w, 402, "Invalid Args")
			return
		}
		if g.permanentOnly && lease != 0 {
			fault(w, 725, "OnlyPermanentLeasesSupported")
			return
		}
		ext, _ := strconv.Atoi(v["NewExternalPort"])
		if e := g.entries[key]; e != nil && (e.static || e.client != v["NewInternalClient"]) {
			if action == "AddPortMapping" {
				fault(w, 718, "ConflictInMappingEntry")
				return
			}
			// AddAnyPortMapping gives the next free port.
			for ext++; g.entries[v["NewProtocol"]+" "+strconv.Itoa(ext)] != nil; ext++ {
			}
			key = v["NewProtocol"] + " " + strconv.Itoa(ext)
		}
		g.entries[key] = &igdEntry{client: v["NewInternalClient"], port: port, desc: v["NewPortMappingDescription"], lease: lease}
		if action == "AddAnyPortMapping" {
			reply(w, service, action, "NewReservedPort", strconv.Itoa(ext))
			return
		}
		reply(w, service, action)
	case "GetSpecificPortMappingEntry":
		e := g.entries[key]
		if e == nil {
			fault(w, 714, "NoSuchEntryInArray")
			return
		}
		reply(w, service, action, "NewInternalPort", strconv.Itoa(e.port), "NewInternalClient", e.client,
			"NewEnabled", "1", "NewPortMappingDescription", e.desc, "NewLeaseDuration", strconv.Itoa(e.lease))
	case "DeletePortMapping":
		if g.entries[key] == nil {
			fault(w, 714, "NoSuchEntryInArray")
			return
		}
		delete(g.entries, key)
		reply(w, service, action)
	default:
		fault(w, 401, "Invalid Action")
	}
}

// with changes the stand-in while it runs.
func (g *igd) with(change func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change()
}

// entry is a copy of the mapping for "UDP 2456", or nil.
func (g *igd) entry(key string) *igdEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.entries[key]; e != nil {
		c := *e
		return &c
	}
	return nil
}

func (g *igd) count(action string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if c == action {
			n++
		}
	}
	return n
}

// --- NAT-PMP and PCP ----------------------------------------------------

type pmpEntry struct {
	external int
	lifetime uint32
	nonce    string // PCP's
}

// pmpServer is a stand-in for a router's port 5351: NAT-PMP, PCP or both.
type pmpServer struct {
	conn *net.UDPConn
	pmp  bool // answers NAT-PMP (version 0)
	pcp  bool // answers PCP (version 2)

	mu       sync.Mutex
	external net.IP
	entries  map[string]*pmpEntry // by "udp 2456" (the internal port)
	taken    map[string]bool      // external ports used by someone else, by "udp 2456"
	requests int
}

func newPMPServer(t *testing.T, pmp, pcp bool) *pmpServer {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
	if err != nil {
		t.Fatal(err)
	}
	s := &pmpServer{conn: c, pmp: pmp, pcp: pcp, external: net.IPv4(198, 51, 100, 4).To4(),
		entries: map[string]*pmpEntry{}, taken: map[string]bool{}}
	t.Cleanup(func() { c.Close() })
	go s.serve()
	return s
}

func (s *pmpServer) port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

func (s *pmpServer) entry(key string) *pmpEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[key]; e != nil {
		c := *e
		return &c
	}
	return nil
}

// with changes the stand-in while it runs.
func (s *pmpServer) with(change func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change()
}

func (s *pmpServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// epoch is the seconds since the stand-in started, which every answer has.
const epoch = 1234

func (s *pmpServer) serve() {
	buf := make([]byte, 1100)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 2 {
			continue
		}
		s.mu.Lock()
		s.requests++
		var out []byte
		switch b := buf[:n]; {
		case b[0] == 0 && s.pmp:
			out = s.natpmp(b)
		case b[0] == 2 && s.pcp:
			out = s.pcpAnswer(b, from)
		case b[0] != 0 && s.pmp:
			// RFC 6886 3.5: a NAT-PMP router answers another version with
			// "unsupported version" in its own format.
			out = make([]byte, 8)
			out[1] = 128 + b[1]
			binary.BigEndian.PutUint16(out[2:4], 1)
			binary.BigEndian.PutUint32(out[4:8], epoch)
		}
		s.mu.Unlock()
		if out != nil {
			s.conn.WriteToUDP(out, from)
		}
	}
}

// freeExternal is the port to give for want: want when nobody has it, else
// the next free one.
func (s *pmpServer) freeExternal(proto string, want int, mine *pmpEntry) int {
	used := func(p int) bool {
		if s.taken[proto+" "+strconv.Itoa(p)] {
			return true
		}
		for _, e := range s.entries {
			if e != mine && e.external == p {
				return true
			}
		}
		return false
	}
	if want == 0 {
		want = 40000
	}
	for used(want) {
		want++
	}
	return want
}

func (s *pmpServer) natpmp(b []byte) []byte {
	op := b[1]
	switch {
	case op == 0 && len(b) == 2:
		out := make([]byte, 12)
		out[1] = 128
		binary.BigEndian.PutUint32(out[4:8], epoch)
		copy(out[8:12], s.external)
		return out
	case (op == 1 || op == 2) && len(b) == 12:
		proto := map[byte]string{1: "udp", 2: "tcp"}[op]
		internal := int(binary.BigEndian.Uint16(b[4:6]))
		suggested := int(binary.BigEndian.Uint16(b[6:8]))
		lifetime := binary.BigEndian.Uint32(b[8:12])
		key := proto + " " + strconv.Itoa(internal)
		out := make([]byte, 16)
		out[1] = 128 + op
		binary.BigEndian.PutUint32(out[4:8], epoch)
		binary.BigEndian.PutUint16(out[8:10], uint16(internal))
		if lifetime == 0 {
			delete(s.entries, key)
			return out
		}
		e := s.entries[key]
		if e == nil {
			e = &pmpEntry{external: s.freeExternal(proto, suggested, nil)}
			s.entries[key] = e
		}
		e.lifetime = lifetime
		binary.BigEndian.PutUint16(out[10:12], uint16(e.external))
		binary.BigEndian.PutUint32(out[12:16], lifetime)
		return out
	}
	out := make([]byte, 8)
	out[1] = 128 + op
	binary.BigEndian.PutUint16(out[2:4], 5) // unsupported opcode
	return out
}

func (s *pmpServer) pcpAnswer(b []byte, from *net.UDPAddr) []byte {
	if len(b) < 24 || b[1]&0x80 != 0 {
		return nil
	}
	op := b[1]
	out := make([]byte, 24)
	out[0], out[1] = 2, 0x80|op
	binary.BigEndian.PutUint32(out[8:12], epoch)
	result := func(code byte) []byte { out[3] = code; return out }
	if !net.IP(b[8:24]).Equal(from.IP) {
		return result(12) // ADDRESS_MISMATCH
	}
	switch op {
	case 0: // ANNOUNCE
		return out
	case 1: // MAP
		if len(b) < 60 {
			return result(3) // MALFORMED_REQUEST
		}
		lifetime := binary.BigEndian.Uint32(b[4:8])
		p := b[24:60]
		nonce, protoNum := string(p[0:12]), p[12]
		internal := int(binary.BigEndian.Uint16(p[16:18]))
		suggested := int(binary.BigEndian.Uint16(p[18:20]))
		proto := map[byte]string{6: "tcp", 17: "udp"}[protoNum]
		if proto == "" {
			return result(9) // UNSUPP_PROTOCOL
		}
		out = append(out, p...)
		key := proto + " " + strconv.Itoa(internal)
		e := s.entries[key]
		if e != nil && e.nonce != nonce {
			return result(2) // NOT_AUTHORIZED: someone else's mapping
		}
		if lifetime == 0 {
			delete(s.entries, key)
			copy(out[44:60], net.IPv6zero)
			return out
		}
		if e == nil {
			e = &pmpEntry{external: s.freeExternal(proto, suggested, nil), nonce: nonce}
			s.entries[key] = e
		}
		e.lifetime = lifetime
		binary.BigEndian.PutUint32(out[4:8], lifetime)
		binary.BigEndian.PutUint16(out[42:44], uint16(e.external))
		copy(out[44:60], s.external.To16())
		return out
	}
	return result(4) // UNSUPP_OPCODE
}
