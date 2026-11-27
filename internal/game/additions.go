package game

// R3 additions to the contract (phase 0', docs/dev/multigame/p0r3-decisions.md).
// The game streams found places where the frozen contract had no word for
// something they needed (docs/dev/multigame/stream-notes.md). Each one is
// settled here as an optional interface, a shared type or a named value, so
// no existing adapter has to change and nothing about Valheim does.

import (
	"valheim-easy-server/internal/worlds"
)

// ---- errors with a next step ----

// ActionError is an error with a next step the panel can offer as a button,
// and the data that step needs. Adapters return it from Plan, the Installer
// or a stop step ("Port 7777 (TCP) is being used by another program",
// Action "pick_ports"); the manager returns the same type (manager.ActionError
// is this type), so the panel shows both the same way: {error, action, data}.
//
// Actions used so far: "install", "settings", "log", "force_stop",
// "pick_ports", "memory" (Data: manager.MemoryShort), "trust", "key",
// "force", "enable_hibernate".
type ActionError struct {
	Msg    string
	Action string
	Data   any // written as "data" by the web server; nil for most actions
}

func (e *ActionError) Error() string { return e.Msg }

// ---- following a run ----

// Replayer is a Monitor that reads a log back differently while the app
// catches up with a server it took over (Valheim: a line then counts at its
// own time when that is earlier than now). The manager calls Replaying(true)
// before each line it reads back and Replaying(false) once it has caught up,
// so what no line says (a "ready after a while" fallback) works straight
// away. Called with the manager's lock held, like every Monitor method.
type Replayer interface {
	Replaying(on bool)
}

// LogReader is a Monitor that can read a finished run's whole log for the
// records (the backfill of runs the app didn't follow), timing each line by
// the log's own clock rather than now. The manager calls FromLog once, on a
// fresh Monitor, before the first line.
type LogReader interface {
	FromLog()
}

// ---- support decided at run time ----

// Supporter is a server whose support on this computer is only known once
// it is installed: Minecraft on a Mac whose macOS can't start the Java the
// newest server needs (spec 5.2 and 10.2 Q6). The manager asks it only when
// Info.OS says Runs (and never in fake mode). CantRun with a reason ("Needs
// Windows: the newest Minecraft needs a newer macOS") shows the reason on the
// game's page instead of Start, and the start is refused with it.
//
// It is called with the manager's lock held (from Status), so it must
// answer from memory or Store().Get(), never by running a program or
// reading files: the adapter finds the answer in its Installer and keeps it
// in Saved.State.
type Supporter interface {
	Support() (Level, string)
}

// LevelOn is whether the game's server runs on goos/goarch, and the reason
// when it doesn't, from Info.OS and Info.NeedsText: the exact "goos/goarch"
// key wins over the GOOS one, and a missing key means CantRun. It ignores
// fake mode; the manager does that (spec 2.2 Fake).
func (i Info) LevelOn(goos, goarch string) (Level, string) {
	exact := goos + "/" + goarch
	for _, key := range []string{exact, goos} {
		if l, ok := i.OS[key]; ok {
			if l == Runs {
				return Runs, ""
			}
			return CantRun, needsText(i, exact, goos)
		}
	}
	return CantRun, needsText(i, exact, goos)
}

func needsText(i Info, exact, goos string) string {
	if s := i.NeedsText[exact]; s != "" {
		return s
	}
	return i.NeedsText[goos]
}

// ---- a licence to accept ----

// Licensed is a game whose server may only run once the owner has accepted
// a licence (Minecraft's EULA, spec 5.2). It is asked of the Game, not the
// Server, because Add a game shows the box before there is an instance.
// EULA is the licence's link. The manager writes the time the owner ticked
// the box into Saved.State[StateEULA]; the adapter only reads it, and its
// Plan refuses to start without it.
type Licensed interface {
	EULA() string
}

// Saved.State keys that the manager, not the adapter, writes. The adapter
// only reads them. Every other State key is the adapter's own.
const (
	// StateEULA is when the owner accepted the game's licence (RFC 3339),
	// written by Add a game, or by the game's Settings tab for an instance
	// that lost it (a list of games rebuilt from its folders, spec 3.4).
	StateEULA = "eula"
	// StateCGNAT is "true" while the router code (spec 6.5) finds that this
	// computer is behind carrier-grade NAT, so the internet can't reach it;
	// the key is absent otherwise, and before the first check. It is the
	// last answer found, so a start before this run's check still has one.
	// Written only for non-legacy instances (the legacy one keeps its
	// settings in settings.json, which never gains a key, invariant I3).
	StateCGNAT = "cgnat"
)

// ---- worlds ----

// Candidate is a world found in an upload or in the owner's own game, ready
// to import: worlds.Candidate under the contract's name, so an adapter can
// write worldstore.Format's Detect and Place without importing
// internal/worlds, which only Valheim's adapter may (spec 2.1). It is the
// same type, not a copy.
type Candidate = worlds.Candidate

// ---- events ----

// Event kinds the manager itself reacts to. Any other kind is the game's own
// ("allow_list_on", "rcon_failed", "wrong_password", "bad_version",
// "load_failed", "save_failed", "bad_password_config", "server_full",
// "port_in_use", "no_steam", "bad_config", "admin_prompt"): the manager does
// nothing with it but show an event that has Text in the activity feed,
// under that kind, so the game's panel module can give it an icon
// (feedIcons in internal/web/ui/assets/games/README.md). A kind nobody gave
// an icon shows as a warning. So a game can add a kind without anyone else
// changing anything.
const (
	KindOnline     = "online"      // ready for players
	KindJoin       = "join"        // Name joined (Account when known)
	KindLeave      = "leave"       // Name left
	KindDeath      = "death"       // Name died (games with Info.Deaths)
	KindSaved      = "saved"       // the world was saved
	KindStopping   = "stopping"    // the server says it is shutting down
	KindCount      = "count"       // Count players are on, by the server's own count (Hold: it can't be faked)
	KindCode       = "code"        // the join code or Game ID is known (RunState.Code)
	KindTurnedAway = "turned_away" // Name was refused (not on the allow list); the panel offers Allow
)

// ---- notes for the activity feed ----

// AddNote adds an entry to the app's activity feed for this instance
// through Env.Note, and does nothing when Note is nil (tests, gametest).
func (e Env) AddNote(kind, text string) {
	if e.Note != nil {
		e.Note(kind, text)
	}
}
