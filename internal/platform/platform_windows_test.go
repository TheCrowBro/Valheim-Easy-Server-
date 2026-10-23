//go:build windows

package platform

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

const waiterSource = `package main

import (
	"os"
	"os/signal"
	"time"
)

func main() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	os.WriteFile(os.Args[1]+".ready", nil, 0o644)
	select {
	case <-c:
		os.WriteFile(os.Args[1], []byte("got ctrl+c"), 0o644)
		os.Exit(0)
	case <-time.After(60 * time.Second):
		os.Exit(2)
	}
}
`

// TestInterruptReachesConsoleProgram checks the mechanism used to ask the
// Valheim server to save and stop: a hidden console program started by
// StartDetached must receive Ctrl+C from Interrupt, and this process must
// survive it.
func TestInterruptReachesConsoleProgram(t *testing.T) {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt) // as the app does
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(waiterSource), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "waiter.exe")
	if out, err := exec.Command("go", "build", "-o", exe, src).CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	marker := filepath.Join(dir, "marker")
	p, err := StartDetached(exe, []string{marker}, dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			p.Kill()
			t.Fatal("helper program never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if p.Exited() {
		t.Fatal("helper exited early")
	}
	if err := p.Interrupt(); err != nil {
		p.Kill()
		t.Fatalf("Interrupt: %v", err)
	}
	done := make(chan int, 1)
	go func() {
		code, _ := p.Wait()
		done <- code
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(20 * time.Second):
		p.Kill()
		t.Fatal("helper didn't stop after Ctrl+C")
	}
	if b, _ := os.ReadFile(marker); string(b) != "got ctrl+c" {
		t.Fatalf("marker = %q", b)
	}

	// Starting another program afterwards must still work normally.
	p2, err := StartDetached(exe, []string{marker + "2"}, dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := p2.Interrupt(); err != nil {
		t.Fatalf("second Interrupt: %v", err)
	}
	code, _ := p2.Wait()
	p2.Close()
	if code != 0 {
		t.Fatalf("second helper exit code %d", code)
	}
}

func TestOwnProcess(t *testing.T) {
	p, err := OpenProc(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if time.Since(p.StartTime()) > time.Hour || p.StartTime().After(time.Now()) {
		t.Fatalf("start time = %v", p.StartTime())
	}
	cpu, mem, err := p.Usage()
	if err != nil || mem == 0 {
		t.Fatalf("usage: cpu=%v mem=%d err=%v", cpu, mem, err)
	}
	exe, _ := os.Executable()
	procs, err := FindByExe(exe)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pi := range procs {
		if pi.PID == os.Getpid() {
			found = true
		}
	}
	if !found {
		t.Fatalf("FindByExe(%s) = %+v", exe, procs)
	}
}

func TestSysSampler(t *testing.T) {
	s := NewSysSampler()
	defer s.Close()
	time.Sleep(1500 * time.Millisecond)
	s.Sample()
	time.Sleep(time.Second)
	smp := s.Sample()
	t.Logf("cpu=%.1f%% mem=%d/%d gpuOK=%v gpus=%+v netOK=%v up=%.0f down=%.0f",
		smp.CPUPercent, smp.MemUsed, smp.MemTotal, smp.GPUOK, smp.GPUs, smp.NetOK, smp.NetUp, smp.NetDown)
	if smp.MemTotal == 0 || smp.MemUsed == 0 || smp.MemUsed > smp.MemTotal {
		t.Fatalf("memory reading wrong: %+v", smp)
	}
	if smp.CPUPercent < 0 || smp.CPUPercent > 100 {
		t.Fatalf("cpu = %v", smp.CPUPercent)
	}
	if !smp.NetOK {
		t.Fatal("network counters unavailable")
	}
}

func TestMachineAndPower(t *testing.T) {
	m := Machine()
	t.Logf("machine: %+v", m)
	if m.MemTotal == 0 || m.CPU == "" || m.OS == "" {
		t.Fatalf("machine info incomplete: %+v", m)
	}
	avail, detail := HibernateStatus()
	t.Logf("hibernate available=%v detail=%q", avail, detail)
	k := NewKeepAwake("Valheim Easy Server test")
	k.Set(true)
	k.Set(false)
	if d, ok := IdleTime(); ok {
		t.Logf("idle for %v", d)
	}
	on, err := LaunchOnLogin()
	t.Logf("launch on login = %v (%v), firewall ready = %v, steam = %q", on, err, FirewallReady(), SteamPath())
	if free, ok := FreeDisk(os.TempDir()); !ok || free == 0 {
		t.Fatalf("free disk = %d %v", free, ok)
	}
}

func TestParseEngineInstance(t *testing.T) {
	luid, eng := parseEngineInstance("pid_1234_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D")
	if luid != "0x00000000_0x0000D1A5" || eng != "3D" {
		t.Fatalf("got %q %q", luid, eng)
	}
	gpus, index := gpuUsage([]pdhItem{
		{"pid_1_luid_0x0_0xA_phys_0_eng_0_engtype_3D", 20},
		{"pid_2_luid_0x0_0xA_phys_0_eng_0_engtype_3D", 15},
		{"pid_1_luid_0x0_0xA_phys_0_eng_1_engtype_Compute_0", 50},
		{"pid_3_luid_0x0_0xB_phys_0_eng_0_engtype_3D", 5},
	}, []string{"AMD FirePro D500", "AMD FirePro D500"})
	if len(gpus) != 2 || gpus[0].Percent != 50 || gpus[1].Percent != 5 || index["0x0_0xB"] != 1 || gpus[0].Name != "AMD FirePro D500" {
		t.Fatalf("gpus = %+v %v", gpus, index)
	}
}

func TestUDPPortInUse(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	if !UDPPortInUse(port) {
		t.Fatalf("port %d is open but reported free", port)
	}
	c.Close()
	if UDPPortInUse(port) {
		t.Fatalf("port %d reported in use after closing", port)
	}
}

func TestNetAdaptersAndSleep(t *testing.T) {
	for _, a := range NetAdapters() {
		t.Logf("adapter: %+v", a)
		if len(a.MAC) != 17 || a.Name == "" {
			t.Fatalf("adapter details wrong: %+v", a)
		}
	}
	ok, detail := SleepStatus()
	t.Logf("sleep available=%v %q", ok, detail)
	for desc, virtual := range map[string]bool{
		"Broadcom NetXtreme Gigabit Ethernet":      false,
		"Broadcom NetXtreme Gigabit Ethernet #2":   false,
		"Intel(R) Ethernet Connection I219-V":      false,
		"Microsoft Hyper-V Network Adapter":        true,
		"Hyper-V Virtual Ethernet Adapter":         true,
		"TAP-Windows Adapter V9":                   true,
		"Bluetooth Device (Personal Area Network)": true,
	} {
		if virtualAdapter(desc) != virtual {
			t.Errorf("virtualAdapter(%q) = %v", desc, !virtual)
		}
	}
}
