package manager

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// recorder is an observer that writes down every call, for the tests of
// the fixed points (hooks.go).
type recorder struct {
	mu    sync.Mutex
	calls []string
	ticks int
}

func (r *recorder) add(format string, a ...any) {
	r.mu.Lock()
	r.calls = append(r.calls, fmt.Sprintf(format, a...))
	r.mu.Unlock()
}

func instID(inst *instance) string {
	if inst == nil {
		return "app"
	}
	return inst.id
}

func (r *recorder) onReady(inst *instance) { r.add("ready %s", inst.id) }
func (r *recorder) onEvent(inst *instance, ev game.Event) {
	r.add("event %s %s %s", inst.id, ev.Kind, ev.Name)
}
func (r *recorder) onExit(inst *instance, run *serverRun, exit *ExitInfo) {
	// The observer gets the run that ended, after the instance has let go
	// of it (hooks.go).
	if run == nil || run.inst != inst || inst.run != nil {
		r.add("exit %s with the wrong run", inst.id)
	}
	r.add("exit %s expected=%v auto=%v", inst.id, exit.Expected, run != nil && run.autoStopped)
}
func (r *recorder) onTick(now time.Time) {
	r.mu.Lock()
	r.ticks++
	r.mu.Unlock()
}
func (r *recorder) onJob(inst *instance, job *Job) {
	state := "started"
	if job.Finished != nil {
		state = "finished"
	}
	r.add("job %s %s %s", instID(inst), job.Kind, state)
}
func (r *recorder) onTakeover(inst *instance) { r.add("takeover %s", inst.id) }
func (r *recorder) onWake(cause string)       { r.add("wake %s", cause) }

// has reports whether a call starting with prefix was made.
func (r *recorder) has(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (r *recorder) tickCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ticks
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, "\n")
}

// listen adds a recorder to m's observers.
func listen(m *Manager) *recorder {
	rec := &recorder{}
	m.mu.Lock()
	m.observers = append(m.observers, rec)
	m.mu.Unlock()
	return rec
}

// Every stream's observer is on the list, in the decided order (A first,
// so auto-stop is decided before the others in a tick).
func TestObserverList(t *testing.T) {
	var names []string
	for _, o := range newObservers(&Manager{}) {
		names = append(names, fmt.Sprintf("%T", o))
	}
	want := []string{"manager.lifecycleObserver", "manager.askObserver", "manager.wakerObserver", "manager.routerObserver"}
	if !slices.Equal(names, want) {
		t.Fatalf("observers = %v, want %v", names, want)
	}
}

// panicker is an observer whose every method panics.
type panicker struct{}

func (panicker) onReady(*instance)                       { panic("onReady") }
func (panicker) onEvent(*instance, game.Event)           { panic("onEvent") }
func (panicker) onExit(*instance, *serverRun, *ExitInfo) { panic("onExit") }
func (panicker) onTick(time.Time)                        { panic("onTick") }
func (panicker) onJob(*instance, *Job)                   { panic("onJob") }
func (panicker) onTakeover(*instance)                    { panic("onTakeover") }
func (panicker) onWake(string)                           { panic("onWake") }

// A panic in one observer is logged; the next still runs and the caller
// keeps the lock, which it lets go as usual.
func TestObserverPanicIsContained(t *testing.T) {
	var logged strings.Builder
	rec := &recorder{}
	m := &Manager{log: log.New(&logged, "", 0), observers: []observer{panicker{}, rec}}
	m.mu.Lock()
	m.observeLocked(func(o observer) { o.onWake(wakePurpose) })
	m.mu.Unlock()
	if !rec.has("wake purpose") {
		t.Fatalf("the observer after the one that panicked wasn't called: %q", rec)
	}
	if !strings.Contains(logged.String(), "panic in manager.panicker: onWake") {
		t.Fatalf("log = %q", logged.String())
	}
	if !m.mu.TryLock() {
		t.Fatal("the lock is still held")
	}
	m.mu.Unlock()
}

// The fixed points (spec 4.1): server.go, jobs.go, power.go's sampleLoop
// and the wake signal call every observer as the legacy game installs,
// starts, plays, stops, is taken over, and the computer wakes and sleeps.
func TestObserversCalledAtFixedPoints(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3")
	env := setup(t)
	m := env.m
	rec := listen(m)
	id := m.legacyID

	install(t, m)
	for _, want := range []string{"job " + id + " install started", "job " + id + " install finished"} {
		if !rec.has(want) {
			t.Fatalf("no %q in:\n%s", want, rec)
		}
	}

	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready and the join", 20*time.Second, func() bool {
		return rec.has("ready "+id) && rec.has("event "+id+" join Astrid")
	})
	if !rec.has("job " + id + " start started") {
		t.Fatalf("no start job in:\n%s", rec)
	}
	waitFor(t, "sample ticks", 5*time.Second, func() bool { return rec.tickCount() >= 3 })

	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the exit", 20*time.Second, func() bool {
		return rec.has("exit "+id+" expected=true auto=false") && rec.has("job "+id+" stop finished")
	})
	if rec.has("exit " + id + " with the wrong run") {
		t.Fatalf("onExit wasn't given the run that ended:\n%s", rec)
	}

	// A takeover: the app closes with the server running, and the next one
	// takes it over and catches up with its log.
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	m.Close()
	m2 := newTestManager(t, env.data, env)
	rec2 := listen(m2)
	m2.Start()
	env.m = m2
	waitFor(t, "the takeover", 15*time.Second, func() bool { return rec2.has("takeover " + id) })
	if rec2.has("ready " + id) {
		t.Fatalf("a taken-over run that was ready got onReady too:\n%s", rec2)
	}

	// Wake packets and a wake on purpose.
	m2.wakeSignal("abcdef")
	m2.wakeSignal("")
	m2.wokenOnPurpose()
	for _, want := range []string{"wake tagged", "wake untagged", "wake purpose"} {
		if !rec2.has(want) {
			t.Fatalf("no %q in:\n%s", want, rec2)
		}
	}

	// Going to sleep is the app's own job (inst nil), and stops the server.
	if err := m2.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sleep job", 40*time.Second, func() bool {
		return rec2.has("job app hibernate finished") && rec2.has("exit "+id+" expected=true auto=false")
	})
	if !rec2.has("job app hibernate started") {
		t.Fatalf("no sleep job start in:\n%s", rec2)
	}
}
