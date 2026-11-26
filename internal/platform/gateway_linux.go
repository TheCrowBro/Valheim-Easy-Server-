//go:build !windows && !darwin

package platform

import (
	"net"
	"os"
)

// DefaultGateway is the router's IPv4 address, from the kernel's routing
// table (Linux only runs the tests).
func DefaultGateway() (net.IP, error) {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil, errNoGateway
	}
	if ip, ok := parseProcNetRoute(string(b)); ok {
		return ip, nil
	}
	return nil, errNoGateway
}
