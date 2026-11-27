package manager

// Auto-stop (spec 4.5, stream A): a game nobody has played on for its
// minutes (60 by default, for every game, Valheim included from R3) stops
// itself through its stop chain, as a quiet job, so the computer can sleep
// right after it. Only a ready game whose presence is proven (2.6) and
// whose count is 0 is stopped this way; presence "none" or unproven goes
// to the "still playing?" rule instead (ask.go, stream D), because a game
// whose players the app can't see must never be stopped under them.
//
// lifecycleObserver is A's observer (hooks.go): the one place A hears
// about readiness, joins, exits, jobs, ticks and wakes.

import (
	"context"
	"fmt"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
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
	if inst.run == nil || inst.srv == nil || !m.presenceKnownLocked(inst) {
		return nil
	}
	minutes := m.autoStopMinutesLocked(inst)
	if minutes <= 0 {
		return nil
	}
	st := &AutoStopStatus{Minutes: minutes}
	if at, ok := m.autoStopAtLocked(inst); ok {
		st.At = &at
	}
	return st
}

// autoStopMinutesLocked is the instance's auto-stop setting: servers.json
// autoStopMinutes (nil = 60, 0 = off). An instance servers.json doesn't
// list (there is none yet, or it is being added) has the decided hour.
func (m *Manager) autoStopMinutesLocked(inst *instance) int {
	if in := m.servers.Instance(inst.id); in != nil {
		return in.AutoStop()
	}
	return servers.DefaultAutoStopMinutes
}

// presenceKnownLocked says whether the app can see who plays the instance:
// its presence is names or a count, and proven on this install (2.6).
// Auto-stop and idle sleep trust a count of 0 only then.
func (m *Manager) presenceKnownLocked(inst *instance) bool {
	return inst.info.Presence != game.Unknown && m.presenceProvenLocked(inst)
}

// inUse says whether anyone is on a running server, by its own count or
// its list of players.
func inUse(run *serverRun) bool {
	st := run.state()
	return st.Count > 0 || len(st.Players) > 0
}

// autoStopAtLocked is when the instance's server stops itself if nobody
// joins, and whether that is set: it is ready, nobody is on, its players
// can be seen, auto-stop is on, and no job of its own or the app's is
// running (a job resets the clock when it ends).
func (m *Manager) autoStopAtLocked(inst *instance) (time.Time, bool) {
	if running(inst.job) || running(m.appJob) {
		return time.Time{}, false
	}
	return m.autoStopDeadlineLocked(inst)
}

// autoStopDeadlineLocked is autoStopAtLocked without the jobs: when nobody
// will have played for the instance's whole auto-stop time. Going to sleep
// (itself a job) asks it, to leave a game whose time is up out of the
// resume set.
func (m *Manager) autoStopDeadlineLocked(inst *instance) (time.Time, bool) {
	run := inst.run
	if run == nil || !run.ready || run.stopping || run.autoStopped || run.replaying || inUse(run) {
		return time.Time{}, false
	}
	minutes := m.autoStopMinutesLocked(inst)
	if minutes <= 0 || !m.presenceKnownLocked(inst) || inst.emptySince.IsZero() {
		return time.Time{}, false
	}
	return inst.emptySince.Add(time.Duration(minutes) * time.Minute), true
}

// autoStopTickLocked is auto-stop's part of each sample tick (onTick),
// before idle sleep is decided: the clock of every game someone plays
// moves on, and a game whose time is up is stopped (in a goroutine, as an
// observer must not block).
func (m *Manager) autoStopTickLocked(now time.Time) {
	m.eachInstance(func(inst *instance) {
		run := inst.run
		if run == nil {
			return
		}
		switch {
		case inUse(run) || inst.emptySince.IsZero() && run.ready:
			m.seenInUseLocked(inst, now) // in use, or a ready game whose clock wasn't started
			return
		case !run.ready:
			inst.emptySince = now // not counting yet: it is starting
			return
		}
		at, ok := m.autoStopAtLocked(inst)
		if !ok || now.Before(at) || m.life.autoBusy[inst.id] {
			return
		}
		if m.life.autoBusy == nil {
			m.life.autoBusy = map[string]bool{}
		}
		m.life.autoBusy[inst.id] = true
		m.go_(func() {
			if err := m.autoStopGame(inst, stopIdle); err != nil {
				m.log.Printf("auto-stop of %s: %v", inst.id, err)
			}
			m.mu.Lock()
			delete(m.life.autoBusy, inst.id)
			m.mu.Unlock()
		})
	})
}

// autoStopGame stops a game that nobody is using: it marks the run
// autoStopped with why (stopIdle for auto-stop, stopAsked for D's "still
// playing?" rule) and, for stopIdle, autoMinutes with the instance's
// minutes, then runs the stop chain as a quiet job (runQuietJob), so
// neither the job nor the exit resets machineIdleSince. M3's onExit then
// picks the stopped post's reason and the activity entry
// (autoStopActivityLocked) from the run. A stop that can't be done (a job
// is running, or the server doesn't stop) takes the marks off again, and
// the clock starts again, so a game that keeps refusing is tried once per
// period, not every tick. It runs without m.mu: an observer starts it with
// m.go_.
func (m *Manager) autoStopGame(inst *instance, why stopReason) error {
	m.mu.Lock()
	run := inst.run
	if run == nil || run.stopping {
		m.mu.Unlock()
		return nil // it is stopping or has stopped already
	}
	minutes := 0
	if why == stopIdle {
		minutes = m.autoStopMinutesLocked(inst)
	}
	run.autoStopped, run.autoReason, run.autoMinutes = true, why, minutes
	m.mu.Unlock()
	// undo takes the marks off a stop that didn't happen and starts the
	// auto-stop clock again. It leaves the idle clock alone, and so does
	// the end of the stop job that failed (job, through autoFailed and
	// onJob): nobody did anything, and a server that won't stop mustn't
	// keep the computer awake by failing every hour. Going to sleep then
	// tries to stop it once more, and sleeps anyway if it still won't
	// (stopForRest), as before auto-stop existed.
	undo := func(job *Job) {
		m.mu.Lock()
		if inst.run == run {
			run.autoStopped, run.autoReason, run.autoMinutes = false, stopStopped, 0
			inst.emptySince = m.clock().Now()
		}
		if job != nil {
			if m.life.autoFailed == nil {
				m.life.autoFailed = map[string]*Job{}
			}
			m.life.autoFailed[inst.id] = job
		}
		m.mu.Unlock()
	}
	err := m.runQuietJob(inst, "stop", "Stopping the server", func(ctx context.Context, j *jobCtl) error {
		err := m.doStop(ctx, j, inst)
		if err != nil {
			undo(j.job)
		}
		return err
	})
	if err != nil {
		undo(nil) // no job started: another one runs, and its end restarts both clocks
	}
	return err
}

// autoStopText says how long nobody played, from the instance's minutes,
// exactly as spec 4.5 gives it: "an hour" at 60, "2 hours" at 120,
// otherwise "<n> minutes" ("90 minutes", and "180 minutes" too: the
// setting is in minutes, so the text says what the owner typed). The
// activity entry and D's stopped post (stopIdle) both use it: "nobody
// played for an hour".
func autoStopText(minutes int) string {
	switch minutes {
	case 60:
		return "an hour"
	case 120:
		return "2 hours"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// autoStopActivityLocked is the activity entry for a run that auto-stop or
// the "still playing?" rule stopped, which M3's onExit adds in place of
// "The server stopped.": "Minecraft stopped: nobody played for an hour."
// (4.5), from the minutes the stop was decided with (run.autoMinutes), and
// for the ask rule "Windrose stopped: nobody answered for 2 hours.", the
// same reason its "Turning … off" post gives (4.6). m.mu must be held.
func (m *Manager) autoStopActivityLocked(run *serverRun) string {
	name := run.inst.short()
	if run.autoReason == stopAsked {
		return name + " stopped: nobody answered for 2 hours."
	}
	return fmt.Sprintf("%s stopped: nobody played for %s.", name, autoStopText(run.autoMinutes))
}

// seenInUseLocked restarts the instance's auto-stop clock (emptySince) at
// now, because the game was just seen in use, and keeps the idle-sleep
// clock (machineIdleSince) from being behind it.
//
// The two clocks must not fight (spec 4.5): with both at 60 minutes, the
// game has to be stopped by auto-stop first, so the computer sleeps right
// after with nothing to start again when it wakes. Auto-stop is decided
// before idle sleep in each tick, so that holds whenever the auto-stop
// clock started no later than the idle one. But the auto-stop clock also
// restarts at moments the idle clock doesn't: when the game becomes ready
// (the idle clock started when its start began, a minute or more before),
// when the last player leaves (the idle clock last moved at the tick
// before), when one of its jobs ends (the idle clock moved when it began).
// Then the idle clock ran out first, up to a few minutes early, and going
// to sleep stopped the game and kept it to start again on the next wake,
// though nobody had played it for an hour. So each time the auto-stop
// clock of a game auto-stop watches restarts, the idle clock moves on to
// the same moment if it is behind: the computer stays awake at most those
// few minutes longer, and auto-stop always comes first. A game being
// stopped by auto-stop or the "still playing?" rule (autoStopped) moves
// nothing: its stop isn't someone doing something, and neither is one of
// those stops failing (autoStopGame restarts only the auto-stop clock).
func (m *Manager) seenInUseLocked(inst *instance, now time.Time) {
	inst.emptySince = now
	run := inst.run
	if run == nil || run.autoStopped || m.autoStopMinutesLocked(inst) <= 0 || !m.presenceKnownLocked(inst) {
		return // auto-stop doesn't watch it: its clock can't race the idle one
	}
	if m.machineIdleSince.Before(now) {
		m.machineIdleSince = now
	}
}

// lifecycleObserver is stream A's observer (hooks.go): auto-stop's clock
// (emptySince), the resume set and idle sleep. Every method runs with m.mu
// held and must not block.
type lifecycleObserver struct{ m *Manager }

// The auto-stop clock starts again whenever the game is seen in use: it
// became ready (or was taken over), someone joined or left (they were
// playing until then), the server counted players, or one of its jobs
// started or ended. Each time, the idle clock is kept in step with it
// (seenInUseLocked).
func (o lifecycleObserver) onReady(inst *instance) { o.m.seenInUseLocked(inst, o.m.clock().Now()) }

func (o lifecycleObserver) onEvent(inst *instance, ev game.Event) {
	switch {
	case ev.Kind == game.KindJoin, ev.Kind == game.KindLeave, ev.Kind == game.KindCount && ev.Count > 0:
		o.m.seenInUseLocked(inst, o.m.clock().Now())
	}
}

func (o lifecycleObserver) onExit(inst *instance, run *serverRun, exit *ExitInfo) {
	inst.emptySince = time.Time{}
}

func (o lifecycleObserver) onTick(now time.Time) { o.m.autoStopTickLocked(now) }

// onJob: the end of an auto-stop's job whose server didn't stop restarts
// only the auto-stop clock (see autoStopGame's undo); every other job's
// start and end is the game seen in use.
func (o lifecycleObserver) onJob(inst *instance, job *Job) {
	if inst == nil {
		return
	}
	m := o.m
	if failed := m.life.autoFailed[inst.id]; failed != nil && failed == job {
		delete(m.life.autoFailed, inst.id)
		inst.emptySince = m.clock().Now()
		return
	}
	m.seenInUseLocked(inst, m.clock().Now())
}

func (o lifecycleObserver) onTakeover(inst *instance) { o.m.seenInUseLocked(inst, o.m.clock().Now()) }

// onWake: a Discord request's packet (tagged) means that request starts
// only its own game, so the resume after waking is called off; a packet
// from the KVM's own Wake-on-LAN button (untagged) resumes at once (4.7).
// A wake on purpose schedules the resume in wokenOnPurpose itself.
func (o lifecycleObserver) onWake(cause string) {
	m := o.m
	switch cause {
	case wakeTagged:
		m.life.taggedAt = m.clock().Now()
		m.cancelResumeLocked()
	case wakeUntagged:
		if len(m.resumeSet) > 0 {
			m.go_(m.resumeNow)
		}
	}
}
