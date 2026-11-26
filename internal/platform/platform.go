// Package platform wraps the operating-system specific parts: running and
// stopping the server process, reading CPU/GPU/RAM usage, power control
// (hibernate, sleep, keep awake, Wake-on-LAN) and desktop integration.
package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Names that copies of the app already installed depend on. An update
// replaces only the program, and the program it replaces may come back
// after a rollback, so a renamed lock would let two copies run on one data
// folder, and a renamed login item or script would leave the old one
// behind. They never change, even when the name people see does
// (platform_identity_test.go checks them, with the firewall rule names in
// winparse.go). They're here rather than in each system's own file so the
// check runs on every system.
const (
	// instanceLockFile is the single-instance lock in the data folder
	// (macOS and Linux).
	instanceLockFile = ".instance.lock"
	// instanceLockPrefix starts the names of the single-instance locks
	// named by id: Windows' named lock (Local\ValheimEasyServer-<id>) and
	// the lock file in the temp folder (ValheimEasyServer-<id>.lock), used
	// on macOS and Linux when the data folder can't be locked.
	instanceLockPrefix = "ValheimEasyServer-"
	// runValue names the app's entry in Windows' Run key, which starts it
	// when the user signs in.
	runValue = "ValheimEasyServer"
	// launchLabel names the app's login item for launchd (macOS).
	launchLabel = "com.thecrowbro.valheimeasyserver"
	// adminScripts names the PowerShell scripts Windows runs with admin
	// rights for the app (* is a random part), in the temp folder.
	adminScripts = "ValheimEasyServer-admin-*.ps1"
)

// ProcInfo describes a running process found on the system.
type ProcInfo struct {
	PID     int
	Exe     string
	Started time.Time
}

// SysSample is one reading of whole-computer usage.
type SysSample struct {
	CPUPercent float64     // all cores, 0..100
	MemTotal   uint64      // bytes
	MemUsed    uint64      // bytes
	GPUs       []GPUSample // one per graphics card, if available
	GPUOK      bool        // GPU counters could be read
	NetUp      float64     // bytes per second sent
	NetDown    float64     // bytes per second received
	NetOK      bool
}

// GPUSample is the usage of one graphics card.
type GPUSample struct {
	Name     string  `json:"name"`
	Percent  float64 `json:"percent"`
	VRAMUsed uint64  `json:"vramUsed"`
}

// MachineInfo describes the computer.
type MachineInfo struct {
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	CPU      string   `json:"cpu"`
	Threads  int      `json:"threads"`
	MemTotal uint64   `json:"memTotal"`
	GPUs     []string `json:"gpus"`
}

// NetAdapter is one of the computer's network connections, as needed to
// set up Wake-on-LAN.
type NetAdapter struct {
	Name        string   `json:"name"`        // what Windows calls it, e.g. "Ethernet"
	Description string   `json:"description"` // the hardware, e.g. "Broadcom NetXtreme Gigabit Ethernet"
	MAC         string   `json:"mac"`         // e.g. "3C:07:54:12:34:56"; Wake-on-LAN sends to this
	IPv4        []string `json:"ipv4"`
	Wired       bool     `json:"wired"`     // Wake-on-LAN only works over a cable
	Connected   bool     `json:"connected"` // a cable is plugged in and the link is up
	WakeArmed   bool     `json:"wakeArmed"` // Windows lets it wake the computer
}

func formatMAC(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, ":")
}

// ErrUnsupported is returned for features this OS doesn't have.
var ErrUnsupported = errors.New("not supported on this system")

// StartCmd starts cmd while holding the console lock, so a child never
// inherits the temporary console state used to send Ctrl+C.
func StartCmd(cmd *exec.Cmd) error {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	return cmd.Start()
}
