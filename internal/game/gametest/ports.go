package gametest

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// pickPorts chooses free ports for an instance's roles, so sessions can run
// side by side and next to whatever else the computer runs. A tied role
// ("game+1") gets its base role's port plus the offset, as the manager
// gives it (spec 2.5). With real, a port must also be free on every
// address, since real servers listen on all of them.
func pickPorts(roles []game.PortRole, real bool) (map[string]int, error) {
try:
	for range 50 {
		out := map[string]int{}
		taken := map[int]bool{}
		for _, tied := range []bool{false, true} {
			for _, r := range roles {
				if (r.Tie != "") != tied {
					continue
				}
				var port int
				var err error
				if tied {
					port, err = tiedPort(r.Tie, out)
				} else {
					port, err = anyFreePort()
				}
				if err != nil {
					return nil, fmt.Errorf("role %q: %v", r.Role, err)
				}
				if port > 65535 || taken[port] || !portFree(r.Protos, port, real) {
					continue try
				}
				out[r.Role] = port
				taken[port] = true
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("found no free ports for %d roles", len(roles))
}

// tiedPort reads "game+1": the game role's port plus one.
func tiedPort(tie string, ports map[string]int) (int, error) {
	base, off, _ := strings.Cut(tie, "+")
	n, err := strconv.Atoi(off)
	if err != nil {
		return 0, fmt.Errorf("tie %q isn't <role>+<number>", tie)
	}
	p, ok := ports[base]
	if !ok {
		return 0, fmt.Errorf("tie %q names no role without a tie", tie)
	}
	return p + n, nil
}

// anyFreePort asks the system for a free port.
func anyFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// portFree says whether every protocol can be opened on the port.
func portFree(protos []string, port int, real bool) bool {
	for _, p := range protos {
		if bindTaken(p, port, real) {
			return false
		}
	}
	return true
}

// bindTaken says whether this process can't open the port: on 127.0.0.1,
// where stand-ins listen, and with real also on every address. Opening the
// very address the server uses is what tells on every system (macOS lets a
// wildcard and a specific address share a TCP port).
func bindTaken(proto string, port int, real bool) bool {
	addrs := []string{"127.0.0.1"}
	if real {
		addrs = append(addrs, "")
	}
	for _, host := range addrs {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		if strings.EqualFold(proto, "udp") {
			c, err := net.ListenPacket("udp", addr)
			if err != nil {
				return true
			}
			c.Close()
		} else {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				return true
			}
			l.Close()
		}
	}
	return false
}

var platformPorts struct {
	once  sync.Once
	works bool
}

// platformSeesPorts says whether platform.PortInUse finds ports in use on
// this system: it is asked about two ports this process holds. Until stream
// P's version lands it says "don't know" for every port, and the checks
// rely on bindTaken alone; afterwards both must agree.
func platformSeesPorts() bool {
	platformPorts.once.Do(func() {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return
		}
		defer l.Close()
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return
		}
		defer c.Close()
		tcp, _ := platform.PortInUse("tcp", l.Addr().(*net.TCPAddr).Port)
		udp, _ := platform.PortInUse("udp", c.LocalAddr().(*net.UDPAddr).Port)
		platformPorts.works = tcp && udp
	})
	return platformPorts.works
}
