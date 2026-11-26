//go:build windows

package platform

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")

const (
	tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	udpTableOwnerPID         = 1 // UDP_TABLE_OWNER_PID
	afInet                   = 2
	afInet6                  = 23
)

// PortInUse says whether a program has this TCP or UDP port open, and
// which one (its process ID, 0 when unknown). It reads Windows' tables of
// open ports, with their owners, instead of trying the port, which would
// make Windows Firewall ask about this app. For TCP only listening
// sockets count.
func PortInUse(proto string, port int) (inUse bool, pid int) {
	if port <= 0 || port > 65535 {
		return false, 0
	}
	type source struct {
		proc  *windows.LazyProc
		af    uintptr
		class uintptr
		table ownerTable
	}
	var sources []source
	switch strings.ToLower(proto) {
	case "tcp":
		sources = []source{
			{procGetExtendedTcpTable, afInet, tcpTableOwnerPIDListener, tcp4Table},
			{procGetExtendedTcpTable, afInet6, tcpTableOwnerPIDListener, tcp6Table},
		}
	case "udp":
		sources = []source{
			{procGetExtendedUdpTable, afInet, udpTableOwnerPID, udp4Table},
			{procGetExtendedUdpTable, afInet6, udpTableOwnerPID, udp6Table},
		}
	default:
		return false, 0
	}
	for _, s := range sources {
		buf, ok := extendedTable(s.proc, s.af, s.class)
		if !ok {
			continue
		}
		if found, owner := s.table.find(buf, port); found {
			return true, owner
		}
	}
	return false, 0
}

// extendedTable fills a buffer from GetExtendedTcpTable or
// GetExtendedUdpTable. The table can grow between asking for its size and
// reading it, so it tries a few times with room to spare.
func extendedTable(proc *windows.LazyProc, af, class uintptr) ([]byte, bool) {
	if proc.Find() != nil {
		return nil, false
	}
	var size uint32
	proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, af, class, 0)
	for try := 0; try < 4; try++ {
		if size == 0 {
			size = 4096
		}
		buf := make([]byte, size+4096)
		size = uint32(len(buf))
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, af, class, 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:min(int(size), len(buf))], true
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue // size now says how much it needs
		default:
			return nil, false
		}
	}
	return nil, false
}
