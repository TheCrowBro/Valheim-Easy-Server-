//go:build windows

package platform

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DefaultGateway is the router's IPv4 address, the one Windows sends
// everything outside the home network to. The router's port mapping
// (NAT-PMP, PCP, UPnP) and the check that the network is back after a
// wake talk to it over IPv4. With several connections up (a cable and
// Wi-Fi), the one Windows prefers wins: the lowest IPv4 metric.
func DefaultGateway() (net.IP, error) {
	size := uint32(15000)
	var buf []byte
	for try := 0; ; try++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|
				windows.GAA_FLAG_SKIP_DNS_SERVER|windows.GAA_FLAG_SKIP_FRIENDLY_NAME,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		// The list can grow between asking for its size and reading it.
		if err != windows.ERROR_BUFFER_OVERFLOW || try >= 3 {
			return nil, errNoGateway
		}
	}
	var best net.IP
	var bestMetric uint32
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			ip := g.Address.IP().To4()
			if ip == nil || ip.IsUnspecified() {
				continue
			}
			if best == nil || a.Ipv4Metric < bestMetric {
				best = append(net.IP(nil), ip...) // buf goes away
				bestMetric = a.Ipv4Metric
			}
			break
		}
	}
	if best == nil {
		return nil, errNoGateway
	}
	return best, nil
}
