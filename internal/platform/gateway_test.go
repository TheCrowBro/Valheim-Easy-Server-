package platform

import (
	"runtime"
	"testing"
)

func TestParseRouteGetDefault(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0
`
	if ip, ok := parseRouteGetDefault(out); !ok || ip.String() != "192.168.1.1" {
		t.Errorf("got %v %v", ip, ok)
	}
	for _, bad := range []string{
		"   route to: default\ndestination: default\n    gateway: link#4\n  interface: utun3\n",
		"route: writing to routing socket: not in table\n",
		"    gateway: fe80::1%en0\n",
		"",
	} {
		if ip, ok := parseRouteGetDefault(bad); ok {
			t.Errorf("%q gave %v", bad, ip)
		}
	}
}

func TestParseProcNetRoute(t *testing.T) {
	out := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"wlan0\t00000000\t0102A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	if ip, ok := parseProcNetRoute(out); !ok || ip.String() != "192.168.1.1" {
		t.Errorf("got %v %v; want the wired one, 192.168.1.1", ip, ok)
	}
	down := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0002\t0\t0\t100\t00000000\t0\t0\t0\n"
	if ip, ok := parseProcNetRoute(down); ok {
		t.Errorf("a route that isn't up gave %v", ip)
	}
	if ip, ok := parseProcNetRoute(""); ok {
		t.Errorf("nothing gave %v", ip)
	}
}

// The CI runners for Windows and macOS have a network with a router.
func TestDefaultGateway(t *testing.T) {
	ip, err := DefaultGateway()
	t.Logf("default gateway %v (%v)", ip, err)
	if runtime.GOOS == "linux" {
		return // a test container may have no network
	}
	if err != nil || ip.To4() == nil || ip.IsUnspecified() {
		t.Fatalf("DefaultGateway = %v, %v", ip, err)
	}
}
