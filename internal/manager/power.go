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
		m.resumeOnWake = running
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
			j.result("The computer slept and has woken up again.") // Sleep returns after waking
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
	saved := time.Now()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-tick.C:
			gap := now.Sub(last)
			last = now
			// The clock keeps running while the computer rests, so a long
			// pause between readings means it has just woken up.
			if gap > m.opt.SampleInterval+20*time.Second {
				m.onWake(gap)
				continue
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

// wakeGrace is how long the computer stays awake after waking up, so
// friends have time to join. Windows itself goes back to sleep about two
// minutes after an unattended wake (such as Wake-on-LAN) unless asked not to.
var wakeGrace = 15 * time.Minute

// onWake runs after the computer comes back from sleep or hibernation.
func (m *Manager) onWake(gap time.Duration) {
	m.mu.Lock()
	now := time.Now()
	m.idleSince = now
	m.wakeHold = now.Add(wakeGrace)
	if m.keepAwakeUntil.Before(m.wakeHold) {
		m.keepAwakeUntil = m.wakeHold
	}
	resume := m.resumeOnWake && m.settings.StartAfterWake && m.run == nil
	m.resumeOnWake = false
	m.net.publicAt = time.Time{} // the address may have changed
	m.mu.Unlock()
	m.updateAwake()
	m.log.Printf("woke up after %s", gap.Round(time.Second))
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
	if m.run != nil && m.run.tracker.Count() > 0 {
		return time.Time{}
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
	// Don't hibernate while someone is using the computer itself.
	if idle, ok := platform.IdleTime(); ok && idle < time.Duration(minutes)*time.Minute {
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
