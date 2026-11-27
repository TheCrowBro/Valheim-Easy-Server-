package routerports

// The words for the owner when the app can't open a game's ports itself
// (spec 6.5): what to do on the router's own page, with the router's and
// this computer's addresses filled in, and what carrier-grade or double NAT
// means for them.

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// CGNATText explains carrier-grade NAT, where forwarding ports can't help.
const CGNATText = "Your internet provider shares one internet address between homes, so friends outside can't reach a server here. " +
	"Ask your provider for a public IPv4 address. " +
	"Games that join with a code or Game ID (Valheim with crossplay, Core Keeper, Windrose) still work."

// ManualSteps tells the owner how to forward the ports on the router by
// hand: "Open your router's page (usually http://192.168.1.1/), find Port
// Forwarding (or Virtual Servers, or NAT), and forward TCP 25565 to
// 192.168.1.20 (this computer)." gateway and lan fill in the addresses
// (nil when unknown); extra is the game's own words (game.Join.Manual),
// put after the steps.
func ManualSteps(gateway, lan net.IP, ports []Mapping, extra string) string {
	page := "Open your router's page in a web browser"
	if gw := gateway.To4(); gw != nil {
		page = "Open your router's page (usually http://" + gw.String() + "/)"
	}
	to := "this computer"
	if ip := lan.To4(); ip != nil {
		to = ip.String() + " (this computer)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, find Port Forwarding (or Virtual Servers, or NAT), and forward %s to %s.", page, PortList(ports), to)
	if extra = strings.TrimSpace(extra); extra != "" {
		b.WriteString(" " + extra)
	}
	if countPorts(ports) == 1 {
		b.WriteString(" If you already forwarded this port, you can ignore this.")
	} else {
		b.WriteString(" If you already forwarded these ports, you can ignore this.")
	}
	return b.String()
}

// DoubleNATText explains what NAT found "double" means here: the router's
// own internet address (external) isn't this home's address on the
// internet (public, nil when unknown), so the ports it opens don't reach
// friends outside until the router in front of it forwards them too.
func DoubleNATText(external, public net.IP, ports []Mapping) string {
	ext := external.To4()
	if ext == nil {
		return ""
	}
	if pub := public.To4(); pub != nil && !ext.IsPrivate() && !cgnatNet.Contains(ext) {
		// Both are internet addresses, but not the same one.
		return fmt.Sprintf("Your router says its internet address is %s, but the internet sees this home as %s, "+
			"so something between them changes it (another router, or a VPN on this computer). "+
			"Friends outside may not reach the ports your router opens.", ext, pub)
	}
	what := "the ports"
	if len(ports) > 0 {
		what = PortList(ports)
	}
	return fmt.Sprintf("Your router is connected through another router (often the box from your internet provider), "+
		"so friends outside can't reach the ports it opens. On that box, forward %s to %s (your router), "+
		"or set the box to bridge mode. If you already did, you can ignore this.", what, ext)
}

// PortList writes ports as the owner reads them on a router's page: "TCP
// 25565", "UDP 2456 and UDP 2457", "TCP and UDP 7777 and TCP 8888". Ports
// are in the order given; a port asked for with both protocols is named
// once.
func PortList(ports []Mapping) string {
	type entry struct {
		port   int
		protos []string
	}
	var list []*entry
	for _, m := range ports {
		i := slices.IndexFunc(list, func(e *entry) bool { return e.port == m.Port })
		if i < 0 {
			list = append(list, &entry{port: m.Port})
			i = len(list) - 1
		}
		p := strings.ToUpper(m.Proto)
		if !slices.Contains(list[i].protos, p) {
			list[i].protos = append(list[i].protos, p)
		}
	}
	parts := make([]string, 0, len(list))
	for _, e := range list {
		slices.Sort(e.protos) // "TCP and UDP"
		parts = append(parts, strings.Join(e.protos, " and ")+" "+strconv.Itoa(e.port))
	}
	switch len(parts) {
	case 0:
		return "the game's ports"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// countPorts is how many different ports there are in ports.
func countPorts(ports []Mapping) int {
	seen := map[int]bool{}
	for _, m := range ports {
		seen[m.Port] = true
	}
	return len(seen)
}
