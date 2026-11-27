package manager

// The memory check before a start (spec 4.3, stream A): a game whose
// Info.MemoryMB is more than the computer has free is not started from the
// panel without the owner's word ("Stop Valheim (3.1 GB) and start", "Start
// anyway", "Cancel"), and is not started at all from Discord or a wake,
// which reply with what is running instead (6.4).
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in. M3's startGameWith (games.go), the one start path of R3,
// already calls memoryForStartLocked before the start job and
// memoryInStartJob inside it, so A never edits M3's files for 4.3.

import (
	"context"
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
// memoryForStartLocked calls it for every start that asks for the check.
func (m *Manager) memoryCheckLocked(inst *instance) error {
	return nil
}

// memoryForStartLocked is the memory step of a start (4.3's step 3), before
// its job is made: startGameWith calls it with m.mu held, after the other
// checks, and gives up the start with the error it returns. It checks the
// memory (memoryCheckLocked) unless o.Memory is "anyway" or o.Source is
// SourceRestart (a crash restart needs the memory the game just had), or
// o.Memory is "stop:<id>": the owner chose to stop that game to make
// room, and memoryInStartJob stops it inside the start job. Stream A also
// refuses here a "stop:<id>" that can't be done (no such instance, it
// isn't running, it has a job of its own); until then such a start answers
// errNotYet rather than starting without stopping anything.
func (m *Manager) memoryForStartLocked(inst *instance, o StartOptions) error {
	switch {
	case strings.HasPrefix(o.Memory, memoryStop):
		return errNotYet
	case o.Memory == memoryAnyway, o.Source == SourceRestart:
		return nil
	}
	return m.memoryCheckLocked(inst)
}

// memoryInStartJob is the part of the memory step that runs inside the
// start job (j), before the server is started: with o.Memory "stop:<id>"
// it stops that instance's server through its stop chain, saying so in the
// job's steps, and waits for it to exit, so its memory is free first. An
// error ends the job without starting. It runs without m.mu.
func (m *Manager) memoryInStartJob(ctx context.Context, j *jobCtl, inst *instance, o StartOptions) error {
	return nil
}

// runningGamesLocked lists the running games with their memory, largest
// first: the memory dialog's buttons and Discord's "Running: Valheim (3.1
// GB), Minecraft (2.3 GB)" (6.4).
func (m *Manager) runningGamesLocked() []RunningGame {
	return nil
}
