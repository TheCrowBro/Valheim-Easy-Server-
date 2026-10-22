package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// Job is a longer task (install, start, stop, backup…) shown with progress.
// Only one runs at a time so they can't step on each other.
type Job struct {
	ID          int        `json:"id"`
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

// runJob starts fn as the current job. It fails with BusyError when another
// job is still running.
func (m *Manager) runJob(kind, title string, cancellable bool, fn func(ctx context.Context, j *jobCtl) error) error {
	m.mu.Lock()
	if m.job != nil && m.job.Finished == nil {
		t := m.job.Title
		m.mu.Unlock()
		return &BusyError{Title: lowerFirst(t)}
	}
	m.jobSeq++
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	job := &Job{ID: m.jobSeq, Kind: kind, Title: title, Percent: -1, Started: time.Now(), Cancellable: cancellable}
	m.job, m.jobCancel = job, cancel
	m.idleSince = time.Now() // someone is doing something: not idle
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
		m.mu.Lock()
		now := time.Now()
		job.Finished = &now
		if err != nil {
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
	return nil
}

// CancelJob cancels the current job if it can be cancelled.
func (m *Manager) CancelJob() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || m.job.Finished != nil {
		return nil
	}
	if !m.job.Cancellable {
		return errors.New("This can't be cancelled at this point.")
	}
	m.jobCancel()
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
