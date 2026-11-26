//go:build windows

package platform

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Start starts a game's server that keeps running if this app closes, as
// StartDetached does, with the game's own standard input and output and
// any extra handles it should keep open (o.Inherit).
//
// The game gets a hidden console of its own (CREATE_NO_WINDOW), which is
// what lets Interrupt send it Ctrl+C later, and only that game gets it. The
// console lock is held while starting, so the game is never created while
// Interrupt has this app ignoring Ctrl+C (SetConsoleCtrlHandler(NULL,
// TRUE)): that setting is inherited, and the game would ignore Ctrl+C for
// good. No CREATE_NEW_PROCESS_GROUP either, for the same reason.
func Start(o StartOptions) (*Proc, error) {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	cmd := exec.Command(o.Exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	// Only set the ones given: a nil *os.File in an io.Reader is not a
	// nil io.Reader, and exec would try to read from it.
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	attr := HiddenProcAttr()
	// Windows passes on only handles marked inheritable, and Go starts
	// programs with a list of exactly the handles they get. So each extra
	// handle is copied as an inheritable one for the new process, and the
	// copy is closed again once it has started: the caller's own handle
	// is left as it was, and no other program started meanwhile gets it.
	self := windows.CurrentProcess()
	for _, f := range o.Inherit {
		if f == nil {
			continue
		}
		var dup windows.Handle
		if err := windows.DuplicateHandle(self, windows.Handle(f.Fd()), self, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			closeHandles(attr.AdditionalInheritedHandles)
			return nil, err
		}
		attr.AdditionalInheritedHandles = append(attr.AdditionalInheritedHandles, syscall.Handle(dup))
	}
	cmd.SysProcAttr = attr
	err := cmd.Start()
	closeHandles(attr.AdditionalInheritedHandles)
	if err != nil {
		return nil, err
	}
	// Open our own handle before releasing Go's, so the PID can't be
	// reused in between.
	p, err := OpenProc(cmd.Process.Pid)
	cmd.Process.Release()
	return p, err
}

func closeHandles(hs []syscall.Handle) {
	for _, h := range hs {
		windows.CloseHandle(windows.Handle(h))
	}
}
