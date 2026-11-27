package manager

// The memory check before a start (spec 4.3, stream A): a game whose
// Info.MemoryMB is more than the computer has free is not started from the
// panel without the owner's word ("Stop Valheim (3.1 GB) and start", "Start
// anyway", "Cancel"), and is not started at all from Discord or a wake,
// which reply with what is running instead (6.4). A game that starts
// without the memory it needs makes every game on the computer slow, or
// is stopped by the system in the middle of a session, so the owner
// decides, and nobody far away does it by accident.
//
// M3's startGameWith (games.go), the one start path of R3, calls
// memoryForStartLocked before the start job and memoryInStartJob inside
// it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The values of StartOptions.Memory other than "" (check the memory).
const (
	memoryAnyway = "anyway" // "Start anyway"
	memoryStop   = "stop:"  // "stop:<instance>": "Stop Valheim (3.1 GB) and start"
)

// MemoryShort is the Data of the ActionError a start that doesn't fit
// fails with (Action "memory"); the web server writes it as "data":
//
//	&ActionError{Msg: "There may not be enough memory to start Palworld: it needs about 10 GB and 6.2 GB is free.",
//		Action: "memory", Data: MemoryShort{NeedMB: 10240, FreeMB: 6349, Running: []RunningGame{{ID: …, Name: "Valheim", MemMB: 3170}}}}
type MemoryShort struct {
	NeedMB  int           `json:"needMB"`
	FreeMB  int           `json:"freeMB"`
	Running []RunningGame `json:"running"` // one "Stop <name> and start" button each
}

// RunningGame is a running game and the memory it uses.
type RunningGame struct {
	ID    string `json:"id"` // the instance, for "stop:<id>"
	Name  string `json:"name"`
	MemMB int    `json:"memMB"`
}

// memoryCheckLocked says whether inst fits in the memory that is free now
// (machine total less what is in use, from the latest sample): nil when it
// does, else an *ActionError with Action "memory" and a MemoryShort.
// Before the first sample nothing is known, and the start goes ahead.
func (m *Manager) memoryCheckLocked(inst *instance) error {
	need := inst.info.MemoryMB
	total, used := m.live.MemTotal, m.live.MemSys
	if need <= 0 || total == 0 {
		return nil
	}
	free := 0
	if total > used {
		free = int((total - used) >> 20)
	}
	if need <= free {
		return nil
	}
	return &ActionError{
		Msg: fmt.Sprintf("There may not be enough memory to start %s: it needs about %s and %s is free.",
			inst.short(), sizeMB(need), sizeMB(free)),
		Action: "memory",
		Data:   MemoryShort{NeedMB: need, FreeMB: free, Running: m.runningGamesLocked()},
	}
}

// sizeMB says an amount of memory for people: "512 MB", "3 GB", "6.2 GB".
func sizeMB(mb int) string {
	if mb < 1024 {
		return fmt.Sprintf("%d MB", mb)
	}
	gb := strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64)
	return strings.TrimSuffix(gb, ".0") + " GB"
}

// memoryForStartLocked is the memory step of a start (4.3's step 3), before
// its job is made: startGameWith calls it with m.mu held, after the other
// checks, and gives up the start with the error it returns. It checks the
// memory (memoryCheckLocked) unless o.Memory is "anyway" or o.Source is
// SourceRestart (a crash restart needs the memory the game just had), or
// o.Memory is "stop:<id>": the owner chose to stop that game to make
// room, and memoryInStartJob stops it inside the start job. A "stop:<id>"
// that can't be done (no such game, it isn't running, it is this game, or
// it has a job of its own) is refused here, before any job is made, rather
// than starting without stopping anything.
//
// It also refuses a hidden legacy game, whatever asked (invariant I1): the
// start's own checks (M3's startChecks) say so first, and this keeps it
// true for every start that comes through here even so.
func (m *Manager) memoryForStartLocked(inst *instance, o StartOptions) error {
	if err := m.hiddenRefusalLocked(inst); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(o.Memory, memoryStop):
		return m.stopForRoomLocked(inst, strings.TrimPrefix(o.Memory, memoryStop))
	case o.Memory == memoryAnyway, o.Source == SourceRestart:
		return nil
	case o.Memory != "":
		return fmt.Errorf("The app doesn't know the memory choice %q.", o.Memory)
	}
	return m.memoryCheckLocked(inst)
}

// stopForRoomLocked says whether the game "stop:<id>" names can be stopped
// to make room for inst.
func (m *Manager) stopForRoomLocked(inst *instance, id string) error {
	other := m.instances[id]
	switch {
	case other == nil:
		return errNoGame(id)
	case other == inst:
		return fmt.Errorf("%s can't be stopped to make room for itself.", inst.short())
	case other.run == nil:
		return fmt.Errorf("%s isn't running, so stopping it makes no room.", other.short())
	case other.run.stopping:
		return nil // already on its way out; the start job waits for it
	}
	if busy := m.busyJobLocked(other); busy != nil {
		return &BusyError{Title: lowerFirst(busy.Title)}
	}
	return nil
}

// memoryInStartJob is the part of the memory step that runs inside the
// start job (j), before the server is started: with o.Memory "stop:<id>"
// it stops that instance's server through its stop chain, saying so in the
// job's steps, and waits for it to exit, so its memory is free first. An
// error ends the job without starting. It runs without m.mu.
func (m *Manager) memoryInStartJob(ctx context.Context, j *jobCtl, inst *instance, o StartOptions) error {
	if !strings.HasPrefix(o.Memory, memoryStop) {
		return nil
	}
	other, err := m.instanceByID(strings.TrimPrefix(o.Memory, memoryStop))
	if err != nil {
		return err
	}
	m.mu.Lock()
	run := other.run
	m.mu.Unlock()
	if run == nil {
		return nil // it stopped meanwhile: the room is there
	}
	j.step(fmt.Sprintf("Stopping %s to make room for %s…", other.short(), inst.short()), -1)
	err = m.doStop(ctx, j, other)
	if ctx.Err() != nil {
		return errCancelled
	}
	if err == nil {
		select {
		case <-run.done: // doStop returns once it has exited; this is only to be sure
		case <-ctx.Done():
			return errCancelled
		}
	}
	if err != nil {
		// Not doStop's own error: its "force stop" button would be for this
		// game, which isn't running, not for the one that didn't stop.
		var ae *ActionError
		msg := err.Error()
		if errors.As(err, &ae) {
			msg = ae.Msg
		}
		return fmt.Errorf("%s didn't stop, so %s wasn't started. %s", other.short(), inst.short(), msg)
	}
	m.addGameActivity(other, "info", "", fmt.Sprintf("%s was stopped to make room for %s.", other.short(), inst.short()))
	return nil
}

// runningGamesLocked lists the running games with their memory, largest
// first: the memory dialog's buttons and Discord's "Running: Valheim (3.1
// GB), Minecraft (2.3 GB)" (6.4). A game on its way out is left out: its
// memory is about to be free.
func (m *Manager) runningGamesLocked() []RunningGame {
	out := []RunningGame{}
	m.eachInstance(func(inst *instance) {
		if r := inst.run; r != nil && !r.stopping {
			out = append(out, RunningGame{ID: inst.id, Name: inst.short(), MemMB: int(r.memNow >> 20)})
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].MemMB > out[j].MemMB })
	return out
}
