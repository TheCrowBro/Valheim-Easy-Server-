package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/worlds"
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

// Hibernate saves the worlds, stops every running game and puts the
// computer to rest: into hibernation, or to sleep if the settings say so.
// The games that were running are remembered (resumeSet), so waking the
// computer on purpose starts them again (resume.go).
func (m *Manager) Hibernate(reason string) error {
	m.mu.Lock()
	sleep := m.settings.RestMode == config.RestSleep
	m.mu.Unlock()
	title := "Hibernating the computer"
	if sleep {
		title = "Putting the computer to sleep"
	}
	return m.runAppJob("hibernate", title, false, func(ctx context.Context, j *jobCtl) error {
		if ok, detail := m.restStatus(sleep); !ok {
			if sleep {
				return &ActionError{Msg: detail, Action: "settings"}
			}
			return &ActionError{Msg: detail, Action: "enable_hibernate"}
		}
		m.mu.Lock()
		now := m.clock().Now()
		var running []*instance
		var owed []string
		m.eachInstance(func(inst *instance) {
			if inst.run == nil {
				return // a foreign server can't be stopped: it sleeps with the computer
			}
			running = append(running, inst)
			if m.resumableLocked(inst, now) {
				owed = append(owed, inst.id)
			}
		})
		m.mu.Unlock()
		m.stopForRest(ctx, j, running)
		m.mu.Lock()
		// A restart still owed from an earlier sleep stays owed: the computer
		// may have woken by itself (a timer) and gone back to sleep since,
		// and only someone waking it uses that up (wokenOnPurpose).
		for _, id := range owed {
			if !slices.Contains(m.resumeSet, id) {
				m.resumeSet = append(m.resumeSet, id)
			}
		}
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
		m.saveGameStats()
		time.Sleep(4 * time.Second) // let open control panels show the message
		// Each stopped game posts in its own channel; with several, give
		// Discord longer to take them all.
		flush := 15 * time.Second
		if len(running) > 1 {
			flush = 30 * time.Second
		}
		m.flushDiscord(flush)
		rest := m.opt.Hibernate
		if sleep {
			rest = m.opt.Sleep
		}
		if err := rest(); err != nil {
			m.mu.Lock()
			m.resumeSet = nil // nothing slept, so nothing is owed: the games stay stopped
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

// stopForRest stops the running games before the computer rests, each
// through its stop chain, all at the same time (spec 4.2), so several
// games save in the time one takes. One game stops exactly as before, with
// its steps shown in the sleep job; with several, the job says which are
// stopping. A game that doesn't stop is left running: resting keeps
// everything in memory, so nothing is lost.
func (m *Manager) stopForRest(ctx context.Context, j *jobCtl, running []*instance) {
	if len(running) == 1 {
		if err := m.doStop(ctx, j, running[0]); err != nil {
			m.log.Printf("stopping before resting: %v", err)
		}
		return
	}
	if len(running) == 0 {
		return
	}
	ids := make([]string, len(running))
	for i, inst := range running {
		ids[i] = inst.id
	}
	m.mu.Lock()
	names := m.resumeNamesLocked(ids)
	m.mu.Unlock()
	j.step("Saving the worlds and stopping "+names+"… Please don't turn off the computer.", -1)
	var wg sync.WaitGroup
	for _, inst := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer m.recoverPanic()
			// Its own steps go to a job nobody sees: the sleep job says it.
			own := &jobCtl{m: m, job: &Job{Kind: "stop", Game: inst.id}}
			if err := m.doStop(ctx, own, inst); err != nil {
				m.log.Printf("stopping %s before resting: %v", inst.id, err)
			}
		}()
	}
	wg.Wait()
}

// resumableLocked says whether a game running as the computer goes to rest
// is started again when it wakes: not one auto-stop was about to stop
// (nobody played it for its whole time), a hidden legacy game (I1) or one
// turned off (8.3).
func (m *Manager) resumableLocked(inst *instance, now time.Time) bool {
	if inst.srv == nil || m.isHiddenLocked(inst) || m.isDisabledLocked(inst) {
		return false
	}
	if at, ok := m.autoStopDeadlineLocked(inst); ok && !now.Before(at) {
		return false
	}
	return true
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
	m.machineIdleSince = time.Now()
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
			m.saveGameStats() // the other games' charts survive the app closing
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
			// The clocks of auto-stop and idle sleep read the lifecycle
			// clock, which is the real one except in tests, where a fake
			// one lets an hour pass in a moment.
			at := m.clock().Now()
			// hooks.go: auto-stop and the other clocks, before idle sleep
			// is decided (spec 4.5). Keep this call where it is.
			m.mu.Lock()
			m.observeLocked(func(o observer) { o.onTick(at) })
			m.mu.Unlock()
			m.checkIdle(at)
			if now.Sub(saved) > 5*time.Minute {
				saved = now
				m.saveData()
				m.saveGameStats()
			}
		}
	}
}

// sample reads the machine's usage and every running game's: the legacy
// game's goes into history.json with the machine's, as it always did; each
// other game's into its own history (games/<id>/history.json, saved by
// saveGameStats). Each run keeps its latest memory and CPU (memNow,
// cpuNow) for Status and the memory check.
func (m *Manager) sample(now time.Time, gap time.Duration) {
	sys := m.sys.Sample()
	m.mu.Lock()
	run := m.legacy().run
	players, ready := 0, false
	if run != nil {
		players, ready = run.state().Count, run.ready
	}
	var others []*serverRun
	m.eachInstance(func(inst *instance) {
		if inst.run != nil && !inst.legacy {
			others = append(others, inst.run)
		}
	})
	m.mu.Unlock()

	var cpu, cores float64
	var mem uint64
	if run != nil {
		cpu, cores, mem = m.runUsage(run, now)
	}
	m.sampleOthers(others, now, gap)
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

// runUsage reads a run's CPU (percent of the whole computer, and cores
// kept busy) and memory since the last reading, and keeps them on the run.
// Nothing is known (zeros) when the system won't say.
func (m *Manager) runUsage(run *serverRun, now time.Time) (cpu, cores float64, mem uint64) {
	used, rss, err := run.proc.Usage()
	if err != nil {
		return 0, 0, 0
	}
	mem = rss
	m.mu.Lock()
	defer m.mu.Unlock()
	if !run.cpuPrevAt.IsZero() && used >= run.cpuPrev {
		wall := now.Sub(run.cpuPrevAt).Seconds()
		if wall > 0 {
			cores = (used - run.cpuPrev).Seconds() / wall
			cpu = cores * 100 / float64(runtime.NumCPU())
		}
	}
	run.cpuPrev, run.cpuPrevAt = used, now
	run.memNow, run.cpuNow = rss, clamp(cpu)
	return cpu, cores, mem
}

// sampleOthers adds a sample of each running non-legacy game to its own
// history and usage by players, loading what was saved of them the first
// time.
func (m *Manager) sampleOthers(runs []*serverRun, now time.Time, gap time.Duration) {
	for _, r := range runs {
		m.loadGameStats(r.inst)
		cpu, _, mem := m.runUsage(r, now)
		m.mu.Lock()
		players, ready := 0, r.ready
		if st := r.state(); st.Count > 0 {
			players = st.Count
		}
		h, b := r.inst.history, r.inst.byPlayers
		m.mu.Unlock()
		s := stats.Sample{T: now.UnixMilli(), CPU: clamp(cpu), Mem: mem, Players: players, Running: ready}
		h.Add(s)
		b.Add(s, gap.Seconds())
	}
}

// historySize is how many samples a day of history holds.
func (m *Manager) historySize() int { return int(24 * time.Hour / m.opt.SampleInterval) }

// loadGameStats gives a non-legacy instance its history and usage by
// players, from games/<id>/history.json and stats.json, the first time it
// needs them. The files are read without m.mu.
func (m *Manager) loadGameStats(inst *instance) {
	m.mu.Lock()
	loaded := inst.legacy || inst.history != nil
	m.mu.Unlock()
	if loaded {
		return
	}
	h := stats.NewHistory(m.historySize())
	if b, err := os.ReadFile(m.instFile(inst, "history.json")); err == nil {
		var saved []stats.Sample
		if json.Unmarshal(b, &saved) == nil {
			cutoff := time.Now().Add(-24 * time.Hour).UnixMilli()
			for _, s := range saved {
				if s.T > cutoff {
					h.Add(s)
				}
			}
		}
	}
	bp, err := stats.Load(m.instFile(inst, "stats.json"))
	if err != nil {
		bp = stats.NewByPlayers()
	}
	m.mu.Lock()
	if inst.history == nil {
		inst.history, inst.byPlayers = h, bp
	}
	m.mu.Unlock()
}

// saveGameStats writes each non-legacy game's history (the last day, made
// lighter, as history.json is) and usage by players into its own folder,
// never into the legacy files (invariant I8).
func (m *Manager) saveGameStats() {
	type saving struct {
		inst *instance
		h    *stats.History
		b    *stats.ByPlayers
	}
	var all []saving
	m.mu.Lock()
	m.eachInstance(func(inst *instance) {
		if !inst.legacy && inst.history != nil {
			all = append(all, saving{inst, inst.history, inst.byPlayers})
		}
	})
	m.mu.Unlock()
	for _, s := range all {
		if err := os.MkdirAll(filepath.Dir(m.instFile(s.inst, "history.json")), 0o755); err != nil {
			continue
		}
		day := stats.Downsample(s.h.Since(time.Now().Add(-24*time.Hour)), 2880)
		if b, err := json.Marshal(day); err == nil {
			config.WriteFileAtomic(m.instFile(s.inst, "history.json"), b, 0o644)
		}
		if b, err := json.Marshal(s.b); err == nil {
			config.WriteFileAtomic(m.instFile(s.inst, "stats.json"), b, 0o644)
		}
	}
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
	j := m.appJob
	return j != nil && j.Kind == "hibernate" && j.Result == sleptResult && j.Finished != nil && j.Finished.After(t)
}

// onWake runs when the computer has just come back from sleep or
// hibernation. It may have woken up by itself (a Mac for network
// maintenance, Windows for updates) and should then go back to sleep, so
// nothing keeps it awake until someone is known to want it (see wakeWanted).
func (m *Manager) onWake(gap time.Duration) {
	m.mu.Lock()
	m.machineIdleSince = time.Now()
	m.wakeHold = time.Time{}     // a hold from before it slept doesn't count any more
	m.net.publicAt = time.Time{} // the address may have changed
	m.life.wokeAt = m.clock().Now()
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
// up: it stays awake for a while, and the games that were running before
// the computer went to sleep start again once the network is back
// (scheduleResume, resume.go), unless a Discord request came for one game.
func (m *Manager) wokenOnPurpose() {
	m.holdAwake()
	m.mu.Lock()
	m.observeLocked(func(o observer) { o.onWake(wakePurpose) }) // hooks.go
	m.mu.Unlock()
	m.addActivity("wake", "", fmt.Sprintf("The computer woke up. It stays awake for at least %d minutes, so friends have time to join.", int(wakeGrace.Minutes())))
	m.scheduleResume()
	m.notify()
}

// wantAwakeLocked reports whether Windows should be kept from sleeping:
// while a server runs (also one this version only watches, foreign.go),
// for a while after waking up, and while a job (other than resting) is in
// progress, such as starting a server.
func (m *Manager) wantAwakeLocked(now time.Time) bool {
	return m.anyRunLocked() || len(m.foreign) > 0 || now.Before(m.wakeHold) || m.workingLocked()
}

// workingLocked reports whether a job other than resting is in progress
// in any slot.
func (m *Manager) workingLocked() bool {
	busy := running(m.appJob) && m.appJob.Kind != "hibernate"
	m.eachInstance(func(i *instance) { busy = busy || running(i.job) && i.job.Kind != "hibernate" })
	return busy
}

// anyRunLocked reports whether any instance's server runs.
func (m *Manager) anyRunLocked() bool {
	on := false
	m.eachInstance(func(i *instance) { on = on || i.run != nil })
	return on
}

// someonePlayingLocked reports whether anyone is on any instance's server,
// by the server's own count.
func (m *Manager) someonePlayingLocked() bool {
	on := false
	m.eachInstance(func(i *instance) { on = on || i.run != nil && i.run.state().Count > 0 })
	return on
}

// maybePlayingLocked reports whether a game is running whose players the
// app can't see (presence "none", or log presence not proven yet on this
// install, spec 2.6): someone may be playing it, so the computer doesn't
// go to sleep by itself. Its own rule (4.6, ask.go) turns it off when
// nobody answers.
func (m *Manager) maybePlayingLocked() bool {
	maybe := false
	m.eachInstance(func(i *instance) { maybe = maybe || i.run != nil && !m.presenceKnownLocked(i) })
	return maybe
}

// recentUploadLocked reports whether someone uploaded a world to any game
// in the last 2 hours (and may be about to import it). An upload left
// alone for longer doesn't count, as for the app's updates.
func (m *Manager) recentUploadLocked() bool {
	recent := false
	m.eachInstance(func(i *instance) {
		for _, u := range i.uploads {
			recent = recent || time.Since(u.Created) < 2*time.Hour
		}
	})
	return recent
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
// The computer sleeps by itself only when nobody plays any game: no game
// counts anyone on, none runs whose players can't be seen, no other
// version's server runs, and nothing is busy (a job in any slot, or a
// world upload), as well as nobody using the computer itself (checkIdle).
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
	if m.someonePlayingLocked() || len(m.foreign) > 0 || m.maybePlayingLocked() {
		return time.Time{} // a foreign server's players can't be seen: it may be in use
	}
	if m.workingLocked() || m.recentUploadLocked() {
		return time.Time{} // it waits for the job to end
	}
	at := m.machineIdleSince.Add(time.Duration(m.settings.IdleMinutes) * time.Minute)
	if m.keepAwakeUntil.After(at) {
		at = m.keepAwakeUntil
	}
	return at
}

// checkIdle puts the computer to rest when nobody has played for the idle
// time. It runs on every sample tick, after auto-stop (onTick), so a game
// auto-stop stops at T+60 lets the computer sleep right after, not an
// hour later: neither its stop job nor its exit counts as someone doing
// something (spec 4.5). A game whose players can't be seen holds the
// computer awake without moving the clock, so once its own rule turns it
// off, nothing more holds it.
func (m *Manager) checkIdle(now time.Time) {
	m.mu.Lock()
	if !m.settings.IdleHibernate || m.someonePlayingLocked() || len(m.foreign) > 0 {
		m.machineIdleSince = now
		m.mu.Unlock()
		return
	}
	// An auto-stop decided in this tick (onTick, just before) counts as
	// busy before its job has started, so the computer waits for the game
	// to be saved and stopped by its own stop chain, not by going to sleep.
	busy := m.anyJobLocked() || len(m.life.autoBusy) > 0 || m.maybePlayingLocked() || m.recentUploadLocked()
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
		if used := now.Add(-idle); used.After(m.machineIdleSince) {
			m.machineIdleSince = used
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

// --- R3 (stream A) ------------------------------------------------------------
//
// A owns this file in R3 and keeps the observer calls in sampleLoop and
// wokenOnPurpose where phase 0' put them (hooks.go).

// lifeState is stream A's own app-wide state (Manager.life), guarded by
// m.mu: whatever auto-stop, the resume, idle sleep, the memory check, the
// ports and the firewall need beyond Manager.resumeSet.
type lifeState struct {
	// portsMu keeps two port picks (Add a game, Pick free ports) from
	// choosing the same free port at once. It is its own lock, not m.mu's:
	// a pick asks the system's port tables, which can take a moment.
	portsMu sync.Mutex
	// clock is what auto-stop, idle sleep and the resume read the time
	// from and wait on; nil is the real clock. Only tests set it, before
	// Start.
	clock    lifeClock
	autoBusy map[string]bool // instances auto-stop has begun stopping
	wokeAt   time.Time       // when the app last noticed the computer waking up
	taggedAt time.Time       // when a Discord request's wake packet was last heard
	resume   *resumeRun      // the resume after waking, waiting or under way; nil when none
	// Add a game asks a game not added yet about itself through a server
	// made only for that (scratch, by game id), and imports its first world
	// from the list of the owner's own worlds CatalogLocal last showed
	// (localCands), whose ids only that server knows.
	scratch    map[string]game.Server
	localCands map[string][]worlds.Candidate
}

// lifeClock is a clock to read and wait on.
type lifeClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock is the computer's clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// clock is the lifecycle clock (see lifeState.clock).
func (m *Manager) clock() lifeClock {
	if c := m.life.clock; c != nil {
		return c
	}
	return realClock{}
}

// HistoryGame returns a game's usage samples taken after since (its Stats
// tab, GET /api/games/{id}/stats). The legacy game's are the machine's
// history, as today; another game's are its own.
func (m *Manager) HistoryGame(id string, since time.Time) ([]stats.Sample, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return nil, err
	}
	if inst.legacy {
		return m.History(since), nil
	}
	m.loadGameStats(inst)
	m.mu.Lock()
	h := inst.history
	m.mu.Unlock()
	if s := h.Since(since); s != nil {
		return s, nil
	}
	return []stats.Sample{}, nil
}

// ByPlayersGame summarises a game's usage by the number of players on.
func (m *Manager) ByPlayersGame(id string) (stats.Summary, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return stats.Summary{}, err
	}
	if inst.legacy {
		return m.ByPlayers(), nil
	}
	m.loadGameStats(inst)
	m.mu.Lock()
	b := inst.byPlayers
	m.mu.Unlock()
	return b.Summary(), nil
}

// ResetStatsGame clears a game's usage-by-players summary.
func (m *Manager) ResetStatsGame(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		m.ResetStats()
		return nil
	}
	m.loadGameStats(inst)
	m.mu.Lock()
	b := inst.byPlayers
	m.mu.Unlock()
	b.Reset()
	m.saveGameStats()
	m.addGameActivity(inst, "info", "", fmt.Sprintf("The usage statistics of %s were reset.", inst.short()))
	return nil
}
