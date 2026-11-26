//go:build darwin

package platform

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PortInUse says whether a program has this TCP or UDP port open, and
// which one (its process ID, 0 when unknown). It asks macOS's own tools
// instead of trying the port, which could make the macOS firewall ask
// about this app. lsof names the process but sees only this user's
// programs; netstat sees every socket without saying whose, so it covers
// a port some other user's program holds. For TCP only listening sockets
// count.
func PortInUse(proto string, port int) (inUse bool, pid int) {
	if port <= 0 || port > 65535 {
		return false, 0
	}
	proto = strings.ToLower(proto)
	var lsofArgs []string
	switch proto {
	case "tcp":
		lsofArgs = []string{"-nP", "-iTCP:" + strconv.Itoa(port), "-sTCP:LISTEN", "-Fp"}
	case "udp":
		lsofArgs = []string{"-nP", "-iUDP:" + strconv.Itoa(port), "-Fp"}
	default:
		return false, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// lsof exits with 1 when it finds nothing, so its output is read
	// whatever the exit code.
	out, _ := exec.CommandContext(ctx, "/usr/sbin/lsof", lsofArgs...).Output()
	if pids := parseLsofPIDs(string(out)); len(pids) > 0 {
		return true, pids[0]
	}
	out, err := exec.CommandContext(ctx, "/usr/sbin/netstat", "-an", "-p", proto).Output()
	if err == nil && parseNetstatPort(string(out), proto, port) {
		return true, 0
	}
	return false, 0
}
