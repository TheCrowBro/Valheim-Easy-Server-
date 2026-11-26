package platform

// Reading the default gateway (the home router) from what macOS and Linux
// print. Each is used on one system only, but they're plain parsers, so
// their tests run everywhere.

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
)

// errNoGateway is what DefaultGateway says when this computer has no
// IPv4 router: the network isn't up, or it has no way out.
var errNoGateway = errors.New("this computer has no default gateway (router) right now")

// parseRouteGetDefault reads macOS's `route -n get default`:
//
//	   route to: default
//	destination: default
//	       mask: default
//	    gateway: 192.168.1.1
//	  interface: en0
//
// The gateway can also be a link ("link#4") for some VPNs, which is no
// router to talk to, so only an IPv4 address counts.
func parseRouteGetDefault(out string) (net.IP, bool) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != "gateway" {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(v)).To4(); ip != nil && !ip.IsUnspecified() {
			return ip, true
		}
		return nil, false
	}
	return nil, false
}

// parseProcNetRoute reads Linux's /proc/net/route and picks the default
// route (destination and mask 0, flags up and gateway) with the lowest
// metric. Addresses are hex in the kernel's (little-endian) byte order.
//
//	Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
//	eth0	00000000	0101A8C0	0003	0	0	100	00000000	0	0	0
func parseProcNetRoute(out string) (net.IP, bool) {
	const rtfUp, rtfGateway = 0x1, 0x2
	var best net.IP
	bestMetric := uint64(0)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&(rtfUp|rtfGateway) != rtfUp|rtfGateway {
			continue
		}
		gw, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || gw == 0 {
			continue
		}
		metric, _ := strconv.ParseUint(f[6], 10, 32)
		if best != nil && metric >= bestMetric {
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, uint32(gw))
		best, bestMetric = ip, metric
	}
	return best, best != nil
}
