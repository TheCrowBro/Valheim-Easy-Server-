//go:build darwin

package platform

import (
	"net"
	"os/exec"
)

// NetAdapters lists the Mac's network ports, wired ones first, as macOS's
// Network settings know them.
func NetAdapters() []NetAdapter {
	out, err := exec.Command("/usr/sbin/networksetup", "-listallhardwareports").Output()
	if err != nil {
		return nil
	}
	womp := wakeForNetwork()
	ifaces := map[string]net.Interface{}
	if list, err := net.Interfaces(); err == nil {
		for _, ifc := range list {
			ifaces[ifc.Name] = ifc
		}
	}
	var wired, other []NetAdapter
	for _, p := range parseHardwarePorts(string(out)) {
		ifc, ok := ifaces[p.Device]
		if !ok || p.MAC == "" {
			continue
		}
		n := NetAdapter{Name: p.Port, Description: p.Device, MAC: p.MAC, Wired: wiredPort(p.Port)}
		if len(ifc.HardwareAddr) == 6 {
			n.MAC = formatMAC(ifc.HardwareAddr)
		}
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
					n.IPv4 = append(n.IPv4, ipn.IP.String())
				}
			}
		}
		if ifc.Flags&net.FlagUp != 0 {
			if b, err := exec.Command("/sbin/ifconfig", p.Device).Output(); err == nil {
				n.Connected = parseIfconfigActive(string(b))
			}
		}
		// "Wake for network access" is one setting for the whole Mac.
		n.WakeArmed = n.Wired && womp
		if n.Wired {
			wired = append(wired, n)
		} else {
			other = append(other, n)
		}
	}
	return append(wired, other...)
}

func wakeForNetwork() bool {
	out, err := exec.Command("/usr/bin/pmset", "-g").Output()
	if err != nil {
		return false
	}
	on, _ := parsePmsetWomp(string(out))
	return on
}

// EnableWakeOnLAN turns on "Wake for network access", so a Wake-on-LAN
// signal wakes the Mac. macOS asks for an administrator's password on the
// screen; this returns before that is answered.
func EnableWakeOnLAN() error {
	return runAdmin("/usr/bin/pmset -a womp 1",
		"Valheim Easy Server wants to turn on Wake for network access, so it can be woken up over the network.")
}
