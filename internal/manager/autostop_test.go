package manager

import (
	"testing"

	"valheim-easy-server/internal/game"
)

// The auto-stop texts follow spec 4.5's rule exactly: "an hour" at 60, "2
// hours" at 120, and the minutes otherwise, so 180 is "180 minutes".
func TestAutoStopText(t *testing.T) {
	for minutes, want := range map[int]string{
		30:  "30 minutes",
		60:  "an hour",
		90:  "90 minutes",
		120: "2 hours",
		180: "180 minutes",
	} {
		if got := autoStopText(minutes); got != want {
			t.Errorf("autoStopText(%d) = %q, want %q", minutes, got, want)
		}
	}
}

// The activity entry of an auto-stopped run names the game and says why:
// auto-stop with the minutes it was decided with, the ask rule with its 2
// hours (spec 4.5 and 4.6).
func TestAutoStopActivity(t *testing.T) {
	m := &Manager{}
	run := &serverRun{inst: &instance{info: game.Info{Short: "Minecraft"}}, autoStopped: true, autoReason: stopIdle, autoMinutes: 60}
	if got, want := m.autoStopActivityLocked(run), "Minecraft stopped: nobody played for an hour."; got != want {
		t.Errorf("auto-stop at 60 = %q, want %q", got, want)
	}
	run.autoMinutes = 90
	if got, want := m.autoStopActivityLocked(run), "Minecraft stopped: nobody played for 90 minutes."; got != want {
		t.Errorf("auto-stop at 90 = %q, want %q", got, want)
	}
	run.inst.info.Short, run.autoReason = "Windrose", stopAsked
	if got, want := m.autoStopActivityLocked(run), "Windrose stopped: nobody answered for 2 hours."; got != want {
		t.Errorf("the ask rule = %q, want %q", got, want)
	}
}
