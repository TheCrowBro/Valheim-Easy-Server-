package manager

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/stats"
)

// Live is the latest usage reading.
type Live struct {
	stats.Sample
	MemTotal uint64               `json:"memTotal"`
	GPUs     []platform.GPUSample `json:"gpus"`
	GPUOK    bool                 `json:"gpuOk"`
	NetOK    bool                 `json:"netOk"`
	Cores    float64              `json:"cores"` // CPU cores the server keeps busy
}

const wakeHint = "Wake it with Wake-on-LAN (for example from an IP KVM) or its power button."

// sleptResult ends a sleep job. On Windows, Sleep returns once the computer
// has woken up again, so it also says when that happened (see sleptSince).
const sleptResult = "The computer slept and has woken up again."

// How the app tells whether someone is using the computer and what woke it
// up, whether it can be only half awake (a Mac's dark wake), and whether
// Sleep returns only once the computer wakes up (a Mac's returns before it
// sleeps). Replaced in tests.
var (
	idleTime     = platform.IdleTime
	darkWake     = platform.DarkWake
	wokeBy       = platform.WokeBy
	darkWakes    = runtime.GOOS == "darwin"
	sleepReturns = runtime.GOOS == "windows"
)

// Hibernate saves the world, stops the server and puts the computer to
// rest: into hibernation, or to sleep if the settings say so.
func (m *Manager) Hibernate(reason string) error {
	m.mu.Lock()
	sleep := m.settings.RestMode == config.RestSleep
	m.mu.Unlock()
	title := "Hibernating the computer"
	if sleep {
		title = "Putting the computer to sleep"
	}
	return m.runJob("hibernate", title, false, func(ctx context.Context, j *jobCtl) error {
		if ok, detail := m.restStatus(sleep); !ok {
			if sleep {
				return &ActionError{Msg: detail, Action: "settings"}
			}
			return &ActionError{Msg: detail, Action: "enable_hibernate"}
		}
		m.mu.Lock()
		running := m.run != nil
		m.mu.Unlock()
		if running {
			if err := m.doStop(ctx, j); err != nil {
				// Resting keeps everything in memory, so nothing is lost
				// even if the server didn't stop.
				m.log.Printf("stopping before resting: %v", err)
			}
		}
		m.mu.Lock()
		// A restart still owed from an earlier sleep stays owed: the computer
		// may have woken by itself (a timer) and gone back to sleep since,
		// and only someone waking it uses that up (wokenOnPurpose).
		m.resumeOnWake = running || m.resumeOnWake
		m.mu.Unlock()
		text := "The computer is going into hibernation. " + wakeHint
		if sleep {
			text = "The computer is going to sleep. " + wakeHint
		}
		if reason != "" {
			text = reason + " " + text
		}
		m.addActivity("hibernate", "", text)
		j.step("Going to rest… "+wakeHint, -1)
		m.saveData()
		time.Sleep(4 * time.Second) // let open control panels show the message
		m.flushDiscord(15 * time.Second)
		rest := m.opt.Hibernate
		if sleep {
			rest = m.opt.Sleep
		}
		if err := rest(); err != nil {
			m.mu.Lock()
			m.resumeOnWake = false
			m.mu.Unlock()
			return err
		}
		if sleep {
			j.result(sleptResult)
		} else {
			j.result("The computer hibernated.")
		}
		return nil
	})
}

// restStatus reports whether the computer can hibernate (or sleep).
func (m *Manager) restStatus(sleep bool) (bool, string) {
	if sleep {
		return m.opt.SleepStatus()
	}
	return m.opt.HibernateStatus()
}

// EnableWakeOnLAN lets the wired network adapters wake the computer
// (Windows asks for admin permission on the screen).
func (m *Manager) EnableWakeOnLAN() error {
	err := platform.EnableWakeOnLAN()
	m.recheckSoon()
	return err
}

// EnableHibernation turns hibernation on (Windows asks for admin
// permission on the screen).
func (m *Manager) EnableHibernation() error {
	if !config.CanHibernate() {
		return &ActionError{Msg: "This version of the app only puts the computer to sleep."}
	}
	err := platform.EnableHibernate()
	m.recheckSoon()
	return err
}

// KeepAwake postpones automatic hibernation (or sleep).
func (m *Manager) KeepAwake() {
	m.mu.Lock()
	m.idleSince = time.Now()
	m.keepAwakeUntil = time.Now().Add(time.Duration(m.settings.IdleMinutes) * time.Minute)
	sleep := m.settings.RestMode == config.RestSleep
	m.mu.Unlock()
	if sleep {
		m.addActivity("info", "", "Automatic sleep was postponed.")
	} else {
		m.addActivity("info", "", "Automatic hibernation was postponed.")
	}
	m.notify()
}

// sampleLoop reads CPU/GPU/RAM usage, notices waking from sleep, and
// handles automatic hibernation.
func (m *Manager) sampleLoop() {
	tick := time.NewTicker(m.opt.SampleInterval)
	defer tick.Stop()
	last := time.Now()
	before := last // the reading before last
	saved := time.Now()
	// woke follows the latest wake-up, while it isn't known yet whether
	// someone wants the computer awake (see wakeWanted).
	var woke pendingWake
	var lastWake time.Time
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-tick.C:
			// The wall clock keeps running while the computer rests (unlike
			// the clock timers use on macOS and Linux), so a long pause
			// between readings, or more time than the timers saw, means it
			// has just woken up.
			wall := now.Round(0).Sub(last.Round(0))
			gap := wall
			if gap <= 0 { // the clock was set back
				gap = m.opt.SampleInterval
			}
			// On Windows the timers' clock keeps running while it sleeps, so a
			// short sleep there only shows when the app's own sleep ends. A
			// long one shows both ways: count it once.
			slept := m.rested(wall, now.Sub(last)) || (now.Sub(lastWake) > time.Minute && m.sleptSince(last))
			asleep := before // taken before it went to rest, even if the app's sleep ended just after a reading
			before, last = last, now
			if slept {
				woke, lastWake = pendingWake{at: now, asleep: asleep}, now
				m.onWake(gap)
				continue
			}
			// Until someone turns up, keep watching: a computer that woke up
			// by itself goes back to sleep by itself, and the next wake-up
			// starts again. A Mac can stay in a dark wake for a long time
			// (a backup, say) before someone wakes it fully.
			if !woke.at.IsZero() && m.wakeWanted(&woke) {
				woke = pendingWake{}
				m.wokenOnPurpose()
			}
			m.updateAwake()
			m.sample(now, gap)
			m.checkIdle(now)
			if now.Sub(saved) > 5*time.Minute {
				saved = now
				m.saveData()
			}
		}
	}
}

func (m *Manager) sample(now time.Time, gap time.Duration) {
	sys := m.sys.Sample()
	m.mu.Lock()
	run := m.run
	players, ready := 0, false
	if run != nil {
		players, ready = run.tracker.Count(), run.ready
	}
	m.mu.Unlock()

	var cpu, cores float64
	var mem uint64
	if run != nil {
		if used, rss, err := run.proc.Usage(); err == nil {
			mem = rss
			m.mu.Lock()
			if !run.cpuPrevAt.IsZero() && used >= run.cpuPrev {
				wall := now.Sub(run.cpuPrevAt).Seconds()
				if wall > 0 {
					cores = (used - run.cpuPrev).Seconds() / wall
					cpu = cores * 100 / float64(runtime.NumCPU())
				}
			}
			run.cpuPrev, run.cpuPrevAt = used, now
			m.mu.Unlock()
		}
	}
	gpu := 0.0
	for _, g := range sys.GPUs {
		if g.Percent > gpu {
			gpu = g.Percent
		}
	}
	s := stats.Sample{T: now.UnixMilli(), CPU: clamp(cpu), CPUSys: sys.CPUPercent, Mem: mem, MemSys: sys.MemUsed,
		GPU: gpu, NetUp: sys.NetUp, NetDown: sys.NetDown, Players: players, Running: run != nil && ready}
	m.history.Add(s)
	m.byPlayers.Add(s, gap.Seconds())
	m.mu.Lock()
	m.live = Live{Sample: s, MemTotal: sys.MemTotal, GPUs: sys.GPUs, GPUOK: sys.GPUOK, NetOK: sys.NetOK, Cores: cores}
	m.mu.Unlock()
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// wakeGrace is how long the computer stays awake after someone wakes it up
// (or asks for the server with a wake signal), so friends have time to
// join. Windows itself goes back to sleep about two minutes after an
// unattended wake (such as Wake-on-LAN) unless asked not to.
var wakeGrace = 15 * time.Minute

// wakeWatch is how long after the computer wakes up the app asks the
// system what woke it, every wakeAskEvery: Windows says a few seconds after
// it wakes up.
var (
	wakeWatch    = 5 * time.Minute
	wakeAskEvery = 5 * time.Second
)

// pendingWake is a wake-up the app has noticed, while it isn't known yet
// whether someone wants the computer awake.
type pendingWake struct {
	at     time.Time          // when the app noticed it
	asleep time.Time          // a reading from before the computer went to rest
	cause  platform.WakeCause // what woke it, once the system says
	asked  time.Time          // when the app last asked the system
}

// rested reports whether the computer rested between two readings, from
// the wall-clock time between them and the time timers saw. On macOS and
// Linux timers don't count the time asleep, which shows even a short sleep.
func (m *Manager) rested(wall, timer time.Duration) bool {
	return wall-timer > 5*time.Second || wall > m.opt.SampleInterval+20*time.Second
}

// sleptSince reports whether a sleep the app started has ended since t,
// where Sleep returns only once the computer has woken up (Windows).
func (m *Manager) sleptSince(t time.Time) bool {
	if !sleepReturns {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.job
	return j != nil && j.Kind == "hibernate" && j.Result == sleptResult && j.Finished != nil && j.Finished.After(t)
}

// onWake runs when the computer has just come back from sleep or
// hibernation. It may have woken up by itself (a Mac for network
// maintenance, Windows for updates) and should then go back to sleep, so
// nothing keeps it awake until someone is known to want it (see wakeWanted).
func (m *Manager) onWake(gap time.Duration) {
	m.mu.Lock()
	m.idleSince = time.Now()
	m.wakeHold = time.Time{}     // a hold from before it slept doesn't count any more
	m.net.publicAt = time.Time{} // the address may have changed
	m.mu.Unlock()
	m.updateAwake()
	m.log.Printf("woke up after %s", gap.Round(time.Second))
	m.notify()
}

// wakeWanted reports whether someone wants the computer awake since it woke
// up: a wake signal was heard (it holds the computer awake), a device woke
// it rather than a timer, or someone is using the computer itself. The
// device can be the power button or the network adapter: the Wake-on-LAN
// packet from an IP KVM's own Wake button wakes the computer, but the
// adapter keeps it, so the app never hears it. A Mac someone uses is fully
// awake, not in a dark wake; elsewhere, they have used the keyboard or
// mouse.
func (m *Manager) wakeWanted(w *pendingWake) bool {
	m.mu.Lock()
	heard := m.wakeHold.After(w.at) // onWake dropped any older hold
	m.mu.Unlock()
	if heard {
		return true
	}
	now := time.Now()
	if w.cause == platform.WakeCauseUnknown && now.Sub(w.at) < wakeWatch && now.Sub(w.asked) >= wakeAskEvery {
		w.asked = now
		w.cause = wokeBy(w.asleep)
	}
	if w.cause == platform.WakeByDevice {
		return true
	}
	if darkWakes {
		return !darkWake()
	}
	if idle, ok := idleTime(); ok && idle < now.Sub(w.at) {
		return true // someone is using it
	}
	// When Windows hasn't said what woke it (the event can be late, or not
	// readable), it counts as someone waking it, as it always did: only a
	// wake Windows says was a timer is left alone. Windows goes back to
	// sleep about two minutes after a wake nobody holds, so this can't wait
	// longer than unknownWait.
	return w.cause == platform.WakeCauseUnknown && now.Sub(w.at) >= unknownWait
}

// unknownWait is how long the app waits for Windows to say what woke it.
var unknownWait = 45 * time.Second

// holdAwake keeps the computer awake for a while, so friends have time to
// join: someone asked for it.
func (m *Manager) holdAwake() {
	if darkWake() {
		platform.AfterWake() // a Wake-on-LAN signal can leave a Mac only half awake
	}
	m.mu.Lock()
	m.wakeHold = time.Now().Add(wakeGrace)
	if m.keepAwakeUntil.Before(m.wakeHold) {
		m.keepAwakeUntil = m.wakeHold
	}
	m.mu.Unlock()
	m.updateAwake()
}

// wokenOnPurpose runs once someone wants the computer awake after it woke
// up: it stays awake for a while, and the server starts again if it was
// running before the computer went to sleep.
func (m *Manager) wokenOnPurpose() {
	m.holdAwake()
	m.mu.Lock()
	resume := m.resumeOnWake && m.settings.StartAfterWake && m.run == nil
	m.resumeOnWake = false
	m.mu.Unlock()
	m.addActivity("wake", "", fmt.Sprintf("The computer woke up. It stays awake for at least %d minutes, so friends have time to join.", int(wakeGrace.Minutes())))
	if resume {
		m.addActivity("info", "", "Starting the server again in 20 seconds (it was running before the computer went to sleep).")
		time.AfterFunc(20*time.Second, func() {
			if m.ctx.Err() != nil {
				return
			}
			if err := m.StartServer(); err != nil {
				m.log.Printf("start after wake: %v", err)
			}
		})
	}
	m.notify()
}

// wantAwakeLocked reports whether Windows should be kept from sleeping:
// while the server runs, for a while after waking up, and while a job
// (other than resting) is in progress, such as starting the server.
func (m *Manager) wantAwakeLocked(now time.Time) bool {
	job := m.job != nil && m.job.Finished == nil && m.job.Kind != "hibernate"
	return m.run != nil || now.Before(m.wakeHold) || job
}

// updateAwake asks Windows to stay awake, or stops asking, as needed. It
// doesn't stop the computer being put to rest on purpose.
func (m *Manager) updateAwake() {
	if m.awake == nil {
		return // not started
	}
	m.mu.Lock()
	want := m.wantAwakeLocked(time.Now())
	m.mu.Unlock()
	m.awake.Set(want)
}

// idleDeadlineLocked is when automatic hibernation will happen, or zero.
func (m *Manager) idleDeadlineLocked() time.Time {
	if !m.settings.IdleHibernate {
		return time.Time{}
	}
	can := m.checks.hibernate.Available
	if m.settings.RestMode == config.RestSleep {
		can = m.checks.sleep.Available
	}
	if !m.checks.at.IsZero() && !can { // it would never happen
		return time.Time{}
	}
	if m.run != nil && m.run.tracker.Count() > 0 {
		return time.Time{}
	}
	if m.job != nil && m.job.Finished == nil && m.job.Kind != "hibernate" {
		return time.Time{} // it waits for the job to end
	}
	at := m.idleSince.Add(time.Duration(m.settings.IdleMinutes) * time.Minute)
	if m.keepAwakeUntil.After(at) {
		at = m.keepAwakeUntil
	}
	return at
}

func (m *Manager) checkIdle(now time.Time) {
	m.mu.Lock()
	if !m.settings.IdleHibernate || (m.run != nil && m.run.tracker.Count() > 0) {
		m.idleSince = now
		m.mu.Unlock()
		return
	}
	busy := m.job != nil && m.job.Finished == nil
	deadline := m.idleDeadlineLocked()
	minutes := m.settings.IdleMinutes
	sleep := m.settings.RestMode == config.RestSleep
	m.mu.Unlock()
	if busy || now.Before(deadline) {
		return
	}
	// Don't hibernate while someone is using the computer itself. Count from
	// their last input instead, so the panel shows when it really will.
	if idle, ok := idleTime(); ok && idle < time.Duration(minutes)*time.Minute {
		m.mu.Lock()
		if used := now.Add(-idle); used.After(m.idleSince) {
			m.idleSince = used
		}
		m.mu.Unlock()
		return
	}
	if ok, _ := m.restStatus(sleep); !ok {
		return
	}
	// If hibernating fails for some reason, try again in a while rather
	// than on every tick.
	m.mu.Lock()
	m.keepAwakeUntil = now.Add(10 * time.Minute)
	m.mu.Unlock()
	reason := fmt.Sprintf("Nobody has played for %d minutes.", minutes)
	if err := m.Hibernate(reason); err != nil {
		m.log.Printf("idle hibernate: %v", err)
	}
}

// History returns usage samples taken after since.
func (m *Manager) History(since time.Time) []stats.Sample { return m.history.Since(since) }

// ByPlayers summarises usage by the number of players online.
func (m *Manager) ByPlayers() stats.Summary { return m.byPlayers.Summary() }

// ResetStats clears the usage-by-players summary.
func (m *Manager) ResetStats() {
	m.byPlayers.Reset()
	m.saveData()
	m.addActivity("info", "", "The usage statistics were reset.")
}
