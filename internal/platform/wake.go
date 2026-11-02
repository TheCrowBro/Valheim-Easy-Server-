package platform

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// parseDarkWake reads `pmset -g systemstate`, which lists what a Mac has
// powered up: "CPU Graphics Audio Network" when it's fully awake, but no
// Graphics in a dark wake (awake only for network or maintenance work, with
// the screen off). It's false when the output doesn't say.
func parseDarkWake(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if _, caps, ok := strings.Cut(line, "Capabilities are:"); ok {
			f := strings.Fields(caps)
			return slices.Contains(f, "CPU") && !slices.Contains(f, "Graphics")
		}
	}
	return false
}

// WakeCause is what woke the computer up, as far as the system says.
type WakeCause int

const (
	// WakeCauseUnknown: the system doesn't say, or hasn't said yet.
	WakeCauseUnknown WakeCause = iota
	// WakeByDevice: a person or a device: the power button, the keyboard or
	// mouse, or the network adapter (Wake-on-LAN, such as an IP KVM's own
	// Wake button).
	WakeByDevice
	// WakeByTimer: the computer itself, for updates, a scheduled task or
	// maintenance.
	WakeByTimer
)

var (
	winWakeTime = regexp.MustCompile(`Name=['"]WakeTime['"]\s*>\s*([^<\s]+)`)
	winWakeType = regexp.MustCompile(`Name=['"]WakeSourceType['"]\s*>\s*(\d+)`)
)

// parseWindowsWake reads the newest "The system has returned from a low
// power state" event (Power-Troubleshooter event 1), as wevtutil prints it
// in XML. It's unknown when that event is from an earlier wake-up than one
// after since: Windows writes it a few seconds after waking up.
func parseWindowsWake(out string, since time.Time) WakeCause {
	out = strings.ReplaceAll(out, "\x00", "") // in case it's UTF-16
	at := winWakeTime.FindStringSubmatch(out)
	kind := winWakeType.FindStringSubmatch(out)
	if at == nil || kind == nil {
		return WakeCauseUnknown
	}
	woke, err := time.Parse(time.RFC3339Nano, at[1])
	if err != nil || !woke.After(since) {
		return WakeCauseUnknown
	}
	// The event's wake source is a number, the same in every language (its
	// text is translated). These are Windows 10 and 11's.
	switch kind[1] {
	case "0", // unknown: the computer's firmware didn't say which device, which is common for Wake-on-LAN
		"1", // the power button
		"2", // the sleep button
		"5": // a device: the network adapter, the keyboard or mouse
		return WakeByDevice
	case "3", // woke up only to hibernate
		"4", // Windows expected someone back, but nobody woke it
		"6", // a wake timer, such as Windows Update's
		"7", // a timer set by an old driver
		"8": // unknown, but probably a timer
		return WakeByTimer
	}
	return WakeCauseUnknown // a kind from a newer Windows
}

// parseMacWakeReason reads `sysctl -n kern.wakereason`, what last woke the
// Mac up: a device such as "GIGE" or "XHC1" (the network adapter, USB) or
// "PWRB (User)" (the power button), or macOS itself, such as "RTC
// (Maintenance)" or "EC.SleepTimer (SleepTimer)".
func parseMacWakeReason(out string) WakeCause {
	r := strings.ToLower(strings.TrimSpace(out))
	if r == "" {
		return WakeCauseUnknown
	}
	for _, timer := range []string{"rtc", "maintenance", "timer", "alarm", "sleepservice"} {
		if strings.Contains(r, timer) {
			return WakeByTimer
		}
	}
	return WakeByDevice
}
