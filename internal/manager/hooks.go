package manager

// The observer list (spec 4.1): how R3's lifecycle features hear what
// happens to a game without editing server.go, jobs.go or power.go. Those
// files call every observer at fixed points, placed in phase 0'; each
// feature's stream fills in its own observer's methods in its own file and
// never moves the calls:
//
//	lifecycleObserver  autostop.go  stream A: auto-stop, the resume set, idle sleep
//	askObserver        ask.go       stream D: the "still playing?" rule, Discord
//	wakerObserver      waker.go     stream WK: the waker's commands
//	routerObserver     router.go    stream N: router ports
//
// Every method is called with m.mu held, so it must not block: no network,
// no waiting for a process, no disk work worth noticing, and no call to a
// Manager method that takes m.mu (StartGame, Status, runQuietJob…). To act,
// an observer notes what to do in its own state and starts the work with
// m.go_, which runs once the caller has let the lock go.
//
// A test can add its own observer to m.observers (under m.mu) to see the
// calls.

import (
	"runtime/debug"
	"time"

	"valheim-easy-server/internal/game"
)

type observer interface {
	// onReady: a run this app follows live became ready. A run the app took
	// over gets onTakeover instead, once the app has caught up with its log
	// (inst.run.ready then says whether it is ready); if it only becomes
	// ready after that, onReady follows.
	onReady(inst *instance)
	// onEvent: each event of a live line or tick (joins, leaves, counts, a
	// game's own kinds), after the manager has noted it. Events read back
	// from a taken-over server's log don't come here, nor the leaves at the
	// end of a run: onExit says that.
	onEvent(inst *instance, ev game.Event)
	// onExit: the run ended. run is that run, handed over because inst.run
	// is already nil (so anything asking "is it running?" already says no):
	// run.rec has the ports it started with, run.mappings its router ports
	// (N unmaps them), run.autoStopped says auto-stop or the "still
	// playing?" rule stopped it and run.autoReason which (stopIdle or
	// stopAsked). inst.lastExit is exit. The run's fields stay guarded by
	// m.mu, like a running one's.
	onExit(inst *instance, run *serverRun, exit *ExitInfo)
	// onTick: every sample tick (Options.SampleInterval, 2 s), after the
	// usage sample and before the idle-sleep check, so auto-stop is decided
	// first (spec 4.5). Not called on the tick that notices a wake-up.
	onTick(now time.Time)
	// onJob: a job started (job.Finished is nil) or finished in inst's slot;
	// inst is nil for the app's slot (Sleep).
	onJob(inst *instance, job *Job)
	// onTakeover: the app took over a server that was already running (after
	// an app restart or an update) and has caught up with its log.
	onTakeover(inst *instance)
	// onWake: wakePurpose when someone woke the computer (a device, or a
	// person using it), wakeTagged for each wake packet of a Discord request
	// (the waker repeats one every 10 s for 3 minutes), wakeUntagged for
	// each packet without a request (the KVM's own Wake-on-LAN button).
	onWake(cause string)
}

// The causes onWake is called with.
const (
	wakePurpose  = "purpose"
	wakeTagged   = "tagged"
	wakeUntagged = "untagged"
)

// newObservers is the observer list, in the order they are called. Stream
// A's comes first, so auto-stop is decided before anything else in a tick.
func newObservers(m *Manager) []observer {
	return []observer{lifecycleObserver{m}, askObserver{m}, wakerObserver{m}, routerObserver{m}}
}

// observeLocked calls fn for every observer, in order. m.mu must be held.
// A panic in one observer is logged; the others still run, and the lock
// stays with the caller, who lets it go as usual.
func (m *Manager) observeLocked(fn func(o observer)) {
	for _, o := range m.observers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.log.Printf("panic in %T: %v\n%s", o, r, debug.Stack())
				}
			}()
			fn(o)
		}()
	}
}
