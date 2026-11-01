//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HibernateStatus: Macs sleep instead of hibernating.
func HibernateStatus() (bool, string) {
	return false, "Macs sleep instead of hibernating."
}

// Hibernate isn't offered on a Mac.
func Hibernate() error { return ErrUnsupported }

// SleepStatus reports whether the Mac can sleep: it always can.
func SleepStatus() (bool, string) { return true, "" }

// Sleep puts the Mac to sleep.
func Sleep() error {
	out, err := exec.Command("/usr/bin/pmset", "sleepnow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("macOS didn't go to sleep: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnableHibernate isn't offered on a Mac.
func EnableHibernate() error { return ErrUnsupported }

// KeepAwake keeps the Mac from falling asleep by itself while it's set,
// with caffeinate, macOS's own tool for that. Sleeping on purpose still
// works.
type KeepAwake struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// NewKeepAwake returns a KeepAwake that isn't holding the Mac awake yet.
func NewKeepAwake(string) *KeepAwake { return &KeepAwake{} }

// Set starts or stops keeping the Mac awake.
func (k *KeepAwake) Set(on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	running := false
	if k.done != nil {
		select {
		case <-k.done:
		default:
			running = true
		}
	}
	if on == running {
		return
	}
	if !on {
		k.cmd.Process.Kill()
		k.cmd, k.done = nil, nil
		return
	}
	// -i: no sleeping while nobody uses it; -w: stop when this app does.
	cmd := exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	k.cmd, k.done = cmd, done
}

// IdleTime is how long it is since someone last used the keyboard or mouse.
func IdleTime() (time.Duration, bool) {
	out, err := exec.Command("/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, false
	}
	return parseHIDIdle(string(out))
}

// AfterWake makes sure the Mac is properly awake after waking up: a
// Wake-on-LAN signal can leave it only half awake (a "dark wake"), with
// the screen off. Declaring someone active wakes it fully.
func AfterWake() {
	cmd := exec.Command("/usr/bin/caffeinate", "-u", "-t", "5")
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
