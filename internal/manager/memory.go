package manager

// The memory check before a start (spec 4.3, stream A): a game whose
// Info.MemoryMB is more than the computer has free is not started from the
// panel without the owner's word ("Stop Valheim (3.1 GB) and start", "Start
// anyway", "Cancel"), and is not started at all from Discord or a wake,
// which reply with what is running instead (6.4).
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in.

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
// does, else an *ActionError with Action "memory" and a MemoryShort. The
// start path (M3's startGameWith) calls it unless StartOptions.Memory is
// "anyway" or the start is a restart.
func (m *Manager) memoryCheckLocked(inst *instance) error {
	return nil
}

// runningGamesLocked lists the running games with their memory, largest
// first: the memory dialog's buttons and Discord's "Running: Valheim (3.1
// GB), Minecraft (2.3 GB)" (6.4).
func (m *Manager) runningGamesLocked() []RunningGame {
	return nil
}
