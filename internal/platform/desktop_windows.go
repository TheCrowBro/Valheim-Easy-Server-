//go:build windows

package platform

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// OpenAppWindow opens the control panel in its own window. Microsoft Edge's
// app mode gives a window without tabs or an address bar; if Edge or Chrome
// can't be found it falls back to the default browser.
func OpenAppWindow(url string) error {
	for _, exe := range []string{"msedge.exe", "chrome.exe"} {
		path := appPath(exe)
		if path == "" {
			continue
		}
		cmd := exec.Command(path, "--app="+url, "--new-window", "--no-first-run",
			"--no-default-browser-check", "--window-size=1280,900")
		if err := cmd.Start(); err == nil {
			cmd.Process.Release()
			return nil
		}
	}
	return OpenURL(url)
}

// OpenURL opens a link in the default browser.
func OpenURL(url string) error {
	return shellOpen(url)
}

// OpenFolder shows a folder in File Explorer.
func OpenFolder(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return shellOpen(path)
}

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func appPath(exe string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+exe, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		if err == nil && fileExists(v) {
			return v
		}
	}
	var candidates []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		switch exe {
		case "msedge.exe":
			candidates = append(candidates, filepath.Join(base, "Microsoft", "Edge", "Application", exe))
		case "chrome.exe":
			candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", exe))
		}
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "ValheimEasyServer"

// LaunchOnLogin reports whether the app starts when this user signs in.
func LaunchOnLogin() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, nil
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil, nil
}

// SetLaunchOnLogin adds or removes the app from Windows startup.
func SetLaunchOnLogin(on bool, exe string, args string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(runValue, fmt.Sprintf(`"%s" %s`, exe, args))
}

const (
	fwGameRule = "Valheim Easy Server - game"
	fwWakeRule = "Valheim Easy Server - wake signal"
)

// FirewallReady reports whether the game server's firewall rule exists.
func FirewallReady() bool { return ruleExists(fwGameRule) }

// WakeRuleReady reports whether the app may hear Wake-on-LAN signals.
func WakeRuleReady() bool { return ruleExists(fwWakeRule) }

func ruleExists(name string) bool {
	cmd := exec.Command(system32("netsh.exe"), "advfirewall", "firewall", "show", "rule", "name="+name)
	cmd.SysProcAttr = HiddenProcAttr()
	return cmd.Run() == nil
}

// FixFirewall allows the game server through Windows Firewall. It first
// removes any "block" rules Windows created when its firewall pop-up was
// dismissed. The control panel only listens on this computer, so the app
// itself needs no rule, except with wake on, to hear Wake-on-LAN signals
// (UDP port 9) from the home network; other rules for it, including any
// left by older versions, are removed. Windows asks for admin permission
// on the screen; this returns before that is answered.
func FixFirewall(serverExe, appExe string, wake bool) error {
	lines := []string{
		"$server = " + psQuote(serverExe),
		"$app = " + psQuote(appExe),
		`netsh advfirewall firewall delete rule name=all "program=$server"`,
		`netsh advfirewall firewall add rule "name=` + fwGameRule + `" dir=in action=allow "program=$server" enable=yes profile=any`,
		`netsh advfirewall firewall delete rule name=all "program=$app"`,
	}
	if wake {
		lines = append(lines, `netsh advfirewall firewall add rule "name=`+fwWakeRule+`" dir=in action=allow "program=$app" protocol=UDP localport=9 remoteip=localsubnet enable=yes profile=any`)
	}
	return runElevatedPowerShell(strings.Join(lines, "\n"))
}

// SingleInstance makes sure only one copy of the app runs per data folder.
// It returns already=true if another copy holds the lock.
func SingleInstance(id string) (release func(), already bool, err error) {
	name, err := windows.UTF16PtrFromString(`Local\ValheimEasyServer-` + id)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return func() {}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() { windows.CloseHandle(h) }, false, nil
}

// ShowError shows a message box (the app has no console window).
func ShowError(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

// SteamPath returns where the Steam client is installed, or "".
func SteamPath() string {
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue("SteamPath"); err == nil && v != "" {
			return filepath.FromSlash(v)
		}
	}
	if p := filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam"); fileExists(filepath.Join(p, "steam.exe")) {
		return p
	}
	return ""
}

// FreeDisk returns the free space on the drive holding path.
func FreeDisk(path string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(filepath.VolumeName(path) + `\`)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return free, true
}

// Confirm asks a yes/no question in a message box on this computer.
func Confirm(title, msg string) bool {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	ret, _ := windows.MessageBox(0, m, t, windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_SETFOREGROUND)
	return ret == 6 // IDYES
}

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

// UDPPortInUse reports whether any program has this UDP port open. It reads
// Windows' table of open ports instead of trying the port, which would make
// Windows Firewall ask about this app.
func UDPPortInUse(port int) bool {
	for _, family := range []struct {
		af      uintptr
		rowSize int
		portOff int
	}{{2, 12, 4}, {23, 28, 20}} { // IPv4 rows, IPv6 rows (MIB_UDP[6]ROW_OWNER_PID)
		var size uint32
		procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, family.af, 1, 0)
		if size == 0 {
			continue
		}
		buf := make([]byte, size+1024)
		size = uint32(len(buf))
		if r, _, _ := procGetExtendedUdpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, family.af, 1, 0); r != 0 {
			continue
		}
		n := int(binary.LittleEndian.Uint32(buf[0:4]))
		for i := 0; i < n; i++ {
			off := 4 + i*family.rowSize + family.portOff
			if off+4 > len(buf) {
				break
			}
			p := binary.LittleEndian.Uint32(buf[off : off+4])
			if int((p&0xff)<<8|(p>>8)&0xff) == port { // stored in network byte order
				return true
			}
		}
	}
	return false
}
