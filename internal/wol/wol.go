// Package wol makes, sends and recognises Wake-on-LAN "magic packets": six
// 0xFF bytes, then the network adapter's address 16 times.
package wol

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Port is where Wake-on-LAN packets are sent.
const Port = 9

// ParseMAC reads a network adapter address such as 3C:07:54:5A:21:9F.
func ParseMAC(s string) (net.HardwareAddr, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("%q isn't a network adapter address (like 3C:07:54:5A:21:9F)", s)
	}
	return hw, nil
}

// Packet is the magic packet that wakes the adapter mac. A tag of up to 6
// bytes goes where a SecureOn password would; adapters without one set
// ignore it, and the app uses it to tell requests apart.
func Packet(mac net.HardwareAddr, tag []byte) []byte {
	p := bytes.Repeat([]byte{0xFF}, 6)
	for range 16 {
		p = append(p, mac...)
	}
	return append(p, tag[:min(len(tag), 6)]...)
}

// Target reports which adapter a magic packet is for, and its tag, if any.
func Target(b []byte) (mac net.HardwareAddr, tag []byte, ok bool) {
	if len(b) < 102 || !bytes.Equal(b[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		return nil, nil, false
	}
	m := b[6:12]
	for i := 1; i < 16; i++ {
		if !bytes.Equal(b[6+6*i:12+6*i], m) {
			return nil, nil, false
		}
	}
	return net.HardwareAddr(bytes.Clone(m)), bytes.Clone(b[102:min(len(b), 108)]), true
}

// Send sends the magic packet for mac to each address (host:port). It
// fails only if it couldn't be sent anywhere.
func Send(mac net.HardwareAddr, tag []byte, addrs []string) error {
	p := Packet(mac, tag)
	var errs []error
	sent := false
	for _, a := range addrs {
		conn, err := net.Dial("udp4", a) // Go allows broadcast on UDP sockets
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := conn.Write(p); err != nil {
			errs = append(errs, err)
		} else {
			sent = true
		}
		conn.Close()
	}
	if !sent {
		return fmt.Errorf("couldn't send the Wake-on-LAN packet: %w", errors.Join(errs...))
	}
	return nil
}

// Broadcasts lists where to send a magic packet so the whole local network
// hears it: 255.255.255.255 and each IPv4 network's own broadcast address.
func Broadcasts() []string {
	out := []string{fmt.Sprintf("255.255.255.255:%d", Port)}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			ip, mask := n.IP.To4(), net.IP(n.Mask).To4()
			if mask == nil {
				continue
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^mask[i]
			}
			out = append(out, net.JoinHostPort(b.String(), fmt.Sprint(Port)))
		}
	}
	return out
}

// Mine reports whether mac belongs to one of this computer's adapters.
func Mine(mac net.HardwareAddr) bool {
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if bytes.Equal(ifc.HardwareAddr, mac) {
			return true
		}
	}
	return false
}
