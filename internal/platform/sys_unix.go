//go:build !windows

package platform

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SysSampler reads whole-computer usage from /proc (Linux). On systems
// without /proc the readings stay at zero.
type SysSampler struct {
	prevIdle, prevTotal uint64
	prevUp, prevDown    uint64
	prevNet             time.Time
}

// NewSysSampler prepares a sampler.
func NewSysSampler() *SysSampler {
	s := &SysSampler{}
	s.prevIdle, s.prevTotal = cpuTicks()
	s.prevUp, s.prevDown, _ = netBytes()
	s.prevNet = time.Now()
	return s
}

// Sample returns usage since the previous call.
func (s *SysSampler) Sample() SysSample {
	var out SysSample
	idle, total := cpuTicks()
	if dt := total - s.prevTotal; total > s.prevTotal {
		out.CPUPercent = float64(dt-(idle-s.prevIdle)) * 100 / float64(dt)
	}
	s.prevIdle, s.prevTotal = idle, total

	mem := meminfo()
	out.MemTotal = mem["MemTotal"]
	if avail, ok := mem["MemAvailable"]; ok && out.MemTotal >= avail {
		out.MemUsed = out.MemTotal - avail
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

// Close frees resources.
func (s *SysSampler) Close() {}

func cpuTicks() (idle, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0
	}
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return idle, total
}

func meminfo() map[string]uint64 {
	out := map[string]uint64{}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			out[strings.TrimSuffix(f[0], ":")] = n * 1024
		}
	}
	return out
}

func netBytes() (up, down uint64, ok bool) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		down += rx
		up += tx
	}
	return up, down, true
}

// Machine describes this computer for the stats page.
func Machine() MachineInfo {
	m := MachineInfo{OS: runtime.GOOS, Threads: runtime.NumCPU(), MemTotal: meminfo()["MemTotal"]}
	m.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
				m.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				m.OS = strings.Trim(v, `"`)
			}
		}
	}
	return m
}
