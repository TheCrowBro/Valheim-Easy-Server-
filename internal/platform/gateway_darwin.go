//go:build darwin

package platform

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// DefaultGateway is the router's IPv4 address, the one this Mac sends
// everything outside the home network to. The router's port mapping
// (NAT-PMP, PCP, UPnP) and the check that the network is back after a
// wake talk to it over IPv4.
func DefaultGateway() (net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		// route fails when there is no default route at all.
		return nil, errNoGateway
	}
	if ip, ok := parseRouteGetDefault(string(out)); ok {
		return ip, nil
	}
	return nil, errNoGateway
}
