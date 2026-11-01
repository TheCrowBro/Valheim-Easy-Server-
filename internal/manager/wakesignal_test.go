package manager

import (
	"fmt"
	"net"
	"testing"
	"time"

	"valheim-easy-server/internal/wol"
)

// A Wake-on-LAN signal for this computer starts the server, once for each
// request; signals for other computers do nothing.
func TestWakeSignalStartsServer(t *testing.T) {
	var mac net.HardwareAddr
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if len(ifc.HardwareAddr) == 6 && ifc.Flags&net.FlagLoopback == 0 {
			mac = ifc.HardwareAddr
			break
		}
	}
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
