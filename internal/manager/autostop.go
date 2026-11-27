package manager

// Auto-stop (spec 4.5, stream A): a game nobody has played on for its
// minutes (60 by default, for every game, Valheim included from R3) stops
// itself through its stop chain, as a quiet job, so the computer can sleep
// right after it. Only a ready game whose presence is proven (2.6) and
// whose count is 0 is stopped this way; presence "none" or unproven goes
// to the "still playing?" rule instead (ask.go, stream D).
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in. lifecycleObserver is A's observer (hooks.go): the one
// place A hears about readiness, joins, exits, jobs, ticks and wakes.

import (
	"fmt"
	"time"

	"valheim-easy-server/internal/game"
)

// AutoStopStatus is a game's "autoStop" in Status (4.9): "Stops at 21:40 if
// nobody joins" on its Home.
type AutoStopStatus struct {
	Minutes int        `json:"minutes"`      // the instance's setting; 0 = never
	At      *time.Time `json:"at,omitempty"` // when it stops if nobody joins; nil while someone plays or it isn't ready
}

// autoStopStatusLocked is the instance's auto-stop for Status, or nil when
// it has none (not running, auto-stop off, or presence not proven).
func (m *Manager) autoStopStatusLocked(inst *instance) *AutoStopStatus {
	return nil
}

// autoStopMinutesLocked is the instance's auto-stop setting: servers.json
// autoStopMinutes (nil = 60, 0 = off). R2 has no auto-stop, so until A
// fills this in it is 0.
func (m *Manager) autoStopMinutesLocked(inst *instance) int {
	return 0
}

// autoStopGame stops a game that nobody is using: it marks the run
// autoStopped with why (stopIdle for auto-stop, stopAsked for D's "still
// playing?" rule), then runs the stop chain as a quiet job (runQuietJob),
// so neither the job nor the exit resets machineIdleSince. It runs without
// m.mu: an observer starts it with m.go_.
func (m *Manager) autoStopGame(inst *instance, why stopReason) error {
	return errNotYet
}

// autoStopText says how long nobody played, from the instance's minutes:
// "an hour" at 60, "2 hours" at 120, otherwise "<n> minutes" (spec 4.5).
// The activity entry and D's stopped post (stopIdle) both use it: "nobody
// played for an hour".
func autoStopText(minutes int) string {
	switch {
	case minutes == 60:
		return "an hour"
	case minutes > 60 && minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// lifecycleObserver is stream A's observer (hooks.go): auto-stop's clock
// (emptySince), the resume set and idle sleep. Every method runs with m.mu
// held and must not block.
type lifecycleObserver struct{ m *Manager }

func (o lifecycleObserver) onReady(inst *instance)                           {}
func (o lifecycleObserver) onEvent(inst *instance, ev game.Event)            {}
func (o lifecycleObserver) onExit(inst *instance, exit *ExitInfo, auto bool) {}
func (o lifecycleObserver) onTick(now time.Time)                             {}
func (o lifecycleObserver) onJob(inst *instance, job *Job)                   {}
func (o lifecycleObserver) onTakeover(inst *instance)                        {}
func (o lifecycleObserver) onWake(cause string)                              {}
