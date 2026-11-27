package gametest

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// Where gametest looks for free ports.
//
// Every system hands out "ephemeral" ports from a range of its own, both
// for a listen on port 0 and for the local end of every outgoing
// connection any program makes: 49152-65535 on Windows and macOS,
// 32768-60999 on Linux. A port picked from that range can be taken by
// somebody's outgoing connection in the moment between the pick and the
// server opening it, and an outgoing connection keeps its port for a
// while after it closes (TIME_WAIT, 30 to 60 seconds). That is how a CI
// run on macOS failed while other packages' tests, run at the same time,
// were making connections. So gametest picks from below all three ranges,
// where a port is only taken by a program that opens it on purpose, and
// checks each pick by opening it. cmd/fakeserver's tests pick from
// 30000-31999, so the two test programs, which go test runs at once,
// never pick the same port. A pick can still lose to some other program
// doing the same, so a start that fails on a stolen port is tried again
// (Session.retryOnNewPorts).
const (
	portsFrom = 20000
	portsTo   = 29990 // exclusive; a tied role ("game+2") stays below 30000
)

// claimed holds the ports this test program has given to sessions that
// are still alive, so sessions running at once never get the same port
// (opening a port to check it can't tell, until the first session's
// server has opened it).
var claimed = struct {
	sync.Mutex
	ports map[int]bool
}{ports: map[int]bool{}}

// pickPorts chooses free ports for an instance's roles, so sessions can run
// side by side and next to whatever else the computer runs. A tied role
// ("game+1") gets its base role's port plus the offset, as the manager
// gives it (spec 2.5). With real, a port must also be free on every
// address, since real servers listen on all of them. The ports are
// claimed until releasePorts gives them back.
func pickPorts(roles []game.PortRole, real bool) (map[string]int, error) {
	claimed.Lock()
	defer claimed.Unlock()
try:
	for i := range 200 {
		out := map[string]int{}
		taken := map[int]bool{}
		for _, tied := range []bool{false, true} {
			for _, r := range roles {
				if (r.Tie != "") != tied {
					continue
				}
				var port int
				var err error
				switch {
				case tied:
					port, err = tiedPort(r.Tie, out)
				case i < 150:
					port = portsFrom + rand.IntN(portsTo-portsFrom)
				default:
					// Nothing free below the ephemeral ranges: let the
					// system choose one from its own.
					port, err = anyFreePort()
				}
				if err != nil {
					return nil, fmt.Errorf("role %q: %v", r.Role, err)
				}
				if port > 65535 || taken[port] || claimed.ports[port] || !portFree(r.Protos, port, real) {
					continue try
				}
				out[r.Role] = port
				taken[port] = true
			}
		}
		for _, p := range out {
			claimed.ports[p] = true
		}
		return out, nil
	}
	return nil, fmt.Errorf("found no free ports for %d roles", len(roles))
}

// releasePorts gives back ports pickPorts claimed.
func releasePorts(ports map[string]int) {
	claimed.Lock()
	defer claimed.Unlock()
	for _, p := range ports {
		delete(claimed.ports, p)
	}
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

// bindTaken says whether this process can't open the port.
//
// Only opening the very address a server holds clashes on every system.
// The stand-ins listen on 127.0.0.1, so that address tells for them. A
// real server listens on every address, as 0.0.0.0 (IPv4) or [::] (IPv6,
// usually taking IPv4 too), and opening another address beside it often
// works: Windows lets a second program open 127.0.0.1:N while another
// holds 0.0.0.0:N (unless that one asked for its port alone), macOS lets a
// Go program do the same over TCP (Go's listeners ask for SO_REUSEADDR,
// which allows it there), and Go's "every address" listener, an IPv6 one
// that also takes IPv4, didn't clash with a server's IPv4 0.0.0.0:N on
// Windows or macOS either. That made the first real-server run on GitHub
// report Terraria's game port (Windows and macOS) and Enshrouded's query
// port (Windows) free while the servers held them, as the systems' own
// port tables (platform.PortInUse) showed. So with real, 0.0.0.0 is tried
// by itself as well.
func bindTaken(proto string, port int, real bool) bool {
	network := "tcp"
	if strings.EqualFold(proto, "udp") {
		network = "udp"
	}
	type try struct{ network, host string }
	tries := []try{{network + "4", "127.0.0.1"}}
	if real {
		tries = append(tries, try{network + "4", "0.0.0.0"}, try{network, ""})
	}
	for _, tr := range tries {
		addr := net.JoinHostPort(tr.host, strconv.Itoa(port))
		if network == "udp" {
			c, err := net.ListenPacket(tr.network, addr)
			if err != nil {
				return true
			}
			c.Close()
		} else {
			l, err := net.Listen(tr.network, addr)
			if err != nil {
				return true
			}
			l.Close()
		}
	}
	return false
}

// maxPortRetries is how many times one start is tried again on new ports
// when another program took a port gametest chose (stolenPort). A port
// that stays taken fails the test after these, with the server's log.
const maxPortRetries = 3

// cantOpen finds a line where a server says it couldn't open a port
// because something else has it: the system's own words on Unix ("address
// already in use", from Go, Java, .NET and C alike) and on Windows ("only
// one usage of each socket address", and "forbidden by its access
// permissions" for a port Windows set aside after it was checked), and
// Minecraft's "FAILED TO BIND TO PORT".
var cantOpen = regexp.MustCompile(`(?i)address already in use|only one usage of each socket address|eaddrinuse|failed to bind|forbidden by its access permissions`)

var (
	number = regexp.MustCompile(`\d+`)
	// portIn finds a port written as ":24567" or "port 24567" (but not
	// the "20:38" of a time or the "2026" of a date).
	portIn = regexp.MustCompile(`(?i)(?:port\s*:?\s*|:)(\d{4,5})\b`)
)

// stolenPort says whether a run ended because another program took one
// of the ports gametest chose, between the choice and the server opening
// it, and if so describes it for the test log. That's the case only when
// all of these hold:
//   - the run never came online (a server that was up had its ports);
//   - its log's last line saying a port was in use names only ports
//     gametest chose (Options.Change can set ports of the test's own,
//     which are never changed behind its back, and a port that isn't the
//     run's is the adapter's own mistake), or names no port while the
//     log's other lines name only ports gametest chose (Minecraft says
//     which port it starts on, then "FAILED TO BIND TO PORT!").
//
// Anything else is the server's own failure, which the test must see.
func (s *Session) stolenPort(a *attachment) (string, bool) {
	s.mu.Lock()
	lines := slices.Clone(a.lines)
	online := slices.ContainsFunc(a.events, func(e game.Event) bool { return e.Kind == "online" })
	ports := maps.Clone(a.rec.Ports)
	picked := maps.Clone(s.picked)
	s.mu.Unlock()
	if online {
		return "", false
	}
	at := -1
	for i, l := range lines {
		if cantOpen.MatchString(l) {
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	named := map[string]int{}
	for _, m := range portIn.FindAllStringSubmatch(lines[at], -1) {
		p, _ := strconv.Atoi(m[1])
		role, ok := roleOf(ports, p)
		if !ok {
			return "", false
		}
		named[role] = p
	}
	if len(named) == 0 {
		named = portsIn(lines, ports)
	}
	if len(named) == 0 {
		return "", false
	}
	for role, p := range named {
		if picked[role] != p {
			return "", false
		}
	}
	roles := slices.Sorted(maps.Keys(named))
	what := make([]string, len(roles))
	for i, r := range roles {
		what[i] = fmt.Sprintf("%s port %d", r, ports[r])
	}
	return fmt.Sprintf("another program took the %s, which gametest chose, before the server could open it (%q)",
		strings.Join(what, " and "), strings.TrimSpace(lines[at])), true
}

// portsIn finds which of the run's ports the lines mention, as whole
// numbers: role → port.
func portsIn(lines []string, ports map[string]int) map[string]int {
	found := map[string]int{}
	for _, l := range lines {
		for _, n := range number.FindAllString(l, -1) {
			v, _ := strconv.Atoi(n)
			if role, ok := roleOf(ports, v); ok {
				found[role] = v
			}
		}
	}
	return found
}

// roleOf finds the role that has port.
func roleOf(ports map[string]int, port int) (string, bool) {
	for role, p := range ports {
		if p == port {
			return role, true
		}
	}
	return "", false
}

// newPorts picks new ports for the roles whose ports gametest chose, and
// saves them, as the owner would before starting again. Roles the test
// set itself (Options.Change) keep their ports.
func (s *Session) newPorts() error {
	ports, err := pickPorts(s.info.Ports, s.o.Real)
	if err != nil {
		return err
	}
	if s.afterPick != nil {
		s.afterPick(ports)
	}
	s.mu.Lock()
	old := s.claimed
	s.claimed = ports
	for role := range s.picked {
		s.picked[role] = ports[role]
	}
	picked := maps.Clone(s.picked)
	s.mu.Unlock()
	releasePorts(old)
	return s.store.Save(func(sv *game.Saved) error {
		if sv.Ports == nil {
			sv.Ports = map[string]int{}
		}
		maps.Copy(sv.Ports, picked)
		return nil
	})
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
