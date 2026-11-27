package routerports

// Finding a UPnP router: an SSDP search (UPnP Device Architecture 1.1,
// section 1.3) is a small HTTP-like message sent over UDP to the UPnP
// multicast group. Every device that matches answers this computer
// directly with the address of its description (LOCATION).

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
)

// searchTargets are what the search asks for. A router of IGD version 2
// must also answer a search for version 1, but some only answer for the
// connection service itself, so that is asked too.
var searchTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
}

// ssdpAnswer is one device's answer to a search.
type ssdpAnswer struct {
	from     net.IP // who answered
	location *url.URL
}

// search sends an SSDP search from lan (any address when nil) and collects
// the answers for f.wait. It stops early, half a second after the gateway
// has answered, since the gateway is the router that matters; another
// device that answers (a mesh point, a second router) is kept as a
// fallback for when the gateway can't be found. An answer whose
// description isn't on the device that answered is dropped, so no device
// on the network can send the app to another address.
func (f finder) search(ctx context.Context, lan, gateway net.IP) []ssdpAnswer {
	dst, err := net.ResolveUDPAddr("udp4", f.ssdp)
	if err != nil {
		return nil
	}
	var local *net.UDPAddr
	if lan != nil {
		local = &net.UDPAddr{IP: lan}
	}
	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return nil
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()) })
	defer stop()

	// The search also goes straight to the gateway (UPnP 1.1's unicast
	// search, which most routers answer): its answer is then a reply to a
	// packet this computer sent to the router, which a firewall on this
	// computer lets in even when it holds back answers to a multicast.
	var uni *net.UDPAddr
	if gateway != nil && f.ssdpUnicast != 0 {
		uni = &net.UDPAddr{IP: gateway, Port: f.ssdpUnicast}
	}
	send := func() {
		for _, st := range searchTargets {
			msg := func(host, mx string) []byte {
				return []byte("M-SEARCH * HTTP/1.1\r\n" +
					"HOST: " + host + "\r\n" +
					"MAN: \"ssdp:discover\"\r\n" + mx +
					"ST: " + st + "\r\n\r\n")
			}
			conn.WriteToUDP(msg("239.255.255.250:1900", "MX: 2\r\n"), dst)
			if uni != nil {
				conn.WriteToUDP(msg(net.JoinHostPort(gateway.String(), "1900"), ""), uni) // a unicast search has no MX
			}
		}
	}
	// UDP can lose a packet; the search goes out twice, as UPnP suggests.
	send()
	resend := time.AfterFunc(300*time.Millisecond, send)
	defer resend.Stop()

	deadline := time.Now().Add(f.wait)
	conn.SetReadDeadline(deadline)
	var out []ssdpAnswer
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	for errs := 0; ; {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			// The wait is over, or ctx ended. Windows also reports an ICMP
			// "port unreachable" from some device as an error on the next
			// read, which says nothing about the others: keep listening.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || ctx.Err() != nil || time.Now().After(deadline) {
				break
			}
			if errs++; errs > 20 {
				break
			}
			continue
		}
		a, ok := parseSSDP(buf[:n], from.IP)
		if !ok || seen[a.location.String()] {
			continue
		}
		seen[a.location.String()] = true
		if gateway != nil && a.from.Equal(gateway) {
			// The gateway first, then whatever else answered.
			out = append([]ssdpAnswer{a}, out...)
			if soon := time.Now().Add(500 * time.Millisecond); soon.Before(deadline) {
				deadline = soon
				conn.SetReadDeadline(deadline)
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// parseSSDP reads one answer to a search: an HTTP response with a LOCATION
// header on the device that sent it.
func parseSSDP(b []byte, from net.IP) (ssdpAnswer, bool) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		return ssdpAnswer{}, false
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ssdpAnswer{}, false
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Scheme != "http" || !sameHost(loc, from) {
		return ssdpAnswer{}, false
	}
	return ssdpAnswer{from: from.To4(), location: loc}, true
}

// sameHost reports whether u's host is the address ip.
func sameHost(u *url.URL, ip net.IP) bool {
	h := net.ParseIP(u.Hostname())
	return h != nil && ip != nil && h.Equal(ip)
}
