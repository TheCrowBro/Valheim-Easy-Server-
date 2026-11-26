package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
)

// Job is a longer task (install, start, stop, backup…) shown with progress.
// Each instance has one job slot, so a game's jobs can't step on each
// other, and the app has one more slot for going to sleep (spec 4.2).
type Job struct {
	ID          int        `json:"id"`
	Game        string     `json:"game,omitempty"` // the instance it's for; "" for the app's own
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Message     string     `json:"message"`
	Percent     float64    `json:"percent"` // -1 when unknown
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished,omitempty"`
	Error       string     `json:"error,omitempty"`
	Action      string     `json:"action,omitempty"` // a suggested next step after an error
	Cancellable bool       `json:"cancellable"`
	Result      string     `json:"result,omitempty"` // success message
}

// ActionError is an error with a suggested follow-up the panel can offer
// as a button (for example "force_stop").
type ActionError struct {
	Msg    string
	Action string
}

func (e *ActionError) Error() string { return e.Msg }

// BusyError means another job is running.
type BusyError struct{ Title string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("Please wait: %s is still in progress.", e.Title)
}

var errCancelled = errors.New("Cancelled.")

// fieldErrors hands a game's field errors to the panel the way settings
// errors always went (config.FieldErrors), so the old panel shows them
// next to the settings as before.
func fieldErrors(e game.FieldErrors) error { return config.FieldErrors(e) }

type jobCtl struct {
	m   *Manager
	job *Job
}

// step updates the message and percentage (-1 = unknown).
func (j *jobCtl) step(msg string, pct float64) {
	j.m.mu.Lock()
	j.job.Message = msg
	j.job.Percent = pct
	j.m.mu.Unlock()
	j.m.notify()
}

func (j *jobCtl) setCancellable(v bool) {
	j.m.mu.Lock()
	j.job.Cancellable = v
	j.m.mu.Unlock()
}

func (j *jobCtl) result(msg string) {
	j.m.mu.Lock()
	j.job.Result = msg
	j.m.mu.Unlock()
}

func running(j *Job) bool { return j != nil && j.Finished == nil }

// runJob starts fn as the legacy instance's job. It fails with BusyError
// when that instance's job or the app's is still running.
func (m *Manager) runJob(kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) error {
	_, err := m.startJob(kind, title, cancellable, fn)
	return err
}

// startJob is runJob that also returns the new job's ID.
func (m *Manager) startJob(kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) (int, error) {
	return m.startInstJob(m.legacy(), kind, title, cancellable, fn)
}

// runInstJob starts fn in an instance's job slot.
func (m *Manager) runInstJob(inst *instance, kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) error {
	_, err := m.startInstJob(inst, kind, title, cancellable, fn)
	return err
}

func (m *Manager) startInstJob(inst *instance, kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) (int, error) {
	return m.launchJob(inst, kind, title, cancellable, fn)
}

// runAppJob starts fn in the app's own slot, which only going to sleep
// uses. It waits for no instance, but is refused while any instance's
// job runs, as before.
func (m *Manager) runAppJob(kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) error {
	_, err := m.launchJob(nil, kind, title, cancellable, fn)
	return err
}

// busyJobLocked is the running job that stops a new one in inst's slot
// (the app's slot for nil): the slot's own job, the app's job, and for the
// app's slot any instance's job.
func (m *Manager) busyJobLocked(inst *instance) *Job {
	if running(m.appJob) {
		return m.appJob
	}
	if inst != nil {
		if running(inst.job) {
			return inst.job
		}
		return nil
	}
	var busy *Job
	m.eachInstance(func(i *instance) {
		if busy == nil && running(i.job) {
			busy = i.job
		}
	})
	return busy
}

// anyJobLocked reports whether a job runs in any slot.
func (m *Manager) anyJobLocked() bool {
	busy := running(m.appJob)
	m.eachInstance(func(i *instance) { busy = busy || running(i.job) })
	return busy
}

// launchJob starts fn as the current job of inst's slot (the app's slot
// for nil).
func (m *Manager) launchJob(inst *instance, kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) (int, error) {
	m.mu.Lock()
	if busy := m.busyJobLocked(inst); busy != nil {
		t := busy.Title
		m.mu.Unlock()
		return 0, &BusyError{Title: lowerFirst(t)}
	}
	if m.app.updating { // the app is about to restart
		m.mu.Unlock()
		return 0, errUpdatingBusy
	}
	// One sequence for every slot, so ids never repeat across games: the
	// old panel tells jobs apart by id.
	m.jobSeq++
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	job := &Job{ID: m.jobSeq, Kind: kind, Title: title, Percent: -1, Started: time.Now(), Cancellable: cancellable}
	if inst != nil {
		job.Game = inst.id
		inst.job, inst.jobCancel = job, cancel
	} else {
		m.appJob, m.appJobCancel = job, cancel
	}
	m.machineIdleSince = time.Now() // someone is doing something: not idle
	m.mu.Unlock()
	m.notify()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.log.Printf("panic in job %s: %v\n%s", kind, r, debug.Stack())
					err = fmt.Errorf("Something went wrong inside the app (%v). Please try again.", r)
				}
			}()
			err = fn(ctx, &jobCtl{m: m, job: job})
		}()
		err = actionOf(err)
		m.mu.Lock()
		now := time.Now()
		job.Finished = &now
		if errors.Is(err, errCancelled) {
			job.Result = "Cancelled."
		} else if err != nil {
			job.Error = err.Error()
			var ae *ActionError
			if errors.As(err, &ae) {
				job.Action = ae.Action
			}
			m.log.Printf("job %s failed: %v", kind, err)
		} else {
			job.Percent = 100
		}
		job.Cancellable = false
		m.mu.Unlock()
		m.notify()
	}()
	return job.ID, nil
}

// legacyJobLocked is the job the old panel shows (Status.job): the newer
// of the legacy instance's and the app's current or last job (spec 4.2).
// With only Valheim that is exactly the one job of older builds.
func (m *Manager) legacyJobLocked() *Job {
	j := m.legacy().job
	if a := m.appJob; a != nil && (j == nil || a.ID > j.ID) {
		j = a
	}
	return j
}

// CancelJob cancels the legacy instance's job (or the app's), if it can be
// cancelled.
func (m *Manager) CancelJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.legacy()
	job, cancel := inst.job, inst.jobCancel
	if !running(job) {
		job, cancel = m.appJob, m.appJobCancel
	}
	if !running(job) {
		return nil
	}
	if !job.Cancellable {
		return errors.New("This can't be cancelled at this point.")
	}
	cancel()
	return nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}
