package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// StartGame takes R3's one start path (spec 4.3): the checks first, then
// stream A's memory step, then the start job. The legacy game runs through
// it as it would from the new panel.
func TestStartGameTakesTheR3Path(t *testing.T) {
	env := setup(t)
	m := env.m
	id := m.legacyID
	m.mu.Lock()
	m.legacy().info.MemoryMB = 0 // the start's memory check mustn't depend on how much memory the runner has free
	m.mu.Unlock()

	// The checks come before the memory step, so a game that isn't
	// installed says so whatever the memory answer would be.
	var ae *ActionError
	err := m.StartGame(id, StartOptions{Memory: "stop:nosuchgame", Source: SourcePanel})
	if !errors.As(err, &ae) || ae.Action != "install" {
		t.Fatalf("a start before the install = %v, want the not-installed error", err)
	}
	install(t, m)

	// Stopping a game that isn't there to make room is refused before any
	// job is made (A's memoryForStartLocked).
	before := currentJobID(m)
	if err := m.StartGame(id, StartOptions{Memory: "stop:nosuchgame", Source: SourcePanel}); err == nil {
		t.Fatal("a start that stops a game that isn't there was accepted")
	}
	if currentJobID(m) != before {
		t.Fatal("a refused start made a job")
	}

	// A start that asks for the memory check runs as job "start".
	if err := m.StartGame(id, StartOptions{Source: SourcePanel}); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, currentJobID(m))
	if job.Kind != "start" || job.Error != "" {
		t.Fatalf("start job = %+v", job)
	}
	if st := m.Status(); st.State != StateOnline {
		t.Fatalf("after the start: state = %s", st.State)
	}
	if err := m.StartGame(id, StartOptions{Memory: "anyway", Source: SourcePanel}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second start = %v, want already running", err)
	}
}

// A run that auto-stop stopped tells the observers so through the run they
// are given, and its activity entry says why instead of "The server
// stopped." (spec 4.5).
func TestAutoStoppedExitSaysWhy(t *testing.T) {
	env := setup(t)
	m := env.m
	rec := listen(m)
	inst := m.legacy()
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "server online", 20*time.Second, func() bool { return m.Status().State == StateOnline })

	// What A's autoStopGame does: mark the run, then stop it as a quiet job.
	m.mu.Lock()
	run := inst.run
	run.autoStopped, run.autoReason, run.autoMinutes = true, stopIdle, 60
	m.mu.Unlock()
	if err := m.runQuietJob(inst, "stop", "Stopping the server", func(ctx context.Context, j *jobCtl) error {
		return m.doStop(ctx, j, inst)
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the exit", 20*time.Second, func() bool { return rec.has("exit " + inst.id + " expected=true auto=true") })
	if rec.has("exit " + inst.id + " with the wrong run") {
		t.Fatalf("onExit wasn't given the run that ended:\n%s", rec)
	}
	want := "Valheim stopped: nobody played for an hour."
	waitFor(t, "the activity entry", 5*time.Second, func() bool {
		acts, _ := m.Activity(0, 0)
		for _, a := range acts {
			if a.Kind == "stopped" && a.Text == want {
				return true
			}
		}
		return false
	})
	acts, _ := m.Activity(0, 0)
	for _, a := range acts {
		if a.Kind == "stopped" && a.Text == "The server stopped." {
			t.Fatalf("an auto-stopped run also says %q", a.Text)
		}
	}
}
