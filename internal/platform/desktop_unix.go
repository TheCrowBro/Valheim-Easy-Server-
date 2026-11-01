//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// OpenURL opens a link in the default browser.
func OpenURL(url string) error { return open(url) }

// OpenFolder shows a folder in the file manager.
func OpenFolder(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return open(path)
}

func open(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// SingleInstance makes sure only one copy of the app runs per data folder,
// with a lock on a file that the system lets go of when the app closes.
// It returns already=true if another copy holds the lock.
func SingleInstance(id string) (release func(), already bool, err error) {
	path := filepath.Join(os.TempDir(), "ValheimEasyServer-"+id+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return func() {}, true, nil
		}
		return nil, false, err
	}
	return func() { f.Close() }, false, nil
}

// Detach starts the program again in the background, in a session of its
// own, so it carries on when the copy that started it ends.
func Detach(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// FreeDisk returns the free space on the drive holding path.
func FreeDisk(path string) (uint64, bool) {
	for p := path; p != "" && p != "/" && p != "."; p = parentDir(p) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err == nil {
			return uint64(st.Bavail) * uint64(st.Bsize), true
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil {
		return uint64(st.Bavail) * uint64(st.Bsize), true
	}
	return 0, false
}

func parentDir(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// UDPPortInUse reports whether a UDP port is taken, by trying to open it.
func UDPPortInUse(port int) bool {
	c, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	c.Close()
	return false
}
