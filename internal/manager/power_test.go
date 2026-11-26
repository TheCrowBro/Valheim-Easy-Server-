package manager

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/platform"
)

// fakeUser stands in for someone at the computer: its keyboard and mouse,
// whether a Mac is fully awake, and what the system says woke it up. Call
// it before setup, so it's put back once the manager has closed.
func fakeUser(t *testing.T) *presence {
	t.Helper()
	p := &presence{}
	p.away()
	oldIdle, oldDark, oldWokeBy := idleTime, darkWake, wokeBy
	idleTime = func() (time.Duration, bool) { return time.Duration(p.idle.Load()), true }
	darkWake = p.dark.Load
	wokeBy = func(since time.Time) platform.WakeCause {
		p.since.Store(since.UnixNano())
		return platform.WakeCause(p.cause.Load())
	}
	t.Cleanup(func() { idleTime, darkWake, wokeBy = oldIdle, oldDark, oldWokeBy })
	return p
}

type presence struct {
	idle  atomic.Int64 // since the last input
	dark  atomic.Bool  // a Mac only half awake
	cause atomic.Int32 // what the system says woke it up
	since atomic.Int64 // the app asked about a wake-up after this (Unix nanoseconds)
}

// away: nobody has touched the computer for hours, a Mac is in a dark
// wake, and a timer woke it up.
func (p *presence) away() {
	p.idle.Store(int64(3 * time.Hour))
	p.dark.Store(true)
	p.cause.Store(int32(platform.WakeByTimer))
}

// set replaces a setting of the app's for a test.
func set[T any](t *testing.T, v *T, to T) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// here: someone is using the computer.
func (p *presence) here() {
	p.idle.Store(0)
	p.dark.Store(false)
}

// A computer that wakes up by itself (a Mac's maintenance dark wake, a
// Windows wake timer) isn't kept awake and doesn't start the server. Once
// someone wants it, by using it or with a wake signal, it stays awake and
// the server starts again.
func TestWakeWaitsForSomeone(t *testing.T) {
	user := fakeUser(t)
	env := setup(t)
	m := env.m
	m.mu.Lock()
	m.resumeOnWake = true                  // the server ran when it went to sleep
	m.wakeHold = time.Now().Add(time.Hour) // a hold from before it slept
	m.mu.Unlock()

	m.onWake(2 * time.Hour)
	woke := &pendingWake{at: time.Now(), asleep: time.Now().Add(-2 * time.Hour)}
	time.Sleep(10 * time.Millisecond)
	m.mu.Lock()
	awake, resume := m.wantAwakeLocked(time.Now()), m.resumeOnWake
	m.mu.Unlock()
	if m.wakeWanted(woke) || awake || !resume || hasText(m, "wake", "The computer woke up") {
		t.Fatalf("woke up by itself: wanted %v, kept awake %v, still to resume %v", m.wakeWanted(woke), awake, resume)
	}

	// When the system never says what woke it, it counts after a while,
	// as it always did (not on a Mac, which tells a dark wake apart).
	if !darkWakes {
		if !m.wakeWanted(&pendingWake{at: time.Now().Add(-unknownWait), asked: time.Now()}) {
			t.Fatal("a wake-up Windows doesn't explain doesn't count")
		}
		user.cause.Store(int32(platform.WakeByTimer))
		if m.wakeWanted(&pendingWake{at: time.Now().Add(-unknownWait), cause: platform.WakeByTimer}) {
			t.Fatal("a wake timer counts")
		}
		user.cause.Store(int32(platform.WakeCauseUnknown))
	}

	user.here()
	if !m.wakeWanted(woke) {
		t.Fatal("someone using the computer doesn't count")
	}
	user.away()
	m.wakeSignal("") // nothing is installed to start, but someone asked
	if !m.wakeWanted(woke) {
		t.Fatal("a wake signal doesn't count")
	}

	// The network adapter (Wake-on-LAN from the KVM's own button, which the
	// app doesn't hear) or the power button woke it: that counts too, even
	// with nobody at the computer.
	user.cause.Store(int32(platform.WakeByDevice))
	if !m.wakeWanted(&pendingWake{at: time.Now()}) {
		t.Fatal("a wake-up by a device doesn't count")
	}

	m.wokenOnPurpose()
	m.mu.Lock()
	awake, resume = m.wantAwakeLocked(time.Now()), m.resumeOnWake
	m.mu.Unlock()
	if !awake || resume {
		t.Fatalf("woken on purpose: kept awake %v, still to resume %v", awake, resume)
	}
	if !hasText(m, "wake", "The computer woke up. It stays awake") || !hasText(m, "info", "Starting the server again in 20 seconds") {
		t.Fatal("no activity about staying awake and starting again")
	}
}

// A sleep too short to show as a pause between readings is still noticed:
// on macOS and Linux timers don't count the time asleep, and on Windows the
// app's own sleep returns once the computer is awake again.
func TestShortSleepNoticed(t *testing.T) {
	m0 := &Manager{opt: Options{SampleInterval: 2 * time.Second}}
	for _, c := range []struct {
		wall, timer time.Duration
		want        bool
	}{
		{2 * time.Second, 2 * time.Second, false},
		{2100 * time.Millisecond, 2 * time.Second, false},
		{12 * time.Second, 2 * time.Second, true}, // a short sleep on a Mac
		{8 * time.Hour, 2 * time.Second, true},
		{8 * time.Hour, 8 * time.Hour, true},        // a long sleep on Windows
		{12 * time.Second, 12 * time.Second, false}, // a short one there: below
	} {
		if got := m0.rested(c.wall, c.timer); got != c.want {
			t.Errorf("%s on the wall clock, %s for timers: rested %v", c.wall, c.timer, got)
		}
	}

	old := sleepReturns
	sleepReturns = true // as on Windows
	t.Cleanup(func() { sleepReturns = old })
	user := fakeUser(t)
	env := setup(t)
	m := env.m
	m.mu.Lock()
	m.wakeHold = time.Now().Add(time.Hour) // a hold from before it slept
	m.mu.Unlock()
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" || job.Result != sleptResult {
		t.Fatalf("sleep job: %+v", job)
	}
	waitFor(t, "the wake to be noticed", 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.wakeHold.IsZero()
	})
	time.Sleep(time.Second)
	if hasText(m, "wake", "The computer woke up") {
		t.Fatal("kept awake with nobody there")
	}
	user.here()
	waitFor(t, "the computer to stay awake", 5*time.Second, func() bool {
		return hasText(m, "wake", "The computer woke up. It stays awake")
	})
}

// The panel's idle deadline is when the computer really rests: counted
// from the last input on its own keyboard or mouse, and none while a job
// is in progress.
func TestIdleDeadlineFollowsInput(t *testing.T) {
	user := fakeUser(t)
	user.idle.Store(int64(2 * time.Minute)) // someone used it 2 minutes ago
	env := setup(t)
	m := env.m
	s := m.Settings()
	s.IdleHibernate, s.IdleMinutes = true, 10
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.machineIdleSince = time.Now().Add(-11 * time.Minute) // nobody has played for a while
	m.mu.Unlock()
	waitFor(t, "the deadline to follow the input", 5*time.Second, func() bool {
		idle := m.Status().Idle
		return idle != nil && time.Until(idle.HibernateAt) > 7*time.Minute
	})

	release := make(chan struct{})
	if err := m.runJob("backup", "Backing up", false, func(context.Context, *jobCtl) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.machineIdleSince = time.Now().Add(-11 * time.Minute)
	m.mu.Unlock()
	idle := m.Status().Idle
	close(release)
	if idle != nil {
		t.Fatalf("rests at %s while a job is in progress", idle.HibernateAt)
	}
	waitJob(t, m, currentJobID(m))
	if n := env.slept.Load() + env.hibernated.Load(); n != 0 {
		t.Fatalf("rested %d times while someone used the computer", n)
	}
}

// Wake-on-LAN from the IP KVM's own Wake button wakes the computer, but
// the network adapter keeps the packet, so the app hears nothing; nobody
// touches the computer either. Once Windows says a device woke it, it stays
// awake and the server starts again, as after any wake-up someone asked
// for.
func TestKVMWakeKeepsAwake(t *testing.T) {
	set(t, &sleepReturns, true) // as on Windows
	set(t, &wakeAskEvery, 100*time.Millisecond)
	user := fakeUser(t)
	user.cause.Store(int32(platform.WakeCauseUnknown)) // Windows hasn't said yet
	var sleptAt atomic.Int64
	env := setup(t, func(o *Options) {
		o.Sleep = func() error {
			sleptAt.Store(time.Now().UnixNano())
			return nil
		}
	})
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	m.mu.Lock()
	m.wakeHold = time.Now().Add(time.Hour) // a hold from before it slept
	m.mu.Unlock()
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" || job.Result != sleptResult {
		t.Fatalf("sleep job: %+v", job)
	}
	waitFor(t, "the app to ask what woke it", 5*time.Second, func() bool { return user.since.Load() != 0 })
	time.Sleep(500 * time.Millisecond)
	m.mu.Lock()
	held := !m.wakeHold.IsZero()
	m.mu.Unlock()
	if held || hasText(m, "wake", "The computer woke up") {
		t.Fatal("kept awake before Windows said what woke it")
	}

	user.cause.Store(int32(platform.WakeByDevice)) // Windows has logged it
	waitFor(t, "the computer to stay awake", 5*time.Second, func() bool {
		return hasText(m, "wake", "The computer woke up. It stays awake")
	})
	m.mu.Lock()
	awake := m.wantAwakeLocked(time.Now().Add(wakeGrace - time.Minute))
	m.mu.Unlock()
	if !awake || !hasText(m, "info", "Starting the server again in 20 seconds") {
		t.Fatalf("after a wake-up from the KVM: kept awake %v, starting again %v", awake,
			hasText(m, "info", "Starting the server again in 20 seconds"))
	}
	// It asked about a wake-up after the computer went to sleep, not an
	// earlier one.
	if since := user.since.Load(); since >= sleptAt.Load() {
		t.Fatalf("asked about a wake-up after %s, but it slept at %s",
			time.Unix(0, since).Format(time.StampMilli), time.Unix(0, sleptAt.Load()).Format(time.StampMilli))
	}
}

// A Mac can stay in a dark wake (for a backup, say) for longer than the app
// asks what woke it. If someone then wakes it fully, that still counts: it
// stays awake and the server starts again.
func TestLongDarkWakeThenFullWake(t *testing.T) {
	set(t, &darkWakes, true)
	set(t, &sleepReturns, true) // so the test's sleep shows as a wake-up
	set(t, &wakeWatch, 300*time.Millisecond)
	user := fakeUser(t) // a maintenance dark wake
	env := setup(t)
	m := env.m
	m.mu.Lock()
	m.wakeHold = time.Now().Add(time.Hour) // a hold from before it slept
	m.mu.Unlock()
	if err := m.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	m.mu.Lock()
	m.resumeOnWake = true // the server ran when it went to sleep
	m.mu.Unlock()
	waitFor(t, "the wake to be noticed", 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.wakeHold.IsZero()
	})
	time.Sleep(2 * wakeWatch)
	if hasText(m, "wake", "The computer woke up") {
		t.Fatal("kept awake in a maintenance dark wake")
	}

	user.dark.Store(false) // someone pressed a key
	waitFor(t, "the computer to stay awake", 5*time.Second, func() bool {
		return hasText(m, "wake", "The computer woke up. It stays awake")
	})
	if !hasText(m, "info", "Starting the server again in 20 seconds") {
		t.Fatal("the server doesn't start again")
	}
}

// A restart owed from an earlier sleep isn't forgotten when the computer
// wakes up by itself (a timer) and the app puts it back to sleep: the next
// time someone wakes it, the server starts again.
func TestResumeOwedThroughTimerWakes(t *testing.T) {
	fakeUser(t) // nobody there; a timer woke it
	env := setup(t)
	m := env.m
	m.mu.Lock()
	m.resumeOnWake = true // the server ran when it first went to sleep
	m.mu.Unlock()
	m.onWake(time.Hour)
	if err := m.Hibernate("Nobody has played for 60 minutes."); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	m.mu.Lock()
	owed := m.resumeOnWake
	m.mu.Unlock()
	if !owed {
		t.Fatal("going back to sleep after a timer wake forgot to start the server again")
	}
}
