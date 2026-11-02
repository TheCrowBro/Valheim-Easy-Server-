package manager

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUser stands in for someone at the computer: its keyboard and mouse,
// and whether a Mac is fully awake. Call it before setup, so it's put back
// once the manager has closed.
func fakeUser(t *testing.T) *presence {
	t.Helper()
	p := &presence{}
	p.away()
	oldIdle, oldDark := idleTime, darkWake
	idleTime = func() (time.Duration, bool) { return time.Duration(p.idle.Load()), true }
	darkWake = p.dark.Load
	t.Cleanup(func() { idleTime, darkWake = oldIdle, oldDark })
	return p
}

type presence struct {
	idle atomic.Int64 // since the last input
	dark atomic.Bool  // a Mac only half awake
}

// away: nobody has touched the computer for hours, and a Mac is in a dark
// wake.
func (p *presence) away() {
	p.idle.Store(int64(3 * time.Hour))
	p.dark.Store(true)
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
	woke := time.Now()
	time.Sleep(10 * time.Millisecond)
	m.mu.Lock()
	awake, resume := m.wantAwakeLocked(time.Now()), m.resumeOnWake
	m.mu.Unlock()
	if m.wakeWanted(woke) || awake || !resume || hasText(m, "wake", "The computer woke up") {
		t.Fatalf("woke up by itself: wanted %v, kept awake %v, still to resume %v", m.wakeWanted(woke), awake, resume)
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
	m.idleSince = time.Now().Add(-11 * time.Minute) // nobody has played for a while
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
	m.idleSince = time.Now().Add(-11 * time.Minute)
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
