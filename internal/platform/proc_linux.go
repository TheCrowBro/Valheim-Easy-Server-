//go:build !windows && !darwin

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A zombie still answers kill(0); treat it as gone.
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return !errors.Is(err, os.ErrNotExist) || !dirExists("/proc/self")
	}
	if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) {
		return b[i+2] != 'Z'
	}
	return true
}

const clockTicks = 100 // USER_HZ on Linux

// Usage returns total CPU time used so far and current memory use.
func (p *Proc) Usage() (time.Duration, uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	f := statFields(string(b))
	if len(f) < 24 {
		return 0, 0, errors.New("unexpected /proc stat format")
	}
	utime, _ := strconv.ParseUint(f[13], 10, 64)
	stime, _ := strconv.ParseUint(f[14], 10, 64)
	rssPages, _ := strconv.ParseUint(f[23], 10, 64)
	cpu := time.Duration(utime+stime) * time.Second / clockTicks
	return cpu, rssPages * uint64(os.Getpagesize()), nil
}

// statFields splits /proc/<pid>/stat, keeping the command name (which can
// contain spaces) as one field.
func statFields(s string) []string {
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return strings.Fields(s)
	}
	out := []string{strings.TrimSpace(s[:open]), s[open+1 : close]}
	return append(out, strings.Fields(s[close+1:])...)
}

func procStartTime(pid int) time.Time {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}
	}
	f := statFields(string(b))
	if len(f) < 22 {
		return time.Time{}
	}
	ticks, err := strconv.ParseUint(f[21], 10, 64)
	if err != nil {
		return time.Time{}
	}
	boot := bootTime()
	if boot.IsZero() {
		return time.Time{}
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicks)
}

func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "btime ") {
			sec, err := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64)
			if err == nil {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}

// FindByExe lists running processes started from exactly this program file.
func FindByExe(exePath string) ([]ProcInfo, error) {
	want, err := filepath.Abs(exePath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil // no /proc (e.g. macOS): nothing to find
	}
	var out []ProcInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil || exe != want || !processAlive(pid) {
			continue
		}
		out = append(out, ProcInfo{PID: pid, Exe: exe, Started: procStartTime(pid)})
	}
	return out, nil
}
