//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacMachine(t *testing.T) {
	m := Machine()
	t.Logf("machine: %+v", m)
	if !strings.HasPrefix(m.OS, "macOS 1") && !strings.HasPrefix(m.OS, "macOS 2") || m.CPU == "" || m.MemTotal == 0 || m.Threads == 0 {
		t.Fatalf("machine info incomplete: %+v", m)
	}
}

func TestMacSampler(t *testing.T) {
	s := NewSysSampler()
	defer s.Close()
	var got SysSample
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		got = s.Sample()
		if got.CPUPercent > 0 && got.NetOK {
			break
		}
	}
	t.Logf("sample: %+v", got)
	if got.MemTotal == 0 || got.MemUsed == 0 || got.MemUsed > got.MemTotal {
		t.Errorf("memory: %d of %d", got.MemUsed, got.MemTotal)
	}
	if got.CPUPercent <= 0 || got.CPUPercent > 100 {
		t.Errorf("cpu = %v", got.CPUPercent)
	}
	if !got.NetOK {
		t.Error("no network reading")
	}
}

// A program in a folder with spaces in its name, like the server under
// ~/Library/Application Support, is found again and stopped.
func TestMacProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Application Support", "valheim_server")
	os.MkdirAll(dir, 0o755)
	exe := filepath.Join(dir, "sleepy")
	b, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := StartDetached(exe, []string{"30"}, dir, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if p.StartTime().IsZero() || time.Since(p.StartTime()) > time.Minute {
		t.Errorf("start time = %v", p.StartTime())
	}
	found, err := FindByExe(exe)
	if err != nil || len(found) != 1 || found[0].PID != p.PID() {
		t.Fatalf("found %+v (%v), want pid %d", found, err, p.PID())
	}
	if procExe(p.PID()) != exe {
		t.Logf("kern.procargs2 gave %q", procExe(p.PID()))
	}
	if _, rss, err := p.Usage(); err != nil || rss == 0 {
		t.Errorf("usage: %d %v", rss, err)
	}
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("still running after Ctrl+C")
	}
	if processAlive(p.PID()) {
		t.Error("alive after exiting")
	}
}

func TestMacDesktop(t *testing.T) {
	adapters := NetAdapters()
	t.Logf("adapters: %+v", adapters)
	if len(adapters) == 0 {
		t.Error("no network ports found")
	}
	if ok, _ := SleepStatus(); !ok {
		t.Error("can't sleep")
	}
	if ok, _ := HibernateStatus(); ok {
		t.Error("hibernation offered on a Mac")
	}
	idle, ok := IdleTime()
	t.Logf("idle %v (%v), firewall ready %v, wake rule ready %v, steam %q", idle, ok, FirewallReady("/nonexistent"), WakeRuleReady("/x.app/Contents/MacOS/x"), SteamPath())

	k := NewKeepAwake("test")
	k.Set(true)
	time.Sleep(300 * time.Millisecond)
	if out, _ := exec.Command("/usr/bin/pmset", "-g", "assertions").Output(); !strings.Contains(string(out), "caffeinate") {
		t.Logf("assertions:\n%s", out)
		t.Error("caffeinate isn't keeping the Mac awake")
	}
	k.Set(false)

	// The login item, in a home folder of its own.
	t.Setenv("HOME", t.TempDir())
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if on, err := LaunchOnLogin(); on || err != nil {
		t.Fatalf("on before: %v %v", on, err)
	}
	if err := SetLaunchOnLogin(true, exe, "--background"); err != nil {
		t.Fatal(err)
	}
	if on, err := LaunchOnLogin(); !on || err != nil {
		t.Fatalf("not on: %v %v", on, err)
	}
	p, _ := launchAgentPath()
	if out, err := exec.Command("/usr/bin/plutil", "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("the login item isn't a valid property list: %s", out)
	}
	if err := SetLaunchOnLogin(false, exe, ""); err != nil {
		t.Fatal(err)
	}
	if on, _ := LaunchOnLogin(); on {
		t.Fatal("still on")
	}
}
