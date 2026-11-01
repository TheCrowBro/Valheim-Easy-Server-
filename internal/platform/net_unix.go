//go:build !windows && !darwin

package platform

import "net"

// NetAdapters lists the network interfaces that have a hardware address.
// Whether they may wake the computer is only known on Windows.
func NetAdapters() []NetAdapter {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []NetAdapter
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		n := NetAdapter{Name: ifc.Name, Description: ifc.Name, MAC: formatMAC(ifc.HardwareAddr),
			Wired: true, Connected: ifc.Flags&net.FlagUp != 0}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				n.IPv4 = append(n.IPv4, ipn.IP.String())
			}
		}
		out = append(out, n)
	}
	return out
}

// EnableWakeOnLAN is only implemented on Windows.
func EnableWakeOnLAN() error { return ErrUnsupported }
