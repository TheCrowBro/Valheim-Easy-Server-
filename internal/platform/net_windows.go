//go:build windows

package platform

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NetAdapters lists the computer's physical network adapters, wired ones
// first. Virtual adapters (VPNs, Hyper-V and so on) are left out because
// Wake-on-LAN can't use them.
func NetAdapters() []NetAdapter {
	size := uint32(15000)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW || size <= uint32(len(buf)) {
			return nil
		}
	}
	armed := wakeArmed()
	var wired, wireless []NetAdapter
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.IfType != windows.IF_TYPE_ETHERNET_CSMACD && a.IfType != windows.IF_TYPE_IEEE80211 {
			continue
		}
		desc := windows.UTF16PtrToString(a.Description)
		if a.PhysicalAddressLength != 6 || virtualAdapter(desc) {
			continue
		}
		n := NetAdapter{
			Name:        windows.UTF16PtrToString(a.FriendlyName),
			Description: desc,
			MAC:         formatMAC(a.PhysicalAddress[:6]),
			Wired:       a.IfType == windows.IF_TYPE_ETHERNET_CSMACD,
			Connected:   a.OperStatus == windows.IfOperStatusUp,
			WakeArmed:   armed[strings.ToLower(desc)],
		}
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			if ip := u.Address.IP(); ip != nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
				n.IPv4 = append(n.IPv4, ip.String())
			}
		}
		if n.Wired {
			wired = append(wired, n)
		} else {
			wireless = append(wireless, n)
		}
	}
	return append(wired, wireless...)
}

func virtualAdapter(desc string) bool {
	d := strings.ToLower(desc)
	for _, s := range []string{"virtual", "hyper-v", "vmware", "virtualbox", "vpn", "tap-", "wan miniport",
		"bluetooth", "loopback", "pseudo", "wireguard", "tailscale", "zerotier", "npcap", "kernel debug"} {
		if strings.Contains(d, s) {
			return true
		}
	}
	return false
}

// wakeArmed lists the devices Windows lets wake the computer (by their
// Device Manager names, lower-cased).
func wakeArmed() map[string]bool {
	cmd := exec.Command(system32("powercfg.exe"), "/devicequery", "wake_armed")
	cmd.SysProcAttr = HiddenProcAttr()
	out, err := cmd.Output()
	armed := map[string]bool{}
	if err != nil {
		return armed
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			armed[strings.ToLower(line)] = true
		}
	}
	return armed
}

// EnableWakeOnLAN lets the wired network adapters wake the computer when
// they receive a Wake-on-LAN "magic packet", and only then, so ordinary
// network traffic doesn't wake it. The adapters aren't restarted, so
// nobody playing is cut off; a few drivers only use the change after the
// computer restarts. Windows asks for admin permission on the screen; this
// returns before that is answered.
func EnableWakeOnLAN() error {
	return runElevatedPowerShell(strings.Join([]string{
		"$ErrorActionPreference = 'SilentlyContinue'",
		"Get-NetAdapter -Physical | Where-Object { $_.MediaType -eq '802.3' } | ForEach-Object {",
		"  Set-NetAdapterPowerManagement -Name $_.Name -WakeOnMagicPacket Enabled -WakeOnPattern Disabled -NoRestart",
		"  Set-NetAdapterAdvancedProperty -Name $_.Name -RegistryKeyword '*WakeOnMagicPacket' -RegistryValue 1 -NoRestart",
		"  Set-NetAdapterAdvancedProperty -Name $_.Name -RegistryKeyword '*WakeOnPattern' -RegistryValue 0 -NoRestart",
		"  powercfg /deviceenablewake \"$($_.InterfaceDescription)\"",
		"}",
	}, "\n"))
}
