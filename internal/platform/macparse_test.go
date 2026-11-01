package platform

import (
	"strings"
	"testing"
	"time"
)

func TestParsePSTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:05.23":     5230 * time.Millisecond,
		"12:01.50":    12*time.Minute + 1500*time.Millisecond,
		"1:02:03.00":  time.Hour + 2*time.Minute + 3*time.Second,
		"2-03:00:00":  51 * time.Hour,
		"  0:00.01  ": 10 * time.Millisecond,
	} {
		if got, ok := parsePSTime(in); !ok || got.Round(time.Millisecond) != want {
			t.Errorf("%q = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "5", "a:b", "1:2:3:4"} {
		if _, ok := parsePSTime(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if cpu, rss, ok := parsePSUsage("   1:02.50 204800\n"); !ok || cpu != 62500*time.Millisecond || rss != 200<<20 {
		t.Errorf("usage = %v %d %v", cpu, rss, ok)
	}
}

func TestParsePSList(t *testing.T) {
	out := "    1 /sbin/launchd\n  812 /Users/astrid/Library/Application Support/ValheimEasyServer/server/valheim_server/Valheim\n  junk\n"
	list := parsePSList(out)
	if len(list) != 2 || list[1].PID != 812 || list[1].Path != "/Users/astrid/Library/Application Support/ValheimEasyServer/server/valheim_server/Valheim" {
		t.Fatalf("list = %+v", list)
	}
}

func TestParseHardwarePorts(t *testing.T) {
	out := `
Hardware Port: Ethernet 1
Device: en0
Ethernet Address: 3c:07:54:5a:21:9f

Hardware Port: Ethernet 2
Device: en1
Ethernet Address: 3c:07:54:5a:21:a0

Hardware Port: Wi-Fi
Device: en2
Ethernet Address: 20:c9:d0:8e:44:17

Hardware Port: Thunderbolt Bridge
Device: bridge0
Ethernet Address: N/A

VLAN Configurations
===================
`
	ports := parseHardwarePorts(out)
	if len(ports) != 4 || ports[0] != (hardwarePort{"Ethernet 1", "en0", "3C:07:54:5A:21:9F"}) || ports[3].MAC != "" {
		t.Fatalf("ports = %+v", ports)
	}
	for port, want := range map[string]bool{"Ethernet 1": true, "Thunderbolt Ethernet Slot 1": true, "USB 10/100/1000 LAN": true,
		"Wi-Fi": false, "Thunderbolt Bridge": false, "Bluetooth PAN": false, "iPhone USB": false} {
		if wiredPort(port) != want {
			t.Errorf("wired(%q) = %v", port, !want)
		}
	}
}

func TestParsePowerAndNetwork(t *testing.T) {
	pmset := "System-wide power settings:\nCurrently in use:\n standby              1\n Sleep On Power Button 1\n womp                 1\n networkoversleep     0\n"
	if on, found := parsePmsetWomp(pmset); !on || !found {
		t.Error("womp 1 not read")
	}
	if on, found := parsePmsetWomp(strings.Replace(pmset, "womp                 1", "womp                 0", 1)); on || !found {
		t.Error("womp 0 not read")
	}
	if _, found := parsePmsetWomp("nothing"); found {
		t.Error("womp found in nothing")
	}
	if !parseIfconfigActive("en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500\n\tether 3c:07:54:5a:21:9f\n\tmedia: autoselect (1000baseT <full-duplex>)\n\tstatus: active\n") {
		t.Error("active link not read")
	}
	if parseIfconfigActive("en1: flags=8822<BROADCAST,SMART,SIMPLEX,MULTICAST> mtu 1500\n\tstatus: inactive\n") {
		t.Error("inactive link read as active")
	}
}

func TestParseUsage(t *testing.T) {
	if cpu, ok := parseTopCPU("CPU usage: 5.12% user, 3.40% sys, 91.48% idle "); !ok || cpu < 8.5 || cpu > 8.53 {
		t.Errorf("cpu = %v %v", cpu, ok)
	}
	if _, ok := parseTopCPU("PhysMem: 15G used (2805M wired, 1234M compressor), 1023M unused."); ok {
		t.Error("memory line read as CPU")
	}
	vm := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                               12000.
Pages active:                            300000.
Pages inactive:                          200000.
Pages speculative:                         5000.
Pages wired down:                        100000.
Pages purgeable:                          10000.
"Translation faults":                  12345678.
File-backed pages:                       250000.
Anonymous pages:                         260000.
Pages occupied by compressor:             40000.
`
	if used, ok := parseVMStat(vm); !ok || used != (250000+100000+40000)*4096 {
		t.Errorf("used = %d %v", used, ok)
	}
	netstat := `Name       Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0        16384 <Link#1>                        123456     0   99999999   123456     0   99999999     0
lo0        16384 127           127.0.0.1         123456     -   99999999   123456     -   99999999     -
en0        1500  <Link#4>    3c:07:54:5a:21:9f  1234567     0 1000000000   987654     0  200000000     0
en0        1500  192.168.1     192.168.1.20      1234567     -  999999999   987654     -  199999999     -
en1*       1500  <Link#5>    3c:07:54:5a:21:a0        0     0          0        0     0          0     0
utun0      1380  <Link#9>                             10     0       5000       10     0       5000     0
en2        1500  <Link#6>    20:c9:d0:8e:44:17     1000     0       3000     1000     0       4000     0
`
	if up, down, ok := parseNetstatBytes(netstat); !ok || down != 1000003000 || up != 200004000 {
		t.Errorf("net = up %d down %d %v", up, down, ok)
	}
	if idle, ok := parseHIDIdle(`    | |   "HIDIdleTime" = 1520000000` + "\n"); !ok || idle != 1520*time.Millisecond {
		t.Errorf("idle = %v %v", idle, ok)
	}
}

func TestParseFirewall(t *testing.T) {
	if !parseFirewallOn("Firewall is enabled. (State = 1)") || !parseFirewallOn("Firewall is blocking all non-essential incoming connections. (State = 2)") || parseFirewallOn("Firewall is disabled. (State = 0)") {
		t.Error("firewall state misread")
	}
	list := `ALF: total number of apps = 2

1 :  /Applications/Steam.app
 	 ( Allow incoming connections )

2 :  /Users/astrid/Library/Application Support/ValheimEasyServer/server/valheim_server/Valheim
 	 ( Block incoming connections )
`
	if listed, allowed := parseFirewallApp(list, "/Applications/Steam.app"); !listed || !allowed {
		t.Error("Steam not allowed")
	}
	if listed, allowed := parseFirewallApp(list, "/Users/astrid/Library/Application Support/ValheimEasyServer/server/valheim_server/Valheim"); !listed || allowed {
		t.Error("the server isn't blocked")
	}
	if listed, _ := parseFirewallApp(list, "/Applications/Other.app"); listed {
		t.Error("an app that isn't there is listed")
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	p := launchAgentPlist("com.example.app", "/Applications/A & B.app/Contents/MacOS/app", []string{"--background"})
	for _, want := range []string{"<string>/Applications/A &amp; B.app/Contents/MacOS/app</string>", "<string>--background</string>", "<key>AbandonProcessGroup</key>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist has no %q:\n%s", want, p)
		}
	}
	if got := appleScriptString(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Errorf("applescript = %s", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shell = %s", got)
	}
}
