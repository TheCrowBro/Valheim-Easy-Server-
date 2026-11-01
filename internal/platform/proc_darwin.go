//go:build darwin

package platform

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const sZomb = 5 // SZOMB: a process that has ended but hasn't been reaped yet

func kinfo(pid int) (*unix.KinfoProc, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return nil, false
	}
	return kp, true
}

func processAlive(pid int) bool {
	kp, ok := kinfo(pid)
	return ok && kp.Proc.P_stat != sZomb
}

func procStartTime(pid int) time.Time {
	kp, ok := kinfo(pid)
	if !ok {
		return time.Time{}
	}
	tv := kp.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000)
}

// Usage returns total CPU time used so far and current memory use.
func (p *Proc) Usage() (time.Duration, uint64, error) {
	out, err := exec.Command("/bin/ps", "-o", "time=,rss=", "-p", strconv.Itoa(p.pid)).Output()
	if err != nil {
		return 0, 0, err
	}
	cpu, rss, ok := parsePSUsage(string(out))
	if !ok {
		return 0, 0, errors.New("unexpected ps output")
	}
	return cpu, rss, nil
}

// FindByExe lists running processes started from exactly this program file.
func FindByExe(exePath string) ([]ProcInfo, error) {
	want, err := filepath.Abs(exePath)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	wantReal := realPath(want)
	var found []ProcInfo
	for _, e := range parsePSList(string(out)) {
		if filepath.Base(e.Path) != filepath.Base(want) || !processAlive(e.PID) {
			continue
		}
		// ps may give only the program's name; the kernel has the path it
		// was started with. Either may go through a link (/var is one).
		for _, exe := range []string{e.Path, procExe(e.PID)} {
			if exe != "" && (exe == want || realPath(exe) == wantReal) {
				found = append(found, ProcInfo{PID: e.PID, Exe: want, Started: procStartTime(e.PID)})
				break
			}
		}
	}
	return found, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// procExe is the program a process runs, from its arguments as the kernel
// keeps them: the count, then the program's path.
func procExe(pid int) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return ""
	}
	b = b[4:]
	if i := bytes.IndexByte(b, 0); i > 0 {
		return string(b[:i])
	}
	return ""
}
