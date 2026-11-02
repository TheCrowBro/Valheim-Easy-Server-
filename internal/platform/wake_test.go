package platform

import (
	"strings"
	"testing"
	"time"
)

func TestParseDarkWake(t *testing.T) {
	for out, want := range map[string]bool{
		"Current System Capabilities are: CPU Graphics Audio Network \nCurrent Power State: 4\n": false,
		"Current System Capabilities are: CPU Network \nCurrent Power State: 4\n":                true,
		"Current System Capabilities are: CPU \n":                                                true,
		"Current System Capabilities are: \n":                                                    false,
		"":                                                                                       false,
		"pmset: unknown option\n":                                                                false,
	} {
		if got := parseDarkWake(out); got != want {
			t.Errorf("%q: dark wake %v, want %v", out, got, want)
		}
	}
}

// winWakeEvent is a wake-up event as wevtutil prints it, cut short.
func winWakeEvent(wake, kind string) string {
	return `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Power-Troubleshooter' Guid='{cdc05e28-c449-49c6-b9d2-88cf761644df}'/>` +
		`<EventID>1</EventID><Version>3</Version><TimeCreated SystemTime='2026-11-02T03:00:12.5104529Z'/><Channel>System</Channel><Computer>VALHEIM-PC</Computer></System>` +
		`<EventData><Data Name='SleepTime'>2026-11-02T01:00:03.1234567Z</Data><Data Name='WakeTime'>` + wake + `</Data><Data Name='SleepDuration'>1500</Data>` +
		`<Data Name='TargetState'>4</Data><Data Name='EffectiveState'>4</Data><Data Name='WakeSourceType'>` + kind + `</Data>` +
		`<Data Name='WakeSourceTextLength'>39</Data><Data Name='WakeSourceText'>Intel(R) Ethernet Connection (7) I219-V</Data>` +
		`<Data Name='WakeTimerOwnerLength'>0</Data><Data Name='WakeTimerOwner'></Data></EventData></Event>`
}

// Windows says in its event log whether a device (such as the network
// adapter, for Wake-on-LAN) or a timer woke it up, as a number that's the
// same in every language.
func TestParseWindowsWake(t *testing.T) {
	since := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC) // before it went to sleep
	const woke = "2026-11-02T03:00:05.0951007Z"
	utf16 := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			b.WriteRune(r)
			b.WriteByte(0)
		}
		return b.String()
	}
	for _, c := range []struct {
		name, out string
		want      WakeCause
	}{
		{"network adapter", winWakeEvent(woke, "5"), WakeByDevice},
		{"power button", winWakeEvent(woke, "1"), WakeByDevice},
		{"firmware didn't say", winWakeEvent(woke, "0"), WakeByDevice},
		{"wake timer", winWakeEvent(woke, "6"), WakeByTimer},
		{"probably a timer", winWakeEvent(woke, "8"), WakeByTimer},
		{"to hibernate", winWakeEvent(woke, "3"), WakeByTimer},
		{"newer kind", winWakeEvent(woke, "9"), WakeCauseUnknown},
		{"an earlier wake-up", winWakeEvent("2026-11-01T22:10:00.5Z", "5"), WakeCauseUnknown},
		{"not logged", "", WakeCauseUnknown},
		{"no time", winWakeEvent("", "5"), WakeCauseUnknown},
		{"double quotes", strings.ReplaceAll(winWakeEvent(woke, "5"), "'", `"`), WakeByDevice},
		{"UTF-16", utf16(winWakeEvent(woke, "6")), WakeByTimer},
	} {
		if got := parseWindowsWake(c.out, since); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// macOS says what last woke the Mac: a device, or itself for maintenance.
func TestParseMacWakeReason(t *testing.T) {
	for out, want := range map[string]WakeCause{
		"GIGE\n":                              WakeByDevice,
		"XHC1\n":                              WakeByDevice,
		"PWRB (User)\n":                       WakeByDevice,
		"RTC (Maintenance)\n":                 WakeByTimer,
		"RTC (Alarm)\n":                       WakeByTimer,
		"EC.SleepTimer (SleepTimer)\n":        WakeByTimer,
		"NUB.SPMISw3IRQ nub-spmi0.0x02 rtc\n": WakeByTimer,
		"\n":                                  WakeCauseUnknown,
		"":                                    WakeCauseUnknown,
	} {
		if got := parseMacWakeReason(out); got != want {
			t.Errorf("%q: %v, want %v", out, got, want)
		}
	}
}
