package platform

// Reading what macOS's own tools print. These are used on macOS only, but
// they're plain text parsers, so their tests run everywhere.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parsePSTime reads a CPU time as macOS's ps prints it: [[dd-]hh:]mm:ss.cc.
func parsePSTime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, false
	}
	total := secs
	mult := 60.0
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil {
			return 0, false
		}
		total += float64(n) * mult
		mult *= 60
	}
	total += float64(days) * 86400
	return time.Duration(total * float64(time.Second)), true
}

// parsePSUsage reads `ps -o time=,rss=`: CPU time used and memory in use
// (ps gives kilobytes).
func parsePSUsage(out string) (time.Duration, uint64, bool) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, 0, false
	}
	cpu, ok := parsePSTime(f[0])
	rss, err := strconv.ParseUint(f[1], 10, 64)
	if !ok || err != nil {
		return 0, 0, false
	}
	return cpu, rss * 1024, true
}

type psEntry struct {
	PID  int
	Path string
}

// parsePSList reads `ps -axww -o pid=,comm=`: each process's ID and the
// program it runs (a path, which can contain spaces).
func parsePSList(out string) []psEntry {
	var list []psEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		pid, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(pid)
		if err != nil || n <= 0 {
			continue
		}
		list = append(list, psEntry{PID: n, Path: strings.TrimSpace(path)})
	}
	return list
}

type hardwarePort struct {
	Port   string // what macOS calls it, e.g. "Ethernet 1" or "Wi-Fi"
	Device string // e.g. "en0"
	MAC    string
}

// parseHardwarePorts reads `networksetup -listallhardwareports`.
func parseHardwarePorts(out string) []hardwarePort {
	var ports []hardwarePort
	var cur *hardwarePort
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Hardware Port":
			ports = append(ports, hardwarePort{Port: v})
			cur = &ports[len(ports)-1]
		case "Device":
			if cur != nil {
				cur.Device = v
			}
		case "Ethernet Address":
			if cur != nil && v != "N/A" {
				cur.MAC = strings.ToUpper(v)
			}
		}
	}
	return ports
}

// wiredPort reports whether a hardware port is a cable connection. Wi-Fi,
// Bluetooth and the Thunderbolt Bridge (a link between Macs) aren't, and
// Wake-on-LAN doesn't work over them.
func wiredPort(port string) bool {
	p := strings.ToLower(port)
	for _, not := range []string{"wi-fi", "airport", "bluetooth", "bridge", "iphone", "ipad", "vpn", "firewire"} {
		if strings.Contains(p, not) {
			return false
		}
	}
	return strings.Contains(p, "ethernet") || strings.Contains(p, "lan")
}

// parsePmsetWomp reads `pmset -g`: whether "Wake for network access" is on.
func parsePmsetWomp(out string) (on, found bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "womp" {
			return f[1] == "1", true
		}
	}
	return false, false
}

// parseIfconfigActive reads `ifconfig <device>`: whether a cable is plugged
// in and the link is up.
func parseIfconfigActive(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "status:"); ok {
			return strings.TrimSpace(v) == "active"
		}
	}
	return false
}

var reTopCPU = regexp.MustCompile(`CPU usage:.*?([\d.]+)% idle`)

// parseTopCPU reads top's "CPU usage: 5.12% user, 3.40% sys, 91.47% idle"
// line: how busy all the cores are together.
func parseTopCPU(line string) (float64, bool) {
	m := reTopCPU.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	idle, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return max(0, min(100, 100-idle)), true
}

var rePageSize = regexp.MustCompile(`page size of (\d+) bytes`)

// parseVMStat reads `vm_stat`: memory in use, counted the way Activity
// Monitor does (apps, wired and compressed), not including files macOS
// keeps cached.
func parseVMStat(out string) (uint64, bool) {
	m := rePageSize.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	page, _ := strconv.ParseUint(m[1], 10, 64)
	pages := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages[strings.Trim(strings.TrimSpace(k), `"`)] = n
		}
	}
	anon, ok := pages["Anonymous pages"]
	if !ok || page == 0 {
		return 0, false
	}
	if p := pages["Pages purgeable"]; p < anon {
		anon -= p
	}
	return (anon + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page, true
}

// parseNetstatBytes reads `netstat -ib -n`: bytes sent and received so far
// over the network ports (not the computer talking to itself or VPNs).
func parseNetstatBytes(out string) (up, down uint64, ok bool) {
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[2], "<Link#") || seen[f[0]] {
			continue
		}
		name := strings.TrimSuffix(f[0], "*")
		if !strings.HasPrefix(name, "en") {
			continue // lo0, utun (VPNs), bridges, AirDrop and the like
		}
		in, err1 := strconv.ParseUint(f[len(f)-5], 10, 64)
		outb, err2 := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		seen[f[0]] = true
		down += in
		up += outb
		ok = true
	}
	return up, down, ok
}

var reHIDIdle = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// parseHIDIdle reads ioreg's HIDIdleTime: nanoseconds since someone last
// used the keyboard or mouse.
func parseHIDIdle(out string) (time.Duration, bool) {
	m := reHIDIdle.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(n), true
}

// parseFirewallOn reads `socketfilterfw --getglobalstate`.
func parseFirewallOn(out string) bool {
	s := strings.ToLower(out)
	if i := strings.Index(s, "state = "); i >= 0 {
		return len(s) > i+8 && s[i+8] != '0'
	}
	return strings.Contains(s, "enabled")
}

// parseFirewallApp reads `socketfilterfw --listapps`: whether the program
// at path is on the list, and whether it may accept connections.
func parseFirewallApp(out, path string) (listed, allowed bool) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		_, p, ok := strings.Cut(line, " :  ")
		if !ok || strings.TrimSpace(p) != path {
			continue
		}
		for _, next := range lines[i+1:] {
			if t := strings.TrimSpace(next); t != "" {
				return true, strings.Contains(strings.ToLower(t), "allow")
			}
		}
		return true, false
	}
	return false, false
}

// xmlEscape makes s safe in a property list.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// launchAgentPlist is the file that makes macOS start the app when the
// user logs in. The server it starts must outlive the app, so launchd
// leaves the process group alone.
func launchAgentPlist(label, exe string, args []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
`)
	for _, a := range args {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`)
	return b.String()
}

// shellQuote quotes a word for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// appleScriptString quotes s as an AppleScript string.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
