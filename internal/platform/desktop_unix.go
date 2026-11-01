//go:build !windows

package platform

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// OpenAppWindow opens the control panel in the default browser.
func OpenAppWindow(url string) error { return OpenURL(url) }

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

// LaunchOnLogin is only implemented on Windows.
func LaunchOnLogin() (bool, error) { return false, nil }

// SetLaunchOnLogin is only implemented on Windows.
func SetLaunchOnLogin(bool, string, string) error { return ErrUnsupported }

// FirewallReady is always true outside Windows (nothing to set up here).
func FirewallReady() bool { return true }

// WakeRuleReady is always true outside Windows.
func WakeRuleReady() bool { return true }

// FixFirewall is only implemented on Windows.
func FixFirewall(string, string, bool) error { return ErrUnsupported }

// SingleInstance does nothing outside Windows.
func SingleInstance(string) (func(), bool, error) { return func() {}, false, nil }

// ShowError prints the message (there is no message box here).
func ShowError(title, msg string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg) }

// SteamPath returns where the Steam client keeps its data, or "".
func SteamPath() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{home + "/.steam/steam", home + "/.local/share/Steam"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
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

// Confirm has no dialog outside Windows; it always says yes.
func Confirm(title, msg string) bool { return true }

// UDPPortInUse reports whether a UDP port is taken, by trying to open it.
func UDPPortInUse(port int) bool {
	c, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	c.Close()
	return false
}
