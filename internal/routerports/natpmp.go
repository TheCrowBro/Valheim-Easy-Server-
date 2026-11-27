package routerports

// NAT-PMP (RFC 6886): small binary requests over UDP to the gateway's port
// 5351. Apple's routers and many others speak it. A request is sent again,
// waiting twice as long each time, until the router answers.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// NAT-PMP's opcodes.
const (
	pmpOpExternal = 0
	pmpOpUDP      = 1
	pmpOpTCP      = 2
)

// natpmpRouter is a router that speaks NAT-PMP.
type natpmpRouter struct {
	addr  *net.UDPAddr // gateway:5351
	try   time.Duration
	tries int
}

func (r *natpmpRouter) Kind() string { return "natpmp" }

// findNATPMP asks the gateway for its internet address; a router that
// answers speaks NAT-PMP.
func (f finder) findNATPMP(ctx context.Context, gateway net.IP) *natpmpRouter {
	r := &natpmpRouter{addr: &net.UDPAddr{IP: gateway, Port: f.pmpPort}, try: f.try, tries: f.tries}
	if _, err := r.ExternalIP(ctx); err != nil {
		var re pmpResultError
		if !errors.As(err, &re) {
			return nil // nothing answered
		}
		// It answered with a refusal (its line may be down): it still speaks
		// NAT-PMP, and says why when asked to map.
	}
	return r
}

// pmpResultError is a NAT-PMP answer that isn't success.
type pmpResultError uint16

func (e pmpResultError) Error() string {
	switch e {
	case 1:
		return "the router doesn't speak this version of NAT-PMP"
	case 2:
		return "the router refused (port mapping is turned off for this computer)"
	case 3:
		return "the router isn't connected to the internet yet"
	case 4:
		return "the router has no room for more port mappings"
	case 5:
		return "the router doesn't support that NAT-PMP request"
	}
	return fmt.Sprintf("the router refused (NAT-PMP result %d)", uint16(e))
}

// exchange sends req to addr and waits for the answer to it: a packet that
// match accepts. It sends again after try, 2×try… for tries sends. Only
// packets from addr count (the socket is connected to it).
func exchange(ctx context.Context, addr *net.UDPAddr, req []byte, match func([]byte) bool, try time.Duration, tries int) ([]byte, error) {
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, 1100) // PCP's largest message
	wait := try
	for i := 0; i < tries && ctx.Err() == nil; i++ {
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		until := time.Now().Add(wait)
		if d, ok := ctx.Deadline(); ok && d.Before(until) {
			until = d
		}
		conn.SetReadDeadline(until)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break // nothing yet (or the port is closed): send again
			}
			if match(buf[:n]) {
				return append([]byte(nil), buf[:n]...), nil
			}
			// Anything else (an answer to an older request, or a router's
			// announcement) is skipped.
		}
		wait *= 2
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errNoAnswer
}

var errNoAnswer = errors.New("the router didn't answer")

// pmpAnswer matches a NAT-PMP answer to opcode op of at least size bytes.
func pmpAnswer(op byte, size int) func([]byte) bool {
	return func(b []byte) bool { return len(b) >= size && b[0] == 0 && b[1] == 128+op }
}

func (r *natpmpRouter) ExternalIP(ctx context.Context) (net.IP, error) {
	b, err := exchange(ctx, r.addr, []byte{0, pmpOpExternal}, pmpAnswer(pmpOpExternal, 12), r.try, r.tries)
	if err != nil {
		return nil, err
	}
	if res := binary.BigEndian.Uint16(b[2:4]); res != 0 {
		return nil, pmpResultError(res)
	}
	return net.IPv4(b[8], b[9], b[10], b[11]).To4(), nil
}

// pmpMap asks for a mapping of port for lease (0 removes it). It answers
// the external port and lease the router gave.
func (r *natpmpRouter) pmpMap(ctx context.Context, proto string, port, external int, lease time.Duration) (int, error) {
	op := byte(pmpOpUDP)
	switch proto {
	case "udp":
	case "tcp":
		op = pmpOpTCP
	default:
		return 0, fmt.Errorf("NAT-PMP can't forward %q", proto)
	}
	req := make([]byte, 12)
	req[1] = op
	binary.BigEndian.PutUint16(req[4:6], uint16(port))
	binary.BigEndian.PutUint16(req[6:8], uint16(external))
	binary.BigEndian.PutUint32(req[8:12], uint32(lease/time.Second))
	b, err := exchange(ctx, r.addr, req, pmpAnswer(op, 16), r.try, r.tries)
	if err != nil {
		return 0, err
	}
	if res := binary.BigEndian.Uint16(b[2:4]); res != 0 {
		return 0, pmpResultError(res)
	}
	if int(binary.BigEndian.Uint16(b[8:10])) != port {
		return 0, errors.New("the router answered for another port")
	}
	return int(binary.BigEndian.Uint16(b[10:12])), nil
}

// Map asks for the same port outside as inside. NAT-PMP mappings belong to
// the computer that asks, so lan isn't sent: the router sees it. When the
// port is taken, the router gives another one, which doesn't count:
// friends would need to be told a different port. So that one is taken
// back and the answer is ErrConflict. (NAT-PMP can't say whether the port
// is forwarded to this computer by hand.)
func (r *natpmpRouter) Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error {
	if lease <= 0 {
		lease = 2 * time.Hour // 0 would remove it
	}
	got, err := r.pmpMap(ctx, m.Proto, m.Port, m.Port, lease)
	if err != nil {
		return err
	}
	if got != m.Port {
		r.pmpMap(ctx, m.Proto, m.Port, 0, 0)
		return fmt.Errorf("%w (the router offered port %d instead)", ErrConflict, got)
	}
	return nil
}

// Unmap removes this computer's mapping of the port: a lease of 0 with
// external port 0 (RFC 6886 section 3.4).
func (r *natpmpRouter) Unmap(ctx context.Context, lan net.IP, m Mapping) error {
	_, err := r.pmpMap(ctx, m.Proto, m.Port, 0, 0)
	return err
}
