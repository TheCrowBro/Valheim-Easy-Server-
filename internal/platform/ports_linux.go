//go:build !windows && !darwin

package platform

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PortInUse says whether a program has this TCP or UDP port open, and
// which one (its process ID, 0 when unknown). Linux only runs the tests,
// so a bind test is enough to say whether the port is free; the kernel's
// socket tables in /proc come first, because they also lead to the
// process holding the port (only this user's processes can be looked
// into). For TCP only listening sockets count.
func PortInUse(proto string, port int) (inUse bool, pid int) {
	if port <= 0 || port > 65535 {
		return false, 0
	}
	proto = strings.ToLower(proto)
	if proto != "tcp" && proto != "udp" {
		return false, 0
	}
	var inodes []string
	for _, table := range []string{proto, proto + "6"} {
		b, err := os.ReadFile("/proc/net/" + table)
		if err != nil {
			continue // no IPv6, or no /proc
		}
		in, found := parseProcNet(string(b), proto == "tcp", port)
		inUse = inUse || found
		inodes = append(inodes, in...)
	}
	if inUse {
		return true, socketOwner(inodes)
	}
	return bindTaken(proto, port), 0
}

// socketOwner finds the process that has one of these sockets open, by
// looking through each process's open files; 0 when none can be seen.
func socketOwner(inodes []string) int {
	if len(inodes) == 0 {
		return 0
	}
	want := map[string]bool{}
	for _, in := range inodes {
		want["socket:["+in+"]"] = true
	}
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", p.Name(), "fd"))
		if err != nil {
			continue // someone else's process
		}
		for _, fd := range fds {
			if link, err := os.Readlink(filepath.Join("/proc", p.Name(), "fd", fd.Name())); err == nil && want[link] {
				return pid
			}
		}
	}
	return 0
}

// bindTaken tries to open the port on every address, as a game server
// would.
func bindTaken(proto string, port int) bool {
	addr := fmt.Sprintf(":%d", port)
	if proto == "tcp" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return true
		}
		l.Close()
		return false
	}
	c, err := net.ListenPacket("udp", addr)
	if err != nil {
		return true
	}
	c.Close()
	return false
}
