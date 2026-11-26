// Package game is the contract between the app and each game it can host.
//
// The manager knows nothing about any one game: it asks the game's adapter
// (a package under internal/games, registered with Register) what the game
// is (Info), how to install, start, watch and stop its server (Server), and
// how friends join it. The types here are frozen: every game stream and the
// manager are written against them at the same time, so changing one means
// changing all of them. Adding a new optional interface later is fine.
//
// This package imports only the standard library, plus internal/worlds and
// internal/mods for the few data types the manager already shows in the
// panel (TestImportRules checks it), so it can never pull a game's code or
// the manager into another package by accident.
package game

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/worlds"
)

// ---- what a game is ----

// Stage is a per-game kill switch, compiled into each build (spec 8.3), so
// a game that turns out to be broken can be held back by the next release
// without touching anyone's data.
type Stage string

const (
	Hidden  Stage = "hidden"  // built in, never shown; its instances are foreign
	Preview Stage = "preview" // can be added; marked "Preview" in Add a game and on its page
	Ready   Stage = "ready"
)

// Level says whether a game's server runs on an operating system.
type Level string

const (
	Runs    Level = "yes"
	CantRun Level = "no" // greyed out in Add a game with Info.NeedsText
)

// Presence is how the app knows who is playing. It decides whether a game
// may stop itself when it looks empty (spec 2.6 and 4.6).
type Presence string

const (
	ByName  Presence = "names" // who, by name (log or probe)
	ByCount Presence = "count" // how many, not who
	Unknown Presence = "none"  // no usable signal: the "still playing?" rule (4.6)
)

// PortRole is one port a game's server listens on. The app picks the real
// number once, when the game is added, starting at Default (spec 2.5).
type PortRole struct {
	Role    string   `json:"role"`   // "game", "query", "direct", "rcon", "rest", "reliable"
	Label   string   `json:"label"`  // "Game port"
	Protos  []string `json:"protos"` // "udp", "tcp"
	Default int      `json:"default"`
	Tie     string   `json:"tie,omitempty"` // "game+1": always that role's port plus one
	Public  bool     `json:"public"`        // friends connect to it: firewall rule and router mapping
}

// PasswordRule says whether a game's server takes a password, and the
// limits the panel checks before the server sees it.
type PasswordRule struct {
	Use      string `json:"use"` // "required", "optional", "none"
	Min      int    `json:"min,omitempty"`
	Max      int    `json:"max,omitempty"`
	Help     string `json:"help"`               // shown under the box
	NoneText string `json:"noneText,omitempty"` // "none": what protects it instead
}

// Info is everything about a game that doesn't depend on an instance: what
// the panel's catalog, the sidebar, Discord and the memory check show.
type Info struct {
	ID      string // "minecraft"; also its folder and, by default, its slash command
	Name    string // "Minecraft: Java Edition"
	Short   string // "Minecraft": sidebar, Discord, reminders ("update Minecraft to …")
	Letter  string // "M"
	Color   string // "#5b8731"
	Command string // Discord slash command, lowercase [a-z0-9_-]{1,32}
	Order   int
	Stage   Stage
	OS      map[string]Level // by "goos/goarch" ("darwin/arm64") or GOOS ("windows"); the exact key wins; missing = CantRun.
	//                          Ignored in fake mode (Env.Fake set), so fake games run on every OS, Linux included (7.1).
	NeedsText   map[string]string // by the same keys: "Needs Windows", "Needs an Intel Mac"
	DownloadMB  int
	DiskMB      int
	MemoryMB    int // what it needs with a few players; the memory check uses it
	MaxPlayers  int
	FriendsNeed string // "Minecraft: Java Edition on PC or Mac, the same version as the server"
	Presence    Presence
	Deaths      bool
	Ports       []PortRole
	Password    PasswordRule
	Legacy      bool // the one game kept in the legacy files (Valheim)
	Import      bool // worlds can be imported from the owner's own game
	Lists       bool // admins / allow list / bans
	Mods        bool // has a Mods tab
}

// Game is one supported game. Its adapter registers it from init().
type Game interface {
	Info() Info
	New(env Env) Server // one per instance
}

// ---- one instance ----

// Dirs are an instance's folders. For the legacy instance they are exactly
// config.ResolvePaths' folders, so Valheim's files never move (invariant I1).
type Dirs struct {
	Root     string // <data>/games/<instance>; the data folder for the legacy instance
	Server   string // installed server files
	Saves    string // worlds and the game's own data
	Backups  string // the app's world backups (zips)
	Logs     string // captured output, one file per run
	Runtime  string // private runtimes (Java, .NET): never shared between instances
	Staging  string // uploads waiting to be imported
	SteamCMD string // shared by every game: <data>/steamcmd
}

// Env is what the manager hands an adapter when it makes an instance's Server.
type Env struct {
	Instance string
	Data     string
	Dirs     Dirs
	Log      *log.Logger
	HTTP     *http.Client
	Store    Store // servers.json-backed; nil for the legacy instance, whose adapter makes its own from Legacy
	Legacy   any   // legacy instance only: the manager's access to settings.json and its injected parts (games/valheim.LegacyStore, 5.1)
	Fake     *Fake // EGS_DEV_FAKE_SERVERS / tests: install the stand-in instead (7.1)
}

// Locking: the manager has one lock (m.mu). It calls Monitor methods, Join, Fingerprint,
// OnlineTexter.Online, ModLister's PlayerMods/ModsCode/WorldMods and Store().Get with m.mu
// held, so adapter code must never call back into the manager or take a lock the manager
// holds. Store().Save, SetWorldMods, Plan, Installer and Stop run without m.mu (from jobs). Data the adapter needs from
// the manager comes as immutable snapshots (LegacyStore, 5.1) or as arguments; the `known`
// func passed to Watch reads a map the manager keeps under m.mu and never locks.

// Store keeps one instance's settings. The manager never reads or writes
// an instance's settings any other way, so the legacy instance can keep
// them in settings.json while every other game keeps them in servers.json.
type Store interface {
	Get() Saved                           // a copy
	Save(change func(*Saved) error) error // atomic; for the legacy instance it writes settings.json
}

// Server is one instance of a game: everything the manager needs to
// install, start, watch, stop and explain it.
type Server interface {
	Store() Store    // the manager always reads and writes an instance's settings through this
	Program() string // the file that runs and listens (a private java/dotnet for those games)
	Installer() Installer
	Build() (Build, bool) // what's installed; read by the checks loop and the install job, never by Status (the legacy instance: as today, 4.8)
	Schema() Schema
	Validate(s Saved, world string) FieldErrors
	Plan(ctx context.Context, req PlanRequest) (Plan, error)
	Watch(rec Record, known func(name string) string) Monitor // known: the account a name last played from
	Stop(rec Record) []StopStep
	Join(in JoinInput) Join
	Worlds() Worlds
	Texts() Texts
	ExitHint(tail []string) string
	Fingerprint(s Saved, world string) string // differs from Plan.Fingerprint → "restart to apply"
}

// ---- optional capabilities (type assertions on Server) ----

// Prober asks the running server itself (RCON, REST, its own API or A2S):
// its counts are authoritative, and an answer also means "it is up".
type Prober interface {
	Probe(ctx context.Context, rec Record) (ProbeResult, error) // ≤ 3 s
}

// Lister is a game with player lists (admins, allow list, bans).
type Lister interface {
	Lists() []ListKind
	ReadList(kind string) ([]string, error)
	WriteList(ctx context.Context, rec *Record, kind string, ids []string) error // live when running, if it can
}

// OnlineTexter writes a game's own "is online" Discord post (Valheim: its
// mods and mods code), where the generic post isn't enough.
type OnlineTexter interface {
	Online(in OnlineInput) string
}

// Afterer does work after launch; the online post waits for it (Valheim's
// mods code).
type Afterer interface {
	After(ctx context.Context, rec Record) (extra map[string]json.RawMessage, err error)
	// Pending says the record lacks After's result, so After runs again after a takeover
	// (Valheim: codeMods set and modsCode empty, as adopt() does today).
	Pending(rec Record) bool
}

// ModLister is a game with mods the app manages (Valheim this round).
type ModLister interface {
	PlayerMods(world string, rec *Record) []mods.Info
	ModsCode(rec *Record) string
	WorldMods() []mods.Info
	SetWorldMods(world string, ids []string) error
}

// Remover tidies up when the game is removed (Satisfactory: nothing to
// unclaim; Valheim: hides only).
type Remover interface {
	Remove(ctx context.Context) error
}

// Recoverer makes a record for a server found running without its run
// file (Valheim: today's adopt defaults).
type Recoverer interface {
	Recover(pid int, started time.Time) Record
}

// Without Recoverer the manager uses {PID, Started, Launched: started, World: Saved.World,
// LogPath: the newest log of this instance, Exe: Program()}.

// ---- install ----

// Progress is one step of an install, for the job's progress bar.
type Progress struct {
	Step    string
	Percent float64 // -1 unknown
}

// Installer installs or repairs an instance's server files.
type Installer interface {
	Install(ctx context.Context, repair bool, progress func(Progress)) error
}

// Build is what's installed.
type Build struct {
	Version string    `json:"version,omitempty"` // an opaque string: "26.3", "1.4.6.1", "1.22.7"
	Build   string    `json:"build,omitempty"`   // Steam build id
	Date    time.Time `json:"date,omitzero"`
}

// ---- launch ----

// Output is where a game's output goes, so the app can follow it.
type Output int

const (
	Capture Output = iota // the app passes Plan.LogPath as stdout and stderr
	LogArg                // the game writes Plan.LogPath, given in its arguments (Valheim)
	GameLog               // the game writes its own file at Plan.LogPath; the app follows it
)

// PlanRequest is what the manager asks for when it starts an instance.
type PlanRequest struct {
	World       string
	New         bool // the world doesn't exist yet: make it from NewWorld
	NewWorld    Values
	Saved       Saved
	LogPath     string // a fresh file in Dirs.Logs: <instance>-2006-01-02_15-04-05.log (legacy: server-….log)
	Update      bool   // update the server first (its "update on start")
	Step        func(msg string, pct float64)
	Cancellable func(bool) // Plan says when Cancel stops working (Valheim: after the mods, as today)
}

// Plan is how to run the server this time. The adapter has already written
// its config files when it returns one.
type Plan struct {
	Exe         string
	Args        []string
	Dir         string
	Env         []string // KEY=VALUE, added to the app's environment
	Output      Output
	LogPath     string
	Console     bool          // the app keeps a pipe to the game's stdin for console stop/save lines (2.11)
	SaveLine    string        // Console games: the line that saves without stopping, sent before the app exits ("save")
	ReadyWait   time.Duration // how long a start waits for "online" before the fallback; the manager adds 5 min (4.3)
	Fingerprint string
	Record      Record // fields to keep in the run record: Server, Port, Password, Secrets, Extra (Played is the manager's, below)
}

// ---- while it runs ----

// Line is one line of the server's output, as the adapter's Monitor reads it.
type Line struct {
	Hide   bool      // noise: not on the Log page
	Text   string    // as shown, without the game's own timestamp
	Time   time.Time // the line's own time; zero when it has none
	Events []Event
}

// Event is something that happened in the game that the app reacts to.
type Event struct {
	Kind string // "online", "join", "leave", "death", "saved", "stopping", "count", "code",
	//             "turned_away", or a game's own warning kind
	//             ("wrong_password", "bad_version", "load_failed", "bad_password_config")
	Time    time.Time
	Name    string
	Account string // the player's platform id or UUID
	Text    string // the feed text; "" = not shown
	Count   int    // "count": players the server itself says are on
	Hold    bool   // "count" a player can't fake: holds the computer awake 15 minutes
}

// Player is someone on the server now.
type Player struct {
	Name    string    `json:"name"`
	Account string    `json:"account,omitempty"`
	Since   time.Time `json:"since"`
}

// RunState is what the Monitor knows about the running server.
type RunState struct {
	Ready      bool
	Players    []Player
	Count      int // -1 when unknown
	Version    string
	Code       string // join/invite code or Game ID, once known
	LastSave   time.Time
	Stopping   bool
	StoppingAt time.Time
	Extra      map[string]string // the adapter's own (Valheim: "publicAddr", "crossplayFailed")
}

// Monitor follows one run of a server. Every method is called with the
// manager's lock held (see Env), so none may block or call the manager.
type Monitor interface {
	Line(line string, now time.Time) Line
	Tick(now time.Time) []Event                  // every 400 ms: overdue-ready and the like
	Probed(p ProbeResult, now time.Time) []Event // fold in a probe's answer (counts win)
	State() RunState
	End(now time.Time) []Event // the run ended: whoever is listed leaves
}

// ProbeResult is a Prober's answer.
type ProbeResult struct {
	Up      bool
	Count   int      // -1 unknown
	Names   []string // nil when the probe can't name them
	Version string
}

// ---- stopping ----

// StopStep is one way of asking the server to save and stop. The manager
// runs a game's steps in order until the server has exited.
type StopStep struct {
	Kind   string                          // "console", "rcon", "call", "interrupt"
	Line   string                          // the console or RCON command; a "console" step after a takeover returns ErrNoConsole
	Call   func(ctx context.Context) error // "call": e.g. a REST save then shutdown
	Wait   time.Duration                   // wait this long for it to exit before the next step
	Resend time.Duration                   // send again after this; 0 = once
	Say    string                          // job step text; "" = "Saving the world and shutting down…"
}

// ErrNoConsole says a step can't be used now: the next one runs at once.
var ErrNoConsole = errors.New("no console")

// ---- friends ----

// JoinInput is what the adapter needs to say how friends join.
type JoinInput struct {
	Rec      *Record   // nil when stopped
	State    *RunState // nil when stopped
	Saved    Saved
	PublicIP string
	LAN      []string          // this computer's LAN IPv4 addresses
	Router   map[string]string // by role: "open", "failed", "off"
}

// Join is how friends join, for the panel's card and the Discord post.
type Join struct {
	How       string   `json:"how"`                 // "code", "address", "id"
	CodeLabel string   `json:"codeLabel,omitempty"` // "Join code", "Invite code", "Game ID"
	Code      string   `json:"code,omitempty"`
	Address   string   `json:"address,omitempty"` // what friends outside type
	LAN       []string `json:"lan"`               // host:port at home
	Port      int      `json:"port"`
	Password  string   `json:"password,omitempty"`
	Public    bool     `json:"public"`
	Crossplay bool     `json:"crossplay"`
	Steps     []string `json:"steps"` // in the friends' game
	Hint      string   `json:"hint"`  // one line for Discord
	Problem   string   `json:"problem,omitempty"`
	Router    bool     `json:"router"`           // friends outside need router ports (false with a code or Game ID)
	Manual    string   `json:"manual,omitempty"` // extra words for the manual router steps (Windrose: "Your router must allow UPnP for NAT punch-through.")
}

// Texts are the words a game uses for its players, and the legacy wordings
// generic messages can't give (so Valheim's texts stay exactly as today).
type Texts struct {
	Players string            // plural noun for the daily post: "vikings", "players"
	Player  string            // "viking", "player"
	Exact   map[string]string // legacy wordings the generic templates can't give, by message id
}

// OnlineInput is what OnlineTexter.Online writes its post from.
type OnlineInput struct {
	Server, World string
	Join          Join
	Password      string // "" to leave it out
	Reminder      []string
	Rec           *Record
}

// ---- worlds ----

// Worlds is an instance's world library: Valheim's existing one, or the
// shared world store every other game uses (spec 2.8).
type Worlds interface {
	List() ([]worlds.World, []worlds.Orphan, error)
	Get(name string) (worlds.World, error)
	Exists(name string) bool
	NameProblem(name string) string
	Backup(name, reason string) (worlds.Backup, error)
	Restore(name, backupID string) error
	Delete(name string) error
	Rename(name, newName string) error // ErrCantRename
	ExportFile(name, dir string) (path string, err error)
	ExportBackup(w io.Writer, name, id string) error
	Scan(dir string) []worlds.Candidate // an unpacked upload
	Local() []worlds.Candidate          // worlds in the owner's own game on this computer
	Import(c []worlds.Candidate, ch []worlds.Choice, busy func(string) bool, move bool) ([]worlds.Placed, error)
	CleanStaging(maxAge time.Duration)
}

// ErrCantRename says a game's worlds can't be renamed by the app.
var ErrCantRename = errors.New("can't rename")

// ListKind is one of a game's player lists.
type ListKind struct {
	ID          string `json:"id"` // "admins", "allowed", "banned"
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
	Live        bool   `json:"live"` // applies without a restart
}

// Fake is set by EGS_DEV_FAKE_SERVERS and tests: the adapter's Installer copies the
// stand-in to Program() and writes fakeserver.json beside it (7.1). In fake mode Info.OS
// and the manager's OS check are ignored, and Program() gives the same layout on every
// GOOS (".exe" only on Windows), so fake games run on Linux, macOS and Windows.
type Fake struct {
	Program string // the built cmd/fakeserver
	Script  string // "join:Alex@1,death:Alex@5,leave:Alex@9"
	Speed   float64
	Delay   time.Duration // how long the pretend download takes
}
