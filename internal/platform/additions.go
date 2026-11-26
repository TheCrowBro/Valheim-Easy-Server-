package platform

// Platform additions for hosting several games (spec 2.12). These are new
// functions only: every existing function keeps its behaviour.
//
// This file holds the types the other streams build against and the parts
// that are the same on every system. Each function lives in files named
// after it, one per system where they differ:
//
//	Start            start_unix.go, start_windows.go
//	PortInUse        ports_windows.go, ports_darwin.go, ports_linux.go (ports_parse.go reads the tables)
//	OpenShared       shared_unix.go, shared_windows.go
//	DefaultGateway   gateway_windows.go, gateway_darwin.go, gateway_linux.go (gateway_parse.go)
//	KnownFolder      knownfolder_windows.go, knownfolder_darwin.go, knownfolder_linux.go
//	RunElevatedWait  elevate_windows.go, elevate_darwin.go, elevate_linux.go (elevate_script.go)
//	ProcName         procname_windows.go, procname_darwin.go, procname_linux.go
//	game firewall    firewall_games.go (+ _windows, _darwin, _linux), winparse_games.go

import (
	"errors"
	"os"
)

// StartOptions is how to start a game's server with Start.
//
// For a game with a console (2.11): Stdin is the read end of an os.Pipe
// and Inherit holds its write end, which the app keeps to type lines. The
// game then holds a write end of its own, so it never sees its console
// end, even when the app is killed; it is stopped by a console line or
// Interrupt, never by closing the pipe. Writing blocks while the pipe is
// full (a game that stopped reading), and once the game is started the
// pipe is in blocking mode (os/exec puts the files it hands on into it),
// so write deadlines don't work on it: type lines from a goroutine that
// may be left waiting.
type StartOptions struct {
	Exe  string
	Args []string
	Dir  string
	// Env is the whole environment the game gets; nil means this app's
	// own, as with StartDetached.
	Env []string
	// Stdin, Stdout and Stderr are given to the game as they are. Files,
	// not pipes the app copies from, so the game's output keeps going to
	// its log after the app exits. nil means the null device.
	Stdin   *os.File
	Stdout  *os.File
	Stderr  *os.File
	Inherit []*os.File // extra handles/fds the child keeps (the stdin pipe's write end, 2.11)
}

// GameRule describes the firewall rules one game's server needs (games
// other than Valheim; Valheim keeps its own rules, see FixFirewall).
//
// Names start with "Easy Game Server - " (old builds' blanket removal only
// matches "Valheim Easy Server - *", so an older build that fixes the
// firewall after a rollback leaves them alone), carry the instance id and a
// fingerprint of the program and the rule's ports, and are scoped to the
// public ports. A game with no public ports (Core Keeper, Windrose: Steam
// relay or NAT punch-through on ports the game picks) sets AnyPort and gets
// one program-wide allow rule instead, as Valheim's is, so the hidden
// server's first listen never raises Windows' firewall pop-up. A rule with
// no ports and AnyPort false needs nothing.
type GameRule struct {
	Instance, Program string
	TCP, UDP          []int
	AnyPort           bool
}

// errNotHere is what the additions return for something this system
// doesn't have. It is this package's ErrUnsupported, and it also matches
// errors.ErrUnsupported, which the phase 0 stubs returned, so callers
// written against either keep working.
var errNotHere error = notHereError{}

type notHereError struct{}

func (notHereError) Error() string { return ErrUnsupported.Error() }

func (notHereError) Is(target error) bool {
	return target == ErrUnsupported || target == errors.ErrUnsupported
}
