package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"valheim-easy-server/internal/wol"
)

// localMAC is the address of one of this computer's network adapters, or
// nil.
func localMAC() net.HardwareAddr {
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if len(ifc.HardwareAddr) == 6 && ifc.Flags&net.FlagLoopback == 0 {
			return ifc.HardwareAddr
		}
	}
	return nil
}

// A Wake-on-LAN signal for this computer starts the server, once for each
// request; signals for other computers do nothing.
func TestWakeSignalStartsServer(t *testing.T) {
	mac := localMAC()
	if mac == nil {
		t.Skip("no network adapter with an address")
	}
	env := setup(t)
	m := env.m
	install(t, m)
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	m.opt.WakePort = port
	m.mu.Lock()
	m.settings.DiscordWake = true // as installing the JetKVM waker does
	m.mu.Unlock()
	m.syncWakeListener()
	signal := func(mac net.HardwareAddr, tag string) {
		t.Helper()
		if err := wol.Send(mac, []byte(tag), []string{fmt.Sprintf("127.0.0.1:%d", port)}); err != nil {
			t.Fatal(err)
		}
	}
	stays := func(what string, state State) {
		t.Helper()
		time.Sleep(500 * time.Millisecond)
		if st := m.Status().State; st != state {
			t.Fatalf("%s: state %s", what, st)
		}
	}

	signal(net.HardwareAddr{2, 0, 0, 0, 0, 1}, "first1")
	stays("a signal for another computer", StateStopped)
	signal(mac, "first1")
	waitFor(t, "the server to start", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if !hasText(m, "info", "Starting the server: someone asked for it from Discord") {
		t.Fatal("no activity about the start")
	}
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	signal(mac, "first1") // the same request, repeated
	stays("a repeat after it was stopped", StateStopped)
	signal(mac, "secnd2")
	waitFor(t, "the server to start again", 20*time.Second, func() bool { return m.Status().State == StateOnline })
}

// A request heard while a job is in progress isn't used up: the waker's
// next repeat starts the server once the job is done. One heard while the
// server runs is met, so the server stays stopped if someone stops it on
// purpose.
func TestWakeRequestWaitsForJob(t *testing.T) {
	env := setup(t)
	m := env.m
	install(t, m)
	release := make(chan struct{})
	if err := m.runJob("backup", "Backing up", false, func(context.Context, *jobCtl) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.wakeSignal("req001")
	st := m.Status()
	close(release)
	if st.State != StateStopped || st.Job == nil || st.Job.Kind != "backup" {
		t.Fatalf("during the job: state %s, job %+v", st.State, st.Job)
	}
	waitJob(t, m, currentJobID(m))
	m.wakeSignal("req001") // the waker's repeat
	waitFor(t, "the server to start", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	waitJob(t, m, currentJobID(m))

	m.wakeSignal("req002")
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	m.wakeSignal("req002")
	time.Sleep(500 * time.Millisecond)
	if st := m.Status().State; st != StateStopped {
		t.Fatalf("a repeat after a stop on purpose: state %s", st)
	}
}

// scriptedConn is a PacketConn whose reads return each of reads in turn: a
// packet, or an error. Then it's closed.
type scriptedConn struct {
	net.PacketConn
	reads []any
	n     int
}

func (c *scriptedConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.n++
	if c.n > len(c.reads) {
		return 0, nil, net.ErrClosed
	}
	switch r := c.reads[c.n-1].(type) {
	case error:
		return 0, nil, r
	case []byte:
		if len(r) > len(b) {
			return 0, nil, errors.New("message too long") // as Windows says
		}
		return copy(b, r), nil, nil
	}
	panic("unexpected read")
}

// An odd packet, such as one too big for the buffer (which fails the read
// on Windows), doesn't stop the app hearing wake signals.
func TestWakeListenerKeepsListening(t *testing.T) {
	env := setup(t)
	m := env.m
	reads := []any{errors.New("wsarecvfrom: a message was larger than the buffer"), make([]byte, 1400)}
	mac := localMAC()
	if mac != nil {
		reads = append(reads, wol.Packet(mac, []byte("odd001")))
	}
	conn := &scriptedConn{reads: reads}
	m.hearWakeSignals(conn) // returns once it's closed
	if conn.n != len(reads)+1 {
		t.Fatalf("stopped listening after %d of %d reads", conn.n, len(reads)+1)
	}
	m.mu.Lock()
	heard := m.wakeHold.After(time.Now())
	m.mu.Unlock()
	if mac != nil && !heard {
		t.Fatal("the wake signal after the odd packets wasn't heard")
	}
}
