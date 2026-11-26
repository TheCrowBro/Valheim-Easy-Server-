# Easy Game Server: the specification (round 2)

This turns Valheim Easy Server into Easy Game Server, which hosts 11 game servers, several at once. Coding agents implement it. Where this file and [decisions.md](decisions.md) disagree, decisions.md wins. This file builds on [carryover.md](carryover.md); the places where it departs from round one are marked **(changed)** and say why.

- **Baseline:** the default branch `ccr-949cf1ef-eeo1jr` at `bed004b` (still the fresh head on 2026-11-26). Work happens on `multigame` (`dc3a5b5` = bed004b + notes). Nothing goes to the default branch except at the R1/R2/R3 gates.
- **Who it serves:** one owner and a few friends. Only the owner's computers run the app (a Windows PC and a 2013 Mac Pro on macOS 12, Intel). Linux runs tests only; it is not a user platform.
- **Words:** *game* = one of the 11 supported games (an adapter in `internal/games/<id>`). *Instance* = one server of a game on this computer; this round there is at most one per game and its id equals the game id (`minecraft`); a second one would be `minecraft-2`. *Legacy instance* = Valheim, the one game whose data lives in the legacy files. *Foreign instance* = an instance in servers.json that this build doesn't run (unknown game, hidden stage, or disabled).
- **"verify"** marks a game fact round one wasn't sure of. The adapter's stream must check it against the real server (its real-server check, or a real log) before the game can be marked ready, and fix the adapter if it's wrong.

Contents: 1 Invariants and the legacy contract · 2 Architecture · 3 Data on disk · 4 Manager · 5 Game adapters · 6 Panel, API, Discord, waker, router ports · 7 Testing and CI · 8 Releases and kill switches · 9 Work breakdown · 10 Questions for the owner.

---

## 1. Invariants and the legacy contract

"Installed copies" are the owner's computers running bed004b or any later release. They update themselves from GitHub releases of the default branch. Everything in this section must hold for every release from R1 on, and is pinned by a test named in brackets.

### 1.1 Invariants

| # | Invariant | Pinned by |
|---|---|---|
| I1 | **Valheim never moves.** Its folders always come from `config.ResolvePaths(dataDir, settings)` (server, saves, saves/worlds_local, backups, logs, uploads, steamcmd, and the ServerDir/SaveDir overrides). No migration copies, moves, renames or deletes a Valheim file. Removing Valheim from the panel only hides it. | `TestLegacyPathsUnchanged` (manager) |
| I2 | **Legacy files keep their names, shapes and meanings** (table 1.3). New data goes only into new files: `servers.json` and `games/<instance>/…`. A legacy file may gain a field only if it is `omitempty`, older builds ignore it, and its absence means today's behaviour. | `TestLegacyFileShapes` (manager), golden JSON in `internal/manager/testdata/legacy/` |
| I3 | **settings.json is frozen** (changed): no field is added, removed, renamed or given a new meaning. App-wide settings that already exist there (idle sleep, auto-update, the main Discord channel, the waker) keep living there; everything new goes into servers.json. Round one's "keep unknown top-level keys" writer isn't needed, because nothing new is ever written into settings.json. | `TestSettingsShapeFrozen` (config) |
| I4 | **runtime.json stays Valheim's run file** at the data root, with the same shape, so a new build takes over a running Valheim exactly as today and an older build still can after a rollback. Other instances use `games/<instance>/run.json`. | `TestRuntimeJSONCompat` (manager) |
| I5 | **The update handshake keeps working:** every existing flag is accepted (`--data --background --no-window --port --dev-fake-server --dev-fake-discord --dev-fake-github --detached`, `-psn_…`); no new flag is ever added (an older build exits with code 2 on an unknown flag, and relaunchArgs passes flags on). Developer switches are `EGS_DEV_*` environment variables. `GET /api/status` answers 200 with its top-level fields. `.instance.json` holds `{"port":N}`. `manager.New` does O(1) work (it runs before the panel listens; the old build kills a new one that doesn't answer within a minute, main.go:300-361). | `TestFlagsUnchanged`, `TestStatusTopLevelKeys` (web), `TestNewIsQuick` (manager: New with 11 instances and 30 logs each < 200 ms) |
| I6 | **Identity unchanged** (table 1.2). Only what people see changes to "Easy Game Server", in R3. | `identity_test.go` files (R1) |
| I7 | **Old API routes keep working** as aliases of the legacy instance, with the same JSON. | `TestLegacyRoutes` (web) |
| I8 | **Other games never write into Valheim's places:** not `logs/server-*.log` (pruned and parsed as Valheim by old builds), not `backups/<name>` (listed as Valheim worlds by old builds), not `uploads/` (emptied by old builds at start), not players/records/lastplayed/stats/history.json. | `TestNoForeignFilesInLegacyPlaces` |
| I9 | Files holding passwords or tokens are written 0600: settings.json, runtime.json, servers.json, `games/*/run.json`, keeper files, jetkvm.json. | `TestSecretFilesPrivate` (Unix) |
| I10 | **No tunnel of any kind** anywhere: no package, route, setting, text or release item. | review checklist; `TestNoTunnel` greps the tree for `playit`, `ngrok`, `tunnel` outside docs/dev |
| I11 | **No new inbound firewall rule for the app itself.** The waker's replies go app → JetKVM (outbound). The existing "wake signal" rule (UDP 9 from the local subnet) is unchanged. | review |
| I12 | **Valheim behaves exactly as today** in R2 (and in R3 apart from the decided changes: auto-stop on, own Discord channel possible, router ports with crossplay off, the new panel). The existing tests keep their intent; texts are golden. | all existing tests; `games/valheim` golden tests |

### 1.2 Identity that must not change

All of these become named constants (R1) and get a test that compares them with the literal value, so a rename fails CI.

| What | Value | Where today |
|---|---|---|
| Data folder (Mac, LOCALAPPDATA fallback) | `ValheimEasyServer` | main.go:456, 479 |
| Downloads/Desktop data subfolder | `Valheim Easy Server` | main.go:462 (`appName`) → new constant `legacyFolderName` |
| Release assets | `ValheimEasyServer.exe`, `ValheimEasyServer-mac` (+ `.sha256`), `ValheimEasyServer-windows.zip`, `ValheimEasyServer-mac.zip` | selfupdate.go:38, build.yml |
| Repo and repo id | `TheCrowBro/Valheim-Easy-Server-`, `1430331409` | selfupdate.go:30-31 |
| Single-instance lock | `Local\ValheimEasyServer-<id>`; `<data>/.instance.lock`; `$TMPDIR/ValheimEasyServer-<id>.lock` | desktop_windows.go:205, desktop_unix.go:49-51 |
| Launch at login | Run value `ValheimEasyServer`; LaunchAgent `com.thecrowbro.valheimeasyserver` | desktop_windows.go:99, desktop_darwin.go:18 |
| Mac bundle | folder `Valheim Easy Server.app`, executable `Contents/MacOS/ValheimEasyServer`, CFBundleIdentifier `com.thecrowbro.valheimeasyserver` | macapp/Info.plist, build.yml:245-248 |
| Valheim firewall rules | prefix `Valheim Easy Server - `, `…game (<sha8>)`, `…wake signal (<sha8>)`, legacy names | winparse.go:15-48, desktop_windows.go:162-181 |
| Admin scripts | `ValheimEasyServer-admin-*.ps1` | power_windows.go:118,124 |
| JetKVM waker | binary `valheim-waker`, `/userdata/valheim-waker/`, `/userdata/init.d/S99valheim-waker`, `/tmp/valheim-waker.{pid,log,json}`, asset `dist/valheim-waker-armv7.gz` | jetkvm.go:44,109,191-223 |
| Panel header | `X-VES` | auth.go:221, dom.js:148, upload.js:261 |
| Workflow file | `.github/workflows/build.yml` (its run number is the build number) | build.yml:3-5 |
| Reminder salt | `valheim-easy-server:` | reminder.go:28 |

The visible name changes in R3 only: the panel title and texts, the manifest's `name`/`short_name`, the tray tooltip, dialog titles (`appName` → "Easy Game Server"), README and READ-ME-FIRST files, and `CFBundleDisplayName` in Info.plist (only new downloads get it; installed Macs keep their Info.plist, because updates replace only the binary). The bundle folder name stays, so a new download never makes a second app beside the old one.

### 1.3 Legacy files

| File (at the data root) | Holds | New builds |
|---|---|---|
| settings.json | Valheim's settings and the app-wide ones | read and write, shape frozen (I3) |
| runtime.json | the running Valheim's run record | read and write, shape frozen (I4) |
| players.json, records.json, lastplayed.json | Valheim's players, records, "what they last played with" | Valheim only |
| stats.json, history.json | usage by players; the last day's samples (machine + Valheim) | Valheim + machine only; other games keep their own |
| activity.json | the activity feed | all games; entries gain `game` (omitempty) |
| modscodes.json | Valheim's mods codes | Valheim only |
| jetkvm.json | the waker's setup | `setup` gains `games` (omitempty string, see 6.4) |
| update.json, .instance.json | the update handshake | unchanged |
| jetkvm-key.pem, logs/app.log | the app's JetKVM key, the app's log | unchanged |

### 1.4 The legacy contract, as behaviour

- **Auto-update:** `selfupdate` and main.go's `startUpdated`/`waitStarted`/`Restore` are not changed. The new build must answer `/api/status` within the minute.
- **Automatic rollback to the previous build:** whatever a new build writes must be fine for the previous build (rollback matrix, 3.6).
- **Takeover of a running Valheim:** `adoptRunning` keeps its order: runtime.json → `OpenProc` → start time within ±2 s → `FindByExe(paths.ServerExe)` → adopt the first one. A server started by an older build (runtime.json without `port`) keeps working as today. A pending Discord post is left for the build that takes over (manager.go:337-348).
- **Data:** nothing in 1.3 changes meaning; Valheim's worlds, backups and logs stay where they are.
- **Routes:** every route in api.go:31-83 stays, bound to the legacy instance.
- **Branch builds don't update themselves (changed, R1):** builds of any branch other than the default get `-X main.build=0`. Round one missed that a test copy built from `multigame` carries the same run-number scheme and would silently update itself back to the newest default-branch release in the middle of the owner's test.

---

## 2. Architecture

### 2.1 Packages and dependency rules

```
main.go                      flags (unchanged), keeper hook, registry import (games/all), tray
internal/game                the contract: types, interfaces, registry, Values (2.2)
internal/game/gametest       runs one adapter against cmd/fakeserver without the manager (7.1)
internal/games/<id>          one adapter per game (section 5); games/all/<id>.go = one blank import each
internal/install             SteamCMD (shared, one at a time), downloads, runtimes, Windows prerequisites
internal/keeper              the keeper process and its client (2.11)
internal/rcon, internal/a2s  Source RCON client; Steam A2S_INFO query
internal/worldstore          the world store for non-legacy games (2.8)
internal/servers             servers.json: types, load, save, migration (3.2)
internal/routerports         UPnP IGD, NAT-PMP, PCP, reachability (6.5)
internal/manager             instances, jobs, lifecycle, Discord, sleep and waking (section 4)
internal/web                 HTTP API (Backend interface) and the embedded panel (6.1, 6.2)
cmd/fakeserver               stand-in servers with a personality per game (7.1)
cmd/ci-stubs                 CI-only stand-ins for GitHub releases and Discord webhooks (not shipped)
```

Unchanged packages keep their jobs: `config` (settings.json, Valheim's arguments and rules), `worlds` (Valheim's world library), `mods` (Valheim's BepInEx/Thunderstore), `logparse` (Valheim's log), `steamcmd` (SteamCMD runner), `platform`, `selfupdate`, `discord`, `waker`, `jetkvm`, `wol`, `records`, `stats`, `tray`.

Rules (checked by `TestImportRules` in internal/game, which reads each package's direct imports with `go list -f '{{.ImportPath}} {{.Imports}}' ./...`, test files excluded):
- `game` directly imports only the standard library, plus `worlds` and `mods` for shared data types (`worlds.World`, `worlds.Backup`, `worlds.Candidate`, `worlds.Choice`, `worlds.Placed`, `worlds.Orphan`, `mods.Info`). `mods.Info` is today's `manager.ModInfo` (`{mods.Mod; Version}`) moved into `internal/mods/info.go` in phase 0; the manager keeps `type ModInfo = mods.Info`, so its JSON doesn't change.
- `games/<id>` may import `game`, `install`, `rcon`, `a2s`, `worldstore`, `platform`, and (Valheim only) `config`, `logparse`, `mods`, `worlds`, `steamcmd`. Never `manager`, `web` or another `games/<id>`.
- `manager` imports `game` and the registry, never a `games/<id>` package. `web` imports `manager` (types) and `game` (types).
- **No hard-coded game ids** in `internal/manager/*.go`, `internal/web/*.go` and the panel shell (`internal/web/ui/assets/*.js`, not `assets/games/`), test files excluded: `TestNoGameIDs` fails if a string literal there is exactly a registered id (`"valheim"`, `'minecraft'`). The legacy game is found with `game.Legacy()`; where the manager must behave as today for Valheim, it asks `info.Legacy`, never the id.
- **Valheim's visible texts stay identical in R2** (the old panel shows them): generic messages fill in `Info.Short`, and where a template can't give today's exact Valheim wording ("The Valheim server isn't installed yet. Click Install first (it's free, about a 1 GB download).", "Installing the Valheim server", "Checking for Valheim server updates…"), the adapter supplies it. Existing tests that check these keep checking them.

### 2.2 The game contract (`internal/game`)

Frozen in phase 0 (9.2). Adding an optional interface later is fine; changing these needs the lead.

```go
package game

// ---- what a game is ----

type Stage string // a per-game kill switch, compiled in (8.3)

const (
	Hidden  Stage = "hidden"  // built in, never shown; its instances are foreign
	Preview Stage = "preview" // can be added; marked "Preview" in Add a game and on its page
	Ready   Stage = "ready"
)

type Level string // whether the server runs on an OS

const (
	Runs    Level = "yes"
	IntelOn Level = "intel" // an Intel-only build: on Apple silicon it needs Rosetta 2
	CantRun Level = "no"    // greyed out in Add a game with Info.NeedsText
)

type Presence string // how the app knows who is playing

const (
	ByName  Presence = "names" // who, by name (log or probe)
	ByCount Presence = "count" // how many, not who
	Unknown Presence = "none"  // no signal: Windrose's Discord rule (4.6)
)

type PortRole struct {
	Role    string   `json:"role"`          // "game", "query", "direct", "rcon", "rest", "reliable"
	Label   string   `json:"label"`         // "Game port"
	Protos  []string `json:"protos"`        // "udp", "tcp"
	Default int      `json:"default"`
	Tie     string   `json:"tie,omitempty"` // "game+1": always that role's port plus one
	Public  bool     `json:"public"`        // friends connect to it: firewall rule and router mapping
}

type PasswordRule struct {
	Use      string `json:"use"` // "required", "optional", "none"
	Min      int    `json:"min,omitempty"`
	Max      int    `json:"max,omitempty"`
	Help     string `json:"help"`               // shown under the box
	NoneText string `json:"noneText,omitempty"` // "none": what protects it instead
}

type Info struct {
	ID          string // "minecraft"; also its folder and, by default, its slash command
	Name        string // "Minecraft: Java Edition"
	Short       string // "Minecraft": sidebar, Discord, reminders ("update Minecraft to …")
	Letter      string // "M"
	Color       string // "#5b8731"
	Command     string // Discord slash command, lowercase [a-z0-9_-]{1,32}
	Order       int
	Stage       Stage
	OS          map[string]Level // by GOOS; a missing OS is CantRun
	NeedsText   string           // "Needs Windows"
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

type Game interface {
	Info() Info
	New(env Env) Server // one per instance
}

func Register(g Game)              // from init() in games/<id>; panics on a duplicate id or a second Legacy
func All() []Game                  // by Info().Order
func Get(id string) (Game, bool)
func Legacy() Game

// ---- one instance ----

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

type Env struct {
	Instance string
	Data     string
	Dirs     Dirs
	Log      *log.Logger
	HTTP     *http.Client
	Store    Store // servers.json-backed; nil for the legacy instance, whose adapter makes its own from Legacy
	Legacy   any   // legacy instance only: the manager's access to settings.json (games/valheim.LegacyStore)
	Fake     *Fake // EGS_DEV_FAKE_SERVERS / tests: install the stand-in instead (7.1)
}

// Values are settings by key. Every value is a string: "true", "25565", "normal".
type Values map[string]string

func (v Values) Int(key string, def int) int
func (v Values) Bool(key string) bool
func (v Values) Clone() Values

type Saved struct {
	Server Values            // server scope
	Worlds map[string]Values // world scope, by world name
	World  string            // the selected world
	Ports  map[string]int    // by role
	State  Values            // the adapter's own bookkeeping (Palworld world ids, Satisfactory claimed…)
}

type Store interface {
	Get() Saved                          // a copy
	Save(change func(*Saved) error) error // atomic; for the legacy instance it writes settings.json
}

type Server interface {
	Store() Store        // the manager always reads and writes an instance's settings through this
	Program() string     // the file that runs and listens (a private java/dotnet for those games)
	Installer() Installer
	Build() (Build, bool) // what's installed; read by the checks loop, never by Status
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

type Prober interface { // RCON/REST/API/A2S: authoritative counts, also "is it up"
	Probe(ctx context.Context, rec Record) (ProbeResult, error) // ≤ 3 s
}
type Lister interface { // player lists
	Lists() []ListKind
	ReadList(kind string) ([]string, error)
	WriteList(ctx context.Context, rec *Record, kind string, ids []string) error // live when running, if it can
}
type OnlineTexter interface { // a game's own "is online" Discord post (Valheim: mods, mods code)
	Online(in OnlineInput) string
}
type Afterer interface { // work after launch; the online post waits for it (Valheim's mods code)
	After(ctx context.Context, rec Record) (extra map[string]json.RawMessage, err error)
}
type ModLister interface { // Valheim this round
	PlayerMods(world string, rec *Record) []mods.Info
	ModsCode(rec *Record) string
	WorldMods() []mods.Info
	SetWorldMods(world string, ids []string) error
}
type Remover interface { // tidy-up when the game is removed (Satisfactory: nothing to unclaim; Valheim: hides only)
	Remove(ctx context.Context) error
}
type Recoverer interface { // a record for a server found running without its run file (Valheim: today's adopt defaults)
	Recover(pid int, started time.Time) Record
}
// Without Recoverer the manager uses {PID, Started, Launched: started, World: Saved.World,
// LogPath: the newest log of this instance, Exe: Program()}.

// ---- install ----

type Progress struct {
	Step    string
	Percent float64 // -1 unknown
}
type Installer interface {
	Install(ctx context.Context, repair bool, progress func(Progress)) error
}
type Build struct {
	Version string    `json:"version,omitempty"` // "1.21.4"
	Build   string    `json:"build,omitempty"`   // Steam build id
	Date    time.Time `json:"date,omitzero"`
}

// ---- settings schema ----

type Scope string

const (
	ServerScope   Scope = "server"   // the instance
	WorldScope    Scope = "world"    // each world
	NewWorldScope Scope = "newWorld" // only when making a world
)

type Field struct {
	Key      string   `json:"key"`
	Scope    Scope    `json:"scope"`
	Kind     string   `json:"kind"` // "text", "password", "number", "bool", "choice", "list"
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Default  string   `json:"default,omitempty"`
	Min      int      `json:"min,omitempty"`
	Max      int      `json:"max,omitempty"`
	Choices  []Choice `json:"choices,omitempty"`
	Restart  bool     `json:"restart,omitempty"`  // applies after a restart
	Advanced bool     `json:"advanced,omitempty"` // under "More settings"
	Add      bool     `json:"add,omitempty"`      // asked in Add a game
}
type Choice struct{ Value, Label string }
type Schema []Field

// FieldErrors maps a field key to a message. It has Error() like config.FieldErrors,
// and web.fail answers both the same way: {error, fields}.
type FieldErrors map[string]string

// ---- launch ----

type Output int

const (
	Capture Output = iota // the app passes Plan.LogPath as stdout and stderr
	LogArg                // the game writes Plan.LogPath, given in its arguments (Valheim)
	GameLog               // the game writes its own file at Plan.LogPath; the app follows it
)

type PlanRequest struct {
	World    string
	New      bool   // the world doesn't exist yet: make it from NewWorld
	NewWorld Values
	Saved    Saved
	LogPath  string // a fresh file in Dirs.Logs: <instance>-2006-01-02_15-04-05.log (legacy: server-….log)
	Update   bool   // update the server first (its "update on start")
	Step     func(msg string, pct float64)
	Cancellable func(bool) // Plan says when Cancel stops working (Valheim: after the mods, as today)
}

type Plan struct {
	Exe         string
	Args        []string
	Dir         string
	Env         []string // KEY=VALUE, added to the app's environment
	Output      Output
	LogPath     string
	Console     bool // start through the keeper, so console lines work after an app restart too
	Fingerprint string
	Record      Record // fields to keep in the run record: Server, Port, Password, Secrets, Extra
}

// Record is the run file: runtime.json for the legacy instance, games/<id>/run.json for others.
// Its first fields are exactly Valheim's runInfo (manager.go:173-201).
type Record struct {
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Launched  time.Time `json:"launched"`
	LogPath   string    `json:"logPath"`
	World     string    `json:"world"`
	ArgsKey   string    `json:"argsKey"` // the fingerprint
	Exe       string    `json:"exe"`
	Announced string    `json:"announced,omitempty"`
	Posted    time.Time `json:"posted,omitempty"`
	PostError string    `json:"postError,omitempty"`
	Server    string    `json:"server,omitempty"`
	Port      int       `json:"port,omitempty"`
	Password  string    `json:"password,omitempty"`
	// Never written for the legacy instance:
	Game     string            `json:"game,omitempty"`
	Instance string            `json:"instance,omitempty"`
	Ports    map[string]int    `json:"ports,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"` // RCON/REST passwords the run started with
	Keeper   string            `json:"keeper,omitempty"`  // path of keeper.json
	// Extra holds the adapter's own fields, read and written at the top level of the
	// file (Valheim: "played", "noCrossplay", "modsCode", "codeMods"), so runtime.json
	// keeps its shape. MarshalJSON/UnmarshalJSON do the inlining.
	Extra map[string]json.RawMessage `json:"-"`
}

// ---- while it runs ----

type Line struct {
	Hide   bool      // noise: not on the Log page
	Text   string    // as shown, without the game's own timestamp
	Time   time.Time // the line's own time; zero when it has none
	Events []Event
}

type Event struct {
	Kind    string // "online", "join", "leave", "death", "saved", "stopping", "count", "code",
	//                "turned_away", or a game's own warning kind
	//                ("wrong_password", "bad_version", "load_failed", "bad_password_config")
	Time    time.Time
	Name    string
	Account string // the player's platform id or UUID
	Text    string // the feed text; "" = not shown
	Count   int    // "count": players the server itself says are on
	Hold    bool   // "count" a player can't fake: holds the computer awake 15 minutes
}

type Player struct {
	Name    string    `json:"name"`
	Account string    `json:"account,omitempty"`
	Since   time.Time `json:"since"`
}

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

type Monitor interface {
	Line(line string, now time.Time) Line
	Tick(now time.Time) []Event                  // every 400 ms: overdue-ready and the like
	Probed(p ProbeResult, now time.Time) []Event // fold in a probe's answer (counts win)
	State() RunState
	End(now time.Time) []Event // the run ended: whoever is listed leaves
}

type ProbeResult struct {
	Up      bool
	Count   int      // -1 unknown
	Names   []string // nil when the probe can't name them
	Version string
}

// ---- stopping ----

type StopStep struct {
	Kind   string // "console", "rcon", "call", "interrupt"
	Line   string // the console or RCON command
	Call   func(ctx context.Context) error // "call": e.g. a REST save then shutdown
	Wait   time.Duration // wait this long for it to exit before the next step
	Resend time.Duration // send again after this; 0 = once
	Say    string        // job step text; "" = "Saving the world and shutting down…"
}

var ErrNoConsole = errors.New("no console") // a step can't be used now: the next one runs at once

// ---- friends ----

type JoinInput struct {
	Rec      *Record   // nil when stopped
	State    *RunState // nil when stopped
	Saved    Saved
	PublicIP string
	LAN      []string          // this computer's LAN IPv4 addresses
	Router   map[string]string // by role: "open", "failed", "off"
}

type Join struct {
	How       string   `json:"how"` // "code", "address", "id"
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
	Router    bool     `json:"router"` // friends outside need router ports (false with a code or Game ID)
}

type Texts struct {
	Players string            // plural noun for the daily post: "vikings", "players"
	Player  string            // "viking", "player"
	Exact   map[string]string // legacy wordings the generic templates can't give, by message id
}

type OnlineInput struct {
	Server, World string
	Join          Join
	Password      string // "" to leave it out
	Reminder      []string
	Rec           *Record
}

// ---- worlds ----

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

var ErrCantRename = errors.New("can't rename")

type ListKind struct {
	ID          string `json:"id"` // "admins", "allowed", "banned"
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Help        string `json:"help"`
	Live        bool   `json:"live"` // applies without a restart
}

// Fake is set by EGS_DEV_FAKE_SERVERS and tests: the adapter's Installer copies the
// stand-in to Program() and writes fakeserver.json beside it (7.1).
type Fake struct {
	Program string        // the built cmd/fakeserver
	Script  string        // "join:Alex@1,death:Alex@5,leave:Alex@9"
	Speed   float64
	Delay   time.Duration // how long the pretend download takes
}
```

How today's Valheim code maps onto the contract (the Valheim stream moves these bodies verbatim; 5.1):

| Today | Contract |
|---|---|
| `steamcmd.SteamCMD` app 896660, `InstalledBuild` | `Installer()`, `Build()` |
| `config.ServerExeName`, `paths.ServerExe` | `Program()` |
| `config.Settings.Validate` | `Validate` (field keys unchanged) |
| `ServerArgs`, `SteamAppId`, DYLD path, `mods.Env`, `prepareMods`, `UpdateOnStart` | `Plan` (Output `LogArg`, log `logs/server-….log`) |
| `argsKey` | `Fingerprint`, `Plan.Fingerprint` |
| `logparse.Parse/IsNoise/Tracker`, `checkOverdue`/`readyWait`, the PeerCount/JoinCode hold | `Watch` → Monitor (`Line`, `Tick`, `Event{Kind:"count", Hold:true}`) |
| `doStop` (Ctrl+C, resend 90 s, give up 4 min) | `Stop` → `[{Kind:"interrupt", Wait:4m, Resend:90s}]` |
| `exitHint` | `ExitHint` |
| `joinSettingsLocked`, `JoinInfo`, codeWait, "crossplay"/"late" | `Join` |
| `onlineMessage.text`, mods lines | `OnlineTexter.Online` (byte-identical) |
| `makeModsCode`, `codePending` | `Afterer.After` |
| `playerModsLocked`, `modsCodeLocked`, `Worlds().Mods`, `SetWorldMods` | `ModLister` |
| `worlds.Library` (Valheim formats) | `Worlds()` = a thin wrapper over the existing Library |
| `config.ReadList/WriteList` | `Lister` |
| `config.WorldOptions` (preset, modifiers, keys, Off) | world-scope schema; stored where it is today (settings.json `worldOptions`) |
| `runInfo` | `Record`; `played`, `noCrossplay`, `modsCode`, `codeMods` in `Extra` |
| `stoppedText`, `dailyText` "vikings" | generic code with `Texts` (identical output for Valheim) |

The manager keeps what isn't game-specific: jobs, adoption, attach/onExit, the tail, the logs ring, crash restarts, records archiving, players, reminders (`played{Game, Mods, Password}` with `Short` and `ModLister`), the Discord queue, sleep and waking.

### 2.3 Install

`internal/install`:

```go
// SteamCMD installs a Steam app with the shared SteamCMD in dirs.SteamCMD. Only one
// SteamCMD runs at a time in the whole app (it updates itself and keeps one package cache):
// a second install waits, showing "Waiting for the <game> download to finish…".
func SteamCMD(app SteamApp, dirs game.Dirs, out func(string)) game.Installer
type SteamApp struct {
	AppID  string  // "1169370"
	Branch string  // "" = public
	Name   string  // "Necesse" (progress and error texts)
	SizeGB float64 // for "not enough free disk space for the Necesse server (it needs about 1 GB)"
}
func SteamBuild(serverDir, appID string) (game.Build, bool) // steamapps/appmanifest_<id>.acf

// Download fetches url into a temp file next to dest, checks the hash (sha1 or sha256,
// "" = none), then renames. Unzip/Untar refuse unsafe paths, keep or set exec bits.
func Download(ctx context.Context, url, hash, dest string, step func(game.Progress)) error
func Unzip(zipPath, dir string, exec func(name string) bool) error
func Untar(tgzPath, dir string) error

// Runtimes go into the instance's own Dirs.Runtime and are never shared, so
// FindByExe on the runtime's path finds only this instance's server.
func Temurin(ctx context.Context, major int, dir string, step func(game.Progress)) (java string, err error)
func DotNet(ctx context.Context, channel string, dir string, step func(game.Progress)) (dotnet string, err error)

// Windows prerequisites, installed system-wide with one admin prompt each; the
// install waits for the prompt's answer (platform.RunElevatedWait).
type Prereq struct {
	Name      string             // "XNA Framework 4.0"
	Installed func() bool        // registry check
	URL, Hash string
	Args      []string           // silent install arguments
}
func EnsurePrereq(ctx context.Context, p Prereq, step func(game.Progress)) error

func WriteBuild(serverDir string, b game.Build) error // .egs-build.json, for non-Steam installs
func ReadBuild(serverDir string) (game.Build, bool)
```

`internal/steamcmd` gains `AppID`, `Name` and `SizeGB` fields on `SteamCMD` (empty = today's Valheim values, so Valheim's install is byte-identical), a package-level `Lock sync.Mutex` taken around `run`, and `InstalledBuildFor(dir, appID)`. The literal `"Server files are up to date."` stays (server.go:242 compares it). Every SteamCMD game passes `UpdateOnStart` by default: SteamCMD only downloads when there's a newer build, so the installed build is the newest at every start (this replaces round one's separate "compare buildids" step for Palworld).

### 2.4 Plan, launch and output

The manager runs every start the same way:

1. checks (4.3), then `Plan(ctx, req)`; the adapter writes its config files and updates first when `req.Update`;
2. `Console` false: `platform.Start(StartOptions{Exe, Args, Dir, Env, Stdout: f, Stderr: f})`, where `f` is `Plan.LogPath` opened for append when `Output == Capture`, else nil;
3. `Console` true: start the keeper (2.11), which starts the game the same way with a stdin pipe;
4. attach: write the run record, follow `Plan.LogPath`, feed each line to `Monitor.Line`.

Rules for adapters:
- **Launch the real server process directly**, never a launcher, `.bat` or script that starts a child (Palworld and Satisfactory: the `-Cmd` shipping exe; Project Zomboid: its bundled java with the classpath from `ProjectZomboid64.json`; Core Keeper: `CoreKeeperServer.exe`). Then one PID is the server: usage, Ctrl+C, kill and adoption all work on it. verify per game.
- **Own log file per run** for new games: `Capture` by default (the app's file is never rotated mid-run). `GameLog` only where the game writes nothing useful to stdout; the tail then reopens the path when it no longer names the open file (rotation) and opens it with `platform.OpenShared` (Windows `FILE_SHARE_DELETE`, so the game can rotate it), and it also appends every line it reads to the run's own file in `Dirs.Logs` (named from `Record.Launched`), so the Log page's download, the records backfill and pruning work the same for every game.
- Environment: the app's environment plus `Plan.Env`. Valheim's `SteamAppId`/DYLD/Doorstop variables stay in Valheim's plan only.
- Logs are pruned per instance: the 30 newest `<instance>-*.log` in `Dirs.Logs`.

### 2.5 Ports as role specs

An instance's ports are `servers.json` `instances[].ports` (role → port), set once when the game is added:

- For each role without a `Tie`, start at `Default`; if any instance already reserves it (servers.json ports, plus Valheim's `settings.Port` and `Port+1`), or `platform.PortInUse(proto, port)` says another program holds it, try the next one, up to `Default+100`. Tied roles follow their base.
- **Ports never move after that.** A clash at start fails the start with `ActionError{Action: "pick_ports"}` ("Port 7777 (TCP) is being used by another program: <name>"). The panel's button asks the app to pick new free ports for the roles the owner didn't choose, then updates the firewall rules and router mappings. A port the owner typed in Settings (`portsChosen`) is never picked again: its clash says "change it in Settings".
- Valheim keeps its own rule unchanged: `UDPPortInUse(s.Port)` at start, the port from settings.json. Its two ports are in the reservation table.
- Roles with `Public: false` (RCON, REST) are told to listen on 127.0.0.1 where the game allows it, get no firewall rule and no router mapping.

### 2.6 Readiness, presence and probes

- **Ready:** an `"online"` event from the Monitor (a log line), or from `Tick` as a fallback (Valheim: 10 minutes after Steam login), or a probe answering `Up` when the adapter says the probe is enough (Palworld, Satisfactory).
- **Presence:** `ByName` (joins and leaves from the log or a probe's names), `ByCount` (a count from a probe or the log), `Unknown`.
- **Probes** (`Prober`) run every 30 s while ready, and once at ready. Their answer goes through `Monitor.Probed`, which reconciles: the probe's count is authoritative; names the probe lists that the log missed are joined, names it doesn't list leave. A probe that fails three times in a row is shown as a warning on the game's page; the log keeps working alone.
- A count above zero from the server itself (`Hold`) resets the game's auto-stop clock and holds the computer awake 15 minutes, as Valheim's PeerCount does today.

### 2.7 Stop chains

`Server.Stop(rec)` returns the steps, built from the run record alone (so it works after a takeover). The manager runs them in order:

- send the step (console line through the keeper, RCON command, call, or Ctrl+C/SIGINT); `ErrNoConsole` or a refused connection moves to the next step at once;
- wait up to `Wait` for the exit, sending again at `Resend`; progress text from `Monitor.State().LastSave` ("World saved. Shutting down…"), as today;
- when the last step's wait ends: `ActionError{Action: "force_stop"}`, as today. **Killing is never automatic**: only the owner's Force stop kills (unchanged `ForceStop`).

Stops of several games run in parallel goroutines. On Windows the Ctrl+C sends still go one at a time through `consoleMu` (about 1 s each); the saving itself overlaps. Each game has its own console (CREATE_NO_WINDOW gives every started console program a new hidden console), so a Ctrl+C reaches only that game. A Windows program built as a GUI program has no console, so Ctrl+C can't reach it (`Interrupt` fails at `AttachConsole`): each adapter whose stop ends in "interrupt" must verify its server is a console program (Enshrouded, Core Keeper, Windrose especially); if it isn't, the chain needs the game's own way to quit, or the adapter documents that only its autosave protects the world and says so in the stop's `Say` text.

### 2.8 Worlds

- **Valheim** keeps `internal/worlds.Library` unchanged; `games/valheim` wraps it as `game.Worlds`.
- **Every other game** uses `internal/worldstore`, which reuses the format-independent ideas of the Library (zip backups with a fingerprint retry while the server saves, all-or-nothing replace with set-aside and undo, import from a zip or folder with safe paths) in new code. `internal/worlds` is not changed.

```go
package worldstore

// Format is how a game keeps its worlds.
type Format struct {
	Dir     string   // where the live worlds are
	Kind    string   // "folder": a world is a folder; "files": files sharing a base name
	Ext     []string // "files": the extensions of one world, first one names it (".wld", ".wld.bak")
	MaxName int
	// Optional: a game whose worlds don't follow Kind (Palworld GUID folders, Satisfactory sessions).
	List    func() ([]Live, error)
	Files   func(name string) ([]string, error)
	Detect  func(root string) []worlds.Candidate // in an upload
	LocalIn func(home string) []string           // the owner's own game's save folders on this OS
	Place   func(c worlds.Candidate, name string) error // after copying, e.g. rename inside the save
}
type Live struct{ Name string; Size int64; Modified time.Time; NeverSaved bool }

func New(f Format, backups, staging string) game.Worlds
```

Backups are `Dirs.Backups/<world>/<stamp>_<reason>.zip` (never in the legacy `backups/`). A backup or restore of the running world refuses with "stop the server first", except that `Backup` while running is allowed (fingerprint retry), as for Valheim. World names: the shared file-name rules (Windows reserved names, `\/:*?"<>|`, no leading/trailing dot or space, at most `MaxName`) plus the game's own `NameProblem`.

Selecting a world is `Saved.World`; the next start uses it. A "new world" is a name plus the new-world values; the server makes it at its first start (Satisfactory: through its API; 5.10).

### 2.9 Settings schema scopes

- `server`: the instance (server name, password, max players, update on start, auto-restart, game options).
- `world`: each world (Valheim's difficulty; Minecraft's difficulty and game mode; Terraria's difficulty is fixed at creation, so it is `newWorld`).
- `newWorld`: asked only when making a world (seed, size, world type).
- `Add: true` fields are asked in Add a game (always server name, password and world).

One generic form in the panel draws any schema. Field errors come back keyed `server.<key>`, `world.<key>`, `newWorld.<key>` from new routes (bare keys on legacy routes, unchanged). Valheim's values are read from and written to settings.json by `games/valheim.LegacyStore`: `serverName password port public crossplay saveIntervalMinutes backups backupShortMinutes backupLongMinutes updateOnStart autoRestart extraArgs serverDir saveDir`; world scope `preset`, `modifier.<name>`, `key.<name>` from `worldOptions` (the `Off`/`-resetmodifiers` logic stays inside the adapter).

App-wide settings (idle sleep, auto-update, start with app, start after wake, the main Discord channel, router ports) are on the App settings and Discord pages, not in any game's schema.

### 2.10 Platform support levels

`Info.OS` per GOOS: `yes`, `intel` (an Intel-only Mac build: on Apple silicon "Needs Rosetta 2" with a link; offered when `platform.Rosetta()` is true), `no` (greyed out with `NeedsText`, "Needs Windows"). On macOS: Valheim, Minecraft, Terraria (intel), Necesse (intel, verify) are offered; the other seven are "Needs Windows": no Mac install code paths, no Mac CI cells, not on the Mac checklist. A game whose instance exists but can't run here (data copied from another computer) shows its page with the reason and no Start button.

### 2.11 The keeper

For games that save only on a console command (Terraria `exit`; Necesse and Vintage Story prefer their console), the app must be able to type a line into the server's console after the app itself restarted (update, crash, "Close app, servers keep running"). The keeper is a tiny broker that outlives the app.

- **Binary:** a copy of the app's own executable at `<data>/bin/keeper-<sha8>[.exe]` (sha8 of the app binary), made on first use. Never `exe` or `exe.old`, so `selfupdate.Swap`/`Restore` are unaffected. Old copies are deleted at start when not running (a running one can't be deleted on Windows; that's fine).
- **Mode:** `keeper-<sha8> --egs-keeper <spec.json>`. main.go checks `os.Args[1] == "--egs-keeper"` as its first statement, before flag parsing, data folder, single-instance lock and macOS detaching; `keeper.Main(path) int` then exits. Only new builds start keepers, so old builds never see the flag.
- **Spec** (`games/<id>/keeper-spec.json`, 0600, deleted by the keeper once read):

```json
{"exe": "C:\\…\\games\\terraria\\server\\Windows\\TerrariaServer.exe",
 "args": ["-config", "C:\\…\\games\\terraria\\server\\serverconfig.txt"],
 "dir": "C:\\…\\games\\terraria\\server\\Windows",
 "env": ["KEY=VALUE"],
 "log": "C:\\…\\games\\terraria\\logs\\terraria-2026-11-26_20-15-00.log",
 "state": "C:\\…\\games\\terraria\\keeper.json",
 "token": "9f2c…64 hex"}
```

- **What it does:** creates a pipe; starts the game with stdin = the pipe's read end, stdout/stderr appended to `log`, and a duplicate of the pipe's write end inherited as an extra handle (Unix `ExtraFiles`; Windows `AdditionalInheritedHandles`), so the game never sees end-of-file even if the keeper dies. Listens on `127.0.0.1:0`. Writes `state` (0600): `{"keeperPid","keeperStarted","pid","started","port","token"}`. Exits when the game exits. Unix: `Setsid`, ignores SIGINT and SIGHUP; the game in its own process group. Windows: started with CREATE_NO_WINDOW (its own hidden console); starts the game with CREATE_NO_WINDOW (another console of its own); never calls `SetConsoleCtrlHandler(NULL, TRUE)` before starting the game (that would be inherited and the game would ignore Ctrl+C).
- **Protocol** (one TCP connection per command, lines end with `\n`): `AUTH <token>` → `OK`; `LINE <text>` → `OK` (written to the game's stdin with `\n`); `PING` → `PONG <pid>`. Anything else, or no AUTH first: the connection closes.

```go
package keeper
func Main(specPath string) int
type State struct { KeeperPID int; KeeperStarted time.Time; PID int; Started time.Time; Port int; Token string }
func Start(app, bin string, s Spec) (State, error)  // copies the binary if needed, starts it, waits ≤ 10 s for state
func Dial(statePath string) (*Client, error)         // reads state, checks PID and start time
func (c *Client) Line(text string) error             // ErrNoConsole when the keeper is gone
```

The run record's `Keeper` holds the state file's path. After a takeover the manager `Dial`s it; when that fails, console steps return `ErrNoConsole` and the stop chain moves on to Ctrl+C (with a warning that the world may not be saved).

### 2.12 Platform additions (`internal/platform`)

New functions only; existing ones keep their behaviour (golden tests).

```go
type StartOptions struct {
	Exe    string
	Args   []string
	Dir    string
	Env    []string
	Stdin  *os.File
	Stdout *os.File
	Stderr *os.File
	Inherit []*os.File // extra handles/fds the child keeps
}
func Start(o StartOptions) (*Proc, error) // like StartDetached (consoleMu, hidden, own group/console)

// PortInUse reads the system's tables (never binds, so no firewall prompt) and
// says which process holds the port, 0 when unknown.
func PortInUse(proto string, port int) (inUse bool, pid int)  // Windows: GetExtended{Tcp,Udp}Table OWNER_PID; mac: lsof -nP; Linux: bind test
func OpenShared(path string) (*os.File, error)                  // Windows FILE_SHARE_READ|WRITE|DELETE
func DefaultGateway() (net.IP, error)                           // Windows GetAdaptersAddresses with gateways; mac `route -n get default`
func KnownFolder(name string) string                            // "appdata", "localappdata", "documents", "savedgames", "home", "appsupport"
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) // Windows ShellExecuteEx runas + wait; mac osascript admin
func Rosetta() bool
func ProcName(pid int) string                                    // for "used by <program>"

// Firewall rules for games other than Valheim. Names start with "Easy Game Server - "
// (old builds' blanket removal only matches "Valheim Easy Server - *"), carry the
// instance id and a fingerprint of the program, and are scoped to the public ports.
type GameRule struct { Instance, Program string; TCP, UDP []int }
func GameFirewallReady(r GameRule) bool
// FixFirewallAll runs today's Valheim script (byte-identical, firewallScript) followed
// by the games' lines, in one admin prompt. Long scripts use the hashed temp-file path.
func FixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error
```

The games' part of the Windows script: one `Remove-NetFirewallRule -DisplayName 'Easy Game Server - *'` (so rules of removed games and old ports go too), then per rule `netsh $fw delete rule name=all "program=<program>"` (Windows' own block rules from a dismissed pop-up; the program is private to the instance) and one `add rule "name=Easy Game Server - <instance> <proto> (<sha8>)" dir=in action=allow "program=<program>" protocol=<TCP|UDP> localport=<ports>` per protocol. Removing a game therefore needs no admin prompt: its rules go at the next fix. macOS: `socketfilterfw --add` and `--unblockapp` per program (the macOS firewall has no ports).

---

## 3. Data on disk

### 3.1 Layout

```
<data>/                              the data folder (unchanged rules, main.go resolveDataDir)
  settings.json  runtime.json  players.json  records.json  lastplayed.json  stats.json
  history.json   activity.json  modscodes.json  jetkvm.json  jetkvm-key.pem
  update.json  .instance.json  .instance.lock                     ← legacy, unchanged (1.3)
  server/  saves/worlds_local/  backups/  logs/  steamcmd/  uploads/   ← Valheim's, unchanged
  servers.json                       NEW: the instances and app-wide new settings (3.2)
  servers.json.bak                   the previous good copy
  bin/keeper-<sha8>[.exe]            keeper copies (2.11)
  games/<instance>/                  one per non-legacy instance
    server/                          installed server
    runtime/                         private Java/.NET
    saves/                           worlds and the game's own data
    backups/<world>/<stamp>_<reason>.zip
    logs/<instance>-YYYY-MM-DD_HH-MM-SS.log   (30 kept)
    uploads/
    run.json                         its run record (0600)
    keeper.json, keeper-spec.json    console games only (0600)
    players.json  records.json  lastplayed.json  history.json  stats.json   same shapes as the legacy ones
```

The shared SteamCMD stays in `<data>/steamcmd`. `logs/app.log` stays the app's log.

### 3.2 servers.json

Written 0600 with `config.WriteFileAtomic`, after copying the current file to `servers.json.bak`. Every level keeps keys it doesn't know (each struct has `Unknown map[string]json.RawMessage` inlined by its own MarshalJSON/UnmarshalJSON), and instances this build doesn't know are kept as raw JSON, so an older build that writes the file loses nothing a newer one put there.

```json
{
  "schema": 1,
  "app": {
    "stars": ["valheim", "minecraft"],
    "activeOpen": true,
    "inactiveOpen": false,
    "disabledGames": [],
    "routerPorts": true
  },
  "instances": [
    {
      "id": "valheim", "game": "valheim", "legacy": true, "added": "2026-12-01T18:00:00Z",
      "discord": {"webhook": "https://discord.com/api/webhooks/…"}
    },
    {
      "id": "minecraft", "game": "minecraft", "added": "2026-12-03T19:12:00Z",
      "settings": {"serverName": "Friends' Minecraft", "maxPlayers": "10", "allowList": "auto", "updateOnStart": "true"},
      "world": "Riverbend",
      "worlds": {"Riverbend": {"difficulty": "normal", "gamemode": "survival"}},
      "ports": {"game": 25565, "rcon": 25575},
      "portsChosen": [],
      "state": {"eula": "2026-12-03T19:12:00Z"},
      "autoStopMinutes": 60,
      "discord": {"on": true, "webhook": "https://discord.com/api/webhooks/…", "stopped": true, "daily": true, "password": false}
    }
  ]
}
```

```go
package servers

const Schema = 1

type File struct {
	Schema    int        `json:"schema"`
	App       App        `json:"app"`
	Instances []Instance `json:"instances"`
	Unknown   map[string]json.RawMessage `json:"-"`
}
type App struct {
	Stars         []string `json:"stars"`
	ActiveOpen    *bool    `json:"activeOpen,omitempty"`   // nil = open
	InactiveOpen  *bool    `json:"inactiveOpen,omitempty"` // nil = closed
	DisabledGames []string `json:"disabledGames,omitempty"`
	RouterPorts   *bool    `json:"routerPorts,omitempty"`  // nil = on
	Unknown       map[string]json.RawMessage `json:"-"`
}
type Instance struct {
	ID              string                 `json:"id"`
	Game            string                 `json:"game"`
	Legacy          bool                   `json:"legacy,omitempty"`
	Added           time.Time              `json:"added"`
	Hidden          bool                   `json:"hidden,omitempty"` // legacy only: "removed" from the panel
	Settings        game.Values            `json:"settings,omitempty"`
	World           string                 `json:"world,omitempty"`
	Worlds          map[string]game.Values `json:"worlds,omitempty"`
	Ports           map[string]int         `json:"ports,omitempty"`
	PortsChosen     []string               `json:"portsChosen,omitempty"`
	State           game.Values            `json:"state,omitempty"`
	AutoStopMinutes *int                   `json:"autoStopMinutes,omitempty"` // nil = 60 (R3); 0 = off
	Discord         *Discord               `json:"discord,omitempty"`
	Unknown         map[string]json.RawMessage `json:"-"`
}
type Discord struct {
	On       bool   `json:"on"`
	Webhook  string `json:"webhook,omitempty"` // "" = the main channel (settings.json discordWebhook)
	Stopped  bool   `json:"stopped"`
	Daily    bool   `json:"daily"`
	Password bool   `json:"password"`
}

type Status int
const (OK Status = iota; Missing; Damaged; Newer)

func Load(path string) (File, Status, error)
func Update(path string, change func(*File) error) error // load, change, back up, write; refuses unless OK
```

For the legacy instance only `discord.webhook` is used (Valheim's own channel); on/stopped/daily/password stay settings.json's `discord`, `discordStopped`, `discordDaily`, `discordPassword`. Its settings and worlds stay in settings.json; its `ports` come from settings.json `port` (game) and `port+1` (query) and are never written here.

### 3.3 Run records

`game.Record` (2.2). For the legacy instance it is runtime.json, marshalled so the file is key-for-key what bed004b writes: the generic fields, then `Extra` inlined (`played`, `noCrossplay`, `modsCode`, `codeMods`), never `game`/`instance`/`ports`/`secrets`/`keeper`. `TestRuntimeJSONCompat` decodes a new file with a copy of bed004b's `runInfo` struct and the other way round, and compares values.

### 3.4 Migration

All of it is O(1) and runs in `manager.New` (I5).

- **R2, first start:** servers.json `Missing` → create `{schema:1, app:{stars:[<legacy id>]}, instances:[{id, game, legacy:true, added:now}]}`. Nothing else is read or written. Valheim's files are untouched.
- **R3, first start:** nothing is written. Defaults come from nil fields: `autoStopMinutes` nil → 60 for every instance (Valheim's auto-stop is switched on here, where the panel shows it; R2 ignored the field, carryover fix 10), `activeOpen`/`inactiveOpen`/`routerPorts` nil → defaults.
- **Damaged** (invalid JSON) or **Newer** (`schema` > 1): **Valheim-only safe mode** (8.3). The file is never overwritten in that state.
- **Missing but `games/` exists** (servers.json deleted by hand): safe mode too, with "Restore the list of games" offering `servers.json.bak`.

### 3.5 Records, players, stats per instance

| | Legacy instance | Other instances |
|---|---|---|
| players | players.json | games/<id>/players.json |
| records | records.json, keys = log base names (`server-….log`) | games/<id>/records.json (`<id>-….log`) |
| reminders | lastplayed.json | games/<id>/lastplayed.json |
| samples | history.json (machine + Valheim, as today) | games/<id>/history.json (its CPU, memory, players) |
| by players | stats.json | games/<id>/stats.json |
| activity | activity.json, entries get `"game"` | same file |

`history.json` keeps the machine series (`cpuSys`, `memSys`, `gpu`, `net*`) and Valheim's `cpu`/`mem`/`players`/`running`, so an older build's charts stay right. The Overview's machine chart reads it; each game's Stats tab reads its own.

### 3.6 Rollback matrix

The automatic rollback goes one step back: to the build in `exe.old`. Each row is tested in CI (7.3) as "the newer build ran, the older build starts on the same data folder".

| From → to | What the older build finds | What happens | OK because |
|---|---|---|---|
| R1 → bed004b | nothing new (R1 adds only tests, constants and CI) | identical | — |
| R2 → R1 | servers.json, activity entries with `game`, jobs with `game` in update-time status | ignores them; adopts Valheim from runtime.json (same shape) | I2, I4; unknown JSON keys are ignored by `json.Unmarshal` |
| R3 → R2 | other games running (games/*/run.json), keepers, per-game files, stars, Valheim's own channel, Valheim auto-stop on, multi-command waker, router mappings, new firewall rules | R2 lists them as foreign: keeps the computer awake and blocks idle sleep while their processes live, shows "Minecraft is running, started by a newer version of the app; this version can't control it", never touches their files; posts Valheim in the main channel; doesn't auto-stop Valheim; reinstalls its own single-/valheim waker 90 s after start (and until then starts Valheim only for wake packets without a game or for the legacy id, 6.4); doesn't renew router leases (they lapse after 2 h); leaves `Easy Game Server - *` firewall rules alone | servers.json passthrough (3.2), foreign-instance awareness and the wake-packet game check are in R2 (8.1) |
| R3.x → R3.y | a game marked ready in x may be preview or hidden in y | hidden → foreign (as above); preview → usable with the label | stages are per build; data unchanged |
| Manual install of R1 or bed004b over R3 data | as R3 → R2, but no awareness: other games keep running unseen, and idle sleep may cut their players off | the owner only gets here by downloading an old build by hand | not supported; noted in docs/dev |

Things no build ever does, so no row needs them: delete or rename a legacy file, write other games' data into legacy files (I8), add a key to settings.json (I3), change runtime.json's shape (I4), rename the JetKVM files (1.2).

---

## 4. Manager

### 4.1 Instances

```go
// internal/manager/instance.go
type instance struct {
	id      string
	g       game.Game
	info    game.Info
	srv     game.Server  // nil for a foreign instance
	dirs    game.Dirs
	legacy  bool
	foreign string       // why this build doesn't run it ("" = it does)

	run        *serverRun
	job        *Job
	jobCancel  context.CancelFunc
	loadWait   *Job
	lastExit   *ExitInfo
	crashes    []time.Time
	known      map[string]*KnownPlayer
	lastPlayed *played
	recs       recordsData
	logs       *ring[LogLine]    // 3000 lines, made at the first run
	uploads    map[string]*upload
	localCands []worlds.Candidate
	history    *stats.History    // nil for the legacy instance (it uses Manager.history)
	byPlayers  *stats.ByPlayers
	emptySince time.Time         // auto-stop clock (4.5)
	ask        askState          // Windrose rule (4.6)
	cache      instCache         // installed, build, firewall: refreshed by checksLoop, read by Status
}

type serverRun struct {           // today's serverRun, generalised
	proc        *platform.Proc
	rec         game.Record      // was info runInfo
	mon         game.Monitor     // was tracker logparse.Tracker
	console     *keeper.Client   // nil without a keeper
	stopping, forced, ready, replaying bool
	readyAt     time.Time
	done, readyCh chan struct{}
	cancelTail  context.CancelFunc
	cpuPrev     time.Duration
	cpuPrevAt   time.Time
	memNow      uint64
	discordAt   time.Time
	discordErr  string
	events      []records.Event
	afterPending bool            // Afterer still running: the online post waits
	probeFails  int
	mappings    []routerports.Mapping
}
```

Manager keeps app-wide things: settings (settings.json), servers (servers.json), `instances map[string]*instance` + `order []string`, `legacyID`, the app job slot, `jobSeq` (one sequence for every job, so ids never repeat across instances: the old panel tells jobs apart by id), the activity ring, `history`/`byPlayers` (machine + Valheim), the Discord queues, network/checks caches, the app update, the waker, `machineIdleSince` (was `idleSince`), `keepAwakeUntil`, `wakeHold`, `resumeSet []string`, `wakeAsked time.Time`, `wakeTags map[string]string`.

Tests reach the legacy instance with `m.legacy()`. Existing tests are updated mechanically (`m.run` → `m.legacy().run`, `m.job` → `m.legacy().job`, `run.info` → `run.rec`, `run.tracker` → the Valheim monitor through `run.mon.(*valheim.Monitor)`); their assertions don't change.

**Public methods.** Every action takes an instance id. The old names stay as legacy wrappers (old routes, tray, main.go's `serverStopper`):

```go
type StartOptions struct {
	Memory string // "" check memory; "anyway"; "stop:<instance>" stop that one first
	Source string // "panel", "discord", "wake", "app", "restart", "resume"
}
func (m *Manager) StartGame(id string, o StartOptions) error
func (m *Manager) StopGame(id string) error
func (m *Manager) RestartGame(id string) error
func (m *Manager) ForceStopGame(id string) error
func (m *Manager) InstallGame(id string, repair bool) error
func (m *Manager) CancelGameJob(id string) error
func (m *Manager) StopAll(reason string) error          // the tray, "stop all and close"

func (m *Manager) StartServer() error    { return m.StartGame(m.legacyID, StartOptions{Memory: "anyway", Source: "panel"}) }
func (m *Manager) StopServer() error     { return m.StopGame(m.legacyID) }
// … RestartServer, ForceStop, InstallServer, CancelJob likewise.
```

Every other per-game method (worlds, uploads, lists, players, records, stats, logs, settings) gets a `…Game(id, …)` form; the old method names call it with `legacyID`.

### 4.2 Jobs

- **One job slot per instance plus one app slot.** An instance action is refused with `BusyError` while that instance's job runs or the app job runs. The app job runs Sleep, the waker install/remove, removing a game, and firewall fixing; it is refused while any instance job runs (as Sleep is today).
- `Job` gains `Game string json:"game,omitempty"` (the instance id).
- `Status.job` (legacy, top level) shows the app job while it runs, else the legacy instance's latest job: the old panel's Sleep and Valheim buttons keep working. `Status.jobs` lists every unfinished job and each instance's last finished one.
- `idleSince` resets move to the right clock: a job resets `machineIdleSince` (nothing sleeps during a download or backup) and its own instance's `emptySince`.
- `busyLocked` (app update) is true while any job in any slot runs or any upload is younger than 2 h.
- `m.app.updating` still blocks every new job in every slot.

### 4.3 Starting a game

`StartGame(id, o)`:

1. refuse if foreign, disabled, not supported on this OS, not installed, already running, or busy;
2. `Validate` (field errors as today);
3. **memory check** unless `o.Memory == "anyway"` or `Source == "restart"`: `need = info.MemoryMB`, `free = machine total − in use` from the latest sample. If `need > free`, fail with
   ```go
   &ActionError{Msg: "There may not be enough memory to start Palworld: it needs about 10 GB and 6.2 GB is free.",
       Action: "memory", Data: MemoryShort{NeedMB: 10240, FreeMB: 6349, Running: []RunningGame{{ID: "valheim", Name: "Valheim", MemMB: 3170}}}}
   ```
   (`ActionError` gains `Data any`, written as `"data"` by `web.fail`). `"stop:<id>"` stops that instance first (inside this job), then starts;
4. job "start": if `FindByExe(srv.Program())` finds it already running (started before this app, say), adopt it ("The server was already running.", as today); else `Plan` (the adapter updates first when `updateOnStart` is on, and says when Cancel stops working); the ports check (2.5; the legacy instance keeps today's check inside its Plan: `UDPPortInUse(port)` with today's message); launch (2.4); attach; wait for ready (same waits as today: `readyWait + 5 min` → "running but hasn't said it's ready");
5. after ready: open the router ports when `Join.Router` (6.5); the Discord online post (6.3).

`AutoRestart` after a crash, `StartWithApp`, and the resume after waking use the same path. Crash budget (3 in 15 minutes) and the restart delay are per instance.

### 4.4 Adoption

At `Start()`, for each non-foreign instance, in order, the same algorithm as today's `adoptRunning`:

1. load its run record (legacy: runtime.json; others: games/<id>/run.json);
2. `OpenProc(rec.PID)`, start time within ±2 s → adopt with the record; else delete the record;
3. else `FindByExe(srv.Program())` → adopt the first match with a record rebuilt from the instance's saved settings (the legacy instance exactly as today). Programs are private per instance (2.3), so a match is this instance's server;
4. on adoption: `Watch(rec)` and replay the log (`replaying`, as today); `keeper.Dial(rec.Keeper)` for console games; re-map router ports; the online post is sent only if the record says it wasn't.

Foreign instances are never adopted. Their `run.json` is checked every minute (PID alive and start time) for 4.7's awareness.

### 4.5 Auto-stop (1 hour, all games)

- Setting: `instances[].autoStopMinutes` (nil = 60, 0 = off), on each game's Settings tab: "Stop when nobody has played for [60] minutes".
- Clock: `emptySince` is reset whenever the game becomes ready, anyone joins, the count is above 0 (log, probe or `Hold`), or a job of that instance runs. Each sample tick, a ready game with presence `names` or `count`, count 0, no job, and `now − emptySince ≥ minutes` is stopped through its stop chain as job "stop", with activity "Minecraft stopped: nobody played for an hour." and the Discord stopped post reason `stopIdle` ("🔴 **X** has stopped: nobody played for an hour. Start it again with /minecraft or from the control panel." — the /command part only when it's starred and the waker is on).
- The game's Home shows "Stops at 21:40 if nobody joins".
- Presence `none`: 4.6.
- R2 doesn't auto-stop (the field is ignored); R3 does, for Valheim too.

### 4.6 Windrose's Discord rule

Used only by a game whose presence is `none` (Windrose, if its real-server check finds no usable signal; 5.11).

- 4 hours after ready (and 4 hours after each reset) the app posts in the game's channel (its own, else the main one): "⏳ **{server}** has been on for 4 hours. Should it stay on? Send any message in this channel in the next 2 hours to keep it on. Otherwise it will be turned off." The post is made with `?wait=true` so its message id is known.
- While the game runs, the app reads the channel every 2 minutes with the waker's bot token (`settings.json discordBotToken`): `GET /api/v10/channels/{channel}/messages?after={last seen id}&limit=20`, `Authorization: Bot <token>`. The channel id comes from `GET <webhook>` (`discord.Webhook(ctx, client, link) (guild, channel string, err error)`, beside today's `Guild`). **Any message not from a bot or a webhook restarts the 4-hour clock** (and ends a pending question). Message content isn't needed (no privileged intent).
- 2 hours after the question with no message: post "🔴 Turning **{server}** off: nobody answered." and stop it (stop chain, reason `stopIdle`).
- The game's Home shows the clock and a **Keep it on** button (`POST /api/games/{id}/keep-on`), which restarts the 4-hour clock.
- If the channel can't be read (no bot token, or 403): the question still goes out, the panel says "The bot can't read #channel, so only Keep it on in the control panel counts" (403: "give the bot View Channel and Read Message History there"), and after 2 more hours it stops. (Question 10.1.)
- If the game has no Discord channel at all: no question; the panel shows the Keep it on button 4 hours in, and it stops after 6 hours unless pressed.

### 4.7 Sleep and waking

**When the computer sleeps by itself** (`IdleHibernate` on, after `IdleMinutes`): only when nobody is playing any game and nothing is busy:
- every non-foreign running instance has presence known and count 0 (a running presence-`none` game counts as "maybe playing" and blocks idle sleep);
- no foreign instance's process is alive;
- no job in any slot, no upload younger than 2 h, no download;
- the user isn't using the computer (as today).

`machineIdleSince` is reset by any join, count above 0, `Hold`, job, wake, or settings change; `idleDeadlineLocked` uses it. Sleep only; hibernation stays off (`HibernationOffered = false`, unchanged).

**Going to sleep** (app job "hibernate", from the button, the tray or idle): stop every running instance in parallel (4.2), post each one's stopped message (reason `stopRest`), `resumeSet = the instances that were running`, flush Discord (up to 30 s with several games), sleep. A foreign instance can't be stopped; it sleeps with the computer. Keep-awake: one `platform.KeepAwake` owner, `want = any instance running || any foreign instance alive || any job || wakeHold`, reason "A game server is running" (R3; R2 keeps "Valheim server is running").

**Waking:**
- `wokenOnPurpose` (a device woke it, or someone uses it): hold awake 15 minutes; if `StartAfterWake` and `resumeSet` isn't empty, schedule `resume` in 30 s (was 20 s).
- `resume`: if a Discord request (tagged wake packet) was heard since the wake, **do nothing** — that request starts only its own game. Otherwise start each instance of `resumeSet` in order (memory check each; one that doesn't fit is skipped with a warning), then clear `resumeSet`.
- **Wake packet with a game** (6.4): start only that game (memory check: if short, don't start and reply "memory" to the waker with what's running), cancel any pending `resume`, set `wakeAsked`. Each request starts its game once (per-game last tag, as today).
- **Tagged packet without a game** (an older waker): the legacy game, as today.
- **Untagged packet** (the KVM's own Wake-on-LAN button): if `resumeSet` isn't empty and not yet resumed → `resume` now; else just hold awake. (When the computer was asleep, the app never hears this packet: the adapter wakes it, and `wokenOnPurpose` does the resume.) Today's "untagged starts Valheim at most once a minute" goes away in R3.
- `resumeSet` is in memory only, as `resumeOnWake` is today.

### 4.8 Status stays fast

`Status()` holds `m.mu` for a snapshot of in-memory state only. Per instance, `installed`, `build`, `firewall` and `freeDisk` come from `instCache`, refreshed by `checksLoop` (every minute, and after installs/fixes). `lanAddrs` is computed once per call. The public IP lookup stays as today. `TestStatusIsQuick`: 11 instances, 3 running, Status < 5 ms.

### 4.9 The Status JSON

Top-level fields stay as they are (the legacy instance's, `TestStatusTopLevelKeys`). New fields:

```json
{
  "games": [
    {
      "id": "minecraft", "game": "minecraft", "name": "Minecraft: Java Edition", "short": "Minecraft",
      "letter": "M", "color": "#5b8731", "stage": "ready", "serverName": "Friends' Minecraft",
      "starred": true, "foreign": "", "disabled": false, "canRun": "yes", "needsText": "",
      "state": "online", "installed": true, "build": {"version": "1.21.4"},
      "world": "Riverbend", "runningWorld": "Riverbend",
      "players": [{"name": "Alex", "account": "8667ba71-…", "since": "2026-12-03T19:40:00Z"}],
      "count": 1, "maxPlayers": 10, "presence": "names",
      "join": {"how": "address", "address": "203.0.113.25", "lan": ["192.168.1.20:25565"], "port": 25565,
               "password": "", "public": false, "crossplay": false, "steps": ["…"], "hint": "…", "router": true},
      "ports": [{"role": "game", "protos": ["tcp"], "port": 25565, "public": true, "chosen": false, "router": "open"}],
      "job": null, "lastExit": null, "needsRestart": false,
      "memMB": 2380, "cpu": 12.5, "needMB": 3072,
      "autoStop": {"minutes": 60, "at": "2026-12-03T21:40:00Z"},
      "ask": null,
      "discord": {"posted": "2026-12-03T19:31:02Z", "error": ""},
      "reminder": ["update Minecraft to 1.21.4"],
      "warnings": []
    }
  ],
  "jobs": [ { "id": 1733254200123, "game": "minecraft", "kind": "start", "…": "…" } ],
  "stars": ["valheim", "minecraft"],
  "sidebar": {"activeOpen": true, "inactiveOpen": false},
  "online": 2, "playing": 3,
  "router": {"kind": "upnp", "externalIP": "203.0.113.25", "cgnat": false, "problem": ""},
  "safeMode": ""
}
```

---

## 5. Game adapters

One package per game, `internal/games/<id>`, registered by `internal/games/all/<id>.go`. Each section lists what the adapter declares and does. Ports are defaults; 2.5 picks free ones. "Stand-in" is the `cmd/fakeserver` personality used by `gametest` and the manager tests; "Real-server check" is the `realserver`-tagged test the CI matrix runs (7.4).

Every stand-in imitates its game's real behaviour as far as the app can see it: it accepts the Plan's command line and reads the config files the Plan writes; prints the ready, join, leave, death, save and stopping lines given below (in the game's format, to stdout or to its own log file); listens on the planned ports; answers its probe and its stop chain the way the real server does (a console line, RCON, REST/API, or Ctrl+C — and on Ctrl+C saves only if the real one does); keeps its worlds in the game's layout; and runs the scripted players. Each section below adds what is particular to it. Every adapter also has: unit tests of its Monitor against sample logs in `testdata/` (copied from a real server where possible; until then written from the facts below, marked `// verify` in the file), a golden test of its command line and config files, and its UI module `internal/web/ui/assets/games/<id>.js` (6.1).

Common to all: server scope `serverName` (Add), `password` when the game has one (Add), `maxPlayers`, `updateOnStart` (default on), `autoRestart` (default on); `Texts{Players: "players", Player: "player"}` unless said.

### 5.1 Valheim (`valheim`) — the legacy game

- **Info:** V, `#f2a33a`, `/valheim`, Windows yes, macOS yes, download ~1 GB (Windows) / ~2 GB (Mac), MemoryMB 4096 (verify from the owner's stats.json), 10 players, presence names, deaths yes, ports game UDP 2456 + query UDP `game+1`, password required (Valheim's rules), import yes, lists yes, mods yes, `Legacy: true`, stage ready. `Texts{Players: "vikings", Player: "viking"}`.
- **Everything as today**, moved from `internal/manager` into `internal/games/valheim` without changing behaviour (table in 2.2): SteamCMD app 896660; `config.ServerArgs`; `SteamAppId=892970`; the Mac DYLD path; BepInEx mods and their Doorstop environment; the r2modman/Gale mods code (`Afterer`); `logparse` (`Monitor` wraps `Tracker`, `Parse`, `IsNoise`; PeerCount and join-code counts become `count` events with `Hold`; `Tick` does the 10-minute Steam-login fallback); Ctrl+C stop (resend 90 s, give up 4 min); `exitHint`; `worlds.Library`; the three permission lists; world options in settings.json. Output `LogArg`, log `logs/server-YYYY-MM-DD_HH-MM-SS.log`. `Program()` = `paths.ServerExe`.
- **`LegacyStore`** (exported for the manager): `type LegacyStore interface { Settings() config.Settings; Paths() config.Paths; Save(config.Settings) error }`. The manager implements it over its settings.json handling (`saveSettingsLocked`, so the Library and installer folders follow folder changes exactly as today) and passes it in `Env.Legacy`. The adapter's `game.Store` maps values to and from it (2.9). The adapter never keeps folders of its own: it asks `Paths()` each time, because ServerDir/SaveDir can change (with the server stopped and no job running, as today), and the manager refreshes the instance's `dirs` after every save.
- **Join:** `How` "code" with crossplay on (code from the log; `Problem` "crossplay"/"late" as today, `codeWait` 3 min), else "address" (the session line's public address, else public IP:port). `Router` = crossplay off. `Steps` and `Hint` are today's texts ("In Valheim, go to Join Game and join by code." / "…add the server by its address.").
- **R3 changes only:** router ports when crossplay is off (6.5); its own Discord channel (servers.json `discord.webhook`); auto-stop on (4.5); untagged wake packets no longer start it (4.7).
- **Stand-in:** `cmd/fakevalheim`, unchanged (the manager tests' `TestMain` keeps building it).
- **Real-server check:** the existing `TestRealServer` (internal/manager, Windows and Intel Mac; `internal/games/valheim/realserver.json` points the matrix at `./internal/manager/`), run through the refactored manager; plus: crossplay off maps router ports when the runner's network has a router that answers (skipped otherwise).

### 5.2 Minecraft: Java Edition (`minecraft`)

- **Info:** M, `#5b8731`, `/minecraft`, Windows yes, macOS yes, download ~60 MB + Java ~50 MB, MemoryMB 3072, 10 players (setting), presence names, deaths yes (verify), ports game TCP 25565 (public) + rcon TCP 25575 (private), password none (`NoneText`: "Minecraft uses an allow list instead of a password: friends are added on the Players tab."), import yes, lists yes, mods no (later round).
- **Install:** `GET https://piston-meta.mojang.com/mc/game/version_manifest_v2.json` → `latest.release` → that version's JSON → `downloads.server.url` + `sha1` → `server/server.jar`; `javaVersion.majorVersion` → `install.Temurin(major, runtime/)` from `https://api.adoptium.net/v3/binary/latest/{major}/ga/{windows|mac}/{x64|aarch64}/jre/hotspot/normal/eclipse`, checksum from `/v3/assets/latest/{major}/hotspot`. `WriteBuild{Version}`. Releases only, never snapshots. verify: the Java major the newest Minecraft needs runs on macOS 12 (the owner's Mac); if not, the Mac Pro can't run the newest server, and Add a game must say so (Mac checklist item).
- **EULA:** Add a game shows "I agree to the Minecraft EULA" with the link https://aka.ms/MinecraftEULA; the instance can't start until it's ticked (`state.eula` = when). `eula.txt` gets `eula=true` only then.
- **Settings:** server: `serverName` (the MOTD), `maxPlayers` 10, `allowList` `auto|on|off` (default auto), `pvp` on, `viewDistance` 10 (advanced), `memoryMB` 2048 (advanced, `-Xmx`). World: `difficulty` peaceful/easy/normal/hard (normal), `gamemode` survival/creative/adventure (survival). New world: `seed`, `levelType` normal/flat/large_biomes/amplified, `hardcore`.
- **Plan:** write `server/server.properties`, keeping keys it doesn't set: `server-port`, `motd`, `max-players`, `difficulty`, `gamemode`, `pvp`, `view-distance`, `white-list`/`enforce-whitelist` (from the allow list state), `enable-rcon=true`, `rcon.port`, `rcon.password` (new random per run, in `Record.Secrets`), `online-mode=true`, `enable-query=false`; for a new world `level-seed`, `level-type`, `hardcore`. Command: `<runtime>/bin/java -Xms512M -Xmx<memoryMB>M -jar server/server.jar --nogui --universe <saves> --world <world> --port <game>` (verify `--universe`/`--world`), Dir `server/`, Output `Capture`.
- **Monitor** (lines `[HH:MM:SS] [Server thread/INFO]: …`, verify the pattern): ready `Done (…s)! For help, type "help"`; version `Starting minecraft server version X`; account `UUID of player <name> is <uuid>`; join `<name> joined the game`; leave `<name> left the game`; death = a Server-thread INFO line that starts with the name of a player who is on, followed by one of the vanilla death messages (the list from the server's `en_us` language file: "was slain by", "fell from a high place", "drowned", "burned to death", "tried to swim in lava", "blew up", "was shot by", "starved to death", "suffocated in a wall", "froze to death", "withered away", "hit the ground too hard", "died", …; verify against the current version), never a chat line (`<name> …`) or advancement; saved `Saved the game` / `All dimensions are saved` (verify); stopping `Stopping server`; turned away `… You are not white-listed on this server!` with the name (verify the line) → event `turned_away`, shown in the feed with an **Allow** button.
- **Probe:** RCON `list` → `There are N of a max of M players online: A, B`. (Changed: RCON instead of round one's management protocol over websocket: one small client also serves Project Zomboid, and RCON covers `list`, `stop` and the allow list.)
- **Stop:** rcon `stop` (Wait 3 min) → interrupt (Wait 2 min; the JVM's shutdown hook saves).
- **Allow list (decision):** `auto` turns it on once the game is reachable from the internet (its router mapping is open and there's no carrier-grade NAT; 6.5): first every known player is added, then `whitelist on`; it stays on after that (`state.allowListOn`). `on`/`off` are fixed. Friends are added on the Players tab, or with **Allow** on a turned-away entry: running → RCON `whitelist add <name>`; stopped → `whitelist.json` with the UUID from the players list or `https://api.mojang.com/users/profiles/minecraft/<name>` (verify).
- **Lists:** allowed (`whitelist.json`), admins (`ops.json`), banned (`banned-players.json`), all live through RCON while running (`whitelist`, `op`/`deop`, `ban`/`pardon`).
- **Worlds:** worldstore, `folder` in `saves/` (a world is a folder with `level.dat`); Detect: folders holding `level.dat`; Local: `%APPDATA%\.minecraft\saves`, `~/Library/Application Support/minecraft/saves`.
- **Join:** "address": the public IP (`:port` only when not 25565); Steps "In Minecraft: Multiplayer → Add Server, type the address." and "Use Minecraft X, the server's version."; `Router` true.
- **Exit hints:** "Failed to bind to port" → port in use; "You need to agree to the EULA" → tick it; `UnsupportedClassVersionError` → Repair (installs the right Java); "Exception reading … level.dat" → restore a backup.
- **Stand-in:** reads `server.properties`; listens on the game port (TCP) and the RCON port (answers `list`, `stop`, `save-all`, `whitelist …`); writes the lines above; makes `<universe>/<world>/level.dat`; saves on `stop` and on SIGINT/Ctrl+C; `deny:Bob@3` in the script prints a turned-away line.
- **Real-server check:** Windows and Intel Mac: install (jar + Java), accept the EULA, start a new world, ready, RCON `list` = 0, stop (level.dat newer), start again on the same world, Join has the address.

### 5.3 Terraria (`terraria`)

- **Info:** T, `#2c7fb8`, `/terraria`, Windows yes, macOS intel, download ~40 MB, MemoryMB 1536, 8 players, presence names, deaths no (verify: whether the server console prints deaths; turn on if it does), port game TCP 7777, password optional, import yes, lists no.
- **Install:** `GET https://terraria.org/api/get/dedicated-servers-names` → newest name (e.g. `terraria-server-1449.zip`) → `GET https://terraria.org/api/download/pc-dedicated-server/<name>` (verify both); unzip into `server/<version>/` (the zip has `Windows/`, `Mac/`, `Linux/`; exec bits aren't stored, so set them on the Mac files). `WriteBuild{Version: "1.4.4.9"}`. Windows prerequisite: **XNA Framework 4.0 Redistributable** (`install.EnsurePrereq`: registry check `HKLM\SOFTWARE\WOW6432Node\Microsoft\XNA\Framework\v4.0` `Installed`=1, msi from Microsoft, `msiexec /i … /quiet`, one admin prompt; verify key and URL). .NET Framework 4 comes with Windows 10/11.
- **Program:** Windows `server/<v>/Windows/TerrariaServer.exe`; Mac the MonoKickstart binary inside `Mac/Terraria Server.app/Contents/MacOS/` (verify its name; launch the binary, not the shell script). On macOS the working folder becomes the bundle's Resources, so every path in the config is absolute.
- **Settings:** server: `serverName` (motd), `password`, `maxPlayers` 8. New world: `size` small/medium/large (autocreate 1/2/3), `difficulty` classic/expert/master/journey (verify the numbers), `evil` random/corruption/crimson, `seed`.
- **Plan:** `server/serverconfig.txt`: `world=<saves>/<world>.wld`, `worldpath=<saves>`, `port`, `maxplayers`, `password`, `motd`, `upnp=0` (the app maps the router itself; verify the key), `secure=1`, `language=en-US`; for a new world `autocreate`, `worldname`, `difficulty`, `seed`. Args `-config <abs path>`. **Console: true** (the keeper; it also captures stdout into the run's log).
- **Monitor:** version from the banner `Terraria Server v1.4.4.9`; ready `Server started` (verify); join `<name> has joined.`; leave `<name> has left.`; saved `Saving world data: 100%` / `Validating world save` (verify); stopping on `exit`.
- **Stop:** console `exit` (Wait 3 min) → interrupt (Wait 1 min, Say "Terraria doesn't save on Ctrl+C: anything since its last save is lost.").
- **Worlds:** worldstore `files`, Ext `.wld`, `.wld.bak`; Local: `Documents\My Games\Terraria\Worlds` (`KnownFolder("documents")`, which OneDrive may move), `~/Library/Application Support/Terraria/Worlds`. Rename: `ErrCantRename` (the name inside the file would differ).
- **Join:** "address"; Steps "In Terraria: Multiplayer → Join via IP, type the address, then the port." and "Use Terraria X: it must be the same version as the server."; `Router` true. Reminder "update Terraria to X" matters here (exact version match).
- **Exit hints:** "already in use" → port; an XNA exception → Repair (installs XNA).
- **Stand-in:** reads `-config`; listens on the port; prints the banner and lines; `exit` on stdin → saving lines, writes the `.wld`, exits 0; Ctrl+C → exits without saving.
- **Real-server check:** Windows (the runner is an admin, so XNA installs silently) and Intel Mac: install, autocreate a small world, ready, `exit` saves (`.wld` exists, newer), start again with it, a takeover in between (restart the manager while it runs, then stop through the keeper).

### 5.4 Necesse (`necesse`)

- **Info:** N, `#8e44ad`, `/necesse`, Windows yes, macOS intel (verify a Mac depot exists for app 1169370 and runs on macOS 12), download ~0.3 GB (verify), MemoryMB 2048, 10 players, presence names (verify the lines), deaths no (verify), port game UDP 14159, password optional, import yes, lists no.
- **Install:** SteamCMD app 1169370, anonymous. The server comes with its own Java (`jre/`; verify the path per OS): `Program()` is that java.
- **Settings:** server: `serverName` (motd), `password`, `maxPlayers` 10 (`-slots`). New world: `seed` (verify the option).
- **Plan:** `<server>/jre/bin/java -Xmx<mem>M -jar Server.jar -nogui -world <world> -port <game> -slots <n> -password <pw> -motd <name>` plus whatever makes it keep its data under `Dirs.Saves` (verify: a `-localdir`-style flag; if there is none, set the data folder variable it reads, `APPDATA` on Windows, to `Dirs.Saves` for this process only, and on macOS the equivalent, verify). **Console: true** (`stop`, verify; `save`, `players`).
- **Monitor:** verify every line against a real log: ready, join, leave, save, stopping, version.
- **Stop:** console `stop` (verify; Wait 3 min) → interrupt (Wait 2 min; verify whether Ctrl+C saves; if it does, keep it second).
- **Worlds:** worldstore under `saves/…/saves` (verify whether a world is a folder or a `.zip`); Local `%APPDATA%\Necesse\saves`, `~/Library/Application Support/Necesse/saves` (verify).
- **Join:** "address"; Steps "In Necesse: Multiplayer → Join server, type the address and port."; `Router` true.
- **Stand-in:** started as the bundled java with `-jar Server.jar -nogui …`; reads `stop`, `save` and `players` on stdin; writes its world where the Plan's data-folder choice puts it.
- **Real-server check:** Windows and Intel Mac: install, new world, ready, stop through the console saves, restart loads it, takeover then stop.

### 5.5 Vintage Story (`vintagestory`)

- **Info:** VS, `#8a6d3b`, `/vintagestory`, Windows yes, macOS no ("Needs Windows": the Mac server is only inside the 600 MB client), download ~60 MB + .NET ~35 MB, MemoryMB 3072, 16 players, presence names, deaths no (verify), port game TCP+UDP 42420, password optional, import yes, lists no. Players must own the game.
- **Install:** version from `https://api.vintagestory.at/stable.json` (verify the shape; `lateststable.txt` as fallback); `https://cdn.vintagestory.at/gamefiles/stable/vs_server_win-x64_<v>.zip` (verify) unzipped into a fresh `server/<v>/`; never pre-releases. .NET: the version in `VintagestoryServer.runtimeconfig.json` (`runtimeOptions.framework.version`) → `install.DotNet` (portable zip via `https://builds.dotnet.microsoft.com/dotnet/release-metadata/<channel>/releases.json`, verify) into `runtime/`. `Program()` = `runtime/dotnet.exe`.
- **Plan:** `runtime/dotnet.exe server/<v>/VintagestoryServer.dll --dataPath <saves>`. Before the first start the app writes `saves/serverconfig.json` with what it sets (later it keeps every key it doesn't set): `ServerName`, `Port`, `MaxClients`, `Password`, `WhitelistMode` (verify the values; off when a password is set), `AdvertiseServer: false`, `WorldConfig.WorldName`, `SaveFileLocation` = `<saves>/Saves/<world>.vcdbs`, `Seed`, `PlayStyle`. **Console: true** (`/stop`).
- **Monitor:** ready `Dedicated Server now running on Port` ; join `… joins.`; leave `Player … left.` or `Player … got removed. Reason: …`; saved `World saved!` (verify); done `Stopped the server!`.
- **Stop:** console `/stop` (Wait 3 min) → interrupt (Wait 2 min, verify whether it saves).
- **Worlds:** worldstore `files`, Ext `.vcdbs`, in `saves/Saves/`; Local `%APPDATA%\VintagestoryData\Saves` (verify).
- **Join:** "address"; Steps "In Vintage Story: Multiplayer → Add server."; `Router` true (UPnP often isn't enough, 6.5 shows the manual steps).
- **Stand-in:** started as `dotnet VintagestoryServer.dll --dataPath …`; reads `serverconfig.json` (port, world file); `/stop` on stdin saves and exits; writes `Saves/<world>.vcdbs`.
- **Real-server check:** Windows: install with .NET, new world, ready, `/stop` saves, restart, takeover then stop.

### 5.6 Project Zomboid (`zomboid`)

- **Info:** PZ, `#b03a2e`, `/zomboid`, Windows yes, macOS no, download ~4 GB (verify), MemoryMB 8192, 16 players, presence names (RCON), deaths no (verify a log line), ports game UDP 16261 + direct UDP `game+1` (both public) + rcon TCP 27015 (private), password optional, import no (single-player saves aren't multiplayer saves; verify), lists no.
- **Install:** SteamCMD app 380870, anonymous, branch per question 10.3 (default: public). `Program()` = the bundled `jre64/bin/java.exe` (verify).
- **Plan:** launch the bundled Java directly with the `vmArgs`, classpath and main class (`zombie.network.GameServer`) read from `ProjectZomboid64.json` (verify), plus `-Xmx<memoryMB>m`, `-cachedir=<saves>` (keeps its whole `Zomboid` folder under the instance; verify) and `-servername <world> -adminusername admin -adminpassword <secret> -port <game> -udpport <direct>` (verify). The app edits `<saves>/Server/<world>.ini` (`PublicName`, `Password`, `MaxPlayers`, `DefaultPort`, `UDPPort`, `RCONPort`, `RCONPassword` = a secret, `Public=false`), keeping other keys. Output `Capture`.
- **Probe:** RCON `players` → `Players connected (N):` + `-name` lines.
- **Monitor:** ready (verify, e.g. `*** SERVER STARTED ***`); joins/leaves from the probe (and log lines once verified).
- **Stop:** rcon `quit` (Wait 4 min) → interrupt (Wait 2 min).
- **Worlds:** a world is the server name: `saves/Saves/Multiplayer/<world>/`, `saves/db/<world>.db`, `saves/Server/<world>*` (`Format.Files`).
- **Join:** "address" with port and password; Steps "In Project Zomboid: Join → add the server's IP, port, a name for you, and the password."; `Router` true.
- **Stand-in:** started as java with the GameServer arguments; reads `Server/<world>.ini` (ports, RCON password); listens on both UDP ports and the RCON port; RCON `players` lists the scripted players, `quit` saves and exits.
- **Real-server check:** Windows: install, start, ready, RCON players = 0, quit saves, restart.

### 5.7 Enshrouded (`enshrouded`)

- **Info:** E, `#16a085`, `/enshrouded`, Windows yes, macOS no (Windows-only server), download ~3 GB (verify), MemoryMB 6144 (verify), 16 players, presence names (verify the lines), deaths no, port query UDP 15637 (public; verify whether a separate game port is still used), password required (a "Friends" user group), import no (verify), lists no.
- **Install:** SteamCMD app 2278520, anonymous. `Program()` = `server/enshrouded_server.exe`.
- **Plan:** write `server/enshrouded_server.json` (verify the current format): `name`, `saveDirectory` = `<saves>/<world>`, `logDirectory` = `<logs>/game`, `ip` `0.0.0.0`, `queryPort`, `slotCount`, `userGroups` = one group "Friends" with the password and the usual rights. Output `Capture` if stdout has the log lines, else `GameLog` (verify).
- **Monitor:** ready `[Session] 'HostOnline' (up)!`; join `Player '<name>' logged in`; leave `Remove Entity for Player '<name>'`; saved (all verify).
- **Probe:** A2S on the query port (count; verify it answers).
- **Stop:** interrupt (Wait 4 min, Resend 90 s; verify it saves on Ctrl+C).
- **Worlds:** a world is a save folder `saves/<world>/`.
- **Join:** "address" (or find it by name in the in-game list; verify); `Router` true.
- **Stand-in:** reads `enshrouded_server.json`; answers A2S on the query port with the scripted player count; saves into `saveDirectory` on Ctrl+C.
- **Real-server check:** Windows (CI builds and tests it now, as decided): install, start, ready, A2S answers, stop, restart.

### 5.8 Core Keeper (`corekeeper`)

- **Info:** C, `#c9a227`, `/corekeeper`, Windows yes, macOS no, download ~1 GB (verify), MemoryMB 2048, 8 players, presence names (verify; see question 10.2), deaths no, ports none (friends join through Steam with the Game ID), password none (`NoneText`: "Friends join with the Game ID: share it only with them."), import yes (verify the folder), lists no.
- **Install:** SteamCMD app 1963720, anonymous. `Program()` = `server/CoreKeeperServer.exe`. Needs a working graphics device: never `-nographics`.
- **Plan:** `CoreKeeperServer.exe -batchmode -logfile <LogPath> -world <index> -worldname <world> -worldseed <seed> -maxplayers <n> -datapath <saves>` (verify each flag), Output `LogArg`.
- **Join:** "id": the Game ID from the file the server writes (`GameID.txt`, verify where), `CodeLabel` "Game ID", `Router` false. Announced like a join code: the online post waits for it.
- **Monitor / stop:** verify the join, leave and ready lines; interrupt (Wait 3 min; verify it saves, and that `CoreKeeperServer.exe` is a console program so Ctrl+C reaches it at all — a Unity player often isn't; if not, find its own quit mechanism or rely on its autosave and say so, 2.7).
- **Worlds:** `saves/worlds/<index>.world.gzip` (verify); names kept in `State` (`world.<index>`).
- **Stand-in:** writes a Game ID file a moment after it starts and the log lines to `-logfile`; opens no ports (like the real one, which goes through Steam).
- **Real-server check:** Windows (verify the runner's basic display adapter is enough): install, start, Game ID appears, stop, restart.

### 5.9 Palworld (`palworld`)

- **Info:** P, `#d6408b`, `/palworld`, Windows yes, macOS no, download ~6 GB, MemoryMB 10240, 32 players, presence names (REST), deaths no, ports game UDP 8211 (public) + rest TCP 8212 (private), password optional, import no, lists no.
- **Install:** SteamCMD app 2394010, anonymous; update on start (friends must match the server's version exactly). Windows prerequisite: VC++ 2015-2022 x64 (`HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64` `Installed`=1; `https://aka.ms/vs/17/release/vc_redist.x64.exe /install /quiet /norestart`). `Program()` = `server/Pal/Binaries/Win64/PalServer-Win64-Shipping-Cmd.exe` (verify).
- **Plan:** copy `DefaultPalWorldSettings.ini` to `Pal/Saved/Config/WindowsServer/PalWorldSettings.ini` if missing, then set in `OptionSettings=(…)`: `ServerName`, `ServerPassword`, `AdminPassword` (secret), `RESTAPIEnabled=True`, `RESTAPIPort`, `RCONEnabled=False`, `PublicPort`, `ServerPlayerMaxNum`, and the world-scope options (difficulty and rates; verify keys). `GameUserSettings.ini` `DedicatedServerName` = the selected world's GUID. Args `Pal -port=<game> -players=<n> -useperfthreads -NoAsyncLoadingThread -UseMultithreadForDS` (verify). Output `Capture`.
- **Probe:** `GET http://127.0.0.1:<rest>/v1/api/info` and `/v1/api/players`, basic auth `admin:<AdminPassword>`; ready when it answers.
- **Stop:** call `POST /v1/api/save` then `POST /v1/api/shutdown {"waittime":1,"message":"The server is stopping."}` (Wait 3 min) → interrupt (Wait 2 min).
- **Worlds:** `server/Pal/Saved/SaveGames/0/<GUID>/` folders; names in `State` (`world.<GUID>` = name); a new world: empty `DedicatedServerName`, and after its first save the new GUID is recorded (verify).
- **Join:** "address" `IP:port` and password; Steps "In Palworld: Join Multiplayer Game, type the address at the bottom."; `Router` true.
- **Stand-in:** reads `PalWorldSettings.ini`; serves the REST endpoints with basic auth on 127.0.0.1 (wrong password → 401); `save` writes the world folder, `shutdown` exits after `waittime`; creates a new GUID folder when `DedicatedServerName` is empty.
- **Real-server check:** Windows: install, start, REST info and players answer, REST save+shutdown, restart same world.

### 5.10 Satisfactory (`satisfactory`)

- **Info:** S, `#d35400`, `/satisfactory`, Windows yes, macOS no, download ~15.5 GB, MemoryMB 12288, 4 players (setting), presence count (API; names if the log has them, verify), deaths no, ports game UDP+TCP 7777 (public; the HTTPS API shares it) + reliable TCP 8888 (public), password optional (client password), import yes (single-player `.sav`, verify), lists no.
- **Install:** SteamCMD app 1690800, anonymous. `Program()` = `server/Engine/Binaries/Win64/FactoryServer-Win64-Shipping-Cmd.exe` (verify).
- **Plan:** `FactoryGame -Port=<game> -ReliablePort=<reliable> -log -unattended` (verify), Output `Capture` or `GameLog` `FactoryGame/Saved/Logs/FactoryGame.log` (verify).
- **API** (`POST https://127.0.0.1:<game>/api/v1`, `{"function": …, "data": …}`; self-signed certificate, pinned at first contact in `State`): first start `PasswordlessLogin` → `ClaimServer{ServerName, AdminPassword}` (admin password = a secret) → `SetClientPassword` → `CreateNewGame{NewGameData{SessionName: <world>}}`; later `PasswordLogin`, `QueryServerState` (players, session), `SaveGame`, `LoadGame`, `Shutdown` (verify each name and field).
- **Probe:** `QueryServerState` → count, `Up`.
- **Stop:** call `SaveGame` + `Shutdown` (Wait 3 min) → interrupt (Wait 2 min).
- **Worlds:** sessions: `.sav` files grouped by session name (verify where the server keeps them); a start loads the session's newest save; import uploads a single-player save (verify `UploadSaveGame`).
- **Join:** "address"; Steps "In Satisfactory: Server Manager → Add Server, type the address and port."; `Router` true.
- **Stand-in:** serves the HTTPS API with a self-signed certificate (unclaimed until `ClaimServer`), counts the scripted players in `QueryServerState`, writes `.sav` files on `SaveGame`, exits on `Shutdown`.
- **Real-server check: on demand only** (7.4): install, claim, new game, query, save, shutdown, restart loads it.

### 5.11 Windrose (`windrose`)

- **Info:** W, `#34495e`, `/windrose`, Windows yes, macOS no (Windows-only server), download ~35 GB, MemoryMB 12288 (about 12 GB for 4 players), 4 players (verify), presence count if the log's periodic "Connected Accounts" dump is usable, else none (decided by the real-server check, below), deaths no, ports none (the invite code works without router setup; verify), password per its config (verify), import no, lists no.
- **Install:** SteamCMD app 4129620, anonymous (verify). `Program()` = the server's shipping exe under `R5/Binaries/Win64/` (verify).
- **Plan:** its settings (name, password, max players) in `R5/ServerDescription.json` (verify keys), Output `GameLog` `R5/Saved/Logs/R5.log`.
- **Monitor:** connect/disconnect lines (`UR5CoopProxyServer::OnAccountDisconnected` and its connect twin) and the periodic "Connected Accounts" dumps (verify both); ready line (verify).
- **Join:** "code": the invite code from `R5/ServerDescription.json` (verify key), `CodeLabel` "Invite code", `Router` false.
- **Stop:** interrupt (Wait 4 min; verify it saves; no RCON, REST or console).
- **Presence decision:** the real-server check waits up to 15 minutes for a "Connected Accounts" dump. If it comes and shows a count, ship `Presence: ByCount` (auto-stop after an hour like every game). If not, ship `Presence: Unknown`, and the game uses the Discord rule (4.6). Both monitors are written; the constant is set from the check's result.
- **Stand-in:** writes `R5/Saved/Logs/R5.log` with the connect/disconnect lines and, when its script says so, the periodic "Connected Accounts" dump (so both presence monitors are tested); writes an invite code into `R5/ServerDescription.json`; saves on Ctrl+C.
- **Real-server check: on demand only.**

---

## 6. Panel, HTTP API, Discord, waker, router ports

### 6.1 Panel (R3; R2 ships the old panel unchanged)

**Shell** (`internal/web/ui/assets/app.js` and new modules; `index.html` title "Easy Game Server"; manifest `name` "Easy Game Server", `short_name` "Game Server"):

- **Routes:** `#/overview` (default), `#/game/<instance>/<tab>` with tabs `home worlds players stats mods settings log`, `#/add`, `#/add/<game>`, `#/app/discord`, `#/app/sleep`, `#/app/settings`. Old hashes (`#/home`, `#/worlds`, …) go to the legacy instance's tab.
- **Sidebar:** Overview at the top. **Active (n)**: starred games, then running unstarred ones (shown with an empty star, only while they run). **Inactive (n)**: the rest. Each list opens and closes; Active open and Inactive closed by default; the state is saved by the app (`POST /api/app/sidebar`), never in browser storage. Each row: the letter tile in the game's colour, its short name, a live player count while it runs, and a star button (`POST /api/games/{id}/star`). Then **+ Add a game**. At the bottom the APP group: **Discord**, **Sleep and waking**, **App settings**, and the footer "Server computer: NAME" (`machine.hostname`).
- **Top bar:** "N games online · M playing" (`online`, `playing`) and **Sleep** (its confirm dialog names the games it will save and stop).
- **Overview:** starred games first, as cards: state, world, how to join (code / address / Game ID, password, Copy), who's on, Start / Stop. Then running games without a star, as cards. Then a collapsible **Inactive games (n)** list with a Start button each. A **memory bar**: the computer's memory split into each running game (its colour and size), other programs, and free. The activity feed of every game.
- **A game's page:** tabs Home, Worlds, Players, Stats, Mods (only when `Info.Mods`), Settings, Log.
  - *Home:* state and Start/Stop/Restart (Force stop when offered); **How friends join** (from `join`, plus the game's module); who's on; when it stops by itself ("Stops at 21:40 if nobody joins") or Windrose's question with **Keep it on**; usage tiles; its setup checklist (installed, firewall, router ports, Java/XNA/VC++ where needed); its activity.
  - *Worlds:* worlds with backups, select, new world (the `newWorld` schema), import (upload, or "from your own game" with `Local()`), download, rename where the game can. Valheim keeps its difficulty and mods controls through its module.
  - *Players:* known players, records ("most deaths" only when `deaths`), and the lists when `Info.Lists` (Minecraft: allow list with Add, and **Allow** on turned-away entries).
  - *Stats:* the game's CPU, memory and players over time; the machine chart stays on the App settings page.
  - *Mods:* Valheim's mod checklist per world and the mods help (today's pages, moved).
  - *Settings:* the schema form (`server` scope; `Advanced` under "More settings"), ports (role, protocol, port, chosen or picked, **Pick free ports**), "Stop when nobody has played for [60] minutes" (0 = never), this game's Discord (on, own channel or main, stopped and daily posts, include the password), and **Remove this game** (keeps its worlds as backups; for Valheim it only hides it).
  - *Log:* the instance's ring and the download of its current log.
- **Memory warning** (on `action: "memory"`): "Palworld needs about 10 GB of memory and 6.2 GB is free." One button per running game "Stop Valheim (3.1 GB) and start", then "Start anyway" and "Cancel". They re-send the start with `memory: "stop:<id>"` / `"anyway"`.
- **Add a game:** every registered game that isn't hidden, as a card: letter tile, name, "Runs on this computer" or greyed with its reason ("Needs Windows", "Needs Rosetta 2"), download size, memory needed, what friends need, "Preview" when in preview, "Added" when it is. Choosing one asks the `Add: true` fields (server name; password with its rule, or the game's `NoneText`), Minecraft's EULA box, and the world: **New** (name + new-world fields) or **From your own game** (`GET /api/catalog/{game}/local`). Then it's added (starred), installs (with its runtime and prerequisites) and shows on its page.
- **Rename:** every visible "Valheim Easy Server" becomes "Easy Game Server" (title, manifest, texts, dialogs, tray); identity names don't change (1.2). Each game's server name is changed on its Settings tab; worlds are renamed on Worlds where the game allows it.
- **Game modules:** `internal/web/ui/assets/games/<id>.js`, loaded by the shell with `import()` for each game in the catalog (same origin, so the CSP allows it), so no stream edits a shared list:

```js
export default {
  id: 'minecraft',
  joinExtras(game, h) { return null; },        // extra nodes in How friends join, or null
  logClass(text) { return null; },             // 'join' | 'leave' | 'save' | 'warn' | null
  feedIcons: { turned_away: 'user-x' },         // feed kind → icon name
  worldsHelp: 'Your worlds are in …',           // where to find worlds on PC/Mac
  uploadAccept: '.zip',
  worldCard(world, game, h) { return null; },   // extra controls on a world card (Valheim: difficulty, mods)
  tabs: {},                                     // replacement tab modules by tab id (Valheim: mods)
};
```

All page state is keyed by instance (a reused tab never shows another game's data); a game's log cursor is per instance.

### 6.2 HTTP API

Every new route sits under `/api/` (so `guard` applies: same-origin and `X-VES` on writes); GETs have no side effects. `{id}` is an instance id. Errors use today's shape `{error, fields?, action?, data?}` with 400 (409 for `BusyError`). Field errors from new routes are keyed `server.<key>`, `world.<key>`, `newWorld.<key>`.

**Unchanged** (legacy instance, same JSON): every route in api.go:31-83. `GET /api/status` gains the fields in 4.9.

**New:**

| Route | Body → answer |
|---|---|
| `GET /api/catalog` | `[{id, name, short, letter, color, stage, canRun ("yes", "intel" or "no"), needsText, downloadMB, memoryMB, maxPlayers, friendsNeed, password: PasswordRule, schema: [Field…] (Add fields), ports: [PortRole…], import, eula?, added}]` |
| `GET /api/catalog/{game}/local` | `{candidates: [worlds.Candidate…]}` from the owner's own game |
| `POST /api/games` | `{game, serverName, password, eula, world: {mode, name, values, candidate}}` (mode "new" or "import") → `{id, job}` (adds it, stars it, picks ports, starts the install job; for a hidden legacy game it only shows it again) |
| `POST /api/games/{id}/remove` | `{}` → `{job}` (app job: stop, back up each world into its backups, remove its router mappings, delete server/runtime/logs, drop it from servers.json and the stars; its firewall rules go at the next fix; `backups/`, players and records stay in `games/<id>/` and show again if it's added back; legacy: only hide) |
| `POST /api/games/{id}/star` | `{on}` |
| `POST /api/app/sidebar` | `{activeOpen, inactiveOpen}` |
| `POST /api/games/{id}/start` | `{memory}`: "" (check), "anyway", or "stop:<id>" |
| `POST /api/games/{id}/stop` · `/restart` · `/force-stop` · `/cancel` | `{}` |
| `POST /api/games/{id}/install` | `{repair}` |
| `GET /api/games/{id}/settings` | `{schema, values: {server, worlds, world}, ports: [{role, protos, port, public, chosen}], autoStopMinutes, discord: {on, webhook, stopped, daily, password, guildOK}}` |
| `POST /api/games/{id}/settings` | the changed parts only, merged on the server: `{values?: {key: value}, ports?: {role: port}, autoStopMinutes?, discord?: {…}}` (partial, so a stale page never blanks a field) |
| `POST /api/games/{id}/ports/pick` | `{}` → new ports for roles not chosen |
| `GET /api/games/{id}/worlds` | `{worlds, orphans, selected, running, folder, schema: {world, newWorld}, values}` |
| `POST /api/games/{id}/worlds/{select,create,delete,backup,restore,rename,values}` | as the legacy world routes; `create` and `values` take `values` by the schema |
| `GET /api/games/{id}/worlds/download` · `/backup-download` | as legacy |
| `POST /api/games/{id}/uploads` …, `GET /api/games/{id}/local-worlds`, `POST …/local-worlds/import` | as the legacy upload routes, per instance |
| `GET /api/games/{id}/players` · `/records?period=` · `/lists` | as legacy; lists as `[{kind: ListKind, ids}]` |
| `POST /api/games/{id}/lists` | `{kind, ids}` |
| `POST /api/games/{id}/allow` | `{name}` (Minecraft's one click) |
| `GET /api/games/{id}/stats?range=` · `/logs?after=` · `/logs/download` | as legacy, per instance |
| `POST /api/games/{id}/keep-on` | `{}` (4.6) |
| `GET /api/router` | `{kind, externalIP, publicIP, cgnat, gateway, lan, mappings: [{instance, role, proto, port, state, error}], steam: [{instance, seen}]}` |
| `POST /api/router/check` | `{}` → runs discovery and the reachability checks again |
| `POST /api/app/settings` | the app-wide settings.json fields, partial: `{idleHibernate?, idleMinutes?, autoUpdate?, startWithApp?, startAfterWake?, routerPorts?}` |
| `POST /api/discord/main` | `{webhook}` (settings.json `discordWebhook`, checked with `discord.Problem`) |
| `POST /api/discord/test` | `{link, game?}` (extends the legacy body) |
| `POST /api/power/stop-all` | `{}` |

`web.Backend` (frozen in R3's phase 0) is the interface `web.Server` uses instead of `*manager.Manager`; `internal/web/webtest.FakeBackend` implements it with canned data for panel work and `go run ./internal/web/webtest/demo` serves the panel with it.

### 6.3 Discord

- **Channels:** the main channel is settings.json `discordWebhook`. Each game posts in its own channel (servers.json `discord.webhook`) or, when that is empty, in the main one. All of a game's posts go to one channel.
- **Posts per game:** online (once ready; once the code is known for code games; after `Afterer`), stopped (reasons: stopped, restart, rest, crash, crash-restart, **idle** — "nobody played for an hour"), the daily records post at 23:59 (or once after waking for yesterday, as today), and Windrose's question (4.6). Non-legacy posts start with the game's short name when they go to the main channel. Valheim's texts are byte-identical to today (golden).
- **Daily post:** per game, from its own records. Games with `Deaths`: today's `dailyText` with the game's `Texts` ("vikings" for Valheim). Games without: "🎮 **3 players** played Terraria today. Most time: **Alex** (2 h 10 min)" with the longest session as the note. `Posted` is per game (each records file has its own).
- **Queues:** one queue per webhook link (`map[string]*postQueue`, 32 posts each, its own goroutine and retries), so one channel's failures never hold up another's; order is kept per link, so "stopped" never overtakes "online". `flushDiscord` waits for all of them. A full queue still drops with a log line.
- **Slash commands:** only starred games get theirs; starring or unstarring updates the waker (6.4). A game's command works only in its channel (its own, else the main one).
- **One Discord server:** the waker answers in one Discord server: the main channel's. A game channel in another server is flagged on its Settings tab ("This channel is in another Discord server, so /terraria won't work there").
- **Settings:** the Discord page has the main channel (link, Test) and a row per game (on, own channel, stopped, daily, password). Valheim's on/stopped/daily/password are settings.json's `discord`, `discordStopped`, `discordDaily`, `discordPassword` (same meanings); its own channel is servers.json.

### 6.4 The JetKVM waker

**Config** (`internal/waker`), new fields optional so either side's older version keeps working:

```go
type Config struct {
	Token, MAC         string
	Query, Name, Guild string        // as today: used for /valheim when Games is empty
	Games []GameCommand  `json:"games,omitempty"`
	Reply *ReplyConfig   `json:"reply,omitempty"`
	API, Gateway string; Targets []string
}
type GameCommand struct {
	ID          string `json:"id"`                // instance id, sent in the wake packet
	Command     string `json:"command"`           // "minecraft"
	Description string `json:"description"`       // "Wake the server computer and start Minecraft"
	Name        string `json:"name"`              // server name, for replies
	Channel     string `json:"channel,omitempty"` // the only channel it works in
	Query       string `json:"query,omitempty"`   // A2S address, when the game answers A2S
}
type ReplyConfig struct {
	Port int    `json:"port"` // UDP port the waker listens on for the app's outcomes (45458; verify it's free on a JetKVM)
	Key  string `json:"key"`  // HMAC key, 64 hex
}
```

- **Commands:** with `Games` set, the waker registers exactly those (one bulk overwrite per Discord server on `GUILD_CREATE`, as today), and ignores `Query`/`Name`. An interaction checks the guild (as today), then `channel_id == Channel` (else an only-you-see-it reply "🔒 /minecraft works in <#channel>."). Waking state is per game, so /minecraft while /valheim is waking works.
- **Wake packet:** today's packet (magic + MAC×16 + the 6-byte tag) followed by `"EGS1"`, one length byte, and the instance id. `wol.PacketFor(mac, tag, id)`, `wol.GameOf(b) string`. Older apps read only the tag and treat it as Valheim; newer apps read the id (no id → the legacy game).
- **Outcome replies** (app → waker, outbound from the server computer, so no new firewall rule): a UDP datagram to the JetKVM's address and `Reply.Port`: `"EGSO1"` + 32-byte HMAC-SHA256(key, json) + json `{"id": "palworld", "tag": "a1b2c3d4e5f6", "state": "starting"|"online"|"memory"|"busy"|"failed"|"disabled", "text": "…", "at": 1764183000}`. The app sends one each time it hears a tagged packet with a game (its current outcome for that request), and when that start becomes ready or fails. The waker drops stale (`at` older than 10 minutes) or wrong-HMAC packets and edits the reply: starting "⏰ Starting **X**…", online "✅ **X** is online. Have fun!", memory "🧠 There isn't enough memory to start **X** right now. Running: Valheim (3.1 GB), Palworld (9.8 GB).", failed "⚠️ **X** didn't start: <reason>." Without any outcome, today's A2S check and 10-minute timeout still apply.
- **App side:** `wakerSetup` gains `Games string` (sha256 of the canonical JSON of the commands list, so the struct stays comparable with `==`) and `ReplyKey` lives in jetkvm.json (0600). `wakerPlan` builds `Games` from the starred, non-foreign, enabled instances, with each channel id from its webhook (`discord.Webhook`). A star change runs `upkeepWaker` 30 s later (debounced), which reinstalls through `jetkvm.Install` as today; the restart registers the new commands.
- **Invite link:** asks for View Channel and Read Message History (permissions `66560`) from R3 on, for Windrose's rule; an existing bot works without re-inviting unless the channel is hidden from @everyone (the Discord page then says so).
- **Compatibility:** JetKVM names and paths unchanged (1.2). An older app reinstalls its own waker within 90 s of starting (version differs). Until then a new waker's /minecraft packet reaches an older app as a tagged packet: bed004b and R1 would start Valheim (a short window, only after a rollback past R2); R2 ignores packets whose id isn't the legacy one (8.1).

### 6.5 Router ports

```go
package routerports

type Mapping struct {
	Proto string // "tcp", "udp"
	Port  int    // same inside and outside
	Desc  string // "Easy Game Server minecraft game"
}
type Router interface {
	Kind() string // "upnp", "pcp", "natpmp"
	ExternalIP(ctx context.Context) (net.IP, error)
	Map(ctx context.Context, lan net.IP, m Mapping, lease time.Duration) error
	Unmap(ctx context.Context, lan net.IP, m Mapping) error
}
// Find tries UPnP IGD (SSDP; WANIPConnection:2, :1, WANPPPConnection:1), then PCP, then
// NAT-PMP (both at gateway:5351).
func Find(ctx context.Context, lan, gateway net.IP) (Router, error)
// NAT reports "cgnat" (external IP in 100.64.0.0/10), "double" (external IP private, or not
// the public IP), or "".
func NAT(external, public net.IP) string
// SteamSees asks Steam which servers it knows at the public IP
// (api.steampowered.com/ISteamApps/GetServersAtAddress/v1/?addr=IP; no key needed).
func SteamSees(ctx context.Context, publicIP string) ([]SteamServer, error)
```

- On by default (servers.json `app.routerPorts`; App settings can turn it off).
- When a game with `Join.Router` becomes ready: map each `Public` role, lease 2 hours, renewed every 30 minutes; removed when it stops. Re-mapped after a takeover and after waking. Never for private roles, never while stopped. Valheim with crossplay on maps nothing (decision); with crossplay off it maps its game and query ports.
- **Reachability:** carrier-grade or double NAT is detected and explained; Steam-registered games are checked with `SteamSees` (verify per game whether it registers; Valheim does when public); other games say "Can't check from here: ask a friend outside to try". No third-party checkers (they would learn the owner's address).
- **When it fails** (no router answers, mapping refused, NAT): the game's Home shows manual steps: "Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), and forward TCP 25565 to 192.168.1.20 (this computer)", with the gateway and LAN address filled in. With carrier-grade NAT: "Your internet provider shares one internet address between homes, so friends outside can't reach a server here. Ask your provider for a public IPv4 address. Games that join with a code or Game ID (Valheim with crossplay, Core Keeper, Windrose) still work."
- **No tunnel** of any kind (I10).

### 6.6 Tray and texts

- R3 tray (Windows): tooltip "Easy Game Server: 2 games online · 3 playing", cut at 120 bytes on a rune boundary; menu: Open control panel · Stop all games (shown while any runs; asks first when anyone plays) · Put computer to sleep · Close app (games keep running) · Stop all games and close app. `tray.Options` loses `Start`/`Stop`/`Running`, gains `StopAll func()` and `AnyRunning func() bool`. R2's tray is unchanged.
- main.go: `appName` = "Easy Game Server" (dialogs) in R3; `legacyFolderName` = "Valheim Easy Server" (R1). `serverStopper` becomes `StopAll(reason) error` + `Status()`; `stopAndWait` waits for every instance and the app job. `confirmPlayers` counts players in every game.
- Activity texts that name "the server" name the game: "Minecraft is online and ready for players."; Valheim's stay as they are.

---

## 7. Testing and CI

### 7.1 Stand-in servers

- **`cmd/fakevalheim`** stays exactly as it is, for Valheim and every existing manager test (built in `TestMain`).
- **`cmd/fakeserver`** stands in for the other ten. One binary; the personality comes from `fakeserver.json` in the folder of its own executable (or up to three parents), never from the environment, so tests can run several at once with different scripts:

```json
{"game": "minecraft", "script": "join:Alex@1,death:Alex@5,deny:Bob@6,leave:Alex@9",
 "speed": 60, "version": "1.21.4", "memMB": 40, "crashAfter": 0, "noReady": false}
```

  The framework (`cmd/fakeserver/main.go`, `fw_*.go`) gives every personality: argument parsing helpers, a log writer (stdout or a file), the script runner, memory ballast, TCP/UDP listeners on its planned ports (so port checks are real), a stdin command loop, Ctrl+C/SIGINT handling, and responders reused from `rcon.TestServer`, `a2s.TestResponder` and an HTTP(S) JSON helper. Each game's personality is its own file `cmd/fakeserver/<id>.go`, registering itself in `init()`.
- **Fake install:** when `Env.Fake` is set, the adapter's `Installer()` returns `install.Fake(fake, Program(), files)`: it copies the fakeserver to `Program()` (`.exe` on Windows), writes `fakeserver.json`, `.egs-build.json`, and placeholder files the adapter's Plan expects (`server.jar`, `VintagestoryServer.dll`, …). In development `EGS_DEV_FAKE_SERVERS=<path to fakeserver>` sets it for every non-legacy game (Valheim keeps `--dev-fake-server`).
- **`internal/game/gametest`** runs one adapter without the manager:

```go
func BuildFakeserver(t testing.TB) string
type Options struct { Fake string; Script string; World string; NewWorld game.Values; Change func(*game.Saved) }
func Start(t *testing.T, g game.Game, o Options) *Session
func (s *Session) WaitEvent(kind, name string, within time.Duration) game.Event
func (s *Session) State() game.RunState
func (s *Session) Takeover()               // as after an app restart: new Monitor from the run record, keeper.Dial
func (s *Session) Stop() (saved time.Time) // runs the stop chain
func Conformance(t *testing.T, g game.Game, o Options)
```

  `Conformance` checks: the fake install makes `Program()` and `Build()`; a new world's Plan; the server is online within 30 s; its public ports are in use (`platform.PortInUse`); scripted joins and leaves (and a death when `Deaths`) become events; the probe answers when it has one; the stop chain ends the process and the world's files changed; a second start loads the same world; a takeover then a stop works (console games through the keeper); `Validate` rejects a 200-character server name; `Fingerprint` changes when a `Restart` field changes; `Join` has steps; `Worlds()` lists the world, and a backup restores.

### 7.2 Compat and golden tests

| Test | Package | Pins |
|---|---|---|
| `TestIdentity` | main, platform, selfupdate, jetkvm, waker, cmd/valheim-waker | every value in 1.2, as literals |
| `TestFlagsUnchanged` | main | the flag set, `relaunchArgs` |
| `TestSettingsShapeFrozen` | config | settings.json's key set and types; `Defaults()` |
| `TestLegacyFileShapes` | manager | runtime/players/records/lastplayed/stats/history/activity/jetkvm/update JSON against `testdata/legacy/*.json` written by bed004b's structs |
| `TestRuntimeJSONCompat` | manager | a new run file decodes with bed004b's `runInfo`, and the reverse, values equal |
| `TestStatusTopLevelKeys` | web | `/api/status` top-level keys and the `job`, `lastExit`, `join`, `app` shapes |
| `TestLegacyRoutes` | web | every old route still answers with the same shape |
| `TestFirewallScriptGolden` | platform | `firewallScript` output for fixed paths; the Valheim part of `FixFirewallAll` identical |
| Valheim golden texts | games/valheim | online/stopped/daily Discord texts, reminders, exit hints, `ServerArgs` (moved from manager tests) |
| `TestNoGameIDs`, `TestImportRules`, `TestNoTunnel` | game | 2.1, I10 |
| `TestServersPassthrough` | servers | unknown keys and instances survive a write |
| `TestNewIsQuick`, `TestStatusIsQuick` | manager | I5, 4.8 |
| `TestForeignInstance` | manager | a live `games/x/run.json` keeps the computer awake, blocks idle sleep, shows the warning, and its files are untouched |
| `TestSafeMode` | manager | damaged, newer and missing-with-games servers.json → Valheim only, file not rewritten |
| `TestWakePacketForOtherGame` | manager | R2 ignores a packet whose id isn't the legacy one |

### 7.3 Upgrade and rollback against real releases (R1, in build.yml)

New steps in the `windows` and `mac` jobs (so they gate releases), using `cmd/ci-stubs` (a stand-in GitHub releases API on 127.0.0.1:18500 and a Discord webhook recorder on 127.0.0.1:18501 that writes each post to a JSON file):

1. Download the **latest real release**'s program (`gh release download --repo $GITHUB_REPOSITORY --pattern ValheimEasyServer.exe` / `ValheimEasyServer-mac`).
2. Start it with `--no-window --data D --port 18457 --dev-fake-server <fakevalheim> --dev-fake-github http://127.0.0.1:18500 --dev-fake-discord http://127.0.0.1:18501` (and, from R3 on, `EGS_DEV_FAKE_SERVERS` in its environment, which it ignores and passes on to the build it starts). Turn on Discord with a stand-in webhook, install, start Valheim, wait for online and for the Discord post.
3. Publish this commit's build with a higher number; `POST /api/app/update {"install":true}`; wait for the new build.
4. Check: state online; runtime.json's PID unchanged (the same server process was taken over); exactly one "online" post; settings.json's password, world and port unchanged; no `*.damaged-*` files; the activity names the update. From R3: add a fake Minecraft, start it.
5. **Back:** `POST /api/system/quit {"stopServer":false}`; start the old release on the same folder; check: it answers, Valheim online with the same PID, no new Discord post, settings unchanged; from R3 also: the old build (R2) warns that Minecraft is running and keeps the computer awake. Stop Valheim through it (saved), start, stop, quit.
6. **Forward again (R3):** start the new build: it takes over the fake Minecraft (same PID) and stops it through its stop chain.

The existing "a build that doesn't start" test stays on Windows and gets a Mac twin (a broken build: `/usr/bin/false`, which has a universal Mach-O header; verify). Mac steps use the bare binary in a plain folder (no `.app`, so no detaching or translocation).

Branch builds (anything but the default branch) are built with `-X main.build=0` (R1).

### 7.4 Real-server matrix

`.github/workflows/real-servers.yml` (replaces `real-server.yml` in R3; until then the old one stays):

- **Triggers:** push to `multigame` and to the default branch (ignoring `docs/**` and `*.md`), and `workflow_dispatch` with inputs `games` ("all" or a comma list) and `big` (also run Satisfactory and Windrose). One run per branch; a newer push cancels an older run.
- **Matrix from one file per game:** a `plan` job reads every `internal/games/*/realserver.json` and outputs the matrix:

```json
{"os": ["windows-latest", "macos-15-intel"], "onDemand": false, "timeoutMinutes": 60,
 "package": "./internal/games/minecraft/", "run": "TestRealServer"}
```

- **Each cell:** `go test -tags realserver -run <run> -v -count=1 -timeout <t>m <package>`; uploads the test data folder's logs when it fails. Each game's `TestRealServer` drives the real manager (`internal/manager/managertest.New(t)` with real installers): add the game, install, start a new world, online, probe, Join, stop (saved), start again (same world), stop, remove. Disk-heavy cells use the runner's largest drive (verify free space for Satisfactory and Windrose on `windows-latest`).
- These runs don't block releases (a separate workflow); a game is marked ready only when its cells are green.

| Game | Windows | Intel Mac | Runs |
|---|---|---|---|
| Valheim | yes | yes | every push |
| Minecraft | yes | yes | every push |
| Terraria | yes | yes | every push |
| Necesse | yes | yes | every push |
| Vintage Story | yes | — | every push |
| Project Zomboid | yes | — | every push |
| Enshrouded | yes | — | every push |
| Core Keeper | yes | — | every push |
| Palworld | yes | — | every push |
| Satisfactory | yes | — | on demand |
| Windrose | yes | — | on demand |

No Mac cell for a "Needs Windows" game. Linux has no real-server cells (not a user platform).

### 7.5 The owner's Mac Pro checklist (R3 candidate)

On the Mac Pro (macOS 12, Intel), with the candidate build:

1. **Valheim:** it's taken over as before; start, join, stop (saved).
2. **Minecraft:** Add a game (EULA), join from a Java client; set the allow list to on, a name not on it is turned away, click Allow, that friend joins; stop.
3. **Terraria:** Add a game with a new small world, join (same version), "Close app (games keep running)", open the app again, Stop: the world kept what you built.
4. **Necesse:** Add a game, join, stop, start again: the world is there.
5. **Sleep and wake:** with two games running, Sleep, then wake with the KVM's button: both start again.

---

## 8. Releases, gates and kill switches

### 8.1 What each release contains

| Release | Contents |
|---|---|
| **R1 "bridge"** | identity constants (`legacyFolderName`, `dataFolderName` and the others in 1.2) with literal tests; the legacy-shape and Status golden tests; `cmd/ci-stubs` and the upgrade/rollback steps against the latest real release on Windows and Mac; branch builds unnumbered. No behaviour change. (Changed from round one: the "keep unknown settings.json keys" writer and the foreign-run-file awareness move out of R1: nothing new is ever written into settings.json, and the build that must cope with other games' run files is R2, the rollback target of R3.) |
| **R2 "core"** | servers.json, migration, safe mode; foreign-instance awareness; the wake-packet game check; the game contract and registry; `games/valheim`; the manager as instances with per-instance jobs, adoption and per-instance files; SteamCMD with an app id and a lock. Old panel. Status gains `games`, `jobs` (the old panel ignores them). Anything else already merged rides along inert: platform additions, the keeper (nothing starts it), worldstore, rcon/a2s, other adapters at stage hidden. |
| **R3 "games"** | the new panel and the rename; Add a game and removing; the other ten adapters (each ready, preview or hidden by its checks); per-game Discord channels, queues and daily posts; the multi-command waker with replies; auto-stop for all games; Windrose's rule; the memory check; sleep and waking with the resume set; router ports; the tray; firewall rules for every game. |
| **R3.x** | one game per build: a stage flip (preview → ready) and its fixes. |

### 8.2 Gates

A release is a merge of `multigame` (or `release/rN` cut from it) into `ccr-949cf1ef-eeo1jr` by the lead, pushed; CI tests, builds and publishes `build-N`; installed copies update within the hour. The merge commit's first line is the release note people see in the app ("Safety nets before several games", "Several games: the core (Valheim only for now)", "Easy Game Server: several games at once").

- **R1:** CI green on Linux, Windows and Mac, including the new upgrade/rollback steps against bed004b's release.
- **R2:**
  - every existing test green, with diffs limited to accessors (reviewed by the lead);
  - the new compat tests (7.2) green;
  - upgrade from the latest release (R1) and back, green on Windows and Mac;
  - the real Valheim check green on Windows and Mac through the new manager.

  Then release. **The owner's soak:** 3+ days of normal play on the released R2, covering an auto-update while Valheim runs (an R2.x fix, or a no-change release made for it), sleep and wake, /valheim, and a takeover. Fixes go out as R2.x. The owner's OK after the soak is the gate for merging R3 (question 10.4).
- **R3:**
  - all CI green;
  - the real-server matrix green for every game marked ready (the rest ship as preview or hidden);
  - upgrade R2 → R3 and back R3 → R2 green on Windows and Mac, with a second fake game running across both;
  - `TestNoGameIDs`, `TestImportRules`, `TestNoTunnel` green;
  - the owner's Mac checklist (7.5) passed on the candidate build.
- **R3.x:** that game's real-server cells green (dispatched for Satisfactory and Windrose); one stage flip per build.

Game stages at R3: **ready** needs its real-server cells green on every OS it's offered on, all its "verify" facts settled, and for Mac-offered games the owner's checklist; **preview** needs its conformance test and one green real-server run; otherwise **hidden**.

### 8.3 Kill switches

- **Stage per game** (compiled in, flipped in an R3.x build): hidden instances become foreign (kept, not run, still kept awake for); preview shows the label.
- **`app.disabledGames`** in servers.json, also set by "Turn this game off" on its Settings tab: the game can't start, isn't resumed, gets no Discord command; its data stays. Works without a new build.
- **Valheim-only safe mode** when servers.json is damaged, newer than this build, or missing while `games/` exists: only the legacy instance runs, servers.json is never rewritten, other games' run files (found by scanning `games/*/run.json`) still keep the computer awake, and a banner offers "Restore the list of games from its backup" (`POST /api/app/servers/restore`, when servers.json.bak is valid).
- **Rollback:** the automatic one (exe.old) and the matrix in 3.6.
- Development only: `EGS_DEV_FAKE_SERVERS`, `EGS_DEV_ALL_STAGES=1` (hidden games shown as preview), `EGS_DEV_SAFE_MODE=1`.

---

## 9. Work breakdown

### 9.1 How the work flows

- **One stream = one coding agent in its own local worktree**, branched from `multigame`: `git worktree add ../egs-<stream> -b mg/<stream> multigame`. It commits only to its branch and edits only the files it owns (below). Anything else goes through the lead.
- Before saying it's done, an agent runs `gofmt -l .`, `go vet ./...` and `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...`, and `go test ./...` (Linux), and checks its acceptance list.
- **The lead** merges finished branches into `multigame` locally (`git merge --no-ff mg/<stream>`), runs the tests, and pushes `multigame`. CI runs the Linux, Windows and Mac tests on that push (and the real-server matrix, 7.4). A Windows or Mac failure goes back to the stream's agent, who fixes it on its branch; the lead merges again. When a stream needs another one's work, it merges `multigame` into its branch after the lead has merged that work.
- **No pull requests.** Releases only at the gates (8.2), by the lead.
- **Contracts are frozen first** (phase 0 for R2, phase 0' for R3) by the lead, so streams can start in parallel against them. A contract change needs the lead: it lands on `multigame` and streams merge it.

### 9.2 Order

```
Phase 1  R1     S1 ─┐
                S2 ─┴─► R1 gate ─► merge to default
Phase 2  P0     lead: internal/game, internal/servers types, package stubs
Phase 3  R2     V ──► M ─────────────────────► R2 gate ─► release, owner soak
                P · I · K · W · F · RC  (parallel with V and M; land inert)
Phase 4  P0'    lead: web.Backend, Status game types, manager R3 files with signatures, waker/wol types
Phase 5  R3     G-minecraft … G-windrose (10) · A · D · WK · N · API · UI · UIV · T · CR   (all parallel)
Phase 6         integration, R3 gate (after the owner's soak OK) ─► release; then R3.x per game
```

The ten game streams can start in phase 3 already, once F, W, I, K, RC and P have landed: an adapter only needs the contract, `gametest` and the fakeserver. Their real-server tests need `managertest` (CR) and Add a game (A), so those come last in each game stream.

### 9.3 Streams

**Phase 0 (lead, `mg/p0`).** Creates and owns until handed over: `internal/game/{contract.go,registry.go,values.go,schema.go,record.go}` with tests (`Record` inlining both ways, `Values` helpers, registry rules), `internal/game/{noids_test.go,imports_test.go,notunnel_test.go}`, `internal/servers/types.go`, and stub files with the exported signatures (bodies return `errors.ErrUnsupported`) for `internal/install`, `internal/keeper`, `internal/worldstore`, `internal/rcon`, `internal/a2s`, `internal/platform/additions.go`, `internal/game/gametest`. *Accept:* builds and vets on all three systems; no behaviour change; this spec's 2.2 compiles as written.

| Stream | Branch | Phase | Needs | Owns | Delivers | Accepted when |
|---|---|---|---|---|---|---|
| **S1** identity and goldens | `mg/r1-golden` | 1 | — | main.go (new constants only); new `*_identity_test.go`/`*_golden_test.go` files in main, platform, selfupdate, jetkvm, waker, cmd/valheim-waker, config, manager, web; `internal/manager/testdata/legacy/` | 1.2 constants; 7.2's identity, flag, settings, legacy-file and Status tests | renaming any identity literal (the lead tries one) fails a test; no non-test behaviour changes; green on 3 systems |
| **S2** upgrade CI | `mg/r1-ci` | 1 | — | `.github/workflows/build.yml`, `cmd/ci-stubs/`, `.github/scripts/upgrade-test.{ps1,sh}` | 7.3 steps 1-5 on Windows and Mac; the Mac broken-build test; branch builds with build 0; `node --check` over `ui/assets/**/*.js` | the steps pass on a `multigame` push against bed004b's release; a branch artifact reports `app.build` 0 |
| **V** Valheim adapter | `mg/valheim` | 3 | P0 | `internal/games/valheim/**`, `internal/games/all/{all.go,valheim.go}` | 5.1: the adapter, with the Valheim code copied out of the manager (the manager's copies are deleted by M) | golden tests moved here pass: online/stopped/daily texts, reminders, `ServerArgs` through `Plan`, `ExitHint`, `Fingerprint` equals `argsKey` for the same settings; the Monitor gives the same activities as `Tracker` on the manager tests' log fixtures |
| **M** manager core | `mg/manager` | 3 | P0, V | `internal/manager/**`, `internal/servers/**`, main.go (registry import, keeper hook, wiring), `internal/wol/game.go` (`GameOf`) | sections 3 and 4 for R2: servers.json, migration, safe mode, instances, per-instance jobs and files, adoption, foreign awareness, wake-packet game check; the manager calls the adapter for everything game-specific; removes the moved Valheim code | every existing test green with accessor-only diffs; 7.2's manager tests green; upgrade/rollback steps green on Windows and Mac; the real Valheim check green (dispatched) |
| **P** platform | `mg/platform` | 3 | P0 | new files only in `internal/platform`: `start_*.go`, `ports_*.go`, `shared_*.go`, `gateway_*.go`, `knownfolder_*.go`, `elevate_*.go`, `rosetta_*.go`, `procname_*.go`, `firewall_games.go`, `winparse_games.go`, and their tests | 2.12 | Windows and Mac CI: `PortInUse` finds the owner PID of a test listener (TCP and UDP); `Start` with a stdin pipe works; `OpenShared` lets the file be renamed while open (Windows); `FixFirewallAll`'s Valheim part is byte-identical to `firewallScript`; no change to existing tests |
| **I** install | `mg/install` | 3 | P0 | `internal/install/**` (not `fake.go`), `internal/steamcmd/**` | 2.3 | Valheim's SteamCMD arguments and texts unchanged (golden); downloads reject a wrong hash and unsafe zip paths, keep exec bits; Temurin, .NET, Mojang and terraria.org resolvers tested against recorded answers (httptest) |
| **K** keeper | `mg/keeper` | 3 | P0 | `internal/keeper/**` | 2.11 (M adds the one-line hook in main.go) | on 3 systems: a test child started through the keeper gets `LINE`s; after the client is dropped, `Dial` works again; the keeper killed → the child never sees end-of-file; Windows: Ctrl+C to the child's PID doesn't reach the keeper |
| **W** worldstore | `mg/worldstore` | 3 | P0 | `internal/worldstore/**` | 2.8 | list, backup, restore, delete, rename, import and export round trips for `folder` and `files`; a locked file leaves the world as it was (Windows); unsafe zip paths refused; nothing written outside its folders |
| **F** fakeserver and gametest | `mg/fakeserver` | 3 | P0, RC | `cmd/fakeserver/{main.go,fw_*.go,demo.go}`, `internal/game/gametest/**`, `internal/install/fake.go` | 7.1 framework, a demo personality, `Conformance` | `Conformance` passes for a demo adapter in its tests on 3 systems; two fakes with different scripts run at once |
| **RC** protocols | `mg/protocols` | 3 | P0 | `internal/rcon/**`, `internal/a2s/**` | clients plus `rcon.TestServer`, `a2s.TestResponder` | multi-packet RCON replies, bad password, A2S challenge, timeouts |

**Phase 0' (lead, `mg/p0r3`).** `internal/web/backend.go` (`Backend` interface), the `games`/`jobs`/`router` types in `internal/manager/status_games.go` with `gameStatusLocked(inst)` calling one hook per stream (`autoStopStatusLocked`, `askStatusLocked`, `routerStatusLocked`, `discordStatusLocked`), the R3 fields on `instance` and `serverRun`, and every R3 manager file below created with its function signatures and empty bodies, so streams call each other from day one (for example `startFromWake(id, tag string) wakeOutcome` in A's `resume.go`, used by WK's `wakesignal.go`); `waker.GameCommand`/`ReplyConfig`, `wol.PacketFor`; `internal/web/ui/assets/games/README.md` with 6.1's module contract.

In R3 every manager file not listed for A, D, WK or N (instance.go, server.go, jobs.go, status.go and the rest) stays with M's agent as **M3**, branch `mg/manager-r3`: the fixes and small hooks other streams ask for, merged first whenever they arrive.

| Stream | Branch | Needs | Owns | Delivers | Accepted when |
|---|---|---|---|---|---|
| **G-minecraft** | `mg/g-minecraft` | P0', F, W, I, RC, P | `internal/games/minecraft/**`, `internal/games/all/minecraft.go`, `cmd/fakeserver/minecraft.go`, `internal/web/ui/assets/games/minecraft.js` (its `realserver.json` is inside its package) | 5.2 | `Conformance` green on Windows and Mac; Monitor tests on sample logs (deaths list, turned away); `server.properties` and command line golden; real-server check green; each "verify" settled with the source noted in `internal/games/minecraft/FACTS.md` |
| **G-terraria** | `mg/g-terraria` | same + K | same pattern | 5.3 | as above, with the keeper takeover |
| **G-necesse** | `mg/g-necesse` | same + K | same pattern | 5.4 | as above |
| **G-vintagestory** | `mg/g-vintagestory` | same + K | same pattern | 5.5 | as above, Windows only |
| **G-zomboid** | `mg/g-zomboid` | same | same pattern | 5.6 | as above, Windows only |
| **G-enshrouded** | `mg/g-enshrouded` | same | same pattern | 5.7 | as above, Windows only |
| **G-corekeeper** | `mg/g-corekeeper` | same | same pattern | 5.8 | as above, Windows only |
| **G-palworld** | `mg/g-palworld` | same | same pattern | 5.9 | as above, Windows only |
| **G-satisfactory** | `mg/g-satisfactory` | same | same pattern | 5.10 | as above; real-server on demand |
| **G-windrose** | `mg/g-windrose` | same | same pattern | 5.11 | as above; on demand; the presence decision recorded |
| **A** lifecycle | `mg/lifecycle` | P0', M | `internal/manager/{addgame.go,remove.go,memory.go,autostop.go,ports.go,firewall.go,power.go,resume.go}` | 2.5, 4.3, 4.5, 4.7, Add/remove, all-games firewall | with fakeservers and a fake clock: ports avoid Valheim's and other instances'; memory short gives `action: "memory"` with data; `stop:<id>` stops then starts; auto-stop after N minutes at count 0 and not with presence none; idle sleep blocked by a presence-none game and by a foreign one; Sleep stops all and records the resume set; a game wake cancels the resume; the KVM packet resumes |
| **D** Discord | `mg/discord` | P0', M | `internal/manager/{discord.go,records.go,reminder.go,ask.go}`, `internal/discord/**` | 6.3, 4.6 | Valheim's texts golden; a failing channel doesn't delay another's posts; daily posts per game; the Windrose rule with a stand-in Discord API (messages endpoint, 403) and a fake clock |
| **WK** waker | `mg/waker` | P0', M | `internal/waker/**`, `internal/wol/**`, `internal/jetkvm/**`, `cmd/valheim-waker/**`, `internal/manager/{waker.go,wakesignal.go}` | 6.4 | bot tests: commands from `Games`, channel check, per-game waking, HMAC replies verified and stale ones dropped; an old config still loads (`TestLoadConfig` unchanged); `wakerSetup` still comparable; `fakekvm` install with games; wake packet round trip, old `Target` unchanged |
| **N** router ports | `mg/router` | P0', M, P | `internal/routerports/**`, `internal/manager/router.go` | 6.5 | stand-in IGD (SSDP + SOAP), NAT-PMP and PCP servers: map, renew, unmap; NAT detection; the manual-steps text filled in |
| **API** | `mg/api` | P0' | `internal/web/*.go`, `internal/web/webtest/**` | 6.2 | `TestLegacyRoutes`; every new route tested against `FakeBackend` and against the manager with fakeservers; partial settings merge; `guard` on every write |
| **UI** | `mg/ui` | P0', API | `internal/web/ui/**` except `assets/games/*.js`; deletes the old page modules at the end | 6.1 | `node --check` on every module; the demo (`go run ./internal/web/webtest/demo`) shows layout A, stars and both lists remembered, Overview, Add a game (greyed "Needs Windows" on a Mac), a game's tabs, the memory dialog; a Go test loads the index and every module |
| **UIV** Valheim pages | `mg/ui-valheim` | P0', UI | `internal/web/ui/assets/games/valheim.js` and new helper modules it needs under `assets/games/valheim/` | today's Valheim-only panel parts (difficulty and mods on world cards, the Mods tab, the join-code card, tutorial, mods help) as the Valheim module | the demo's Valheim pages do what today's pages do |
| **T** rename, tray, docs | `mg/rename` | P0', A | `internal/tray/**`, main.go (visible name, tray wiring, `serverStopper`), `README.md`, `docs/READ-ME-FIRST*.txt`, `macapp/Info.plist` (`CFBundleDisplayName` only) | 1.2's visible rename, 6.6 | identity tests still green; tooltip cut is rune-safe; no "tunnel" in the docs; `plutil -lint` passes |
| **CR** real-server CI | `mg/ci-real` | P0', A | `.github/workflows/real-servers.yml`, `.github/workflows/build.yml` (R3 changes: smoke greps "Easy Game Server", 7.3 steps 4-6), `internal/manager/managertest/**` | 7.4, 7.3's R3 parts | a dispatch runs Valheim and Minecraft cells green; the matrix comes from the `realserver.json` files; the upgrade steps run with a second fake game |

All phase-5 streams run in parallel. Merge order the lead follows: API before UI and UIV; A before T and CR; each G stream whenever it's accepted (stage preview until its real-server check is green).

---

## 10. Questions only the owner can answer

1. **Windrose without a bot that can read its channel** (the JetKVM waker isn't set up, or the bot can't see the channel): the plan is to still post the 4-hour question, count only the control panel's **Keep it on**, and turn it off 2 hours later. Is that right?
2. **Another game with no join/leave signal:** if a game other than Windrose (Core Keeper is the likeliest) turns out not to report who is on, should it use the same Discord rule, or keep running until someone stops it?
3. **Project Zomboid:** do your friends play the stable Build 41 or the Build 42 beta? Server and players must be on the same one.
4. **R2's 3-day run:** the plan releases R2 to your computers (that's how the auto-update while Valheim runs and the takeover get tested) and waits for your OK after 3+ days before R3 is merged. Or do you want a test build before R2 is released?
5. **The Mac checklist for R3:** may the test build run on your real data folder (it takes Valheim over like an update; going back to the released build is the tested rollback), or should it use a separate test folder?
6. **Tray menu:** with several games it loses "Start server" and gets "Stop all games". Is that enough, or do you want Start/Stop per game in a submenu?
