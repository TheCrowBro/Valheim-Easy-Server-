// Package platform wraps the operating-system specific parts: running and
// stopping the server process, reading CPU/GPU/RAM usage, power control
// (hibernate, keep awake) and desktop integration.
package platform

import (
	"errors"
	"os/exec"
	"time"
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

// ErrUnsupported is returned for features this OS doesn't have.
var ErrUnsupported = errors.New("not supported on this system")

// StartCmd starts cmd while holding the console lock, so a child never
// inherits the temporary console state used to send Ctrl+C.
func StartCmd(cmd *exec.Cmd) error {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	return cmd.Start()
}
