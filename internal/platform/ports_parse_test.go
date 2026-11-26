package platform

import (
	"encoding/binary"
	"net"
	"os"
	"reflect"
	"testing"
)

// tableBuf builds a buffer as Windows' GetExtended{Tcp,Udp}Table fills
// it: the count, then the rows.
func tableBuf(t ownerTable, rows ...func(row []byte)) []byte {
	buf := make([]byte, 4+len(rows)*t.rowSize)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, fill := range rows {
		fill(buf[4+i*t.rowSize : 4+(i+1)*t.rowSize])
	}
	return buf
}

func row(t ownerTable, port, pid, state int) func([]byte) {
	return func(r []byte) {
		r[t.portOff], r[t.portOff+1] = byte(port>>8), byte(port) // network byte order
		r[t.portOff+2], r[t.portOff+3] = 0xAA, 0xBB              // the rest of the field means nothing
		binary.LittleEndian.PutUint32(r[t.pidOff:], uint32(pid))
		if t.stateOff >= 0 {
			binary.LittleEndian.PutUint32(r[t.stateOff:], uint32(state))
		}
	}
}

func TestOwnerTables(t *testing.T) {
	const timeWait, established = 11, 5
	for name, c := range map[string]struct {
		table ownerTable
		state int
	}{
		"tcp4": {tcp4Table, mibTCPStateListen},
		"tcp6": {tcp6Table, mibTCPStateListen},
		"udp4": {udp4Table, 0},
		"udp6": {udp6Table, 0},
	} {
		buf := tableBuf(c.table,
			row(c.table, 2456, 100, c.state),
			row(c.table, 25565, 4242, c.state),
			row(c.table, 0x1E61, 7, c.state), // 7777
		)
		if found, pid := c.table.find(buf, 25565); !found || pid != 4242 {
			t.Errorf("%s: 25565 = %v %d", name, found, pid)
		}
		if found, pid := c.table.find(buf, 7777); !found || pid != 7 {
			t.Errorf("%s: 7777 = %v %d", name, found, pid)
		}
		if found, _ := c.table.find(buf, 0x611E); found {
			t.Errorf("%s: read the port in the wrong byte order", name)
		}
		if found, _ := c.table.find(buf, 9999); found {
			t.Errorf("%s: found a port nobody has", name)
		}
		if found, _ := c.table.find(buf[:len(buf)-1], 7777); found {
			t.Errorf("%s: read past the end of a short buffer", name)
		}
		if found, _ := c.table.find(nil, 7777); found {
			t.Errorf("%s: found a port in nothing", name)
		}
	}
	// Only a listening TCP socket stops a game listening: not one left
	// waiting after a server stopped, or a connection.
	for _, table := range []ownerTable{tcp4Table, tcp6Table} {
		buf := tableBuf(table, row(table, 7777, 0, timeWait), row(table, 7777, 55, established))
		if found, _ := table.find(buf, 7777); found {
			t.Error("a connection counted as the port in use")
		}
	}
}

func TestParseLsofPIDs(t *testing.T) {
	if got := parseLsofPIDs("p4242\nf12\np77\n\n"); !reflect.DeepEqual(got, []int{4242, 77}) {
		t.Errorf("got %v", got)
	}
	if got := parseLsofPIDs(""); got != nil {
		t.Errorf("got %v from nothing", got)
	}
}

func TestParseNetstatPort(t *testing.T) {
	tcp := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  192.168.1.20.52344     17.57.146.20.5223      ESTABLISHED
tcp46      0      0  *.25565                *.*                    LISTEN
tcp4       0      0  127.0.0.1.17777        *.*                    LISTEN
tcp6       0      0  ::1.7000               *.*                    LISTEN
`
	for port, want := range map[int]bool{25565: true, 17777: true, 7777: false, 7000: true, 52344: false, 5223: false} {
		if got := parseNetstatPort(tcp, "tcp", port); got != want {
			t.Errorf("tcp %d: %v", port, got)
		}
	}
	udp := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
udp4       0      0  *.2456                 *.*
udp6       0      0  fe80::1%lo0.123        *.*
udp46      0      0  *.5353                 *.*
`
	for port, want := range map[int]bool{2456: true, 123: true, 5353: true, 2457: false} {
		if got := parseNetstatPort(udp, "udp", port); got != want {
			t.Errorf("udp %d: %v", port, got)
		}
	}
	if parseNetstatPort(udp, "tcp", 2456) {
		t.Error("a UDP socket counted for TCP")
	}
}

func TestParseProcNet(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1E61 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 41234 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1E61 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 41299 1 0000000000000000 20 4 30 10 -1
   2: 00000000:63DD 00000000:0000 06 00000000:00000000 03:00000ABC 00000000     0        0 0 3 0000000000000000
`
	if in, found := parseProcNet(tcp, true, 7777); !found || !reflect.DeepEqual(in, []string{"41234"}) {
		t.Errorf("7777: %v %v", in, found)
	}
	if _, found := parseProcNet(tcp, true, 25565); found {
		t.Error("a socket waiting after a stop (TIME_WAIT) counted as listening")
	}
	udp := `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  511: 00000000:0998 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 50001 2 0000000000000000 0
`
	if in, found := parseProcNet(udp, false, 2456); !found || !reflect.DeepEqual(in, []string{"50001"}) {
		t.Errorf("udp 2456: %v %v", in, found)
	}
}

// PortInUse finds this test's own listeners, by its process ID, and
// nothing once they're closed.
func TestPortInUseOwnPorts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp := l.Addr().(*net.TCPAddr).Port
	u, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp := u.LocalAddr().(*net.UDPAddr).Port
	for _, c := range []struct {
		proto string
		port  int
	}{{"tcp", tcp}, {"TCP", tcp}, {"udp", udp}, {"UDP", udp}} {
		if inUse, pid := PortInUse(c.proto, c.port); !inUse || pid != os.Getpid() {
			t.Errorf("PortInUse(%q, %d) = %v, %d; want true, %d", c.proto, c.port, inUse, pid, os.Getpid())
		}
	}
	l.Close()
	u.Close()
	if inUse, pid := PortInUse("tcp", tcp); inUse {
		t.Errorf("TCP %d in use by %d after closing", tcp, pid)
	}
	if inUse, pid := PortInUse("udp", udp); inUse {
		t.Errorf("UDP %d in use by %d after closing", udp, pid)
	}
	for _, c := range []struct {
		proto string
		port  int
	}{{"sctp", tcp}, {"tcp", 0}, {"udp", -1}, {"udp", 65536}} {
		if inUse, pid := PortInUse(c.proto, c.port); inUse || pid != 0 {
			t.Errorf("PortInUse(%q, %d) = %v, %d", c.proto, c.port, inUse, pid)
		}
	}
}
