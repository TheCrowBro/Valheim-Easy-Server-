package routerports

import (
	"net"
	"testing"
)

// The manual steps (spec 6.5), with the router's and this computer's
// addresses filled in.
func TestManualSteps(t *testing.T) {
	gw, lan := net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.20")
	for _, c := range []struct {
		ports []Mapping
		extra string
		gw    net.IP
		lan   net.IP
		want  string
	}{
		{[]Mapping{minecraftGame}, "", gw, lan,
			"Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), " +
				"and forward TCP 25565 to 192.168.1.20 (this computer). If you already forwarded this port, you can ignore this."},
		{[]Mapping{valheimGame, {Proto: "udp", Port: 2457}}, "", gw, lan,
			"Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), " +
				"and forward UDP 2456 and UDP 2457 to 192.168.1.20 (this computer). If you already forwarded these ports, you can ignore this."},
		{[]Mapping{{Proto: "udp", Port: 7777}, {Proto: "tcp", Port: 7777}, {Proto: "tcp", Port: 8888}},
			"Your router must allow UPnP for NAT punch-through.", gw, lan,
			"Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), " +
				"and forward TCP and UDP 7777 and TCP 8888 to 192.168.1.20 (this computer). " +
				"Your router must allow UPnP for NAT punch-through. If you already forwarded these ports, you can ignore this."},
		{[]Mapping{minecraftGame}, "", nil, nil,
			"Open your router's page in a web browser, find Port Forwarding (or Virtual Servers, or NAT), " +
				"and forward TCP 25565 to this computer. If you already forwarded this port, you can ignore this."},
	} {
		if got := ManualSteps(c.gw, c.lan, c.ports, c.extra); got != c.want {
			t.Errorf("ManualSteps(%v)\n got %q\nwant %q", c.ports, got, c.want)
		}
	}
}

func TestNATTexts(t *testing.T) {
	want := "Your internet provider shares one internet address between homes, so friends outside can't reach a server here. " +
		"Ask your provider for a public IPv4 address. Games that join with a code or Game ID (Valheim with crossplay, Core Keeper, Windrose) still work."
	if CGNATText != want {
		t.Errorf("CGNATText = %q", CGNATText)
	}
	ports := []Mapping{valheimGame, {Proto: "udp", Port: 2457}}
	if got, want := DoubleNATText(net.ParseIP("192.168.0.2"), net.ParseIP("203.0.113.7"), ports),
		"Your router is connected through another router (often the box from your internet provider), so friends outside "+
			"can't reach the ports it opens. On that box, forward UDP 2456 and UDP 2457 to 192.168.0.2 (your router), "+
			"or set the box to bridge mode. If you already did, you can ignore this."; got != want {
		t.Errorf("double NAT (private):\n got %q\nwant %q", got, want)
	}
	if got, want := DoubleNATText(net.ParseIP("198.51.100.4"), net.ParseIP("203.0.113.7"), ports),
		"Your router says its internet address is 198.51.100.4, but the internet sees this home as 203.0.113.7, "+
			"so something between them changes it (another router, or a VPN on this computer). "+
			"Friends outside may not reach the ports your router opens."; got != want {
		t.Errorf("double NAT (two addresses):\n got %q\nwant %q", got, want)
	}
}
