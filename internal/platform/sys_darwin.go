//go:build darwin

package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// SysSampler reads whole-computer usage with macOS's own tools: top for
// how busy the processors are, vm_stat for memory and netstat for the
// network.
type SysSampler struct {
	mu     sync.Mutex
	cpu    float64
	top    *exec.Cmd
	closed bool

	memTotal         uint64
	prevUp, prevDown uint64
	prevNet          time.Time
}

// NewSysSampler prepares a sampler.
func NewSysSampler() *SysSampler {
	s := &SysSampler{}
	s.memTotal, _ = unix.SysctlUint64("hw.memsize")
	s.prevUp, s.prevDown, _ = netBytes()
	s.prevNet = time.Now()
	go s.watchCPU()
	return s
}

// watchCPU keeps top running in logging mode and reads the CPU line it
// prints every two seconds.
func (s *SysSampler) watchCPU() {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		cmd := exec.Command("/usr/bin/top", "-l", "0", "-s", "2", "-n", "0", "-F", "-R")
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err == nil {
			s.top = cmd
		}
		s.mu.Unlock()
		if err == nil {
			first := true
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				cpu, ok := parseTopCPU(sc.Text())
				if !ok {
					continue
				}
				if first { // since the computer started, not just now
					first = false
					continue
				}
				s.mu.Lock()
				s.cpu = cpu
				s.mu.Unlock()
			}
			cmd.Wait()
		}
		s.mu.Lock()
		closed := s.closed
		s.top = nil
		s.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(10 * time.Second) // top stopped: start it again
	}
}

// Sample returns usage since the previous call.
func (s *SysSampler) Sample() SysSample {
	var out SysSample
	s.mu.Lock()
	out.CPUPercent = s.cpu
	s.mu.Unlock()
	out.MemTotal = s.memTotal
	if b, err := exec.Command("/usr/bin/vm_stat").Output(); err == nil {
		if used, ok := parseVMStat(string(b)); ok {
			out.MemUsed = min(used, out.MemTotal)
		}
	}
	if up, down, ok := netBytes(); ok {
		now := time.Now()
		if secs := now.Sub(s.prevNet).Seconds(); secs > 0 && up >= s.prevUp && down >= s.prevDown {
			out.NetUp = float64(up-s.prevUp) / secs
			out.NetDown = float64(down-s.prevDown) / secs
			out.NetOK = true
		}
		s.prevUp, s.prevDown, s.prevNet = up, down, now
	}
	return out
}

// Close stops reading.
func (s *SysSampler) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.top != nil && s.top.Process != nil {
		s.top.Process.Kill()
	}
}

func netBytes() (up, down uint64, ok bool) {
	b, err := exec.Command("/usr/sbin/netstat", "-ib", "-n").Output()
	if err != nil {
		return 0, 0, false
	}
	return parseNetstatBytes(string(b))
}

// Machine describes this Mac for the stats page.
func Machine() MachineInfo {
	m := MachineInfo{OS: "macOS", Threads: runtime.NumCPU()}
	m.Hostname, _ = os.Hostname()
	m.MemTotal, _ = unix.SysctlUint64("hw.memsize")
	m.CPU, _ = unix.Sysctl("machdep.cpu.brand_string")
	if v, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output(); err == nil {
		m.OS = "macOS " + strings.TrimSpace(string(v))
	}
	if model, err := unix.Sysctl("hw.model"); err == nil && model != "" {
		m.OS += " (" + model + ")" // e.g. MacPro6,1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(ctx, "/usr/sbin/system_profiler", "-json", "SPDisplaysDataType").Output(); err == nil {
		var r struct {
			Displays []struct {
				Model string `json:"sppci_model"`
			} `json:"SPDisplaysDataType"`
		}
		if json.Unmarshal(b, &r) == nil {
			for _, d := range r.Displays {
				if d.Model != "" {
					m.GPUs = append(m.GPUs, d.Model)
				}
			}
		}
	}
	return m
}
