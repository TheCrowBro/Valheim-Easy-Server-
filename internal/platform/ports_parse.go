package platform

// Reading the systems' tables of open ports for PortInUse. Each is used on
// one system only, but they're plain parsers, so their tests run
// everywhere.

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// ownerTable describes one of the tables Windows' GetExtendedTcpTable and
// GetExtendedUdpTable fill in: a 4-byte count, then rows of rowSize bytes.
// Each row keeps its local port in network byte order in the first two
// bytes of the 4-byte field at portOff, and its owner's process ID at
// pidOff. stateOff is where a TCP row keeps its state, -1 for UDP.
type ownerTable struct {
	rowSize, portOff, pidOff, stateOff int
}

// The row layouts, from Windows' MIB_TCPROW_OWNER_PID,
// MIB_TCP6ROW_OWNER_PID, MIB_UDPROW_OWNER_PID and MIB_UDP6ROW_OWNER_PID.
var (
	tcp4Table = ownerTable{rowSize: 24, portOff: 8, pidOff: 20, stateOff: 0}
	tcp6Table = ownerTable{rowSize: 56, portOff: 20, pidOff: 52, stateOff: 48}
	udp4Table = ownerTable{rowSize: 12, portOff: 4, pidOff: 8, stateOff: -1}
	udp6Table = ownerTable{rowSize: 28, portOff: 20, pidOff: 24, stateOff: -1}
)

// mibTCPStateListen is MIB_TCP_STATE_LISTEN.
const mibTCPStateListen = 2

// find looks for port in a table buffer. For TCP only listening rows
// count: a connection left waiting after a server stopped (TIME_WAIT) or
// a connection a program made doesn't stop a game listening on the port.
func (t ownerTable) find(buf []byte, port int) (found bool, pid int) {
	if len(buf) < 4 {
		return false, 0
	}
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n; i++ {
		row := 4 + i*t.rowSize
		if row+t.rowSize > len(buf) {
			break
		}
		r := buf[row : row+t.rowSize]
		if int(r[t.portOff])<<8|int(r[t.portOff+1]) != port {
			continue
		}
		if t.stateOff >= 0 && binary.LittleEndian.Uint32(r[t.stateOff:]) != mibTCPStateListen {
			continue
		}
		return true, int(binary.LittleEndian.Uint32(r[t.pidOff:]))
	}
	return false, 0
}

// parseLsofPIDs reads `lsof -F p`: a line "p<pid>" for each process.
func parseLsofPIDs(out string) []int {
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "p"); ok {
			if n, err := strconv.Atoi(rest); err == nil && n > 0 {
				pids = append(pids, n)
			}
		}
	}
	return pids
}

// parseNetstatPort reads macOS's `netstat -an -p tcp` or `-p udp`: whether
// a socket has this local port (for TCP, a listening one). netstat sees
// every user's sockets, where lsof sees only this user's.
//
//	Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
//	tcp46      0      0  *.25565                *.*                    LISTEN
//	udp4       0      0  127.0.0.1.7777         *.*
func parseNetstatPort(out, proto string, port int) bool {
	suffix := "." + strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], proto) || !strings.HasSuffix(f[3], suffix) {
			continue
		}
		if proto == "tcp" && (len(f) < 6 || f[5] != "LISTEN") {
			continue
		}
		return true
	}
	return false
}

// procNetLinuxListen is the state of a listening TCP socket in Linux's
// /proc/net/tcp (TCP_LISTEN).
const procNetLinuxListen = "0A"

// parseProcNet reads Linux's /proc/net/tcp, tcp6, udp or udp6: the socket
// inodes with this local port (for TCP, listening ones), which lead to
// the process holding them.
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 41234 …
func parseProcNet(out string, tcp bool, port int) (inodes []string, found bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || !strings.HasSuffix(f[0], ":") {
			continue
		}
		_, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		if p, err := strconv.ParseUint(hexPort, 16, 16); err != nil || int(p) != port {
			continue
		}
		if tcp && f[3] != procNetLinuxListen {
			continue
		}
		found = true
		if f[9] != "0" {
			inodes = append(inodes, f[9])
		}
	}
	return inodes, found
}
