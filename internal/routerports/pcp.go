package routerports

// PCP, the Port Control Protocol (RFC 6887): NAT-PMP's successor, on the
// same port 5351 of the gateway. A MAP request asks for a mapping; its
// answer carries the router's internet address. Each mapping has a nonce,
// which a renewal or removal must repeat; the app makes it from the
// mapping itself, so a copy of the app that took over after an update can
// still renew or remove what an earlier one made.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// PCP's opcodes and the parts of a message.
const (
	pcpVersion    = 2
	pcpOpAnnounce = 0
	pcpOpMap      = 1
	pcpHeader     = 24 // the common header
	pcpMapSize    = 36 // a MAP request's or answer's own part
)

// pcpRouter is a router that speaks PCP.
type pcpRouter struct {
	addr  *net.UDPAddr // gateway:5351
	lan   net.IP       // this computer, as the router should see it
	try   time.Duration
	tries int

	mu       sync.Mutex
	external net.IP // the internet address the last MAP answer gave
}

func (r *pcpRouter) Kind() string { return "pcp" }

// findPCP sends PCP's ANNOUNCE, which every PCP server answers and which
// changes nothing on the router. A router that only speaks NAT-PMP answers
// it with NAT-PMP's "unsupported version" (RFC 6887 section 9), which is
// no PCP router.
func (f finder) findPCP(ctx context.Context, lan, gateway net.IP) *pcpRouter {
	if lan == nil {
		return nil // PCP requests carry this computer's address
	}
	r := &pcpRouter{addr: &net.UDPAddr{IP: gateway, Port: f.pmpPort}, lan: lan, try: f.try, tries: f.tries}
	b, err := r.exchange(ctx, r.header(pcpOpAnnounce, 0), pcpOpAnnounce)
	if err != nil || b[0] != pcpVersion {
		return nil
	}
	return r
}

// header is the common start of a request (RFC 6887 section 7.1): version,
// opcode, lifetime and the client's address, written as IPv6 (an IPv4
// address mapped into it).
func (r *pcpRouter) header(op byte, lifetime uint32) []byte {
	b := make([]byte, pcpHeader)
	b[0], b[1] = pcpVersion, op
	binary.BigEndian.PutUint32(b[4:8], lifetime)
	copy(b[8:24], r.lan.To16())
	return b
}

// exchange sends a request and waits for its answer, or for a NAT-PMP
// router's "unsupported version".
func (r *pcpRouter) exchange(ctx context.Context, req []byte, op byte) ([]byte, error) {
	return exchange(ctx, r.addr, req, func(b []byte) bool {
		switch {
		case len(b) >= pcpHeader && b[0] == pcpVersion && b[1] == 0x80|op:
			return true
		case len(b) >= 4 && b[0] == 0 && b[1] == 128+op:
			return true // NAT-PMP's answer to a version it doesn't speak
		}
		return false
	}, r.try, r.tries)
}

// pcpResultError is a PCP answer that isn't success.
type pcpResultError byte

func (e pcpResultError) Error() string {
	switch e {
	case 1:
		return "the router doesn't speak this version of PCP"
	case 2:
		return "the router refused (port mapping is turned off for this computer)"
	case 7:
		return "the router isn't connected to the internet yet"
	case 8:
		return "the router has no room for more port mappings"
	case 9:
		return "the router can't forward that protocol"
	case 10:
		return "the router won't make more mappings for this computer"
	case 11:
		return "the router can't give that port"
	case 12:
		return "the router sees this computer at another address (something between them changes it)"
	}
	return fmt.Sprintf("the router refused (PCP result %d)", byte(e))
}

// nonce is the mapping's own 12 bytes: the same for the same mapping from
// this computer, every time.
func (r *pcpRouter) nonce(m Mapping) []byte {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s %s %d", r.lan, m.Proto, m.Port))
	return sum[:12]
}

// pcpMap asks for a mapping (lifetime 0 removes it) and answers the
// external port and address the router gave.
func (r *pcpRouter) pcpMap(ctx context.Context, m Mapping, lifetime uint32) (int, net.IP, error) {
	var proto byte
	switch m.Proto {
	case "tcp":
		proto = 6
	case "udp":
		proto = 17
	default:
		return 0, nil, fmt.Errorf("PCP can't forward %q", m.Proto)
	}
	req := append(r.header(pcpOpMap, lifetime), make([]byte, pcpMapSize)...)
	p := req[pcpHeader:]
	nonce := r.nonce(m)
	copy(p[0:12], nonce)
	p[12] = proto
	binary.BigEndian.PutUint16(p[16:18], uint16(m.Port))
	if lifetime > 0 {
		binary.BigEndian.PutUint16(p[18:20], uint16(m.Port)) // the same port outside, please
	}
	copy(p[20:36], net.IPv4zero.To16()) // any internet address of the router's
	b, err := r.exchange(ctx, req, pcpOpMap)
	if err != nil {
		return 0, nil, err
	}
	if b[0] != pcpVersion {
		return 0, nil, pcpResultError(1)
	}
	if res := b[3]; res != 0 {
		return 0, nil, pcpResultError(res)
	}
	if len(b) < pcpHeader+pcpMapSize {
		return 0, nil, errors.New("the router's PCP answer is too short")
	}
	a := b[pcpHeader:]
	if string(a[0:12]) != string(nonce) || a[12] != proto || int(binary.BigEndian.Uint16(a[16:18])) != m.Port {
		return 0, nil, errors.New("the router answered for another mapping")
	}
	ext := net.IP(append([]byte(nil), a[20:36]...)).To4()
	if ext != nil && !ext.IsUnspecified() {
		r.mu.Lock()
		r.external = ext
		r.mu.Unlock()
	}
	return int(binary.BigEndian.Uint16(a[18:20])), ext, nil
}

// ExternalIP is the address the router's last MAP answer gave. Before the
// first mapping, PCP has no request that only asks for it, so the router is
// asked over NAT-PMP, which most PCP routers also speak.
func (r *pcpRouter) ExternalIP(ctx context.Context) (net.IP, error) {
	r.mu.Lock()
	ext := r.external
	r.mu.Unlock()
	if ext != nil {
		return ext, nil
	}
	pmp := &natpmpRouter{addr: r.addr, try: r.try, tries: r.tries}
	ip, err := pmp.ExternalIP(ctx)
	if err != nil {
		return nil, errors.New("the router doesn't say its internet address until it forwards a port")
	}
	return ip, nil
}

// Map asks for the same port outside as inside. When the router gives
// another port, it doesn't count (friends would need to be told it), so it
// is taken back and the answer is ErrConflict.
func (r *pcpRouter) Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error {
	if lease <= 0 {
		lease = 2 * time.Hour // 0 would remove it
	}
	got, _, err := r.pcpMap(ctx, m, uint32(lease/time.Second))
	if err != nil {
		return err
	}
	if got != m.Port {
		r.pcpMap(ctx, m, 0)
		return fmt.Errorf("%w (the router offered port %d instead)", ErrConflict, got)
	}
	return nil
}

// Unmap removes the mapping: a MAP request for it with lifetime 0.
func (r *pcpRouter) Unmap(ctx context.Context, lan net.IP, m Mapping) error {
	_, _, err := r.pcpMap(ctx, m, 0)
	return err
}
