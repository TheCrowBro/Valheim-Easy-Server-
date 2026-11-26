package platform

// Platform additions for hosting several games (spec 2.12). These are new
// functions only: every existing function keeps its behaviour.
//
// Phase 0 stub: the exported signatures below are the contract other
// streams build against. Stream P owns this file from now on and may split
// it into per-system files or delete it. Until then every call fails with
// errors.ErrUnsupported (or returns "don't know"), and no shipped code
// calls any of it.

import (
	"context"
	"errors"
	"net"
	"os"
)

// StartOptions is how to start a game's server with Start.
type StartOptions struct {
	Exe     string
	Args    []string
	Dir     string
	Env     []string
	Stdin   *os.File
	Stdout  *os.File
	Stderr  *os.File
	Inherit []*os.File // extra handles/fds the child keeps (the stdin pipe's write end, 2.11)
}

// Start starts a game's server like StartDetached (consoleMu, hidden, Setpgid only on Unix, own hidden console on Windows).
func Start(o StartOptions) (*Proc, error) {
	return nil, errors.ErrUnsupported
}

// PortInUse reads the system's tables (never binds, so no firewall prompt) and
// says which process holds the port, 0 when unknown.
// Windows: GetExtended{Tcp,Udp}Table OWNER_PID; mac: lsof -nP; Linux: bind test.
func PortInUse(proto string, port int) (inUse bool, pid int) {
	return false, 0
}

// OpenShared opens a file another program may rename or delete while it is
// open (Windows FILE_SHARE_READ|WRITE|DELETE), so following a game's own
// log never stops the game rotating it.
func OpenShared(path string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

// DefaultGateway is the router's address.
// Windows GetAdaptersAddresses with gateways; mac `route -n get default`.
func DefaultGateway() (net.IP, error) {
	return nil, errors.ErrUnsupported
}

// KnownFolder finds one of the user's folders by name: "appdata",
// "localappdata", "documents", "savedgames", "home", "appsupport".
// It returns "" when there is no such folder on this system.
func KnownFolder(name string) string {
	return ""
}

// RunElevatedWait runs exe as an administrator and waits for it, returning
// its exit code. Windows ShellExecuteEx runas + wait; mac osascript admin.
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) {
	return -1, errors.ErrUnsupported
}

// ProcName is a process's program name, for "used by <program>"; "" when unknown.
func ProcName(pid int) string {
	return ""
}

// Firewall rules for games other than Valheim. Names start with "Easy Game Server - "
// (old builds' blanket removal only matches "Valheim Easy Server - *"), carry the
// instance id and a fingerprint of the program, and are scoped to the public ports.
// A game with no public ports (Core Keeper, Windrose: Steam relay or NAT punch-through on
// ports the game picks) gets one program-wide allow rule instead, as Valheim's is, so the
// hidden server's first listen never raises Windows' firewall pop-up.
type GameRule struct {
	Instance, Program string
	TCP, UDP          []int
	AnyPort           bool
}

// GameFirewallReady says whether r's rules are in place.
func GameFirewallReady(r GameRule) bool {
	return false
}

// FixFirewallAll runs today's Valheim script (byte-identical, firewallScript) followed
// by the games' lines, in one admin prompt. Long scripts use the hashed temp-file path.
func FixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error {
	return errors.ErrUnsupported
}
