package routerports

import (
	"net"
	"testing"
)

// Spec 6.5: carrier-grade NAT (the router's address is in 100.64.0.0/10)
// and double NAT (it is private, or isn't the address the internet sees)
// are told apart from a router with this home's own internet address.
func TestNAT(t *testing.T) {
	for _, c := range []struct {
		external, public, want string
	}{
		{"203.0.113.7", "203.0.113.7", ""},
		{"203.0.113.7", "", ""}, // the public address isn't known: nothing to say
		{"100.64.0.1", "203.0.113.7", "cgnat"},
		{"100.127.255.254", "", "cgnat"},
		{"100.128.0.1", "100.128.0.1", ""}, // just outside the shared space
		{"192.168.0.2", "203.0.113.7", "double"},
		{"10.20.30.40", "", "double"},
		{"172.16.5.4", "203.0.113.7", "double"},
		{"198.51.100.4", "203.0.113.7", "double"}, // two internet addresses: something in between
		{"203.0.113.7", "2001:db8::1", ""},        // an IPv6 public address says nothing about IPv4
		{"0.0.0.0", "203.0.113.7", ""},            // the router isn't connected yet
		{"", "203.0.113.7", ""},
	} {
		if got := NAT(net.ParseIP(c.external), net.ParseIP(c.public)); got != c.want {
			t.Errorf("NAT(%q, %q) = %q, want %q", c.external, c.public, got, c.want)
		}
	}
}
