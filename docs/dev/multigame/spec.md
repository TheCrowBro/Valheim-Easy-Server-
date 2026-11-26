# Easy Game Server: the specification (round 2)

This turns Valheim Easy Server into Easy Game Server, which hosts 11 game servers, several at once. Coding agents implement it. Where this file and [decisions.md](decisions.md) disagree, decisions.md wins. This file builds on [carryover.md](carryover.md); the places where it departs from round one are marked **(changed)** and say why.

- **Baseline:** the default branch `ccr-949cf1ef-eeo1jr` at `bed004b` (still the fresh head on 2026-11-26). Work happens on `multigame` (bed004b plus notes commits, each marked `[skip ci]`). Nothing goes to the default branch except at the release gates (8.2).
- **Who it serves:** one owner and a few friends. Only the owner's computers run the app (a Windows PC and a 2013 Mac Pro on macOS 12, Intel). Linux runs tests only; it is not a user platform.
- **Words:** *game* = one of the 11 supported games (an adapter in `internal/games/<id>`). *Instance* = one server of a game on this computer; this round there is at most one per game and its id equals the game id (`minecraft`); a second one would be `minecraft-2`. *Legacy instance* = Valheim, the one game whose data lives in the legacy files. *Foreign instance* = an instance in servers.json that this build doesn't run (unknown game, hidden stage, or disabled); in R2, which has no servers.json, any other game's live `games/*/run.json`.
- **"verify"** marks a game fact round one wasn't sure of. The adapter's stream must check it against the real server (its real-server check, or a real log) before the game can be marked ready, and fix the adapter if it's wrong.

Contents: 1 Invariants and the legacy contract · 2 Architecture · 3 Data on disk · 4 Manager · 5 Game adapters · 6 Panel, API, Discord, waker, router ports · 7 Testing and CI · 8 Releases and kill switches · 9 Work breakdown · 10 The owner's answers and open questions.

### Changes after review

An adversarial review (83 findings; each one's verdict is in [review.md](review.md)) changed this spec as follows:

- **Cut:** the keeper (console games keep a stdin pipe the app holds; after an app restart their stop falls back to Ctrl+C, 2.11), the app → JetKVM UDP outcome replies (the app posts Discord replies through its webhooks, 6.4), `web.Backend` and `FakeBackend` (6.2), the `intel` support level and Rosetta detection (2.10), servers.json writes in R2 (3.4), the restore route and banner of safe mode (8.3).
- **Release process:** every release is a `--no-ff` merge with a release-note message and no `[skip ci]` (8.2); R2.x fixes come from `release/r2`; R2.x and R3.x releases need the owner's OK (owner question Q1); R3 ships every game at least as preview; R2 registers Valheim only; the release job also requires a green real-server run (7.4); a direct R1 → R3 upgrade is tested.
- **CI:** tests and the upgrade steps use numbered copies of the build, and only uploaded branch artifacts are build 0 (1.4, 7.3); stream branches get CI on all three systems before they're merged (9.1); fake games run on Linux too (7.1); the real-server matrix runs only changed cells, nightly and at gates, and game checks run without the manager (7.4).
- **Manager:** the app job slot holds only Sleep; the waker and firewall keep their own locks; `Status.job` is the newest job of the legacy and app slots (4.2); the auto-stop and idle-sleep clocks no longer fight (4.5); the resume after a wake waits for the network (4.7); a start never waits for another game's download (2.3); adoption reruns Valheim's mods code (4.4); locking and cache rules (2.2, 4.8); `played` is a typed field of the run record (2.2).
- **Owner-facing rules:** the Minecraft allow list in `auto` is on before the port opens (5.2); Windrose's question and stop texts (4.6); hiding Valheim really takes it out of use (I1); start-with-app per game (2.9); Add a game asks whether to star (6.1); the waker never registers commands nobody starred (6.4); a manual router forward counts as open (6.5); all visible names have an owner (1.2).
- **Game facts** checked against the real servers and official sources on 2026-11-26: Minecraft 26.3 (no `pvp` key, log strings, Java 25 runs on macOS 11+), Terraria 1.4.6.1 (zip layout, Mac library path, UDP), Necesse, Vintage Story, Project Zomboid (Build 42, the `.bat` launcher), Enshrouded, Core Keeper (has join/leave lines), Palworld, Satisfactory (saves in LocalAppData), Windrose (a 3 GB download; its config and worlds). Each game's section now says what is settled.
- **Section 10** now records the owner's answers of 2026-11-26 and asks only what is still open.

---

## 1. Invariants and the legacy contract

"Installed copies" are the owner's computers running bed004b or any later release. They update themselves from GitHub releases of the default branch. Everything in this section must hold for every release from R1 on, and is pinned by a test named in brackets.

### 1.1 Invariants

| # | Invariant | Pinned by |
|---|---|---|
| I1 | **Valheim never moves.** Its folders always come from `config.ResolvePaths(dataDir, settings)` (server, saves, saves/worlds_local, backups, logs, uploads, steamcmd, and the ServerDir/SaveDir overrides). No migration copies, moves, renames or deletes a Valheim file. Removing Valheim from the panel only hides it: its files stay, but hiding stops it, unstars it, and leaves it out of the sidebar, start-with-app, the resume set and the waker's commands, so a hidden Valheim never runs unseen (3.2). | `TestLegacyPathsUnchanged`, `TestHiddenLegacy` (manager) |
| I2 | **Legacy files keep their names, shapes and meanings** (table 1.3). New data goes only into new files: `servers.json` (from R3) and `games/<instance>/…`. A legacy file may gain a field only if it is `omitempty`, older builds ignore it, and its absence means today's behaviour. | `TestLegacyFileShapes` (manager), golden JSON in `internal/manager/testdata/legacy/` |
| I3 | **settings.json is frozen** (changed): no field is added, removed, renamed or given a new meaning. App-wide settings that already exist there (idle sleep, auto-update, the main Discord channel, the waker) keep living there; everything new goes into servers.json. Round one's "keep unknown top-level keys" writer isn't needed, because nothing new is ever written into settings.json. | `TestSettingsShapeFrozen` (config) |
| I4 | **runtime.json stays Valheim's run file** at the data root, with the same shape, so a new build takes over a running Valheim exactly as today and an older build still can after a rollback. Other instances use `games/<instance>/run.json`. | `TestRuntimeJSONCompat` (manager) |
| I5 | **The update handshake keeps working:** every existing flag is accepted (`--data --background --no-window --port --dev-fake-server --dev-fake-discord --dev-fake-github --detached`, `-psn_…`); no new flag is ever added (an older build exits with code 2 on an unknown flag, and relaunchArgs passes flags on). Developer switches are `EGS_DEV_*` environment variables. `GET /api/status` answers 200 with its top-level fields. `.instance.json` holds `{"port":N}`. `manager.New` does O(1) work (it runs before the panel listens; the old build kills a new one that doesn't answer within a minute, main.go:300-361). | `TestFlagsUnchanged`, `TestStatusTopLevelKeys` (web), `TestNewIsQuick` (manager: with 11 instances and 30 logs each, New reads only settings.json, servers.json and the run files, checked through a counting hook on file and process access; a loose 2 s bound, skipped under `-race`) |
| I6 | **Identity unchanged** (table 1.2). Only what people see changes to "Easy Game Server", in R3. | `identity_test.go` files (R1) |
| I7 | **Old API routes keep working** as aliases of the legacy instance, with the same JSON. | `TestLegacyRoutes` (web) |
| I8 | **Other games never write into Valheim's places:** not `logs/server-*.log` (pruned and parsed as Valheim by old builds), not `backups/<name>` (listed as Valheim worlds by old builds), not `uploads/` (emptied by old builds at start), not players/records/lastplayed/stats/history.json. | `TestNoForeignFilesInLegacyPlaces` |
| I9 | Files holding passwords or tokens are written 0600: settings.json, runtime.json, servers.json, `games/*/run.json`, jetkvm.json. | `TestSecretFilesPrivate` (Unix) |
| I10 | **No tunnel of any kind** anywhere: no package, route, setting, text or release item. | review checklist; `TestNoTunnel` greps the tree for `playit`, `ngrok`, `tunnel` outside docs/dev |
| I11 | **No new inbound firewall rule for the app itself.** Replies to Discord requests go out through the game's webhook (6.4); nothing needs to reach the app from the JetKVM except today's wake packets. The existing "wake signal" rule (UDP 9 from the local subnet) is unchanged. | review |
| I12 | **Valheim behaves exactly as today** in R2 (and in R3 apart from the decided changes: auto-stop on, own Discord channel possible, router ports with crossplay off, the new panel). The existing tests keep their intent; texts are golden. | all existing tests; `games/valheim` golden tests |

### 1.2 Identity that must not change

All of these become named constants (R1) and get a test that compares them with the literal value, so a rename fails CI.

| What | Value | Where today |
|---|---|---|
| Data folder (Mac, LOCALAPPDATA fallback) | `ValheimEasyServer` | main.go:456, 479 |
| Downloads/Desktop data subfolder | `Valheim Easy Server` | main.go:462 (`appName`) → new constant `legacyFolderName` |
| Release assets | `ValheimEasyServer.exe`, `ValheimEasyServer-mac` (+ `.sha256`), `ValheimEasyServer-windows.zip`, `ValheimEasyServer-mac.zip` | selfupdate.go:38, build.yml |
| Release tags and downloads (the update channel) | tags `build-N` (`^build-(\d{1,9})$`), assets downloaded from the release's `browser_download_url` on github.com for that repo | selfupdate.go:64, 120-128, build.yml release job |
| Repo and repo id | `TheCrowBro/Valheim-Easy-Server-`, `1430331409` | selfupdate.go:30-31 |
| Single-instance lock | `Local\ValheimEasyServer-<id>`; `<data>/.instance.lock`; `$TMPDIR/ValheimEasyServer-<id>.lock` | desktop_windows.go:205, desktop_unix.go:49-51 |
| Launch at login | Run value `ValheimEasyServer`; LaunchAgent `com.thecrowbro.valheimeasyserver` | desktop_windows.go:99, desktop_darwin.go:18 |
| Mac bundle | folder `Valheim Easy Server.app`, executable `Contents/MacOS/ValheimEasyServer`, CFBundleIdentifier `com.thecrowbro.valheimeasyserver` | macapp/Info.plist, build.yml:245-248 |
| Windows program identity | `InternalName` `ValheimEasyServer`, `OriginalFilename` `ValheimEasyServer.exe`, manifest identity name `ValheimEasyServer` | winres/winres.json |
| Valheim firewall rules | prefix `Valheim Easy Server - `, `…game (<sha8>)`, `…wake signal (<sha8>)`, legacy names | winparse.go:15-48, desktop_windows.go:162-181 |
| Admin scripts | `ValheimEasyServer-admin-*.ps1` | power_windows.go:118,124 |
| JetKVM waker | binary `valheim-waker`, `/userdata/valheim-waker/`, `/userdata/init.d/S99valheim-waker`, `/tmp/valheim-waker.{pid,log,json}`, asset `dist/valheim-waker-armv7.gz` | jetkvm.go:44,109,191-223 |
| Panel header | `X-VES` | auth.go:221, dom.js:148, upload.js:261 |
| Workflow file | `.github/workflows/build.yml` (its run number is the build number) | build.yml:3-5 |
| Reminder salt | `valheim-easy-server:` | reminder.go:28 |

The visible name changes in R3 only, all of it in stream T (9.3), in one commit so CI never sees half a rename:
- the panel: `index.html`'s title, the manifest's `name`/`short_name`, the texts in `internal/web/ui/assets/*.js`, and the assertion in `internal/web/web_test.go:214`;
- the Windows tray tooltip, and dialog titles (`appName` → "Easy Game Server"); main.go's messages;
- winres.json's visible fields: `FileDescription`, `ProductName`, `CompanyName` and the manifest `description` (shown in Task Manager, Startup apps and Windows' firewall pop-up);
- the two macOS admin prompts (desktop_darwin.go:143, net_darwin.go:71), the text inside downloaded world zips (worlds/library.go:606, the one change to `internal/worlds`), and the Discord key post (manager/waker.go:554);
- README and READ-ME-FIRST files; `CFBundleDisplayName` and `CFBundleName` in Info.plist (only new downloads get them; installed Macs keep their Info.plist, because updates replace only the binary);
- build.yml's Windows and Mac smoke tests, which look for the name in the panel.

The bundle folder name stays `Valheim Easy Server.app`, so a new download never makes a second app beside the old one, and Finder, Spotlight and Login Items keep showing "Valheim Easy Server" on the Mac. So on macOS, panel texts that send the owner to the app ("open … again from the Applications folder") name what is really there: "open Valheim Easy Server again from the Applications folder". Golden tests that hold these texts are updated in the same commit.

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
- **Branch artifacts don't update themselves (changed, R1):** the programs uploaded as artifacts by a build of any branch other than the default get `-X main.build=0`. Round one missed that a test copy built from `multigame` carries the same run-number scheme and would silently update itself back to the newest default-branch release in the middle of the owner's test. The CI steps that need a numbered build (the existing self-update test, 7.3) build their own copies with `-X main.build=$RUN_NUMBER` (and N+1) into a test-only folder that is never uploaded, on every branch. A build-0 copy has no updater, so the owner runs a candidate as 7.5 says, never by copying it into Applications.

---

## 2. Architecture

### 2.1 Packages and dependency rules

```
main.go                      flags (unchanged), registry import (games/all), tray
internal/game                the contract: types, interfaces, registry, Values (2.2)
internal/game/gametest       runs one adapter without the manager, against cmd/fakeserver or the real server (7.1)
internal/games/<id>          one adapter per game (section 5), with its own download resolvers;
                             games/all/<id>.go = one blank import each
internal/install             generic helpers: SteamCMD (shared, one at a time), downloads, unpacking,
                             the Temurin and .NET runtimes, Windows prerequisites, build files
internal/rcon, internal/a2s  Source RCON client; Steam A2S_INFO query
internal/worldstore          the world store for non-legacy games (2.8)
internal/servers             servers.json: types, load, save, migration (3.2)
internal/routerports         UPnP IGD, NAT-PMP, PCP, reachability (6.5)
internal/manager             instances, jobs, lifecycle, Discord, sleep and waking (section 4)
internal/web                 HTTP API and the embedded panel (6.1, 6.2)
cmd/fakeserver               stand-in servers with a personality per game (7.1)
cmd/ci-stubs                 CI-only stand-ins for GitHub releases and Discord webhooks (not shipped)
```

Unchanged packages keep their jobs: `config` (settings.json, Valheim's arguments and rules), `worlds` (Valheim's world library), `mods` (Valheim's BepInEx/Thunderstore), `logparse` (Valheim's log), `steamcmd` (SteamCMD runner), `platform`, `selfupdate`, `discord`, `waker`, `jetkvm`, `wol`, `records`, `stats`, `tray`.

Rules (checked by `TestImportRules` in internal/game, which reads each package's direct imports with `go list -f '{{.ImportPath}} {{.Imports}}' ./...`, test files excluded):
- `game` directly imports only the standard library, plus `worlds` and `mods` for shared data types (`worlds.World`, `worlds.Backup`, `worlds.Candidate`, `worlds.Choice`, `worlds.Placed`, `worlds.Orphan`, `mods.Info`). `mods.Info` is today's `manager.ModInfo` (`{mods.Mod; Version}`) moved into `internal/mods/info.go` in phase 0; the manager keeps `type ModInfo = mods.Info`, so its JSON doesn't change.
- `games/<id>` may import `game`, `install`, `rcon`, `a2s`, `worldstore`, `platform`, and (Valheim only) `config`, `logparse`, `mods`, `worlds`, `steamcmd`. Never `manager`, `web` or another `games/<id>`.
- `manager` imports `game` and the registry, never a `games/<id>` package (its test files may). `web` imports `manager` and `game` (types).
- **No hard-coded game ids** in `internal/manager/*.go`, `internal/web/*.go` and the panel shell (`internal/web/ui/assets/*.js`, not `assets/games/`), test files excluded: `TestNoGameIDs` fails if a string literal there is exactly a registered id (`"valheim"`, `'minecraft'`). The legacy game is found with `game.Legacy()`; where the manager must behave as today for Valheim, it asks `info.Legacy`, never the id.
- **Valheim's visible texts stay identical in R2** (the old panel shows them): generic messages fill in `Info.Short`, and where a template can't give today's exact Valheim wording ("The Valheim server isn't installed yet. Click Install first (it's free, about a 1 GB download).", "Installing the Valheim server", "Checking for Valheim server updates…"), the adapter supplies it in `Texts.Exact`. Existing tests that check these keep checking them. (The real download is about 2 GB, which `Info.DownloadMB` says for the new panel; the legacy sentence stays as it is.)

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
	CantRun Level = "no" // greyed out in Add a game with Info.NeedsText
)

type Presence string // how the app knows who is playing

const (
	ByName  Presence = "names" // who, by name (log or probe)
	ByCount Presence = "count" // how many, not who
	Unknown Presence = "none"  // no usable signal: the "still playing?" rule (4.6)
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
	OS          map[string]Level // by "goos/goarch" ("darwin/arm64") or GOOS ("windows"); the exact key wins; missing = CantRun.
	//                              Ignored in fake mode (Env.Fake set), so fake games run on every OS, Linux included (7.1).
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
	Legacy   any   // legacy instance only: the manager's access to settings.json and its injected parts (games/valheim.LegacyStore, 5.1)
	Fake     *Fake // EGS_DEV_FAKE_SERVERS / tests: install the stand-in instead (7.1)
}

// Locking: the manager has one lock (m.mu). It calls Monitor methods, Join, Fingerprint,
// OnlineTexter.Online, ModLister's PlayerMods/ModsCode/WorldMods and Store().Get with m.mu
// held, so adapter code must never call back into the manager or take a lock the manager
// holds. Store().Save, SetWorldMods, Plan, Installer and Stop run without m.mu (from jobs). Data the adapter needs from
// the manager comes as immutable snapshots (LegacyStore, 5.1) or as arguments; the `known`
// func passed to Watch reads a map the manager keeps under m.mu and never locks.

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
	// Pending says the record lacks After's result, so After runs again after a takeover
	// (Valheim: codeMods set and modsCode empty, as adopt() does today).
	Pending(rec Record) bool
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
	Version string    `json:"version,omitempty"` // an opaque string: "26.3", "1.4.6.1", "1.22.7"
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
	Console     bool          // the app keeps a pipe to the game's stdin for console stop/save lines (2.11)
	SaveLine    string        // Console games: the line that saves without stopping, sent before the app exits ("save")
	ReadyWait   time.Duration // how long a start waits for "online" before the fallback; the manager adds 5 min (4.3)
	Fingerprint string
	Record      Record // fields to keep in the run record: Server, Port, Password, Secrets, Extra (Played is the manager's, below)
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
	Played    Played    `json:"played"` // no omitempty, exactly as bed004b writes it; owned by the manager for every instance
	Server    string    `json:"server,omitempty"`
	Port      int       `json:"port,omitempty"`
	Password  string    `json:"password,omitempty"`
	// Never written for the legacy instance:
	Game     string            `json:"game,omitempty"`
	Instance string            `json:"instance,omitempty"`
	Name     string            `json:"name,omitempty"` // the game's Short name, for older builds' "is running" warning
	Ports    map[string]int    `json:"ports,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"` // RCON/REST passwords the run started with
	// Extra holds the adapter's own fields, read and written at the top level of the
	// file (Valheim: "noCrossplay", "modsCode", "codeMods"), so runtime.json keeps its
	// shape. MarshalJSON/UnmarshalJSON do the inlining.
	Extra map[string]json.RawMessage `json:"-"`
}

// Played is what a run started with, for the reminders (today's manager.played, moved).
// Mods == nil means "started by an older version"; non-legacy instances write {} rather
// than null, so the lastplayed.json load rule (Mods != nil) holds for them too.
type Played struct {
	Game     string            `json:"game,omitempty"`
	Mods     map[string]string `json:"mods"`
	Password string            `json:"password,omitempty"` // a hash, enough to notice a change
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

type Monitor interface { // every method is called with the manager's lock held (see Env)
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
	Line   string // the console or RCON command; a "console" step after a takeover returns ErrNoConsole
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
	Manual    string   `json:"manual,omitempty"` // extra words for the manual router steps (Windrose: "Your router must allow UPnP for NAT punch-through.")
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
// stand-in to Program() and writes fakeserver.json beside it (7.1). In fake mode Info.OS
// and the manager's OS check are ignored, and Program() gives the same layout on every
// GOOS (".exe" only on Windows), so fake games run on Linux, macOS and Windows.
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
| `m.opt.Installer` (a `steamcmd.Installer`: the real SteamCMD, `--dev-fake-server`'s, or a test's `steamcmd.Fake`), `InstalledBuild` | `Installer()` wraps `LegacyStore.Installer()` read at each call and converts `steamcmd.Progress` to `game.Progress`; `Build()` |
| `config.ServerExeName`, `paths.ServerExe` | `Program()` |
| `config.Settings.Validate` | `Validate` (field keys unchanged) |
| `ServerArgs`, `SteamAppId`, DYLD path, `mods.Env`, `prepareMods` (with `LegacyStore.ModsURL()` and `ModVersions()`), `UpdateOnStart` | `Plan` (Output `LogArg`, log `logs/server-….log`) |
| `argsKey` | `Fingerprint`, `Plan.Fingerprint` |
| `logparse.Parse/IsNoise/Tracker`, `checkOverdue`/`readyWait`, the PeerCount/JoinCode hold | `Watch` → Monitor (`Line`, `Tick`, `Event{Kind:"count", Hold:true}`); `readyWait` becomes the exported `valheim.ReadyWait`, read at each call by `Tick` and by `Plan` (`Plan.ReadyWait`) |
| `doStop` (Ctrl+C, resend 90 s, give up 4 min) | `Stop` → `[{Kind:"interrupt", Wait:4m, Resend:90s}]` |
| `exitHint` | `ExitHint` |
| `joinSettingsLocked`, `JoinInfo`, codeWait, "crossplay"/"late" | `Join`; `codeWait` becomes the exported `valheim.CodeWait`, read at each call |
| `onlineMessage.text`, mods lines | `OnlineTexter.Online` (byte-identical) |
| `makeModsCode`, `codePending`, adopt's rerun | `Afterer.After`, `Afterer.Pending`; `codePending` becomes `serverRun.afterPending` |
| `playerModsLocked`, `modsCodeLocked`, `Worlds().Mods`, `SetWorldMods` | `ModLister` |
| `worlds.Library` (Valheim formats) | `Worlds()` = a thin wrapper over the existing Library |
| `config.ReadList/WriteList` | `Lister` |
| `config.WorldOptions` (preset, modifiers, keys, Off) | world-scope schema; stored where it is today (settings.json `worldOptions`) |
| `runInfo` | `Record` (`played` is the typed `Record.Played`); `noCrossplay`, `modsCode`, `codeMods` in `Extra` |
| `stoppedText`, `dailyText` "vikings" | generic code with `Texts` (identical output for Valheim) |

The manager keeps what isn't game-specific: jobs, adoption, attach/onExit, the tail, the logs ring, crash restarts, records archiving, players, reminders (`played{Game, Mods, Password}` with `Short` and `ModLister`), the Discord queue, sleep and waking.

### 2.3 Install

`internal/install`:

```go
// SteamCMD installs a Steam app with the shared SteamCMD in dirs.SteamCMD. Only one
// SteamCMD runs at a time in the whole app (it updates itself and keeps one package cache):
// an install or repair waits, showing "Waiting for the <game> download to finish…".
// An update-on-start never waits (see below).
func SteamCMD(app SteamApp, dirs game.Dirs, out func(string)) game.Installer
type SteamApp struct {
	AppID  string  // "1169370"
	Branch string  // "" = public
	Name   string  // "Necesse" (progress and error texts)
	SizeGB float64 // for "not enough free disk space for the Palworld server (it needs about 6 GB)"
}
func SteamBuild(serverDir, appID string) (game.Build, bool) // steamapps/appmanifest_<id>.acf

// Hash is a published checksum. Sources publish different ones: Mojang sha1, Adoptium
// sha256, Vintage Story md5, .NET sha512.
type Hash struct {
	Algo string // "md5", "sha1", "sha256", "sha512"; "" = none
	Hex  string
}

// Download fetches url into a temp file next to dest, checks the hash, then renames.
// Unzip/Untar refuse unsafe paths, keep or set exec bits.
func Download(ctx context.Context, url string, hash Hash, dest string, step func(game.Progress)) error
func Unzip(zipPath, dir string, exec func(name string) bool) error
func Untar(tgzPath, dir string) error

// Runtimes go into the instance's own Dirs.Runtime and are never shared, so
// FindByExe on the runtime's path finds only this instance's server. Each returns the
// path it found by searching the unpacked folder for bin/java[.exe] (on macOS
// Temurin's is under <jdk>/Contents/Home/bin/java) or dotnet[.exe]: callers use the
// returned path, never a guessed one.
func Temurin(ctx context.Context, major int, dir string, step func(game.Progress)) (java string, err error)
func DotNet(ctx context.Context, channel string, dir string, step func(game.Progress)) (dotnet string, err error)

// Windows prerequisites, installed system-wide with one admin prompt each; the
// install waits for the prompt's answer (platform.RunElevatedWait).
type Prereq struct {
	Name      string      // "XNA Framework 4.0"
	Installed func() bool // registry check
	URL       string
	Hash      Hash
	Args      []string // silent install arguments
}
func EnsurePrereq(ctx context.Context, p Prereq, step func(game.Progress)) error

func WriteBuild(serverDir string, b game.Build) error // .egs-build.json, for non-Steam installs
func ReadBuild(serverDir string) (game.Build, bool)
```

`internal/install` keeps only these generic helpers. Each game's own version and download resolver (Mojang's piston-meta, terraria.org, Vintage Story's stable.json) lives in `internal/games/<id>`, with its recorded answers in that package's `testdata/`, so the game's stream owns the facts it must check. Temurin (Adoptium API) and DotNet (Microsoft's release metadata) are shared runtimes and stay in `install`.

`internal/steamcmd` gains `AppID`, `Name` and `SizeGB` fields on `SteamCMD` (empty = today's Valheim values, so Valheim's install is byte-identical), `InstalledBuildFor(dir, appID)`, and a package-level lock:
- **The lock covers the whole `Install`**, including `ensureSteamCMD`'s first-time download and unpack into the shared folder, not just `run`.
- **The success line is matched against the instance's own app id** (today's `reSuccess` is built from the const `AppID` "896660", so any other app would end with "SteamCMD finished without confirming the install").
- **A start never waits for another game's download.** An update-on-start takes the lock with `TryLock`; when another game's install holds it, the update is skipped and the start goes on with the installed build, through today's path: the warning activity "Couldn't check for server updates (<Satisfactory> is downloading). Starting the version that's installed." The next start checks again. This holds for every game, from the panel, Discord, a wake or a resume. Installs and repairs still wait for the lock.

The literal `"Server files are up to date."` stays (server.go:242 compares it). Every SteamCMD game passes `UpdateOnStart` by default: SteamCMD only downloads when there's a newer build, so the installed build is the newest at every start unless another download was running (this replaces round one's separate "compare buildids" step for Palworld).

### 2.4 Plan, launch and output

The manager runs every start the same way:

1. checks (4.3), then `Plan(ctx, req)`; the adapter writes its config files and updates first when `req.Update`;
2. `platform.Start(StartOptions{Exe, Args, Dir, Env, Stdout: f, Stderr: f})`, where `f` is `Plan.LogPath` opened for append when `Output == Capture`, else nil (a file, never a pipe, so the game's output survives the app exiting);
3. `Console` true: the same, plus a stdin pipe the app keeps (2.11);
4. attach: write the run record, follow `Plan.LogPath`, feed each line to `Monitor.Line`.

Rules for adapters:
- **Launch the real server process directly**, never a launcher, `.bat` or script that starts a child (Palworld and Satisfactory: the `-Cmd` shipping exe; Project Zomboid: its bundled java with the classpath and flags read from `StartServer64.bat`; Core Keeper: `CoreKeeperServer.exe`; Terraria on macOS: `TerrariaServer.bin.osx` with the script's `DYLD_LIBRARY_PATH`). Then one PID is the server: usage, Ctrl+C, kill and adoption all work on it. Whatever environment the skipped script sets goes into `Plan.Env`. verify per game.
- **Own log file per run** for new games: `Capture` by default (the app's file is never rotated mid-run). `GameLog` only where the game writes nothing useful to stdout; the tail then reopens the path when it no longer names the open file (rotation) and opens it with `platform.OpenShared` (Windows `FILE_SHARE_DELETE`, so the game can rotate it), and it also appends every line it reads to the run's own file in `Dirs.Logs` (named from `Record.Launched`), so the Log page's download, the records backfill and pruning work the same for every game.
- Environment: the app's environment plus `Plan.Env`. Valheim's `SteamAppId`/DYLD/Doorstop variables stay in Valheim's plan only.
- Logs are pruned per instance: the 30 newest `<instance>-*.log` in `Dirs.Logs`.

### 2.5 Ports as role specs

An instance's ports are `servers.json` `instances[].ports` (role → port), set once when the game is added:

- For each role without a `Tie`, start at `Default`; if any instance already reserves it (servers.json ports, plus Valheim's `settings.Port` and `Port+1`), or `platform.PortInUse(proto, port)` says another program holds it, try the next one, up to `Default+100`. Tied roles follow their base.
- **Ports never move after that.** A clash at start fails the start with `ActionError{Action: "pick_ports"}` ("Port 7777 (TCP) is being used by another program: <name>"). The panel's button asks the app to pick new free ports for the roles the owner didn't choose, then updates the firewall rules and router mappings. A port the owner typed in Settings (`portsChosen`) is never picked again: its clash says "change it in Settings".
- Valheim keeps its own rule unchanged: `UDPPortInUse(s.Port)` at start, the port from settings.json. Its two ports are in the reservation table.
- Roles with `Public: false` (Minecraft and Zomboid RCON, Palworld REST) get no router mapping and are never in a firewall rule's ports. None of these servers can bind them to 127.0.0.1 alone (Minecraft's RCON binds to `server-ip`, which also binds the game port; Palworld has no bind option), so they listen on every interface. What protects them: a new random password for every run (`Record.Secrets`), and on Windows the port-scoped allow rule plus the default inbound block, which keep them off the home network. On macOS the firewall is per program, so the allowed java also exposes RCON to the home network; its random password is the protection, and the game's setup checklist says so.

### 2.6 Readiness, presence and probes

- **Ready:** an `"online"` event from the Monitor (a log line), or from `Tick` as a fallback (Valheim: 10 minutes after Steam login), or from `Monitor.Probed` when a probe answering `Up` is enough for that game (Palworld, Satisfactory): returning `"online"` from `Probed` is how an adapter says so.
- **Presence:** `ByName` (joins and leaves from the log or a probe's names), `ByCount` (a count from a probe or the log), `Unknown`.
- **Unproven log presence counts as unknown.** No CI cell has a player join, so a wrong join pattern would leave the count at 0 forever, and auto-stop or idle sleep would cut friends off. So for a game whose presence comes from log lines only (no probe that counts: Terraria, Necesse, Vintage Story, Core Keeper, Windrose, Enshrouded without A2S), the manager treats presence as `none` (4.6's rule, which never stops a game silently, and blocks idle sleep) until this install has seen that game's first `join` event, kept in servers.json as the instance's `presenceSeen` time. The manager needs no list of games for this: an instance's presence counts as proven when it is the legacy instance (Valheim's lines are proven by years of use, so its auto-stop is the decided hour from R3's first start), or its adapter implements `Prober` and the latest probe answered with a count, or `presenceSeen` is set. Stage `ready` needs a real join and leave log excerpt in the adapter's `testdata/` with its source in FACTS.md (8.2). Where it's cheap, a probe count cross-checks the log (A2S for Steam servers).
- **Probes** (`Prober`) run every 5 s from launch until ready (failures then count for nothing), once at ready, and every 30 s while ready. Their answer goes through `Monitor.Probed`, which reconciles: the probe's count is authoritative; names the probe lists that the log missed are joined, names it doesn't list leave. A probe that fails three times in a row is shown as a warning on the game's page; the log keeps working alone.
- A count above zero from the server itself (`Hold`) resets the game's auto-stop clock and holds the computer awake 15 minutes, as Valheim's PeerCount does today.

### 2.7 Stop chains

`Server.Stop(rec)` returns the steps, built from the run record alone (so it works after a takeover). The manager runs them in order:

- send the step (console line through the app's stdin pipe, RCON command, call, or Ctrl+C/SIGINT); `ErrNoConsole` (no pipe: the game was taken over after an app restart) or a refused connection moves to the next step at once;
- wait up to `Wait` for the exit, sending again at `Resend`; progress text from `Monitor.State().LastSave` ("World saved. Shutting down…"), as today;
- when the last step's wait ends: `ActionError{Action: "force_stop"}`, as today. **Killing is never automatic**: only the owner's Force stop kills (unchanged `ForceStop`).

Stops of several games run in parallel goroutines. On Windows the Ctrl+C sends still go one at a time through `consoleMu` (about 1 s each); the saving itself overlaps. The app is a GUI program (`-H=windowsgui`) with no console of its own; each game gets its own hidden console from CREATE_NO_WINDOW, so a Ctrl+C reaches only that game. A Windows program built as a GUI program has no console, so Ctrl+C can't reach it (`Interrupt` fails at `AttachConsole`): each adapter whose stop ends in "interrupt" must verify its server is a console program (Enshrouded, Core Keeper, Windrose especially); if it isn't, the chain needs the game's own way to quit, or the adapter documents that only its autosave protects the world and says so in the stop's `Say` text.

### 2.8 Worlds

- **Valheim** keeps `internal/worlds.Library` unchanged; `games/valheim` wraps it as `game.Worlds`.
- **Every other game** uses `internal/worldstore`, which reuses the format-independent ideas of the Library (zip backups with a fingerprint retry while the server saves, all-or-nothing replace with set-aside and undo, import from a zip or folder with safe paths) in new code. `internal/worlds` is not changed, except the visible name in the note inside downloaded world zips (library.go:606, R3, stream T).
- Worlds normally live under `Dirs.Saves`. One exception: Satisfactory's dedicated server keeps its saves in the Windows user's `%LocalAppData%\FactoryGame\Saved\SaveGames\server` whatever its install folder (5.10), so its `Format.Dir` points there, and removing the game leaves them in place.

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

App-wide settings (idle sleep, auto-update, start after wake, the main Discord channel, router ports) are on the App settings and Discord pages, not in any game's schema.

**Start when the app starts** is per game (changed): settings.json's `startWithApp` keeps today's meaning, "start Valheim when the app opens", and shows on Valheim's Settings tab; every other game has servers.json `startWithApp` (default off), on its own Settings tab. So the owner never sees several servers launch at login that they didn't choose one by one. Today's rule stays for all of them: nothing is started by this when the app restarted for an update.

### 2.10 Platform support levels

`Info.OS` by `goos/goarch` or GOOS: `yes`, or `no` (greyed out with its `NeedsText`). Only the owner's Windows PC and Intel Mac Pro run the app, so there is no Rosetta detection (changed: round one's `intel` level is gone). On macOS: Valheim, Minecraft, Terraria and Necesse are offered; Terraria's and Necesse's Mac servers are x86_64 builds, so they are `darwin/amd64: yes` and `darwin/arm64: no` ("Needs an Intel Mac"; Necesse's architecture is verified by its real-server check, 5.4); the other seven are "Needs Windows": no Mac install code paths, no Mac CI cells, not on the Mac checklist. A game whose instance exists but can't run here (data copied from another computer) shows its page with the reason and no Start button. Fake mode ignores all of this (2.2 `Fake`).

### 2.11 Console games (changed: no keeper)

Some servers save only on a console command typed on stdin (Terraria `save`/`exit`; Necesse and Vintage Story prefer their console). Round one planned a "keeper" broker process that outlives the app, so console lines would work after the app restarts. It is cut this round: it was the hardest cross-platform part of the spec (a copy of the app binary, a hook before flag parsing, a token protocol, inherited handles, a state file), it served three games, and no decision asks for taking those games over across an update (only Valheim, which needs no console). The real risk is covered more simply:

- **The app holds the pipe.** `platform.Start` with `Stdin` = the read end of a pipe the app keeps the write end of (`serverRun.stdin`). The game is also given a duplicate of the write end as an inherited handle (`StartOptions.Inherit`: Unix `ExtraFiles`, Windows `AdditionalInheritedHandles`), so it never sees end-of-file when the app exits: its console simply goes quiet.
- **Save before the app exits.** Before the app exits with a console game running (an update, Quit, "Close app", or the Mac's quit), it writes the game's `Plan.SaveLine` (Terraria `save`; Necesse `save`; Vintage Story `/autosavenow`, both verify) and waits up to 10 s for the saved line.
- **Updates wait for players.** `busyLocked` (the app update) is also true while a console game that is running has players on (or may have: its presence is `none` or not yet proven, 2.6), so an automatic update waits (it retries every 10 minutes, as today) and doesn't cut the game's console while people play. A manual update from the panel goes ahead after warning that the game's console will be gone until its next start.
- **After a takeover** the run has no pipe: "console" stop steps return `ErrNoConsole` and the chain moves on to Ctrl+C, whose `Say` text says what is at risk (Terraria: "Terraria saves every 10 minutes and when the last player leaves; anything since then is lost."). Terraria's server saves every 10 minutes and when the last player leaves (its changelog), so the loss is bounded.
- **Unix:** `platform.Start` makes the game its own process group with `Setpgid`, exactly as `StartDetached` does (never `Setsid` with it: the two together fail at fork). The app never uses `signal.Ignore` for SIGINT or SIGHUP, because an ignored signal stays ignored across exec and the game's interrupt step would never work; it uses `signal.Notify` (main.go already does).
- **Windows:** the app has no console; each game gets its own hidden console from CREATE_NO_WINDOW, and the app never calls `SetConsoleCtrlHandler(NULL, TRUE)` before starting one (that would be inherited, and the game would ignore Ctrl+C).
- If a game's real-server check shows that Ctrl+C after a takeover loses more than its autosave interval, the keeper comes back in a later round for that game.

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
	Inherit []*os.File // extra handles/fds the child keeps (the stdin pipe's write end, 2.11)
}
func Start(o StartOptions) (*Proc, error) // like StartDetached (consoleMu, hidden, Setpgid only on Unix, own hidden console on Windows)

// PortInUse reads the system's tables (never binds, so no firewall prompt) and
// says which process holds the port, 0 when unknown.
func PortInUse(proto string, port int) (inUse bool, pid int)  // Windows: GetExtended{Tcp,Udp}Table OWNER_PID; mac: lsof -nP; Linux: bind test
func OpenShared(path string) (*os.File, error)                  // Windows FILE_SHARE_READ|WRITE|DELETE
func DefaultGateway() (net.IP, error)                           // Windows GetAdaptersAddresses with gateways; mac `route -n get default`
func KnownFolder(name string) string                            // "appdata", "localappdata", "documents", "savedgames", "home", "appsupport"
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) // Windows ShellExecuteEx runas + wait; mac osascript admin
func ProcName(pid int) string                                    // for "used by <program>"

// Firewall rules for games other than Valheim. Names start with "Easy Game Server - "
// (old builds' blanket removal only matches "Valheim Easy Server - *"), carry the
// instance id and a fingerprint of the program, and are scoped to the public ports.
// A game with no public ports (Core Keeper, Windrose: Steam relay or NAT punch-through on
// ports the game picks) gets one program-wide allow rule instead, as Valheim's is, so the
// hidden server's first listen never raises Windows' firewall pop-up.
type GameRule struct { Instance, Program string; TCP, UDP []int; AnyPort bool }
func GameFirewallReady(r GameRule) bool
// FixFirewallAll runs today's Valheim script (byte-identical, firewallScript) followed
// by the games' lines, in one admin prompt. Long scripts use the hashed temp-file path.
func FixFirewallAll(serverExe, appExe string, wake bool, games []GameRule) error
```

Today's `FixFirewall` stays a synchronous call, not a job (4.2); R3's firewall fix calls `FixFirewallAll` the same way.

The games' part of the Windows script: one `Remove-NetFirewallRule -DisplayName 'Easy Game Server - *'` (so rules of removed games and old ports go too), then per rule `netsh $fw delete rule name=all "program=<program>"` (Windows' own block rules from a dismissed pop-up; the program is private to the instance) and one `add rule "name=Easy Game Server - <instance> <proto> (<sha8>)" dir=in action=allow "program=<program>" protocol=<TCP|UDP> localport=<ports>` per protocol (`AnyPort`: one `add rule … "program=<program>"` with no protocol or port). Removing a game therefore needs no admin prompt: its rules go at the next fix. macOS: `socketfilterfw --add` and `--unblockapp` per program (the macOS firewall has no ports). Because one elevated script carries Valheim's fix and the games', the Windows CI job runs `FixFirewallAll` for real (the runner is an administrator) with a fake game rule, checks with `Get-NetFirewallRule` that Valheim's rules and the `Easy Game Server - <id>` rules exist with the expected program and ports, then removes them (7.2).

---

## 3. Data on disk

### 3.1 Layout

```
<data>/                              the data folder (unchanged rules, main.go resolveDataDir)
  settings.json  runtime.json  players.json  records.json  lastplayed.json  stats.json
  history.json   activity.json  modscodes.json  jetkvm.json  jetkvm-key.pem
  update.json  .instance.json  .instance.lock                     ← legacy, unchanged (1.3)
  server/  saves/worlds_local/  backups/  logs/  steamcmd/  uploads/   ← Valheim's, unchanged
  servers.json                       NEW in R3: the instances and app-wide new settings (3.2)
  servers.json.bak                   the previous good copy
  games/<instance>/                  one per non-legacy instance
    server/                          installed server
    runtime/                         private Java/.NET
    saves/                           worlds and the game's own data
    backups/<world>/<stamp>_<reason>.zip
    logs/<instance>-YYYY-MM-DD_HH-MM-SS.log   (30 kept)
    uploads/
    run.json                         its run record (0600)
    players.json  records.json  lastplayed.json  history.json  stats.json   same shapes as the legacy ones
```

The shared SteamCMD stays in `<data>/steamcmd`. `logs/app.log` stays the app's log. Satisfactory's worlds are the one thing outside the data folder (`%LocalAppData%\FactoryGame\Saved\SaveGames\server`, where its server always keeps them; 2.8, 5.10).

### 3.2 servers.json

Created by R3 on its first start (R2 never writes or needs it, 3.4). Written 0600 with `config.WriteFileAtomic`, after copying the current file to `servers.json.bak`. Instances this build doesn't know are kept as raw JSON, and the file, `app` and each instance keep keys they don't know (an `Unknown map[string]json.RawMessage` filled and written back by one shared helper), so an R3.x build that writes the file loses nothing a newer one put there.

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
      "startWithApp": false,
      "presenceSeen": "2026-12-03T19:40:00Z",
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
	Hidden          bool                   `json:"hidden,omitempty"` // legacy only: "removed" from the panel (I1: stopped, unstarred, never started)
	Settings        game.Values            `json:"settings,omitempty"`
	World           string                 `json:"world,omitempty"`
	Worlds          map[string]game.Values `json:"worlds,omitempty"`
	Ports           map[string]int         `json:"ports,omitempty"`
	PortsChosen     []string               `json:"portsChosen,omitempty"`
	State           game.Values            `json:"state,omitempty"`
	AutoStopMinutes *int                   `json:"autoStopMinutes,omitempty"` // nil = 60 (R3); 0 = off
	StartWithApp    bool                   `json:"startWithApp,omitempty"`    // non-legacy only (2.9)
	PresenceSeen    time.Time              `json:"presenceSeen,omitzero"`     // first join seen on this install (2.6)
	Discord         *Discord               `json:"discord,omitempty"`         // nil = the defaults below
	Unknown         map[string]json.RawMessage `json:"-"`
}
type Discord struct {
	On       bool   `json:"on"`
	Webhook  string `json:"webhook,omitempty"` // "" = the main channel (settings.json discordWebhook)
	Stopped  bool   `json:"stopped"`
	Daily    bool   `json:"daily"`
	Password bool   `json:"password"`
}

// A nil Discord (every game just added) means: {On: true, Webhook: "", Stopped: true,
// Daily: true, Password: false}. With no main channel and no own webhook nothing can be
// posted, which the Discord page shows. So a new game posts in the main channel, as
// decided, without the owner touching anything (tested in D).

type Status int
const (OK Status = iota; Missing; Damaged; Newer)

func Load(path string) (File, Status, error) // Damaged: falls back to servers.json.bak (3.4)
func Update(path string, change func(*File) error) error // load, change, back up, write; refuses unless OK
```

For the legacy instance only `discord.webhook` is used (Valheim's own channel); on/stopped/daily/password stay settings.json's `discord`, `discordStopped`, `discordDaily`, `discordPassword`. Its settings and worlds stay in settings.json; its `ports` come from settings.json `port` (game) and `port+1` (query) and are never written here.

### 3.3 Run records

`game.Record` (2.2). For the legacy instance it is runtime.json, marshalled so the file is key-for-key what bed004b writes: the generic fields (with the typed `played`), then `Extra` inlined (`noCrossplay`, `modsCode`, `codeMods`), never `game`/`instance`/`name`/`ports`/`secrets`. `TestRuntimeJSONCompat` decodes a new file with a copy of bed004b's `runInfo` struct and the other way round, and compares values.

### 3.4 Migration

All of it is O(1) and runs in `manager.New` (I5).

- **R2 (changed):** servers.json is neither read nor written. The legacy instance is built from the registry (`game.Legacy()`) and settings.json, exactly as today's data says; other games' processes are found by scanning `games/*/run.json` (foreign awareness, 4.4). So R2, the build the owner soaks, carries no new write code at all.
- **R3, first start:** servers.json `Missing` and no `games/*/` folder → create `{schema:1, app:{stars:[<legacy id>]}, instances:[{id, game, legacy:true, added:now}]}`. Defaults come from nil fields: `autoStopMinutes` nil → 60 for every instance (Valheim's auto-stop is switched on here, where the panel shows it, carryover fix 10), `activeOpen`/`inactiveOpen`/`routerPorts` nil → defaults, `discord` nil → 3.2's defaults. Valheim's files are untouched.
- **Damaged** (invalid JSON): load `servers.json.bak` instead, log it and add a warning activity ("The list of games was damaged; the app used its backup from <time>."); the damaged file is kept as `servers.json.damaged-<stamp>` and the backup is written back on the next change. Only when both are unreadable: **Valheim-only safe mode** (8.3).
- **Newer** (`schema` > 1): Valheim-only safe mode; the file is never overwritten.
- **Missing but `games/` exists** (deleted by hand): `servers.json.bak` when it's valid; else the list is rebuilt from the `games/*/` folders whose name is a registered game id (default settings, not starred, ports picked again only for roles whose default is free), with a warning activity. No restore route or banner is needed.

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
| R2 → R1 | activity entries with `game`, jobs with `game` in update-time status (R2 writes no servers.json) | ignores them; adopts Valheim from runtime.json (same shape) | I2, I4; unknown JSON keys are ignored by `json.Unmarshal` |
| R3 → R2 | servers.json, other games running (games/*/run.json), per-game files, stars, Valheim hidden or with its own channel, Valheim auto-stop on, multi-command waker, router mappings, new firewall rules | R2 ignores servers.json (a hidden Valheim shows again in the old panel); lists running other games as foreign from their run.json: keeps the computer awake and blocks idle sleep while their processes live, shows "Minecraft is running, started by a newer version of the app; this version can't control it" (the name comes from the run record's `name`), never touches their files; posts Valheim in the main channel; doesn't auto-stop Valheim; reinstalls its own single-/valheim waker 90 s after start (and until then starts Valheim only for wake packets without a game or for the legacy id, 6.4); doesn't renew router leases (they lapse after 2 h); leaves `Easy Game Server - *` firewall rules alone | R2 never writes servers.json (3.4); foreign-instance awareness and the wake-packet game check are in R2 (8.1) |
| R3 → R1 (a computer that was off during R2, so `exe.old` is R1) | as R3 → R2 | R1 has no foreign awareness: other games keep running unseen and idle sleep may cut their players off, until the next build installs (the failed one is skipped, as today) | happens only if R3 fails to start on such a computer; the direct R1 → R3 upgrade is tested at the R3 gate (8.2) |
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
	lastPlayed *game.Played
	recs       recordsData
	logs       *ring[LogLine]    // 3000 lines, made at the first run
	uploads    map[string]*upload
	localCands []worlds.Candidate
	history    *stats.History    // nil for the legacy instance (it uses Manager.history)
	byPlayers  *stats.ByPlayers
	emptySince time.Time         // auto-stop clock (4.5)
	ask        askState          // the "still playing?" rule (4.6)
	cache      instCache         // installed, build, firewall, freeDisk for non-legacy instances (4.8)
}

type serverRun struct {           // today's serverRun, generalised
	proc        *platform.Proc
	rec         game.Record      // was info runInfo
	mon         game.Monitor     // was tracker logparse.Tracker
	stdin       *os.File         // Console games: the app's end of the game's stdin pipe; nil after a takeover (2.11)
	autoStopped bool             // this run is being stopped by auto-stop or the ask rule (4.5)
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
	afterPending bool            // Afterer still running: the online post waits (was runInfo.codePending)
	probeFails  int
	mappings    []routerports.Mapping
}
```

Manager keeps app-wide things: settings (settings.json), servers (servers.json, from R3), `instances map[string]*instance` + `order []string`, `legacyID`, the app job slot (Sleep only, 4.2), `jobSeq` (one sequence for every job, so ids never repeat across instances: the old panel tells jobs apart by id), the activity ring, `history`/`byPlayers` (machine + Valheim), the Discord queues, network/checks caches, the app update, the waker, `machineIdleSince` (was `idleSince`), `keepAwakeUntil`, `wakeHold`, `resumeSet []string`, `wakeAsked time.Time`, `wakeTags map[string]string`, `lan []net.IP` (cached, 4.8). The waker keeps its own `wk.busy` lock, as today.

Lifecycle features of other streams (auto-stop, router ports, Discord, the ask rule, waking) hook in through one observer list that M3's files (server.go, jobs.go) and the sample loop (`sampleLoop` in power.go, which A owns in R3) call at fixed points placed by phase 0', so no other stream edits server.go or jobs.go, and A keeps the calls in power.go where phase 0' put them (carryover fix 17; frozen in phase 0', 9.3):

```go
// internal/manager/hooks.go: each method is called with m.mu held and must not block.
type observer interface {
	onReady(inst *instance)
	onEvent(inst *instance, ev game.Event)
	onExit(inst *instance, exit *ExitInfo, auto bool) // auto: stopped by auto-stop or the ask rule
	onTick(now time.Time)                            // every sample tick, before the idle-sleep check
	onJob(inst *instance, job *Job)                  // a job started or finished (inst nil for the app slot)
	onTakeover(inst *instance)
	onWake(cause string)                             // "purpose", "tagged", "untagged"
}
```

Tests reach the legacy instance with `m.legacy()`. Existing tests are updated mechanically, and only with these replacements (the R2 gate's allowed diffs): `m.run` → `m.legacy().run`, `m.job` → `m.legacy().job`, `run.info` → `run.rec`, `runInfo{…}` → `game.Record{…}`, `run.info.codePending` → `run.afterPending`, `run.tracker` → the Valheim monitor through `run.mon.(*valheim.Monitor)`, `readyWait` → `valheim.ReadyWait`, `codeWait` → `valheim.CodeWait`, `m.known`/`m.recs`/`m.uploads` → `m.legacy().known`/`.recs`/`.uploads`, `m.idleSince` → `m.machineIdleSince`, `m.handleLine(run, …)` → `m.handleLine(m.legacy(), run, …)` (only if its signature gains the instance); their assertions don't change. `m.resumeOnWake` stays as it is in R2 (R3's `resumeSet` replaces it in A's stream, whose tests then say which games resume). Tests that set `m.opt.Installer` or `m.opt.ModsURL` after `New` keep working unchanged, because the Valheim adapter reads them through `LegacyStore` at each call (5.1).

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

- **One job slot per instance plus one app slot.** An instance action is refused with `BusyError` while that instance's job runs or the app job runs.
- **The app slot runs only Sleep** ("hibernate"); it is refused while any instance job runs, as Sleep is today. Removing a game is a job in that game's own slot ("remove"), so it blocks nothing else.
- **Not jobs, as today (changed):** the waker's install, removal and upkeep stay synchronous calls guarded by the waker's own `wk.busy` lock (waker.go:213-229), so `POST /api/jetkvm/install` still answers with field errors and the waker view in the same response; firewall fixing (`FixFirewall`, `FixFirewallAll`) stays a synchronous call. None of them shows in `Status.job`, none refuses a game's Start, and none is refused while a game installs. A star-triggered upkeep that finds `wk.busy` set retries 30 s later (6.4).
- `Job` gains `Game string json:"game,omitempty"` (the instance id).
- `Status.job` (legacy, top level) is whichever has the higher id, finished or not: the app slot's current-or-last job or the legacy instance's current-or-last job. With only Valheim that is exactly today's `m.job`, so the old panel (which reports a job when `job.finished && job.id > lastJob`) and `waitJob` still see a finished Sleep job and its result. `sleptSince` reads the app slot's last job. `Status.jobs` lists every unfinished job and each slot's last finished one.
- **Idle clocks:** a job resets its own instance's `emptySince`. It holds the computer awake while it runs (idle sleep waits for it, as today), and resets `machineIdleSince` when it starts, except a stop started by auto-stop or the ask rule (4.5).
- `busyLocked` (app update) is true while any job in any slot runs, any upload is younger than 2 h, or a running console game has players on (2.11).
- `m.app.updating` still blocks every new job in every slot.

### 4.3 Starting a game

`StartGame(id, o)`:

1. refuse if foreign, hidden, disabled, not supported on this OS (ignored in fake mode), not installed, already running, or busy;
2. `Validate` (field errors as today);
3. **memory check** unless `o.Memory == "anyway"` or `Source == "restart"`: `need = info.MemoryMB`, `free = machine total − in use` from the latest sample. If `need > free`, fail with
   ```go
   &ActionError{Msg: "There may not be enough memory to start Palworld: it needs about 10 GB and 6.2 GB is free.",
       Action: "memory", Data: MemoryShort{NeedMB: 10240, FreeMB: 6349, Running: []RunningGame{{ID: "valheim", Name: "Valheim", MemMB: 3170}}}}
   ```
   (`ActionError` gains `Data any`, written as `"data"` by `web.fail`). `"stop:<id>"` stops that instance first (inside this job), then starts;
4. job "start": if `FindByExe(srv.Program())` finds it already running (started before this app, say), adopt it ("The server was already running.", as today); else `Plan` (the adapter updates first when `updateOnStart` is on, never waiting for another game's download, 2.3, and says when Cancel stops working); the ports check (2.5; the legacy instance keeps today's check inside its Plan: `UDPPortInUse(port)` with today's message); launch (2.4); attach; wait for ready (as today: `Plan.ReadyWait + 5 min` → "running but hasn't said it's ready"; Valheim's `ReadyWait` is today's `readyWait`);
5. after ready: open the router ports when `Join.Router` (6.5); the Discord online post (6.3).

`AutoRestart` after a crash, start-with-app (2.9) and the resume after waking use the same path. Crash budget (3 in 15 minutes) and the restart delay are per instance. A hidden legacy instance is never started (I1); its Start answers "Valheim is removed from the panel. Add it again first.".

**Installed and build after an install:** the install job refreshes its instance's `instCache` (installed, build) before it sets `job.Finished`, so a Start straight after an install job is never refused as "not installed", and the existing test helper that checks `Build` right after `waitJob` keeps passing (4.8).

### 4.4 Adoption

At `Start()`, for each non-foreign instance, in order, the same algorithm as today's `adoptRunning`:

1. load its run record (legacy: runtime.json; others: games/<id>/run.json);
2. `OpenProc(rec.PID)`, start time within ±2 s → adopt with the record; else delete the record;
3. else `FindByExe(srv.Program())` → adopt the first match with a record rebuilt from the instance's saved settings (the legacy instance exactly as today). Programs are private per instance (2.3), so a match is this instance's server;
4. on adoption, as today's `adopt` and `attach` do:
   - `Watch(rec)` and replay the log (`replaying`);
   - an adopted record with `announced` set but `posted` zero and no `postError` (written by an older build) gets `posted = launched`;
   - if the server implements `Afterer` and `Pending(rec)` is true (Valheim: `codeMods` set, `modsCode` empty, because the previous app closed before making the mods code), set `afterPending` and run `After` again before the online post;
   - the online post is sent only if the record says it wasn't;
   - console games have no stdin pipe now (2.11); router ports are mapped again; the `onTakeover` hook runs.

   `TestAdoptRerunsAfter` pins it: adopt a record with `codeMods` set and `modsCode` missing, and the online post includes the code.

Foreign instances are never adopted. Their `run.json` is checked every minute (PID alive and start time) for 4.7's awareness. In R2, which has no servers.json, every `games/*/run.json` is treated this way (3.4).

### 4.5 Auto-stop (1 hour, all games)

- Setting: `instances[].autoStopMinutes` (nil = 60, 0 = off), on each game's Settings tab: "Stop when nobody has played for [60] minutes".
- Clock: `emptySince` is reset whenever the game becomes ready, anyone joins, the count is above 0 (log, probe or `Hold`), or a job of that instance runs. In each sample tick, **auto-stop is evaluated before idle sleep** (`onTick`): a ready game with presence `names` or `count` (and its presence proven, 2.6), count 0, no job, and `now − emptySince ≥ minutes` is stopped through its stop chain as job "stop", marked `autoStopped`. That stop job and the exit it causes do not reset `machineIdleSince`; they only hold the computer awake while the job runs. So with the defaults (both 60 minutes) the last player leaving at T means the game stops at T+60 and the computer sleeps right after, not at T+120 (a fake-clock test in A pins it, with an empty resume set).
- Texts are built from the instance's minutes: "an hour" at 60, "2 hours" at 120, otherwise "<n> minutes" ("90 minutes"; tested at 30, 60 and 120). Activity: "Minecraft stopped: nobody played for an hour." Discord stopped post, reason `stopIdle`: "🔴 **X** has stopped: nobody played for an hour. Start it again with /minecraft or from the control panel." (the /command part only when it's starred and the waker is on).
- The game's Home shows "Stops at 21:40 if nobody joins".
- Presence `none` (and unproven log presence, 2.6): 4.6.
- R2 doesn't auto-stop; R3 does, for Valheim too.

### 4.6 The "still playing?" rule (Windrose and any game without a usable signal)

Used by a game whose presence is `none`: Windrose until a real session's log shows usable join and leave lines (5.11), any other game whose presence turns out unusable (decided: the same rule), and a game with log-only presence until this install has seen its first join (2.6).

- 4 hours after ready (and 4 hours after each reset) the app posts in the game's channel (its own, else the main one). When the bot can read that channel: "⏳ **{server}** has been on for 4 hours. Should it stay on? Send any message in this channel in the next 2 hours to keep it on. Otherwise it will be turned off." When it can't (no bot token, or the last read answered 403): "⏳ **{server}** has been on for 4 hours. Should it stay on? Ask the owner to press Keep it on in the control panel within 2 hours. Otherwise it will be turned off." The post is made with `?wait=true` so its message id is known.
- While the game runs, the app reads the channel every 2 minutes with the waker's bot token (`settings.json discordBotToken`): `GET /api/v10/channels/{channel}/messages?after={last seen id}&limit=20`, `Authorization: Bot <token>`. The channel id comes from `GET <webhook>` (`discord.Webhook(ctx, client, link) (guild, channel string, err error)`, beside today's `Guild`). **Any message not from a bot or a webhook restarts the 4-hour clock** (and ends a pending question). Message content isn't needed (no privileged intent).
- The game's Home shows the clock and a **Keep it on** button (`POST /api/games/{id}/keep-on`), which restarts the 4-hour clock. When the channel can't be read, the panel says "The bot can't read #channel, so only Keep it on in the control panel counts" (403: "give the bot View Channel and Read Message History there").
- 2 hours after the question with no message and no Keep it on: post "🔴 Turning **{server}** off: nobody answered for 2 hours." and stop it (stop chain, `autoStopped`, reason `stopAsked`). `stopAsked` makes no second stopped post: that line is the stopped post.
- No Discord channel at all (no main channel and no own one): the same clock without a post. At 4 hours the game's Home and the Overview show the question with Keep it on, and 2 hours later it stops (owner question Q2 confirms this).
- A running presence-`none` game blocks idle sleep ("maybe playing", 4.7).

### 4.7 Sleep and waking

**When the computer sleeps by itself** (`IdleHibernate` on, after `IdleMinutes`): only when nobody is playing any game and nothing is busy:
- every non-foreign running instance has presence known and count 0 (a running presence-`none` game counts as "maybe playing" and blocks idle sleep);
- no foreign instance's process is alive;
- no job in any slot, no upload younger than 2 h, no download;
- the user isn't using the computer (as today).

`machineIdleSince` is reset by any join, count above 0, `Hold`, the start of a job (except an auto-stop's), a wake, a settings change, and a game's exit (except one caused by auto-stop or the ask rule); `idleDeadlineLocked` uses it. Sleep only; hibernation stays off (`HibernationOffered = false`, unchanged).

**Going to sleep** (app job "hibernate", from the button or idle): stop every running instance in parallel (4.2), post each one's stopped message (reason `stopRest`), `resumeSet = the instances that were running`, leaving out any whose auto-stop deadline had passed and a hidden legacy instance; flush Discord (up to 30 s with several games), sleep. A foreign instance can't be stopped; it sleeps with the computer. Keep-awake: one `platform.KeepAwake` owner, `want = any instance running || any foreign instance alive || any job || wakeHold`, reason "A game server is running" (R3; R2 keeps "Valheim server is running").

**Waking:**
- `wokenOnPurpose` (a device woke it, or someone uses it): hold awake 15 minutes; if `StartAfterWake` and `resumeSet` isn't empty, schedule `resume`. **The resume waits until the network is really back** (the wake listener is bound again and the default gateway answers: `platform.DefaultGateway()` returns it and a TCP connection to it on port 53 or 80 is accepted or refused within 1 s, either of which proves the link is up; no ICMP; checked every 2 s, at most 3 minutes), **then 30 s more.** The waker repeats a Discord request's tagged packet every 10 s for 3 minutes, so a request that woke the computer is heard in that window even when the network is slow to come back.
- `resume`: if a Discord request (tagged wake packet) was heard since the wake, **do nothing**: that request starts only its own game. Otherwise start each instance of `resumeSet` in order (memory check each; one that doesn't fit is skipped with a warning), then clear `resumeSet`. A tagged packet heard while a resume is under way cancels the games it hasn't started yet; the ones already starting go on (auto-stop stops them in an hour if nobody plays).
- **Wake packet with a game** (6.4): start only that game, cancel any pending `resume`, set `wakeAsked`. Each request starts its game once (per-game last tag, as today). If it can't start (memory short, busy, disabled, not installed), the app posts why in the game's channel (6.4).
- **Tagged packet without a game** (an older waker): the legacy game, as today.
- **Untagged packet** (the KVM's own Wake-on-LAN button): if `resumeSet` isn't empty and not yet resumed → `resume` now; else just hold awake. (When the computer was asleep, the app never hears this packet: the adapter wakes it, and `wokenOnPurpose` does the resume.) Today's "untagged starts Valheim at most once a minute" goes away in R3.
- `resumeSet` is in memory only, as `resumeOnWake` is today.
- Tests (A and WK, fake clock): a tagged packet first heard 40 s after the wake, with the network back at 25 s, starts only its game; with the network back at 5 s and no tagged packet, the resume starts the resume set at 35 s.

### 4.8 Status stays fast

`Status()` holds `m.mu` for a snapshot of in-memory state only:
- For non-legacy instances, `installed`, `build`, `firewall` and `freeDisk` come from `instCache`, refreshed by `checksLoop` (every minute), by the install job before it finishes (4.3), and after folder changes.
- The legacy instance keeps today's synchronous `os.Stat` of the server program and the appmanifest read (status.go:138, 158-163): they are cheap, and the existing tests check `Build` straight after an install job.
- From R3, the LAN addresses come from a cache that `checksLoop` refreshes every minute and after a wake or a network change (today `lanAddrs` calls `net.Interfaces` and each interface's `Addrs` in every Status, which on Windows is one `GetAdaptersAddresses` per call).
- The public IP lookup stays as today.
- `TestStatusIsQuick`: 11 instances, 3 running; through a counting hook on file, process and network-interface access, `Status()` makes no such calls for non-legacy instances; a loose wall-clock bound (100 ms) only when not under `-race`. A `-race` test runs `Status()` in a loop while `UpdateSettings` and log lines are handled.

### 4.9 The Status JSON

Top-level fields stay as they are (the legacy instance's, `TestStatusTopLevelKeys`). New fields (R2 adds `games`, with Valheim only, and `jobs`; the rest come in R3):

```json
{
  "games": [
    {
      "id": "minecraft", "game": "minecraft", "name": "Minecraft: Java Edition", "short": "Minecraft",
      "letter": "M", "color": "#5b8731", "stage": "ready", "serverName": "Friends' Minecraft",
      "starred": true, "foreign": "", "disabled": false, "canRun": "yes", "needsText": "",
      "state": "online", "installed": true, "build": {"version": "26.3"},
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
      "reminder": ["update Minecraft to 26.3"],
      "warnings": []
    }
  ],
  "jobs": [ { "id": 42, "game": "minecraft", "kind": "start", "…": "…" } ],
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

Every stand-in imitates its game's real behaviour as far as the app can see it: it accepts the Plan's command line and reads the config files the Plan writes; prints the ready, join, leave, death, save and stopping lines given below (in the game's format, to stdout or to its own log file); listens on the planned ports; answers its probe and its stop chain the way the real server does (a console line, RCON, REST/API, or Ctrl+C — and on Ctrl+C saves only if the real one does); keeps its worlds in the game's layout; and runs the scripted players. Each section below adds what is particular to it. Every adapter also has: unit tests of its Monitor against sample logs in `testdata/` (copied from a real server where possible; until then written from the facts below, marked `// verify` in the file), a golden test of its command line and config files, its own download resolver with recorded answers in `testdata/` where it isn't a SteamCMD game, `FACTS.md` (each fact with its source and date), and its UI module `internal/web/ui/assets/games/<id>.js` (6.1).

"Settled" below means checked on 2026-11-26 against the real server files, Steam's app info or the publisher's documentation; the adapter's FACTS.md records the source. What is still marked verify is checked by the game's real-server check or a real log (8.2).

Common to all: server scope `serverName` (Add), `password` when the game has one (Add), `maxPlayers`, `updateOnStart` (default on), `autoRestart` (default on); `Texts{Players: "players", Player: "player"}` unless said.

### 5.1 Valheim (`valheim`) — the legacy game

- **Info:** V, `#f2a33a`, `/valheim`, Windows yes, macOS yes, download ~2 GB on each OS (settled: Steam depots 2.04 GB Windows, 2.09 GB macOS; the old panel's "about a 1 GB download" sentence stays in `Texts.Exact`), MemoryMB 4096 (a warning threshold: the owner can always start anyway), 10 players, presence names, deaths yes, ports game UDP 2456 + query UDP `game+1`, password required (Valheim's rules), import yes, lists yes, mods yes, `Legacy: true`, stage ready. `Texts{Players: "vikings", Player: "viking"}`.
- **Everything as today**, moved from `internal/manager` into `internal/games/valheim` without changing behaviour (table in 2.2): SteamCMD app 896660; `config.ServerArgs`; `SteamAppId=892970`; the Mac DYLD path; BepInEx mods and their Doorstop environment; the r2modman/Gale mods code (`Afterer`, with `Pending`); `logparse` (`Monitor` wraps `Tracker`, `Parse`, `IsNoise`; PeerCount and join-code counts become `count` events with `Hold`; `Tick` does the Steam-login fallback after `valheim.ReadyWait`, today's 10 minutes); Ctrl+C stop (resend 90 s, give up 4 min); `exitHint`; `worlds.Library`; the three permission lists; world options in settings.json. Output `LogArg`, log `logs/server-YYYY-MM-DD_HH-MM-SS.log`. `Program()` = `paths.ServerExe`. `valheim.ReadyWait` and `valheim.CodeWait` are exported package variables, read at each use, so tests can shorten them as they shorten `readyWait` and `codeWait` today.
- **`LegacyStore`** (exported for the manager), read at each call and never captured when the adapter is built:

  ```go
  type LegacyStore interface {
  	Settings() config.Settings // immutable snapshot (an atomic.Pointer swapped by saveSettingsLocked); never takes m.mu
  	Paths() config.Paths       // likewise
  	Save(config.Settings) error // takes m.mu: called only from job goroutines and handlers, never with m.mu held
  	Installer() steamcmd.Installer // m.opt.Installer now: honours NewInstaller (--dev-fake-server), SetInstallDir after folder changes, and tests' steamcmd.Fake
  	ModsURL() string               // m.opt.ModsURL now
  	ModVersions() map[string]string     // a copy
  	SetModVersions(map[string]string)   // takes m.mu, as above
  }
  ```

  The manager implements it over its settings.json handling (`saveSettingsLocked`, so the Library and installer folders follow folder changes exactly as today) and passes it in `Env.Legacy`. The adapter's `game.Store` maps values to and from it (2.9). The adapter never keeps folders of its own: it asks `Paths()` each time, because ServerDir/SaveDir can change (with the server stopped and no job running, as today), and the manager refreshes the instance's `dirs` after every save. The manager tests that set `m.opt.Installer` or `m.opt.ModsURL` after `New` keep working unchanged (VM's acceptance).
- **Join:** `How` "code" with crossplay on (code from the log; `Problem` "crossplay"/"late" as today, `CodeWait` 3 min), else "address" (the session line's public address, else public IP:port). `Router` = crossplay off. `Steps` and `Hint` are today's texts ("In Valheim, go to Join Game and join by code." / "…add the server by its address.").
- **R3 changes only:** router ports when crossplay is off (6.5); its own Discord channel (servers.json `discord.webhook`); auto-stop on (4.5); untagged wake packets no longer start it (4.7); hiding it takes it out of use (I1).
- **Stand-in:** `cmd/fakevalheim`, unchanged (the manager tests' `TestMain` keeps building it).
- **Real-server check:** the existing `TestRealServer` (internal/manager, Windows and Intel Mac; `internal/games/valheim/realserver.json` points the matrix at `./internal/manager/` and names `.github/scripts/build-doorstop.sh` as its macOS `setup`, as real-server.yml does today), run through the refactored manager; plus: crossplay off maps router ports when the runner's network has a router that answers (skipped otherwise).

### 5.2 Minecraft: Java Edition (`minecraft`)

- **Info:** M, `#5b8731`, `/minecraft`, Windows yes, macOS yes, download ~60 MB + Java ~45 MB, MemoryMB 3072, 10 players (setting), presence names (and the RCON count), deaths yes, ports game TCP 25565 (public) + rcon TCP 25575 (private), password none (`NoneText`: "Minecraft uses an allow list instead of a password: friends are added on the Players tab."), import yes, lists yes, mods no (later round).
- **Versions are opaque strings.** Mojang now numbers releases by year: the newest release on 2026-11-26 is "26.3" (snapshot "26.4-pre-1"). Never parse a version as "1.x"; examples, the stand-in's default and the golden files use "26.3".
- **Install:** `GET https://piston-meta.mojang.com/mc/game/version_manifest_v2.json` → `latest.release` → that version's JSON → `downloads.server.url` + `sha1` → `server/server.jar`; `javaVersion.majorVersion` (25 for 26.3) → `install.Temurin(major, runtime/)` from `https://api.adoptium.net/v3/binary/latest/{major}/ga/{windows|mac}/{x64|aarch64}/jre/hotspot/normal/eclipse`, checksum from `/v3/assets/latest/{major}/hotspot`. The Plan uses the java path `Temurin` returns (on macOS it is `<jdk>/Contents/Home/bin/java`). `WriteBuild{Version}`. Releases only, never snapshots. The resolver lives in `games/minecraft`.
- **macOS 12:** settled: Temurin 25's macOS x64 JRE needs macOS 11 (its java's minimum OS is 11.0), so the newest server runs on the Mac Pro. As insurance for later versions, the installer runs `java -version` after installing Java; if that Java can't start on this macOS, it installs the newest release whose Java major does start (walking back through the manifest), and the Join steps and the reminder say "Use Minecraft X" with that version (owner question Q6).
- **EULA:** Add a game shows "I agree to the Minecraft EULA" with the link https://aka.ms/MinecraftEULA; the instance can't start until it's ticked (`state.eula` = when). `eula.txt` gets `eula=true` only then.
- **Settings:** server: `serverName` (the MOTD), `maxPlayers` 10, `allowList` `auto|on|off` (default auto), `viewDistance` 10 (advanced), `memoryMB` 2048 (advanced, `-Xmx`). World: `difficulty` peaceful/easy/normal/hard (normal), `gamemode` survival/creative/adventure (survival). New world: `seed`, `levelType` normal/flat/large_biomes/amplified, `hardcore`. (Changed: no `pvp`. Settled: 26.3's server.properties has no `pvp` key any more; PvP is a game rule, left at the game's default this round.)
- **Plan:** write `server/server.properties`, keeping keys it doesn't set: `server-port`, `motd`, `max-players`, `difficulty`, `gamemode`, `view-distance`, `white-list`/`enforce-whitelist` (the allow-list rule below, decided before the server starts), `enable-rcon=true`, `rcon.port`, `rcon.password` (new random per run, in `Record.Secrets`), `online-mode=true`, `enable-query=false`; for a new world `level-seed`, `level-type`, `hardcore`. Command: `<java> -Xms512M -Xmx<memoryMB>M -jar server/server.jar --nogui --universe <saves> --world <world> --port <game>` (settled: 26.3's options include `--nogui`, `--universe`, `--world`, `--port`), Dir `server/`, Output `Capture`.
- **Monitor** (settled from 26.3's classes and language file): lines `[HH:MM:SS] [<thread>/<LEVEL>]: …` from any thread, matched with `^\[\d\d:\d\d:\d\d\] \[[^/]+/(INFO|WARN|ERROR)\]: `; ready `Done (…s)! For help, type "help"`; version `Starting minecraft server version X`; account `UUID of player <name> is <uuid>` (written by the login thread, not the server thread); join `<name> joined the game`, or `<name> (formerly known as <old>) joined the game` with the suffix stripped; leave `<name> left the game`; death = a server-thread line that starts with the name of a player who is on, followed by one of the vanilla death messages (the 106 `death.*` entries of 26.3's `en_us` language file, kept in testdata), never a chat line (`<name> …`) or an advancement; saved: during a stop `Saving worlds` and `Saving chunks for level '…'`, after `save-all` `Saved the game` (there is no "All dimensions are saved" line any more); stopping `Stopping server`; turned away `^(\S+) \(/[^)]*\) lost connection: You are not white-listed on this server!$` → event `turned_away` with the name, shown in the feed with an **Allow** button. The same strings are used in the stand-in and the testdata.
- **Probe:** RCON `list` → `There are N of a max of M players online: A, B`. (Changed in round two: RCON instead of the management protocol over websocket: one small client also serves Project Zomboid, and RCON covers `list`, `stop` and the allow list.)
- **Stop:** rcon `stop` (Wait 3 min) → interrupt (Wait 2 min; the JVM's shutdown hook saves).
- **Allow list (decision: "on once reachable from the internet"):** in `auto`, the Plan writes `white-list=true` and `enforce-whitelist=true` into server.properties, after adding every known player to whitelist.json, before the server starts and so before any router mapping opens the port. It does so unless the app has found that the internet can't reach this computer: carrier-grade NAT detected (6.5) and no login from a public address ever seen. So with router ports on (the default), with them off (the owner may forward by hand), and after manual router steps were shown, the list is on from the first start. If the list is off and a player logs in from a non-private address (the line `<name>[/<ip>:<port>] logged in …`), the app turns it on at once over RCON (`whitelist on`). Once on it stays on (`state.allowListOn`). `on`/`off` are fixed. Friends are added on the Players tab, or with **Allow** on a turned-away entry: running → RCON `whitelist add <name>`; stopped → `whitelist.json` with the UUID from the players list or `https://api.mojang.com/users/profiles/minecraft/<name>` (settled: it answers `{"id": "069a79f444e94726a5befca90e38aaf5", "name": …}`, 32 hex digits without dashes; the app writes it dashed, 8-4-4-4-12, as whitelist.json needs).
- **Lists:** allowed (`whitelist.json`), admins (`ops.json`), banned (`banned-players.json`), all live through RCON while running (`whitelist`, `op`/`deop`, `ban`/`pardon`).
- **Worlds:** worldstore, `folder` in `saves/` (a world is a folder with `level.dat`); Detect: folders holding `level.dat`; Local: `%APPDATA%\.minecraft\saves`, `~/Library/Application Support/minecraft/saves`.
- **Join:** "address": the public IP (`:port` only when not 25565); Steps "In Minecraft: Multiplayer → Add Server, type the address." and "Use Minecraft X, the server's version."; `Router` true.
- **Exit hints:** a case-insensitive "FAILED TO BIND TO PORT" (26.3 prints `**** FAILED TO BIND TO PORT!`) → port in use; "You need to agree to the EULA" → tick it; `UnsupportedClassVersionError` → Repair (installs the right Java); "Exception reading … level.dat" → restore a backup (verify).
- **Setup checklist** (its Home): on macOS, "The RCON port (25575) can be reached from your home network; it has a new random password every start." (2.5).
- **Stand-in:** reads `server.properties`; listens on the game port (TCP) and the RCON port (answers `list`, `stop`, `save-all`, `whitelist …`); writes the lines above; makes `<universe>/<world>/level.dat`; saves on `stop` and on SIGINT/Ctrl+C; `deny:Bob@3` in the script prints a turned-away line.
- **Real-server check:** Windows and Intel Mac: install (jar + Java), accept the EULA, start a new world with `allowList` auto (server.properties has `white-list=true` before the start), ready, RCON `list` = 0, stop (level.dat newer), start again on the same world, Join has the address.

### 5.3 Terraria (`terraria`)

- **Info:** T, `#2c7fb8`, `/terraria`, Windows yes, macOS `darwin/amd64` yes and `darwin/arm64` no (settled: the Mac server is x86_64 only), download ~57 MB, MemoryMB 1536, 8 players, presence names (log only: 2.6's rule until proven), deaths no (verify: whether the server console prints deaths; turn on if it does), port game TCP 7777 (and UDP if the real check finds the server listening on it: 1.4.6 added crossplay, and its UPnP code maps both TCP and UDP; then the role's Protos are `["tcp","udp"]`, so the firewall rule and router mapping cover it), password optional, import yes, lists no.
- **Install** (resolver in `games/terraria`): `GET https://terraria.org/api/get/dedicated-servers-names` → a JSON list of zip names (on 2026-11-26 `["terraria-server-1461.zip","terraria-server-1461.zip"]`: duplicates removed) → newest → `GET https://terraria.org/api/download/pc-dedicated-server/<name>` (57 MB; no published checksum, HTTPS only). The zip has one top-level version folder (`1461/Windows/`, `1461/Mac/`, `1461/Linux/`): unzip into `server/<v>/` and lift that folder's contents up, so `server/<v>/Windows/TerrariaServer.exe` exists. Version from the name: "1461" → "1.4.6.1". Exec bits aren't stored (FAT entries), so set them on `TerrariaServer.bin.osx`. `WriteBuild{Version: "1.4.6.1"}`. A golden test runs against a recorded listing and a small zip with the version folder at the top. Windows prerequisite: **XNA Framework 4.0 Redistributable** (settled: the Windows server is a 32-bit .NET Framework 4 console program that references XNA): `install.EnsurePrereq` with the registry check `HKLM\SOFTWARE\WOW6432Node\Microsoft\XNA\Framework\v4.0` `Installed`=1 (verify the key), the MSI `https://download.microsoft.com/download/A/C/2/AC2C903B-E6E8-42C2-9FD7-BEBAC362A930/xnafx40_redist.msi` (7,054,848 bytes; its SHA-256 pinned in the code), `msiexec /i … /quiet`, one admin prompt. .NET Framework 4 comes with Windows 10/11.
- **Program:** Windows `server/<v>/Windows/TerrariaServer.exe`; macOS `server/<v>/Mac/Terraria Server.app/Contents/MacOS/TerrariaServer.bin.osx` (settled: the MonoKickstart binary, x86_64, macOS 10.9+), launched directly with what the shipped `TerrariaServer` script would set: Dir = that `MacOS` folder, and `Env` `DYLD_LIBRARY_PATH=<abs>/Contents/MacOS/osx`, where `libGNS_Wrapper`, `libSDL3`, `libFNA3D` and `libFAudio` are (the server loads GNS_Wrapper for its networking; the libraries need macOS 11+, fine on macOS 12). Every path in the config is absolute.
- **Settings:** server: `serverName` (motd), `password`, `maxPlayers` 8. New world: `size` small/medium/large (autocreate 1/2/3), `difficulty` classic/expert/master/journey (settled: 0/1/2/3, from the bundled serverconfig.txt), `evil` random/corruption/crimson, `seed`.
- **Plan:** `server/serverconfig.txt`: `world=<saves>/<world>.wld`, `worldpath=<saves>`, `port`, `maxplayers`, `password`, `motd`, `upnp=0` (settled: the key exists and the server forwards ports itself by default; the app maps the router instead), `secure=1`, `language=en-US`; for a new world `autocreate`, `worldname`, `difficulty`, `seed`. Args `-config <abs path>`. **Console: true** (2.11); `SaveLine` `save`.
- **Monitor** (settled from the server's own text table: "Server started", "{0} has joined.", "{0} has left.", "Saving world data:", "Validating world save:"; a real log still has to show the exact lines with names, 2.6): version from the banner (`Terraria Server v1.4.6.1`, verify its form); ready `Server started`; join `<name> has joined.`; leave `<name> has left.`; saved `Saving world data:` / `Validating world save:`; stopping on `exit`.
- **Stop:** console `exit` (Wait 3 min) → interrupt (Wait 1 min, Say "Terraria saves every 10 minutes and when the last player leaves; anything since then is lost." Settled from its changelog: dedicated servers save every 10 minutes and when the last player leaves).
- **Worlds:** worldstore `files`, Ext `.wld`, `.wld.bak`; Local: `Documents\My Games\Terraria\Worlds` (`KnownFolder("documents")`, which OneDrive may move), `~/Library/Application Support/Terraria/Worlds`. Rename: `ErrCantRename` (the name inside the file would differ).
- **Join:** "address"; Steps "In Terraria: Multiplayer → Join via IP, type the address, then the port." and "Use Terraria X: it must be the same version as the server."; `Router` true. Reminder "update Terraria to X" matters here (exact version match).
- **Exit hints:** "already in use" → port; an XNA exception → Repair (installs XNA).
- **Stand-in:** reads `-config`; listens on the port; prints the banner and lines; `save` and `exit` on stdin → saving lines, writes the `.wld` (and `exit` exits 0); Ctrl+C → exits without saving.
- **Real-server check:** Windows (the runner is an admin, so XNA installs silently) and Intel Mac: install, autocreate a small world, ready (`Server started` and a TCP connection accepted on the port, so a missing native library fails CI, not the owner's checklist), `exit` saves (`.wld` exists, newer), start again with it; a takeover in between (`Takeover()`, then stop: the console is gone, so the chain goes to Ctrl+C, and the world from the last save is intact); `PortInUse("udp", port)` recorded in FACTS.md.

### 5.4 Necesse (`necesse`)

- **Info:** N, `#8e44ad`, `/necesse`, Windows yes, macOS `darwin/amd64` yes (settled: a macOS depot exists; whether its java is x86_64, arm64 or universal is read with `file` in the Mac real-server check, which decides `darwin/arm64`), download ~180 MB (settled: Windows depot 180 MB, macOS 163 MB), MemoryMB 2048, 10 players, presence names (log only, 2.6; verify the lines), deaths no (verify), port game UDP 14159, password optional, import yes, lists no.
- **Install:** SteamCMD app 1169370, anonymous. The server comes with its own Java. `Program()` per OS (settled from Steam's launch configs): Windows `server/jre/bin/java.exe`, macOS `server/jre/Contents/Home/bin/java`.
- **Settings:** server: `serverName` (motd), `password`, `maxPlayers` 10 (`-slots`). No new-world fields besides the name (settled: the server has no seed option).
- **Plan:** `<java> -Xmx<mem>M -jar Server.jar -nogui -datadir <Dirs.Saves> -world <world> -port <game> -slots <n> -password <pw> -motd <name> -logging 0` (settled from the server's documented parameters: `-datadir` keeps its cache, logs and saves under the instance, so no APPDATA override is needed; `-logging 0` because the app captures stdout itself). Dir `server/`, Output `Capture`. **Console: true** (2.11); `SaveLine` `save`.
- **Monitor:** ready `Type help for list of commands.` and the clean end `Exiting in 2 seconds...` (as a real server wrapper uses them); join, leave, save and version lines verify from a real log.
- **Stop:** console `exit` (settled: `/stop`, `/exit` and `/quit` all save and stop; Wait 3 min, done on `Exiting in 2 seconds...`) → interrupt (Wait 2 min; verify whether Ctrl+C saves).
- **Worlds:** worldstore `files`, Ext `.zip` in `<saves>/saves/` (settled: worlds are zipped by default, `-zipsaves 1`); Local `%APPDATA%\Necesse\saves`, `~/Library/Application Support/Necesse/saves` (verify).
- **Join:** "address"; Steps "In Necesse: Multiplayer → Join server, type the address and port."; `Router` true.
- **Stand-in:** started as the bundled java with `-jar Server.jar -nogui …`; reads `exit`, `save` and `players` on stdin; writes its world zip under `-datadir`.
- **Real-server check:** Windows and Intel Mac: install, new world, ready, stop through the console saves, restart loads it, takeover then stop; on the Mac, `file` on the java, recorded in FACTS.md.

### 5.5 Vintage Story (`vintagestory`)

- **Info:** VS, `#8a6d3b`, `/vintagestory`, Windows yes, macOS no ("Needs Windows": the Mac server is only inside the 600 MB client), download ~61 MB + .NET ~35 MB, MemoryMB 3072, 16 players, presence names (log only, 2.6), deaths no (verify), port game TCP+UDP 42420, **password required** (changed: since 1.20 the server is invite-only by default, and with no allow-list screen this round the password is what lets friends in, so the app turns the whitelist off and asks for a password), import yes, lists no. Players must own the game.
- **Install** (resolver in `games/vintagestory`; settled): `https://api.vintagestory.at/stable.json` is a map ordered newest first ("1.22.7" on 2026-11-26; `lateststable.txt` gives the same and is the fallback). That version's `windowsserver` entry gives `urls.cdn` (`https://cdn.vintagestory.at/gamefiles/stable/vs_server_win-x64_<v>.zip`) and an `md5`, checked with `install.Hash{Algo: "md5"}`. Unzipped into a fresh `server/<v>/` (the zip has no top folder); never pre-releases. .NET: `VintagestoryServer.runtimeconfig.json` names `Microsoft.NETCore.App` `10.0.0` → channel "10.0" → `install.DotNet` reads `https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json`, takes `latest-runtime`, and picks the file named exactly `dotnet-runtime-win-x64.zip` (not the apphost pack) with its sha512, into `runtime/`. `Program()` = `runtime/dotnet.exe`.
- **Plan:** `runtime/dotnet.exe server/<v>/VintagestoryServer.dll --dataPath <saves>`. The Plan writes `saves/serverconfig.json` before each start, and only then: the server rewrites the file from memory when it shuts down, so it is never edited while running. It keeps every key it doesn't set and sets `ServerName`, `Port`, `MaxClients`, `Password`, `WhitelistMode: 1` (Off; settled: 0 Default, 1 Off, 2 On), `AdvertiseServer: false`, `WorldConfig.WorldName`, `SaveFileLocation` = `<saves>/Saves/<world>.vcdbs`, `Seed`, `PlayStyle`. **Console: true** (`/stop`); `SaveLine` `/autosavenow` (verify).
- **Monitor** (settled from the 1.22.7 server's strings): ready `Dedicated Server now running on Port <p> and all ips!`; join `{0} {1} joins.` (verify which part is the name); leave `Player <name> left.` or `Player <name> got removed. Reason: …`; saved `World saved! Saved …`; done `Stopped the server!`.
- **Stop:** console `/stop` (Wait 3 min) → interrupt (Wait 2 min, verify whether it saves).
- **Worlds:** worldstore `files`, Ext `.vcdbs`, in `saves/Saves/`; Local `%APPDATA%\VintagestoryData\Saves` (verify).
- **Join:** "address"; Steps "In Vintage Story: Multiplayer → Add server."; `Router` true (UPnP often isn't enough, 6.5 shows the manual steps).
- **Stand-in:** started as `dotnet VintagestoryServer.dll --dataPath …`; reads `serverconfig.json` (port, world file); `/stop` on stdin saves and exits; writes `Saves/<world>.vcdbs`; prints the strings above.
- **Real-server check:** Windows: install with .NET, new world, ready, `/stop` saves, restart, takeover then stop.

### 5.6 Project Zomboid (`zomboid`)

- **Info:** PZ, `#b03a2e`, `/zomboid`, Windows yes, macOS no, download ~7.1 GB (settled: depots 6.85 GB + 0.2 GB for Windows), MemoryMB 8192, 16 players, presence names (RCON), deaths no (verify a log line), ports game UDP 16261 + direct UDP `game+1` (both public) + rcon TCP 27015 (private), password optional, import no (single-player saves aren't multiplayer saves; verify), lists no.
- **Install:** SteamCMD app 380870, anonymous, public branch (decided: Build 42, which is Steam's default branch; settled: public buildid 26486687 on 2026-11-26, the same as "pre-release"; `legacy41` would be Build 41). `Program()` = the bundled `jre64/bin/java.exe` (verify; Build 42 runs on Java 25).
- **Plan:** on Windows, Steam launches `StartServer64.bat` (settled). The adapter parses it: the `SET PZ_CLASSPATH=` value, the `-D`/`-XX` flags (including `-Djava.library.path=natives/;natives/win64/;.`) and the main class `zombie.network.GameServer`; it drops the .bat's `-Xms`/`-Xmx` and runs `jre64/bin/java.exe` with Dir = the server root. `ProjectZomboid64.json` (which configures the Linux launcher) is only a fallback; if neither parses, the install fails with "Couldn't read how to start the Project Zomboid server. Try Repair." A golden test runs against a recorded Build 42 `.bat`. Then `-Xmx<memoryMB>m`, `-cachedir=<saves>` (keeps its whole `Zomboid` folder under the instance; verify) and `-servername <world> -adminusername admin -adminpassword <secret> -port <game> -udpport <direct>` (verify). The app edits `<saves>/Server/<world>.ini` (`PublicName`, `Password`, `MaxPlayers`, `DefaultPort`, `UDPPort`, `RCONPort`, `RCONPassword` = a secret, `Public=false`), keeping other keys (settled defaults: 16261, 16262, 27015, an empty RCON password, not public, 32 players). Output `Capture`.
- **Probe:** RCON `players` → `Players connected (N):` + `-name` lines.
- **Monitor:** ready: the substring `*** SERVER STARTED` (a real line reads `LOG : Network , … *** SERVER STARTED ****`); joins/leaves from the probe (and log lines once verified).
- **Stop:** rcon `quit` (Wait 4 min) → interrupt (Wait 2 min).
- **Worlds:** a world is the server name: `saves/Saves/Multiplayer/<world>/`, `saves/db/<world>.db`, `saves/Server/<world>*` (`Format.Files`).
- **Join:** "address" with port and password; Steps "In Project Zomboid: Join → add the server's IP, port, a name for you, and the password."; `Router` true.
- **Stand-in:** started as java with the GameServer arguments; reads `Server/<world>.ini` (ports, RCON password); listens on both UDP ports and the RCON port; RCON `players` lists the scripted players, `quit` saves and exits.
- **Real-server check:** Windows: install, start, ready, RCON players = 0, quit saves, restart.

### 5.7 Enshrouded (`enshrouded`)

- **Info:** E, `#16a085`, `/enshrouded`, Windows yes, macOS no (Windows-only server), download ~8 GB and DiskMB ~13000 (settled: depot 8.05 GB; the publisher says ~13 GB unpacked and recommends 30 GB free, so Add a game warns below 30 GB), MemoryMB 6144 (settled: ~4.4 GB idle, at most ~6 GB), 16 players (`slotCount` 1–16), presence count (A2S; names only if a real log has them: hosting guides show `[online] Added Peer … id <SteamID64>` and `[online] Removed Peer`, with ids rather than names), deaths no, port query UDP 15637 (public; settled: there is no separate game port any more; whether TCP is needed too is checked in the real check), password required (one "Friends" user group), import no (verify), lists no.
- **Install:** SteamCMD app 2278520, anonymous. `Program()` = `server/enshrouded_server.exe`.
- **Plan:** write `server/enshrouded_server.json`: `name`, `saveDirectory` = `<saves>/<world>`, `logDirectory` = `<logs>/game`, `ip` `0.0.0.0`, `queryPort`, `slotCount`, the top-level `password` left empty (settled: "no longer used"), and `userGroups` = one group `{"name": "Friends", "password": …, "canKickBan": false, "canAccessInventories": true, "canEditBase": true, "canExtendBase": true, "reservedSlots": 0}` (verify the rights). Output `Capture` (settled: the console shows the log lines).
- **Monitor:** ready `[Session] 'HostOnline' (up)!` or `[Session] finished transition from 'Lobby' to 'Host_Online'` (settled, publisher's FAQ); `[online] Added Peer` / `Removed Peer` as count events (verify against a real log); it saves every 5 minutes (publisher).
- **Probe:** A2S on the query port (count; verify it answers).
- **Stop:** interrupt (Wait 4 min, Resend 90 s; the publisher says it runs in a console window, so Ctrl+C should reach it; verify it saves).
- **Worlds:** a world is a save folder `saves/<world>/`.
- **Join:** "address"; Steps (settled: friends can't type an address in the game): "In Enshrouded: Join → turn on Show dedicated servers and find <name>." and "Or in Steam: View → Game Servers → Favorites → + → <ip>:<query port>, then join it from Enshrouded's list."; `Router` true.
- **Stand-in:** reads `enshrouded_server.json`; answers A2S on the query port with the scripted player count; prints the ready and peer lines; saves into `saveDirectory` on Ctrl+C.
- **Real-server check:** Windows (CI builds and tests it now, as decided): install, start, ready, A2S answers, stop, restart.

### 5.8 Core Keeper (`corekeeper`)

- **Info:** C, `#c9a227`, `/corekeeper`, Windows yes, macOS no, download ~420 MB (settled: depot 417 MB), MemoryMB 2048, 8 players, presence names (log; 2.6's rule until a real log proves the lines below; changed: round one expected no signal, but its logs have join, leave and ready lines, so it gets the normal 1-hour auto-stop once proven), deaths no, ports none (friends join through Steam with the Game ID; its firewall rule is program-wide, 2.12), password none (`NoneText`: "Friends join with the Game ID: share it only with them."), import yes (verify the folder), lists no.
- **Install:** SteamCMD app 1963720, anonymous. Steam starts `Launch.bat`, which starts `CoreKeeperServer.exe`; `Program()` = `server/CoreKeeperServer.exe`. Needs a working graphics device: never `-nographics` (part of world generation runs on the GPU).
- **Plan:** `CoreKeeperServer.exe -batchmode -logfile <LogPath> -world <index 0–29> -worldname <world> -worldseed <seed> -worldmode <mode> -maxplayers <n> -datapath <saves>` (settled from a hosting guide for 1.3; `-port`, direct connect, stays off), Output `LogArg`.
- **Join:** "id": the Game ID from `<server>/GameInfo.txt`, which the server writes next to its exe at each start (settled; the app deletes it before a start so an old one isn't read, and falls back to `GameID.txt`), from its `GameID:` or `Steam GameID:` line. `CodeLabel` "Game ID", `Router` false. Announced like a join code: the online post waits for it.
- **Monitor** (from a community server image's log parser; verify against a real log): ready `Started session with info: `; join `[userid:<id>] player <name> connected islocalplayer=…` (the Monitor keeps a userid → name map); leave `Disconnected from userid:<id> with reason <reason>` (named through the map).
- **Stop:** interrupt (Wait 3 min); verify: the shipped launcher has a "press q to quit" path, so the real check reads `Launch.bat`/`Launch.ps1` and copies what it does on q, because `CoreKeeperServer.exe` may be a GUI-subsystem Unity program that Ctrl+C can't reach (2.7).
- **Worlds:** `saves/worlds/<index>.world.gzip` (verify); names kept in `State` (`world.<index>`).
- **Stand-in:** writes `GameInfo.txt` a moment after it starts and the log lines to `-logfile`; opens no ports (like the real one, which goes through Steam).
- **Real-server check:** Windows (verify the runner's basic display adapter is enough): install, start, Game ID appears, stop, restart.

### 5.9 Palworld (`palworld`)

- **Info:** P, `#d6408b`, `/palworld`, Windows yes, macOS no, download ~5.75 GB (settled: depot 5.75 GB), MemoryMB 10240 (the docs: 16 GB recommended, 8 GB starts), 32 players, presence names (REST), deaths no, ports game UDP 8211 (public) + rest TCP 8212 (private), password optional, import no, lists no.
- **Install:** SteamCMD app 2394010, anonymous; update on start (friends must match the server's version exactly). Windows prerequisite: VC++ 2015-2022 x64 (`HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64` `Installed`=1; `https://aka.ms/vs/17/release/vc_redist.x64.exe /install /quiet /norestart`). `Program()` = `server/Pal/Binaries/Win64/PalServer-Win64-Shipping-Cmd.exe` (verify).
- **Plan:** copy `DefaultPalWorldSettings.ini` to `Pal/Saved/Config/WindowsServer/PalWorldSettings.ini` if missing (settled), then set in `OptionSettings=(…)`: `ServerName`, `ServerPassword`, `AdminPassword` (secret), `RESTAPIEnabled=True`, `RESTAPIPort`, `RCONEnabled=False`, `ServerPlayerMaxNum`, and the world-scope options (difficulty and rates; verify keys). Not `PublicPort` (settled: it doesn't change the listening port). `GameUserSettings.ini` `DedicatedServerName` = the selected world's GUID. Args `Pal -port=<game> -players=<n>` (changed: version 1.0's docs say leaving `-useperfthreads -NoAsyncLoadingThread -UseMultithreadForDS` unset may improve performance). Output `Capture`.
- **Probe:** `GET http://127.0.0.1:<rest>/v1/api/info` and `/v1/api/players` (settled: `{players: [{name, accountName, playerId, userId, ip, ping, …}]}`), basic auth `admin:<AdminPassword>`; ready when it answers.
- **Stop:** call `POST /v1/api/save` then `POST /v1/api/shutdown {"waittime":1,"message":"The server is stopping."}` (settled: `waittime` is required; Wait 3 min) → interrupt (Wait 2 min).
- **Worlds:** `server/Pal/Saved/SaveGames/0/<GUID>/` folders; names in `State` (`world.<GUID>` = name); a new world: empty `DedicatedServerName`, and after its first save the new GUID is recorded (verify).
- **Join:** "address" `IP:port` and password; Steps "In Palworld: Join Multiplayer Game, type the address at the bottom."; `Router` true.
- **Stand-in:** reads `PalWorldSettings.ini`; serves the REST endpoints with basic auth on 127.0.0.1 (wrong password → 401); `save` writes the world folder, `shutdown` exits after `waittime`; creates a new GUID folder when `DedicatedServerName` is empty.
- **Real-server check:** Windows: install, start, REST info and players answer, REST save+shutdown, restart same world.

### 5.10 Satisfactory (`satisfactory`)

- **Info:** S, `#d35400`, `/satisfactory`, Windows yes, macOS no, download ~15.5 GB, MemoryMB 12288 (the wiki: 8 GB, 16 GB for big saves or more than 4 players), 4 players (setting), presence count (API `numConnectedPlayers`; names if the log has them, verify), deaths no, ports game UDP+TCP 7777 (public; the HTTPS API shares it) + reliable TCP 8888 (public) (settled: 15000 and 15777 are no longer used), password optional (client password), import yes (a single-player `.sav` through `UploadSaveGame`), lists no.
- **Install:** SteamCMD app 1690800, anonymous. Steam launches `FactoryServer.exe -log -unattended`, which starts the real server; `Program()` = `server/Engine/Binaries/Win64/FactoryServer-Win64-Shipping-Cmd.exe` (settled, the wiki).
- **Plan:** `FactoryServer-Win64-Shipping-Cmd.exe FactoryGame -Port=<game> -ReliablePort=<reliable> -log -unattended` (verify the port flags), Output `GameLog` `server/FactoryGame/Saved/Logs/FactoryGame.log` (settled: its logs are in the install folder).
- **API** (`POST https://127.0.0.1:<game>/api/v1`, `{"function": …, "data": …}`; self-signed certificate, pinned at first contact in `State`; settled from the published OpenAPI description, which uses camelCase fields, so the client and the stand-in use camelCase JSON tags): first start (in `Afterer.After`, which runs right after launch, retries until the API answers, and holds the online post until it's done; `Pending` is true while `State` has no claim) `PasswordlessLogin` → `ClaimServer{serverName, adminPassword}` (admin password = a secret) → `SetClientPassword` → `CreateNewGame{newGameData: {sessionName: <world>, mapName, startingLocation, bSkipOnboarding, …}}`; later `PasswordLogin`, `QueryServerState` (→ `serverGameState{activeSessionName, numConnectedPlayers, playerLimit, …}`), `SaveGame{saveName}`, `LoadGame{saveName, enableAdvancedGameSettings}`, `SetAutoLoadSessionName`, `EnumerateSessions`, `UploadSaveGame`, `DownloadSaveGame`, `Shutdown`.
- **Probe:** `QueryServerState` → count, `Up`.
- **Stop:** call `SaveGame` + `Shutdown` (settled: the wiki calls the API's Shutdown the best way; Wait 3 min) → interrupt (Wait 2 min; Ctrl+C in its window also works).
- **Worlds:** settled: the server always keeps its saves in the Windows user's `%LocalAppData%\FactoryGame\Saved\SaveGames\server`, outside the data folder (2.8, 3.1). The world store points there (`KnownFolder("localappdata")`), listing sessions by grouping `.sav` files (or `EnumerateSessions` while it runs); selecting a session sets it with `SetAutoLoadSessionName` (and `LoadGame` while running); backups copy the session's files; import is `UploadSaveGame`. Removing the game leaves them in place.
- **Join:** "address"; Steps "In Satisfactory: Server Manager → Add Server, type the address and port."; `Router` true.
- **Stand-in:** serves the HTTPS API with a self-signed certificate and camelCase fields (unclaimed until `ClaimServer`), counts the scripted players in `QueryServerState`, writes `.sav` files on `SaveGame` into its saves folder (in fake mode under the instance), exits on `Shutdown`.
- **Real-server check: on demand only** (7.4): install, claim, new game, query, save, shutdown, restart loads it.

### 5.11 Windrose (`windrose`)

- **Info:** W, `#34495e`, `/windrose`, Windows yes, macOS no (Windows-only server), download ~3 GB and DiskMB 35000 (settled: depot 3.04 GB; 35 GB is the publisher's recommended disk space, which round one took for the download), MemoryMB 12288 (12 GB for 4 players), 4 players (`MaxPlayerCount`, default 4), presence `none` until a real session's log proves its lines (below), deaths no, ports none (settled: "ports are dynamically assigned via NAT punch-through; ensure your router supports UPnP", so `Join.Manual` says so and its firewall rule is program-wide, 2.12), password optional (`Password`, `IsPasswordProtected`), import no, lists no. Friends' game version must match the server's (publisher).
- **Install:** SteamCMD app 4129620, anonymous (settled; Steam's Windows launch is `WindroseServer.exe -log`). Then start it once and stop it, which makes `ServerDescription.json` and the first world (publisher: "Start and stop the server once to generate them"). `Program()`: the process that keeps running with `-log` (the real check finds whether `WindroseServer.exe` stays or starts the shipping exe under `R5/Binaries/Win64/`; Program follows what it finds).
- **Plan:** edit `R5/ServerDescription.json` only while the server is stopped (publisher: "This file should only be modified while the server is shut down"): in `ServerDescription_Persistent` set `ServerName`, `Password`, `IsPasswordProtected` (= a password is set), `MaxPlayerCount` and `WorldIslandId` (the selected world), and keep every other key (`Version`, `DeploymentId`, `PersistentServerId`, `InviteCode`, `UserSelectedRegion`, `P2pProxyAddress`, `UseDirectConnection`, …). Output `GameLog` `R5/Saved/Logs/R5.log`.
- **Monitor:** connect/disconnect lines (`UR5CoopProxyServer::OnAccountDisconnected` and its connect twin) and the periodic "Connected Accounts" dumps; ready line (verify all from a real log).
- **Join:** "code": `ServerDescription_Persistent.InviteCode` (0-9, a-z, A-Z, at least 6 characters), `CodeLabel` "Invite code", `Router` false.
- **Worlds:** `Format.List` over `R5/Saved/SaveProfiles/Default/RocksDB_v2/<game version>/Worlds/<world id>/`, named by each `WorldDescription.json`'s `WorldName`; selecting a world sets `WorldIslandId`; backups only while stopped (the folder is a live database); import and rename aren't supported (`ErrCantRename`).
- **Stop:** interrupt (Wait 4 min; verify it saves), then, if it isn't a console program, the window-close event (the publisher's way: close its window, or end the process). No RCON, REST or console.
- **Presence decision (changed):** CI has no game client, so a real-server check can't show a join. Presence is decided from a real session in which at least one player connects and disconnects: the owner (or a friend) joins the Windows PC's server once, and its R5.log goes into `testdata/` with the source in FACTS.md. If that log shows usable connect and disconnect lines (or dumps with a count), ship `Presence: ByCount` (auto-stop after an hour like every game); until then ship `Presence: Unknown`, and the game uses 4.6's rule (decided). Both monitors are written; the constant is set from that log.
- **Stand-in:** writes `R5/Saved/Logs/R5.log` with the connect/disconnect lines and, when its script says so, the periodic "Connected Accounts" dump (so both presence monitors are tested); writes `R5/ServerDescription.json` with the nested keys and an invite code; saves on Ctrl+C.
- **Real-server check: on demand only** (decided; owner question Q5 asks whether that still holds at 3 GB).

---

## 6. Panel, HTTP API, Discord, waker, router ports

### 6.1 Panel (R3; R2 ships the old panel unchanged)

**Shell** (`internal/web/ui/assets/app.js` and new modules; `index.html` title "Easy Game Server"; manifest `name` "Easy Game Server", `short_name` "Game Server"; these and every other visible "Easy Game Server" are written by stream T's one rename commit, 1.2: until it lands, UI's new modules keep "Valheim Easy Server" wherever today's panel shows the app's name, so `web_test.go:214` and build.yml's smoke tests stay green):

- **Routes:** `#/overview` (default), `#/game/<instance>/<tab>` with tabs `home worlds players stats mods settings log`, `#/add`, `#/add/<game>`, `#/app/discord`, `#/app/sleep`, `#/app/settings`. Old hashes (`#/home`, `#/worlds`, …) go to the legacy instance's tab.
- **Sidebar:** Overview at the top. **Active (n)**: starred games, then running unstarred ones (shown with an empty star, only while they run). **Inactive (n)**: the rest. Each list opens and closes; Active open and Inactive closed by default; the state is saved by the app (`POST /api/app/sidebar`), never in browser storage. Each row: the letter tile in the game's colour, its short name, a live player count while it runs, and a star button (`POST /api/games/{id}/star`). Then **+ Add a game**. At the bottom the APP group: **Discord**, **Sleep and waking**, **App settings**, and the footer "Server computer: NAME" (`machine.hostname`).
- **Top bar:** "N games online · M playing" (`online`, `playing`) and **Sleep** (its confirm dialog names the games it will save and stop).
- **Overview:** starred games first, as cards: state, world, how to join (code / address / Game ID, password, Copy), who's on, Start / Stop. Then running games without a star, as cards. Then a collapsible **Inactive games (n)** list with a Start button each. A **memory bar**: the computer's memory split into each running game (its colour and size), other programs, and free. The activity feed of every game.
- **A game's page:** tabs Home, Worlds, Players, Stats, Mods (only when `Info.Mods`), Settings, Log.
  - *Home:* state and Start/Stop/Restart (Force stop when offered); **How friends join** (from `join`, plus the game's module); who's on; when it stops by itself ("Stops at 21:40 if nobody joins") or the "still playing?" question with **Keep it on** (4.6); usage tiles; its setup checklist (installed, firewall, router ports, Java/XNA/VC++ where needed, and game-specific notes such as Minecraft's RCON on the Mac); its activity.
  - *Worlds:* worlds with backups, select, new world (the `newWorld` schema), import (upload, or "from your own game" with `Local()`), download, rename where the game can. Valheim keeps its difficulty and mods controls through its module.
  - *Players:* known players, records ("most deaths" only when `deaths`), and the lists when `Info.Lists` (Minecraft: allow list with Add, and **Allow** on turned-away entries).
  - *Stats:* the game's CPU, memory and players over time; the machine chart stays on the App settings page.
  - *Mods:* Valheim's mod checklist per world and the mods help (today's pages, moved).
  - *Settings:* the schema form (`server` scope; `Advanced` under "More settings"), ports (role, protocol, port, chosen or picked, **Pick free ports**), "Stop when nobody has played for [60] minutes" (0 = never), "Start when the app starts" (2.9), this game's Discord (on, own channel or main, stopped and daily posts, include the password), **Turn this game off** (8.3's `disabledGames`: it can't start and gets no command until turned on again), and **Remove this game** (keeps its worlds as backups; for Valheim it hides it: stopped, unstarred, no command, not started by anything, files untouched; I1).
  - *Log:* the instance's ring and the download of its current log.
- **Memory warning** (on `action: "memory"`): "Palworld needs about 10 GB of memory and 6.2 GB is free." One button per running game "Stop Valheim (3.1 GB) and start", then "Start anyway" and "Cancel". They re-send the start with `memory: "stop:<id>"` / `"anyway"`.
- **Add a game:** every registered game that isn't hidden, as a card: letter tile, name, "Runs on this computer" or greyed with its reason ("Needs Windows", "Needs an Intel Mac"), download size, disk space, memory needed, what friends need, "Preview" when in preview, "Added" when it is. Choosing one asks the `Add: true` fields (server name; password with its rule, or the game's `NoneText`), Minecraft's EULA box, the world: **New** (name + new-world fields) or **From your own game** (`GET /api/catalog/{game}/local`), and a checkbox "Star it: show it under Active and add /<command> in Discord" (ticked by default, so a new game doesn't vanish into the closed Inactive list; owner question Q3). Then it's added, installs (with its runtime and prerequisites) and shows on its page.
- **Rename:** every visible "Valheim Easy Server" becomes "Easy Game Server" (1.2 lists every place; stream T does it in one commit); identity names don't change. On macOS, texts that send the owner to the app use the name it really has in Applications (1.2). Each game's server name is changed on its Settings tab; worlds are renamed on Worlds where the game allows it.
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
| `GET /api/catalog` | `[{id, name, short, letter, color, stage, canRun ("yes" or "no", for this computer), needsText, downloadMB, diskMB, memoryMB, maxPlayers, friendsNeed, password: PasswordRule, schema: [Field…] (Add fields), ports: [PortRole…], import, eula?, added}]` |
| `GET /api/catalog/{game}/local` | `{candidates: [worlds.Candidate…]}` from the owner's own game |
| `POST /api/games` | `{game, serverName, password, values?, eula, star, world: {mode, name, values, candidate}}` (`values`: any other `Add` fields of the server scope, by key; mode "new" or "import") → `{id, job}` (adds it, stars it when `star`, picks ports, starts the install job; for a hidden legacy game it only shows it again) |
| `POST /api/games/{id}/remove` | `{}` → `{job}` (a job in that game's own slot: stop, back up each world into its backups, remove its router mappings, delete server/runtime/logs, drop it from servers.json and the stars; its firewall rules go at the next fix; `backups/`, players and records stay in `games/<id>/` and show again if it's added back; legacy: hide, I1) |
| `POST /api/games/{id}/star` | `{on}` |
| `POST /api/app/sidebar` | `{activeOpen, inactiveOpen}` |
| `POST /api/games/{id}/start` | `{memory}`: "" (check), "anyway", or "stop:<id>" |
| `POST /api/games/{id}/stop` · `/restart` · `/force-stop` · `/cancel` | `{}` |
| `POST /api/games/{id}/install` | `{repair}` |
| `GET /api/games/{id}/settings` | `{schema, values: {server, worlds, world}, ports: [{role, protos, port, public, chosen}], autoStopMinutes, startWithApp, disabled, discord: {on, webhook, stopped, daily, password, guildOK}}` |
| `POST /api/games/{id}/settings` | the changed parts only, merged on the server: `{values?: {key: value}, ports?: {role: port}, autoStopMinutes?, startWithApp?, disabled?, discord?: {…}}` (partial, so a stale page never blanks a field; `disabled` adds or removes the id in `app.disabledGames`, and turning a running game off stops it first) |
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
| `POST /api/app/settings` | the app-wide fields, partial: `{idleHibernate?, idleMinutes?, autoUpdate?, startAfterWake?, routerPorts?}` (settings.json, except `routerPorts` in servers.json) |
| `POST /api/discord/main` | `{webhook}` (settings.json `discordWebhook`, checked with `discord.Problem`) |
| `POST /api/discord/test` | `{webhook, game?}` (the legacy body, which the old panel sends as `{webhook}`, plus an optional instance id; `TestLegacyRoutes` covers the legacy body) |
| `POST /api/power/stop-all` | `{}` |

`web` keeps calling `*manager.Manager` (changed: no `web.Backend` interface and no `FakeBackend`, which would have been a second, canned implementation of about 85 methods that could drift from the real one). Phase 0' creates the R3 manager methods with their signatures, so API can be written against them at once. Until A and M3 land, the UI stream works against recorded status and settings JSON fixtures served by a small dev handler (`go run ./internal/web/devfixtures`, never shipped); after that, against the real app with `--dev-fake-server` and `EGS_DEV_FAKE_SERVERS`. API's tests run against the manager with fake servers.

### 6.3 Discord

- **Channels:** the main channel is settings.json `discordWebhook`. Each game posts in its own channel (servers.json `discord.webhook`) or, when that is empty, in the main one. All of a game's posts go to one channel.
- **Defaults:** a game's Discord settings are 3.2's defaults until the owner changes them (on, the main channel, stopped and daily posts, no password), so a newly added game posts in the main channel. Test in D: a game added with the main channel set posts its online and stopped messages there.
- **Posts per game:** online (once ready; once the code is known for code games; after `Afterer`), stopped (reasons: stopped, restart, rest, crash, crash-restart, **idle** — "nobody played for an hour", with the instance's own minutes, 4.5 — and **asked** — 4.6's "Turning X off" line, which is its only stopped post), the daily records post at 23:59 (or once after waking for yesterday, as today), the "still playing?" question (4.6), and replies to Discord requests (6.4). Non-legacy posts start with the game's short name when they go to the main channel. Valheim's texts are byte-identical to today (golden).
- **Daily post:** per game, from its own records. Games with `Deaths`: today's `dailyText` with the game's `Texts` ("vikings" for Valheim). Games without: "🎮 **3 players** played Terraria today. Most time: **Alex** (2 h 10 min)" with the longest session as the note. `Posted` is per game (each records file has its own).
- **Queues:** one queue per webhook link (`map[string]*postQueue`, 32 posts each, its own goroutine and retries), so one channel's failures never hold up another's; order is kept per link, so "stopped" never overtakes "online". `flushDiscord` waits for all of them. A full queue still drops with a log line.
- **Slash commands:** only starred games get theirs; starring or unstarring updates the waker (6.4). A game's command works only in its channel (its own, else the main one).
- **One Discord server:** the waker answers in one Discord server: the main channel's, taken from `discordWebhook` whenever `discord.Problem(link)` is "", whatever Valheim's own `discord` on/off says (today it is set only when Valheim's posts are on, waker.go:201, and an empty guild means "any server"; tested with Valheim's Discord off). A game channel in another server is flagged on its Settings tab ("This channel is in another Discord server, so /terraria won't work there").
- **Settings:** the Discord page has the main channel (link, Test) and a row per game (on, own channel, stopped, daily, password). Valheim's on/stopped/daily/password are settings.json's `discord`, `discordStopped`, `discordDaily`, `discordPassword` (same meanings); its own channel is servers.json.

### 6.4 The JetKVM waker

**Config** (`internal/waker`), new fields optional so either side's older version keeps working:

```go
type Config struct {
	Token, MAC         string
	Query, Name, Guild string // as today; R2 and older apps fill Query/Name for /valheim. R3 apps leave Query and Name empty.
	// Games is nil (absent) in configs written by R2 and older apps: the waker registers /valheim
	// from Query/Name as today. R3 apps always write it, [] when nothing is starred (an empty
	// slice is written as [], never dropped: no omitempty). Present means exactly these commands,
	// so [] means none, and the waker bulk-overwrites the server's commands with an empty list.
	Games *[]GameCommand `json:"games"`
	API, Gateway string; Targets []string
}
type GameCommand struct {
	ID          string `json:"id"`              // instance id, sent in the wake packet
	Command     string `json:"command"`         // "minecraft"
	Description string `json:"description"`     // "Wake the server computer and start Minecraft"
	Name        string `json:"name"`            // server name, for replies
	Channel     string `json:"channel"`         // the only channel it works in; never empty (below)
	Query       string `json:"query,omitempty"` // A2S address, when the game answers A2S (Valheim)
}
```

- **Commands:** with `Games` present, the waker registers exactly those (one bulk overwrite per Discord server on `GUILD_CREATE`, as today), and ignores `Query`/`Name`. An interaction checks the guild (as today), then `channel_id == Channel` (else an only-you-see-it reply "🔒 /minecraft works in <#channel>."). Waking state is per game, so /minecraft while /valheim is waking works.
- **Which games get a command:** the starred, non-foreign, enabled, non-hidden instances whose channel id can be found: their own webhook's channel, else the main channel's. A starred game with no channel at all (no own webhook and no main one) gets no command, and the Discord page says why ("Add a Discord channel so /minecraft can work"). So unstarring everything leaves the bot with no commands, never a leftover /valheim.
- **Wake packet:** today's packet (magic + MAC×16 + the 6-byte tag) followed by `"EGS1"`, one length byte, and the instance id. `wol.PacketFor(mac, tag, id)`, `wol.GameOf(b) string`. Older apps read only the tag and treat it as Valheim; newer apps read the id (no id → the legacy game).
- **Replies (changed: no app → JetKVM channel).** The waker answers the interaction as it does today: "⏰ Waking the server computer and starting **Minecraft**. It posts here when it's online." For a game with `Query` (Valheim) it keeps today's A2S check and its "✅ … is online" edit; after 10 minutes without an answer it says "⚠️ **Valheim** didn't answer within 10 minutes. If the app couldn't start it, it says why above; otherwise someone may need to look at it through the KVM." For other games it claims nothing more. The app itself posts in the game's channel (the command's channel), through the webhook queue it already has (6.3), whatever the game's own Discord on/off says, because someone asked there:
  - online: the game's normal online post (with join info, as decided for that game's posts); already running: "✅ **Minecraft** is already online." with the same join info;
  - memory: "🧠 There isn't enough memory to start **Palworld** right now. Running: Valheim (3.1 GB), Minecraft (2.3 GB)." (decided: don't start, reply and list what's running);
  - busy, disabled, not installed, or failed: "⚠️ **Palworld** didn't start: <reason>."

  This replaces round one's UDP outcome replies with an HMAC key: no listener on the JetKVM, no key in jetkvm.json, no port to check, and nothing that can fail silently (a refusal always reaches the channel the request came from).
- **App side:** `wakerSetup` gains `Games string` (sha256 of the canonical JSON of the commands list, so the struct stays comparable with `==`). `wakerPlan` builds `Games` as above, with each channel id from its webhook (`discord.Webhook`), and the guild from the main channel (6.3). A star change runs `upkeepWaker` 30 s later (debounced), which reinstalls through `jetkvm.Install` as today, under the waker's own `wk.busy` lock (not a job, 4.2); if the lock is busy it tries again 30 s later; the restart registers the new commands.
- **Invite link:** asks for View Channel and Read Message History (permissions `66560`) from R3 on, for 4.6's rule; an existing bot works without re-inviting unless the channel is hidden from @everyone (the Discord page then says so).
- **Compatibility:** JetKVM names and paths unchanged (1.2). An older app reinstalls its own waker within 90 s of starting (version differs). Until then a new waker's /minecraft packet reaches an older app as a tagged packet: bed004b and R1 would start Valheim (a short window, only after a rollback past R2); R2 ignores packets whose id isn't the legacy one (8.1). A new waker reading an old app's config (no `games`) registers /valheim as today.
- **Tests (WK):** unstar everything → the bot overwrites with zero guild commands; star Minecraft with no channel → no /minecraft; an R2 config without `games` → /valheim as today (`TestLoadConfig` unchanged); a refused Discord start posts the memory text in that game's channel (fake webhook).

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
- **Ports forwarded by hand:** owners who already forwarded a port on the router (common for Valheim with crossplay off) may see the UPnP mapping refused as a conflict (UPnP error 718). The app then asks the router for that entry (`GetSpecificPortMappingEntry`); if it points at this computer's LAN address and port, the port counts as "open (set on the router)". For a Steam-registered game, `SteamSees` listing the server at the public address also counts as open. Neither case shows manual steps.
- **When it fails** (no router answers, mapping refused, NAT): the game's Home shows manual steps (with `Join.Manual`'s extra words, and "If you already forwarded these ports, you can ignore this."): "Open your router's page (usually http://192.168.1.1/), find Port Forwarding (or Virtual Servers, or NAT), and forward TCP 25565 to 192.168.1.20 (this computer)", with the gateway and LAN address filled in. With carrier-grade NAT: "Your internet provider shares one internet address between homes, so friends outside can't reach a server here. Ask your provider for a public IPv4 address. Games that join with a code or Game ID (Valheim with crossplay, Core Keeper, Windrose) still work."
- **No tunnel** of any kind (I10).

### 6.6 Tray and texts

- R3 tray (Windows), as decided: tooltip "Easy Game Server: 2 games online · 3 playing", cut at 120 bytes on a rune boundary; menu: **Open the control panel** · **Stop all games** (asks first when anyone plays) · **Quit**. No per-game entries, no Sleep entry. Quit closes the app and leaves running games running, as today's "Close app (server keeps running)" does (owner question Q4 confirms); console games get their save line first (2.11). `tray.Options` loses `Start`/`Stop`/`Running`/the sleep item, gains `StopAll func()` and `AnyRunning func() bool`. R2's tray is unchanged.
- main.go: `appName` = "Easy Game Server" (dialogs) in R3; `legacyFolderName` = "Valheim Easy Server" (R1). `serverStopper` becomes `StopAll(reason) error` + `Status()`; `stopAndWait` waits for every instance and the app job. `confirmPlayers` counts players in every game.
- Activity texts that name "the server" name the game: "Minecraft is online and ready for players."; Valheim's stay as they are.

---

## 7. Testing and CI

### 7.1 Stand-in servers

- **`cmd/fakevalheim`** stays exactly as it is, for Valheim and every existing manager test (built in `TestMain`).
- **`cmd/fakeserver`** stands in for the other ten. One binary; the personality comes from `fakeserver.json` in the folder of its own executable (or up to three parents), never from the environment, so tests can run several at once with different scripts:

```json
{"game": "minecraft", "script": "join:Alex@1,death:Alex@5,deny:Bob@6,leave:Alex@9",
 "speed": 60, "version": "26.3", "memMB": 40, "crashAfter": 0, "noReady": false}
```

  The framework (`cmd/fakeserver/main.go`, `fw_*.go`) gives every personality: argument parsing helpers, a log writer (stdout or a file), the script runner, memory ballast, TCP/UDP listeners on its planned ports (so port checks are real), a stdin command loop, Ctrl+C/SIGINT handling, and responders reused from `rcon.TestServer`, `a2s.TestResponder` and an HTTP(S) JSON helper. Each game's personality is its own file `cmd/fakeserver/<id>.go`, registering itself in `init()`.
- **Fake install:** when `Env.Fake` is set, the adapter's `Installer()` returns `install.Fake(fake, Program(), files)`: it copies the fakeserver to `Program()` (`.exe` on Windows), writes `fakeserver.json`, `.egs-build.json`, and placeholder files the adapter's Plan expects (`server.jar`, `VintagestoryServer.dll`, …). In development `EGS_DEV_FAKE_SERVERS=<path to fakeserver>` sets it for every non-legacy game (Valheim keeps `--dev-fake-server`).
- **Fake games run everywhere:** in fake mode `Info.OS` and the manager's OS check are ignored and `Program()` has the same layout on every system (2.2), so every adapter's `Conformance` and the manager tests with fake games run on Linux too, where the agents test locally, as well as on Windows and macOS. Only the panel's catalog and a real start use `Info.OS`.
- **`internal/game/gametest`** runs one adapter without the manager, against the stand-in or, with `Real`, against the real server:

```go
func BuildFakeserver(t testing.TB) string
type Options struct {
	Fake     string      // the built fakeserver; "" with Real
	Real     bool        // use the adapter's real Installer and Plan (the realserver-tagged tests)
	Script   string
	World    string
	NewWorld game.Values
	Change   func(*game.Saved)
}
func Start(t *testing.T, g game.Game, o Options) *Session
func (s *Session) WaitEvent(kind, name string, within time.Duration) game.Event
func (s *Session) State() game.RunState
func (s *Session) Takeover()               // as after an app restart: a new Monitor from the run record, no stdin pipe
func (s *Session) Stop() (saved time.Time) // runs the stop chain
func Conformance(t *testing.T, g game.Game, o Options)
```

  `Conformance` checks: the fake install makes `Program()` and `Build()`; a new world's Plan; the server is online within 30 s; its public ports are in use (`platform.PortInUse`); scripted joins and leaves (and a death when `Deaths`) become events; the probe answers when it has one; the stop chain ends the process and the world's files changed; a second start loads the same world; a takeover then a stop works (console games: the console step gives `ErrNoConsole` and the chain moves on); `Validate` rejects a 200-character server name; `Fingerprint` changes when a `Restart` field changes; `Join` has steps; `Worlds()` lists the world, and a backup restores.

### 7.2 Compat and golden tests

| Test | Package | Pins |
|---|---|---|
| `TestIdentity` | main, platform, selfupdate, jetkvm, waker, cmd/valheim-waker | every value in 1.2, as literals |
| `TestFlagsUnchanged` | main | the flag set, `relaunchArgs` |
| `TestSettingsShapeFrozen` | config | settings.json's key set and types; `Defaults()` |
| `TestLegacyFileShapes` | manager | runtime/players/records/lastplayed/stats/history/activity/jetkvm/update JSON against `testdata/legacy/*.json` written by bed004b's structs: every bed004b key is present with the same JSON type, and any extra key must be on an explicit allow-list (activity `game`; jetkvm.json `setup.games`), so R2 and R3 additions need no golden edits while an unexpected key still fails |
| `TestRuntimeJSONCompat` | manager | a new run file decodes with bed004b's `runInfo`, and the reverse, values equal |
| `TestStatusTopLevelKeys` | web | `/api/status`: every bed004b top-level key, and the `job`, `lastExit`, `join`, `app` shapes, present with the same types; extra top-level keys only from the allow-list `games, jobs, stars, sidebar, online, playing, router, safeMode` |
| `TestLegacyRoutes` | web | every old route still answers with the same shape (including `POST /api/discord/test {webhook}`) |
| `TestFirewallScriptGolden` | platform | `firewallScript` output for fixed paths; the Valheim part of `FixFirewallAll` identical; a game with ports and one without (`AnyPort`) |
| `TestFixFirewallAllReal` | platform (Windows CI only, `EGS_CI_ADMIN=1`) | runs `FixFirewallAll` for real with a fake game rule; `Get-NetFirewallRule` finds Valheim's rules and the `Easy Game Server - <id>` rules with the expected program and ports; then removes them |
| bed004b behaviour goldens (R1, S1) | manager | recorded from today's code into `internal/manager/testdata/golden/`: `ServerArgs` and `argsKey` for a table of settings; online, stopped and daily Discord texts; reminders; exit hints; `Tracker` activities for log excerpts extracted into testdata. The merged Valheim adapter and manager (stream VM) must reproduce them |
| `TestNoGameIDs`, `TestImportRules`, `TestNoTunnel` | game | 2.1, I10 |
| `TestServersPassthrough` | servers | unknown keys and instances survive a write |
| `TestNewIsQuick`, `TestStatusIsQuick` | manager | I5, 4.8: no per-instance disk, process or network calls (a counting hook); loose time bounds, skipped under `-race`. In R2 (VM) they run with Valheim plus ten foreign `games/*/run.json` files (R2 has no servers.json); M3 extends them to 11 instances from servers.json in R3 |
| `TestLegacyPathsUnchanged` | manager (R2) | I1: every Valheim folder the manager and the adapter use equals `config.ResolvePaths`, with and without ServerDir/SaveDir overrides, before and after a settings save |
| `TestNoForeignFilesInLegacyPlaces` | manager (R3) | I8: after fake games install, run, write logs and records, back up and import a world, nothing new appears in `logs/server-*.log`, `backups/`, `uploads/` or the legacy players/records/lastplayed/stats/history files |
| `TestSecretFilesPrivate` | manager, servers (Unix) | I9: settings.json, runtime.json, servers.json, `games/*/run.json` and jetkvm.json are written 0600 |
| `TestForeignInstance` | manager | a live `games/x/run.json` keeps the computer awake, blocks idle sleep, shows the warning with the record's `name`, and its files are untouched |
| `TestSafeMode` | manager (R3) | damaged servers.json → the backup is used and the damaged file kept; newer → Valheim only, file not rewritten; missing with `games/` → rebuilt from the backup or the folders |
| `TestWakePacketForOtherGame` | manager | R2 ignores a packet whose id isn't the legacy one |
| `TestHiddenLegacy` | manager (R3) | hiding Valheim stops and unstars it; it isn't started by start-with-app, the resume or a wake packet; its files are untouched |

### 7.3 Upgrade and rollback against real releases (R1, in build.yml)

New steps in the `windows` and `mac` jobs (so they gate releases), using `cmd/ci-stubs` (a stand-in GitHub releases API on 127.0.0.1:18500 and a Discord webhook recorder on 127.0.0.1:18501 that writes each post to a JSON file). The steps build their own numbered copies of this commit (`-X main.build=$RUN_NUMBER` as N, and N+1) into a test folder that is never uploaded, so they work on every branch, while the uploaded branch artifacts stay build 0 (1.4). The existing self-update test does the same.

1. Download the **latest real release**'s program (`gh release download --repo $GITHUB_REPOSITORY --pattern ValheimEasyServer.exe` / `ValheimEasyServer-mac`). The upgrade script takes the release tag to start from as a parameter (default: the latest), so the R3 gate's extra runs below can name R1's and bed004b's `build-N` tags, which the lead records in docs/dev/multigame when each is released.
2. Start it with `--no-window --data D --port 18457 --dev-fake-server <fakevalheim> --dev-fake-github http://127.0.0.1:18500 --dev-fake-discord http://127.0.0.1:18501` (and, from R3 on, `EGS_DEV_FAKE_SERVERS` in its environment, which it ignores and passes on to the build it starts). Turn on Discord with a stand-in webhook, install, start Valheim, wait for online and for the Discord post.
3. Publish copy N; `POST /api/app/update {"install":true}`; wait for the new build.
4. Check: state online; runtime.json's PID unchanged (the same server process was taken over); exactly one "online" post; settings.json's password, world and port unchanged; no `*.damaged-*` files; the activity names the update. From R3: add a fake Minecraft, start it. Then keep the new build running at least 3 minutes with Valheim adopted, and check that the process is alive and that a sample and a checks pass have run since the takeover. Then publish copy N+1 and update: the new build updates itself while Valheim runs and takes Valheim over again with the same PID, with no new post. This proves a build can deliver its own fix, which the R2 soak relies on.
5. **Back:** stop publishing the higher builds in the stub (and turn `autoUpdate` off through `POST /api/settings`), so the old release can't update itself again two minutes in; `POST /api/system/quit {"stopServer":false}`; start the old release on the same folder; check: it answers, Valheim online with the same PID, no new Discord post, settings unchanged, and `app.build` stays the old release's number to the end of the step; from R3 also: the old build (R2) warns that Minecraft is running and keeps the computer awake. Stop Valheim through it (saved), start, stop, quit.
6. **Forward again (R3):** start the new build: it takes over the fake Minecraft (same PID) and stops it through its stop chain.

At the R3 gate the same steps also run from R1's release and from bed004b's straight to the R3 candidate (a computer that was off during R2 goes R1 → R3), checking steps 4 and 5's points plus servers.json created with Valheim starred (8.2).

The existing "a build that doesn't start" test stays on Windows and gets a Mac twin (a broken build: `/usr/bin/false`, which has a universal Mach-O header; verify). Mac steps use the bare binary in a plain folder (no `.app`, so no detaching or translocation).

### 7.4 Real-server matrix

`.github/workflows/real-server.yml`, rewritten in place by stream CR (changed: not a new `real-servers.yml`). GitHub fires `workflow_dispatch` and `schedule` only for a workflow file that exists on the default branch (a dispatch against another branch then runs that branch's copy of it), so a new file name could be neither dispatched nor scheduled until R3 is released, and nothing could satisfy the release check below at the R3 gate. `real-server.yml` has a dispatchable copy on the default branch from bed004b on; until CR's rewrite lands it runs only Valheim's check.

- **Triggers:** push to `multigame` (ignoring `docs/**` and `*.md`); `workflow_dispatch` on any branch (`multigame`, an `mg/<stream>` branch before its merge, `release/r2`, `release/r3`) with inputs `games` ("all", "changed" or a comma list) and `big` (also run Satisfactory and Windrose); and a nightly schedule. The schedule only fires from the default branch's copy, so it works from R3 on, and a scheduled run checks out `multigame` (`actions/checkout` with `ref: multigame` when `github.event_name == 'schedule'`); before R3 the lead dispatches `games=all` on `multigame` instead after a day of merges. On a push the `plan` job runs only the cells of games whose package changed (`git diff` against the previous push), or every cell when shared code changed (`internal/install`, `internal/steamcmd`, `internal/platform`, `internal/manager`, `internal/game`, `cmd/fakeserver` excluded). A newer push cancels an older push run; dispatched and nightly runs are never cancelled.
- **Matrix from one file per game:** the `plan` job reads every `internal/games/*/realserver.json` and outputs the matrix:

```json
{"os": ["windows-latest", "macos-15-intel"], "onDemand": false, "timeoutMinutes": 60,
 "package": "./internal/games/minecraft/", "run": "TestRealServer",
 "setup": {"macos": ".github/scripts/build-doorstop.sh"}}
```

  `setup` (optional, a script per OS) runs before the test: Valheim's points at `build-doorstop.sh` on macOS, as today's real-server.yml does, so its Mac cell tests the doorstop releases ship.
- **Each cell:** `go test -tags realserver -run <run> -v -count=1 -timeout <t>m <package>`; uploads the test data folder's logs when it fails. Each game's `TestRealServer` uses `gametest` with `Real: true` (no manager): install, start a new world, online, probe, Join, stop (saved), start again (same world), takeover then stop. So the game streams can settle their facts as soon as their adapter exists. Valheim's check (the existing one, through the refactored manager) and Minecraft's (through `managertest`, adding the game with Add a game and removing it) are the two manager-level real checks. Disk-heavy cells use the runner's largest drive (verify free space for Satisfactory on `windows-latest`).
- **Releases wait for it (changed):** the lead dispatches `real-server.yml` with `games=all` on the branch whose head is the exact commit to be released (`multigame`, `release/r2` for an R2.x, `release/r3` for an R3.x), and build.yml's release job refuses to publish unless that workflow has a successful run on the merge's second parent. The release job has no checkout, so it reads the parents with `gh api repos/$GITHUB_REPOSITORY/commits/$GITHUB_SHA --jq '.parents[1].sha'` (no second parent: refuse) and the runs with `gh run list --workflow real-server.yml --commit <sha> --json conclusion,event`; it gains the `actions: read` permission for that. The file keeps its name, so this check is the same from R1 to R3. A game is marked ready only when its cells are green.

| Game | Windows | Intel Mac | Runs |
|---|---|---|---|
| Valheim | yes | yes | changed, nightly, gates |
| Minecraft | yes | yes | changed, nightly, gates |
| Terraria | yes | yes | changed, nightly, gates |
| Necesse | yes | yes | changed, nightly, gates |
| Vintage Story | yes | — | changed, nightly, gates |
| Project Zomboid | yes | — | changed, nightly, gates |
| Enshrouded | yes | — | changed, nightly, gates |
| Core Keeper | yes | — | changed, nightly, gates |
| Palworld | yes | — | changed, nightly, gates |
| Satisfactory | yes | — | on demand (and gates when it is ready) |
| Windrose | yes | — | on demand (and gates when it is ready) |

No Mac cell for a "Needs Windows" game. Linux has no real-server cells (not a user platform).

### 7.5 The owner's Mac Pro checklist (R3 candidate)

**Running the candidate.** The candidate is a branch build (build 0), which never updates itself (1.4). As decided, it runs on the owner's real data folder, taking Valheim over as the final update would; backups are kept.
1. Back up first (decided: backups are kept). In the installed app, press **Back up** on Valheim's world (Worlds page); then in Terminal copy the app's own files: `mkdir -p ~/EGS-before-R3 && cp -p ~/Library/Application\ Support/ValheimEasyServer/*.json ~/EGS-before-R3/`. Keep both until R3 has been released and run. Then choose Quit in the installed app (games keep running).
2. Download the candidate's `ValheimEasyServer-mac` artifact into a folder of its own (never into Applications, and never over `Valheim Easy Server.app`), then in Terminal: `xattr -d com.apple.quarantine ValheimEasyServer-mac; chmod +x ValheimEasyServer-mac; ./ValheimEasyServer-mac`. Without `--data` it uses the real data folder (`~/Library/Application Support/ValheimEasyServer`). Don't turn on "Start at login" in the candidate: it would point the login item at this copy, which never updates.
3. Go through the checks below.
4. Afterwards: stop every game except Valheim in the candidate, Quit it, and open the installed app from Applications (that is the tested R3 → R2 rollback). Its panel shows the released build number.

The checks, on the Mac Pro (macOS 12, Intel):

1. **Valheim:** it's taken over as before; start, join, stop (saved). Optional, if a friend outside can help: with crossplay off, the router mapping appears (or "open (set on the router)"), and the friend joins by address.
2. **Minecraft:** Add a game (EULA) with the allow list on `auto` and router ports on: the list is on by itself; join from a Java client (after Allow for your own name); a name not on it is turned away, click Allow, that friend joins; stop.
3. **Terraria:** Add a game with a new small world, join (same version), build something, Quit the app (games keep running), open it again, Stop: the world kept what you built before quitting (the app saved it on the way out, 2.11).
4. **Necesse:** Add a game, join, stop, start again: the world is there.
5. **Sleep and wake:** with two games running, Sleep, then wake with the KVM's button: both start again.

---

## 8. Releases, gates and kill switches

### 8.1 What each release contains

| Release | Contents |
|---|---|
| **R1 "bridge"** | identity constants (`legacyFolderName`, `dataFolderName` and the others in 1.2) with literal tests; the legacy-shape, Status and bed004b behaviour goldens (7.2); `cmd/ci-stubs` and the upgrade/rollback steps against the latest real release on Windows and Mac, with numbered test copies; uploaded branch artifacts unnumbered; the release job's real-server check. No behaviour change. (Changed from round one: the "keep unknown settings.json keys" writer and the foreign-run-file awareness move out of R1: nothing new is ever written into settings.json, and the build that must cope with other games' run files is R2, the rollback target of R3.) |
| **R2 "core"** | foreign-instance awareness (scanning `games/*/run.json`); the wake-packet game check; the game contract and a registry with **Valheim only** (`internal/games/all` imports only `valheim.go`; the R2 gate checks that `game.All()` has exactly one entry); `games/valheim`; the manager as instances with per-instance jobs, adoption and per-instance files; SteamCMD with an app id and the lock (2.3). Old panel. Status gains `games` (Valheim) and `jobs` (the old panel ignores them). No servers.json (3.4). Packages no R2 code imports (worldstore, rcon/a2s, gametest, the fakeserver) and the new platform files may already be on the branch; they aren't linked or aren't called. |
| **R3 "games"** | servers.json; the new panel and the rename; Add a game and removing; the other ten adapters, every one at least preview (8.2); per-game Discord channels, queues and daily posts; the multi-command waker; auto-stop for all games; the "still playing?" rule; the memory check; sleep and waking with the resume set; router ports; the tray; firewall rules for every game. |
| **R2.x, R3.x** | fixes (R2.x) and, after R3, stage flips (preview → ready) with their fixes, one game per build. Each is released only with the owner's OK (owner question Q1). |

### 8.2 Gates

**How a release is made.** Only the lead releases, and only at a gate the owner has approved (R1, R2, R3, and each R2.x or R3.x the owner agrees to):

```sh
git checkout ccr-949cf1ef-eeo1jr
git merge --no-ff multigame -m "Safety nets before several games"   # or --no-ff release/r2 for an R2.x
git log -1 --format=%B        # the release note: one line people see in the app, never "[skip ci]"
git push origin ccr-949cf1ef-eeo1jr
```

- Always `--no-ff` with `-m`: the default branch doesn't move between gates, so a plain merge would fast-forward, and the pushed head would be multigame's newest commit. Its message would become the release note (build.yml takes the notes from `head_commit.message`), and multigame's notes commits carry `[skip ci]`, which makes GitHub skip the whole push: no tests, no release.
- Before pushing, the lead checks that the head is a merge commit whose subject is the intended note and that its message has no `[skip ci]`. After pushing, the lead checks that the "Build and test" run started on the default branch and that `build-N` was published.
- Release notes: "Safety nets before several games", "Several games: the core (Valheim only for now)", "Easy Game Server: several games at once".
- The real-server run on the exact commit being released must be green before the merge (7.4; the release job checks it).

**R2.x fixes.** At the R2 gate the lead creates `release/r2` from the R2 merge commit on the default branch. An R2.x fix is made there (by the stream that owns the code, in a worktree from `release/r2`), merged `--no-ff` into the default branch at an approved gate, and merged forward into `multigame`. multigame is never merged into the default branch for an R2.x. After R3, `release/r3` works the same way for R3.x while game streams continue on multigame.

- **R1:** CI green on Linux, Windows and Mac, including the new upgrade/rollback steps against bed004b's release; the real Valheim check (real-server.yml, dispatched on `multigame`) green on the commit.
- **R2:**
  - every existing test green, with diffs limited to the replacements listed in 4.1 (reviewed by the lead);
  - the new compat tests (7.2) green, including the bed004b behaviour goldens;
  - `game.All()` has exactly one entry (Valheim);
  - an old-panel test: `Status.job` doesn't change during a waker install or a firewall fix, and a finished Sleep job is reported;
  - upgrade from the latest release (R1) and back, green on Windows and Mac, including the 3-minute run and the N → N+1 update with Valheim running (7.3);
  - the real Valheim check green on Windows and Mac through the new manager.

  Then release. **The owner's soak** (decided): R2 reaches the owner as a normal auto-update, which tests the update and the takeover for real when Valheim is running at the time. 3+ days of normal play on the released R2 follow, covering an auto-update while Valheim runs, sleep and wake, /valheim, and a takeover. If R2 arrived while Valheim wasn't running, or a fix is needed, an R2.x release covers it (a no-change R2.x only with the owner's OK, Q1). The owner's OK after the soak is the gate for merging R3 (decided).
- **R3:**
  - all CI green;
  - every listed game ships at least as preview; a game may ship hidden only when its real-server check failed, and the lead names each such game, with the reason, to the owner before the R3 merge;
  - the real-server matrix green for every game marked ready;
  - upgrade R2 → R3 and back R3 → R2 green on Windows and Mac, with a second fake game running across both;
  - upgrade from R1's release and from bed004b's straight to the R3 candidate, with Valheim running: takeover with the same PID, one online post, settings unchanged, servers.json created with Valheim starred (7.3);
  - `TestNoGameIDs`, `TestImportRules`, `TestNoTunnel` green;
  - the owner's Mac checklist (7.5) passed on the candidate build.
- **R3.x:** that game's real-server cells green (dispatched for Satisfactory and Windrose); one stage flip per build; the owner's OK.

Game stages at R3: **ready** needs its real-server cells green on every OS it's offered on, all its "verify" facts settled (for log-presence games, a real join and leave log excerpt in its testdata, 2.6), and for Mac-offered games the owner's checklist; **preview** needs its conformance test and one green real-server run; **hidden** only as above.

### 8.3 Kill switches

- **Stage per game** (compiled in, flipped in an R3.x build): hidden instances become foreign (kept, not run, still kept awake for); preview shows the label.
- **`app.disabledGames`** in servers.json, also set by "Turn this game off" on its Settings tab: the game can't start, isn't resumed, gets no Discord command; its data stays. Works without a new build.
- **Valheim-only safe mode** (R3) when servers.json is from a newer version, or it and its backup are both unreadable (3.4): only the legacy instance runs, servers.json is never rewritten, other games' run files (found by scanning `games/*/run.json`) still keep the computer awake, and the Overview says why. (Changed: a damaged file falls back to its backup by itself, and a missing one is rebuilt, so there is no restore route or banner.)
- **Rollback:** the automatic one (exe.old) and the matrix in 3.6.
- Development only: `EGS_DEV_FAKE_SERVERS`, `EGS_DEV_ALL_STAGES=1` (hidden games shown as preview), `EGS_DEV_SAFE_MODE=1`.

---

## 9. Work breakdown

### 9.1 How the work flows

- **One stream = one coding agent in its own local worktree**, branched from `multigame`: `git worktree add ../egs-<stream> -b mg/<stream> multigame`. It commits only to its branch and edits only the files it owns (below). Anything else goes through the lead.
- Before saying it's done, an agent runs `gofmt -l .`, `go vet ./...` and `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...`, and `go test -race ./...` (Linux, as CI's Linux job does), and checks its acceptance list.
- **CI before merging (changed):** agents can only test on Linux, so the lead pushes the finished `mg/<stream>` branch to origin; build.yml runs on any branch push (Linux, Windows, Mac) and never releases from it. The branch's head must be green on all three before the merge. A failure goes back to the stream's agent.
- **The lead** then merges into `multigame` locally (`git merge --no-ff mg/<stream>`), runs the tests, pushes `multigame`, and checks its CI again. When a stream needs another one's work, it merges `multigame` into its branch after the lead has merged that work.
- **No pull requests.** Releases only at the gates (8.2), by the lead, with `--no-ff` and a release-note message.
- **No tags.** Nobody pushes a git tag: build.yml's release job also publishes a release for any pushed `v*` tag (`refs/tags/v`), whatever branch it is on.
- **Contracts are frozen first** (phase 0 for R2, phase 0' for R3) by the lead, so streams can start in parallel against them. A contract change needs the lead: it lands on `multigame` and streams merge it.
- **Before the R2 cut** multigame takes only R2 work and packages R2 doesn't link (8.1). Game streams may start earlier on their branches, but their `internal/games/all/<id>.go` registration (and so their merge into multigame) waits until `release/r2` is cut.

### 9.2 Order

```
Phase 1  R1     S1 ─┐
                S2 ─┴─► R1 gate ─► merge to default
Phase 2  P0     lead: internal/game, package stubs
Phase 3  R2     VM ────────────────────────────► R2 gate ─► release, release/r2 cut, owner soak
                SV · P · I · W · F · RC  (parallel with VM; SV and I are used by R2, the rest land unlinked)
                G-* streams may start on their branches (merged after the R2 cut)
Phase 4  P0'    lead: Status game types, manager R3 files with signatures, observer hooks, waker/wol types
                CR's first commit: real-server.yml as the per-game matrix (G streams' real checks need it)
Phase 5  R3     G-minecraft … G-windrose (10) · A · D · WK · N · API · UI · UIV · T · CR · M3   (all parallel)
Phase 6         integration, R3 gate (after the owner's soak OK) ─► release, release/r3 cut; then R3.x per game
```

The ten game streams can start once F, W, I, RC and P have landed: an adapter needs only the contract, `gametest` and the fakeserver, and its real-server test runs through `gametest` with `Real` (7.4), so it doesn't wait for the manager. It does wait for the matrix: CR's first commit (real-server.yml's rewrite, the `plan` job reading `realserver.json`, the `gametest` cells) is merged right after the R2 cut, ahead of the rest of phase 5, and a G stream merges `multigame` into its branch so the lead can dispatch its cells on `mg/g-<id>`. Windrose's presence decision needs a real session log from the owner's PC (5.11), which the lead asks for as soon as the adapter installs and starts the real server.

### 9.3 Streams

**Phase 0 (lead, `mg/p0`).** Creates and owns until handed over: `internal/game/{contract.go,registry.go,values.go,schema.go,record.go}` with tests (`Record` inlining both ways with the typed `played`, `Values` helpers, registry rules), `internal/game/{noids_test.go,imports_test.go,notunnel_test.go}`, and stub files with the exported signatures (bodies return `errors.ErrUnsupported`) for `internal/install`, `internal/worldstore`, `internal/rcon`, `internal/a2s`, `internal/platform/additions.go` (handed to P, who owns it from then on and may split or delete it), `internal/game/gametest`; and `internal/mods/info.go` (`mods.Info`, moved from the manager, which keeps `type ModInfo = mods.Info`, 2.1). *Accept:* builds and vets on all three systems; no behaviour change; this spec's 2.2 compiles as written.

| Stream | Branch | Phase | Needs | Owns | Delivers | Accepted when |
|---|---|---|---|---|---|---|
| **S1** identity and goldens | `mg/r1-golden` | 1 | — | main.go (new constants only); the literal-to-constant edits for 1.2's values in the non-test files of platform, selfupdate, jetkvm, waker, web and manager (each value unchanged, no behaviour change); new `*_identity_test.go`/`*_golden_test.go` files in main, platform, selfupdate, jetkvm, waker, cmd/valheim-waker, config, manager, web; `internal/manager/testdata/{legacy,golden}/` | 1.2 constants; 7.2's identity, flag, settings, legacy-file and Status tests (with the allow-lists), and `TestLegacyRoutes` against bed004b's routes (so R2's rewiring is checked against today's answers); the bed004b behaviour goldens recorded from today's code | renaming any identity literal (the lead tries one) fails a test; no non-test behaviour changes; green on 3 systems |
| **S2** upgrade CI | `mg/r1-ci` | 1 | — | `.github/workflows/build.yml`, `cmd/ci-stubs/`, `.github/scripts/upgrade-test.{ps1,sh}` | 7.3 steps 1-5 on Windows and Mac with numbered test copies; the Mac broken-build test; uploaded branch artifacts with build 0; the release job's real-server check (7.4); `node --check` over `ui/assets/**/*.js` | the existing self-update step and 7.3 steps 1-5 pass on a `multigame` push against bed004b's release; the uploaded branch artifact reports `app.build` 0 |
| **VM** Valheim adapter and manager core (changed: one stream, so the Valheim code moves once and never exists twice) | `mg/manager` | 3 | P0, SV | `internal/games/valheim/**`, `internal/games/all/{all.go,valheim.go}`, `internal/manager/**` (except SV's file), main.go (registry import, wiring), and `internal/web/*.go` only where the refactor forces a compile fix (no route or JSON change in R2) | 5.1 and sections 3-4 for R2: the adapter with the code moved out of the manager; instances, per-instance jobs and files, adoption (with `Afterer.Pending`), the `LegacyStore` snapshots, foreign awareness through SV's scanner, the wake-packet game check; the manager calls the adapter for everything game-specific | the bed004b behaviour goldens reproduced (arguments, `Fingerprint` = `argsKey`, texts, Tracker activities); every existing test green with only 4.1's replacements; the manager tests' injected `Fake` installer and `ModsURL` still take effect; 7.2's manager tests and the `-race` Status test green; upgrade/rollback steps green on Windows and Mac; the real Valheim check green (dispatched) |
| **SV** side pieces | `mg/side` | 3 | P0 | `internal/servers/**` (used from R3), `internal/manager/foreign.go` (scan `games/*/run.json`, PID and start-time check, the warning text), `internal/wol/game.go` (`GameOf`, `PacketFor`) | 3.2 and 3.4's load rules (backup fallback, rebuild, passthrough); foreign awareness's scanner; the wake packet's game id | `TestServersPassthrough`; damaged → backup, missing → rebuild; a live fake run.json found and a dead one ignored; packet round trip, old tag unchanged |
| **P** platform | `mg/platform` | 3 | P0 | `internal/platform/additions.go` and new files only in `internal/platform`: `start_*.go`, `ports_*.go`, `shared_*.go`, `gateway_*.go`, `knownfolder_*.go`, `elevate_*.go`, `procname_*.go`, `firewall_games.go`, `winparse_games.go`, and their tests | 2.12 | Windows and Mac CI: `PortInUse` finds the owner PID of a test listener (TCP and UDP); `Start` with a stdin pipe works, and with the app's end held by a helper that is then killed, the child keeps running and never sees end-of-file; SIGINT (Unix) and Ctrl+C (Windows, from a test helper built with `-H=windowsgui`) reach a child started with `Start`; `OpenShared` lets the file be renamed while open (Windows); `FixFirewallAll`'s Valheim part is byte-identical to `firewallScript`, and `TestFixFirewallAllReal` passes on the Windows runner; no change to existing tests |
| **I** install | `mg/install` | 3 | P0 | `internal/install/**` (not `fake.go`), `internal/steamcmd/**` | 2.3 | Valheim's SteamCMD arguments and texts unchanged (golden); the lock covers `ensureSteamCMD`; another app id's success line is recognised; a start's update skips (doesn't wait) while a fake 10-minute install holds the lock, finishing in under 30 s; downloads reject a wrong md5, sha1, sha256 and sha512 and unsafe zip paths, keep exec bits; Temurin (including the macOS `Contents/Home` layout) and .NET resolvers tested against recorded answers (httptest) |
| **W** worldstore | `mg/worldstore` | 3 | P0 | `internal/worldstore/**` | 2.8 | list, backup, restore, delete, rename, import and export round trips for `folder` and `files`; a locked file leaves the world as it was (Windows); unsafe zip paths refused; nothing written outside its folders |
| **F** fakeserver and gametest | `mg/fakeserver` | 3 | P0, RC | `cmd/fakeserver/{main.go,fw_*.go,demo.go}`, `internal/game/gametest/**`, `internal/install/fake.go` | 7.1 framework, a demo personality, `Conformance`, `Options.Real` | `Conformance` passes for a demo adapter in its tests on Linux, Windows and Mac (a console demo included: its takeover gives `ErrNoConsole` and the chain moves on); two fakes with different scripts run at once |
| **RC** protocols | `mg/protocols` | 3 | P0 | `internal/rcon/**`, `internal/a2s/**` | clients plus `rcon.TestServer`, `a2s.TestResponder` | multi-packet RCON replies, bad password, A2S challenge, timeouts |

**Phase 0' (lead, `mg/p0r3`).** The `games`/`jobs`/`router` types in `internal/manager/status_games.go` with `gameStatusLocked(inst)` calling one hook per stream (`autoStopStatusLocked`, `askStatusLocked`, `routerStatusLocked`, `discordStatusLocked`); the R3 fields on `instance` and `serverRun`; the **observer list** (4.1, `hooks.go`) and its calls from server.go, jobs.go and power.go's `sampleLoop` at fixed points, with no-op observers in A's, D's, WK's and N's files, so those streams only fill in bodies and never edit M3's files; the shared types and signatures more than one stream uses: `ActionError.Data` (A's memory refusal, written by API's `web.fail`), `discord.Webhook` (used by D's ask rule and WK's `wakerPlan`), and `internal/routerports`' types and function signatures with stub bodies (`serverRun.mappings` needs `routerports.Mapping`; N fills them in); every R3 manager file below created with its function signatures and empty bodies, so streams call each other from day one (for example `startFromWake(id, tag string) error` in A's `resume.go`, used by WK's `wakesignal.go`); every new public manager method the API needs, with its signature; `waker.GameCommand`; `internal/web/ui/assets/games/README.md` with 6.1's module contract.

In R3 every manager file not listed for A, D, WK or N (instance.go, server.go, jobs.go, status.go and the rest) stays with VM's agent as **M3**, branch `mg/manager-r3`: the R3 parts of sections 3-4 that are no other stream's (servers.json creation and safe mode, hidden legacy, start-with-app per game, the `Status.job` rule, the install job's cache refresh, the LAN cache, the servers.json-backed `game.Store`, stars, the sidebar state and `disabled`), the manager's side of section 2 that Valheim didn't need in R2 (2.4's `Capture` and `GameLog` output with the reopening tail, 2.6's probe loop and `presenceSeen`, 2.7's parallel stops, 2.11's stdin pipe, save line before exit and update wait, `EGS_DEV_FAKE_SERVERS` → `Env.Fake`), and the fixes and small hooks other streams ask for, merged first whenever they arrive. *M3 is accepted when:* `TestSafeMode`, `TestHiddenLegacy`, `TestNoForeignFilesInLegacyPlaces`, `TestSecretFilesPrivate`, and `TestNewIsQuick`/`TestStatusIsQuick` with 11 instances are green; the `-race` Status test passes with several fake games running; a console demo game saves on Quit and, after a takeover, stops through Ctrl+C; a `GameLog` game's log survives the game rotating it; every existing test is still green.

| Stream | Branch | Needs | Owns | Delivers | Accepted when |
|---|---|---|---|---|---|
| **G-minecraft** | `mg/g-minecraft` | P0 (P0' for its UI module), F, W, I, RC, P | `internal/games/minecraft/**` (its resolver, `realserver.json`, `FACTS.md` and testdata inside), `internal/games/all/minecraft.go`, `cmd/fakeserver/minecraft.go`, `internal/web/ui/assets/games/minecraft.js` | 5.2 | `Conformance` green on Linux, Windows and Mac; Monitor tests on sample logs (deaths list, turned away, renamed join); `server.properties` (with the allow list) and command line golden; real-server check green; each "verify" settled with the source noted in `FACTS.md` |
| **G-terraria** | `mg/g-terraria` | same | same pattern | 5.3 | as above, with the takeover falling back to Ctrl+C |
| **G-necesse** | `mg/g-necesse` | same | same pattern | 5.4 | as above |
| **G-vintagestory** | `mg/g-vintagestory` | same | same pattern | 5.5 | as above; real check Windows only |
| **G-zomboid** | `mg/g-zomboid` | same | same pattern | 5.6 | as above; real check Windows only |
| **G-enshrouded** | `mg/g-enshrouded` | same | same pattern | 5.7 | as above; real check Windows only |
| **G-corekeeper** | `mg/g-corekeeper` | same | same pattern | 5.8 | as above; real check Windows only |
| **G-palworld** | `mg/g-palworld` | same | same pattern | 5.9 | as above; real check Windows only |
| **G-satisfactory** | `mg/g-satisfactory` | same | same pattern | 5.10 | as above; real check on demand |
| **G-windrose** | `mg/g-windrose` | same | same pattern | 5.11 | as above; real check on demand; the presence decision recorded from a real session log |
| **A** lifecycle | `mg/lifecycle` | P0', VM | `internal/manager/{addgame.go,remove.go,memory.go,autostop.go,ports.go,firewall.go,power.go,resume.go}` | 2.5, 4.3, 4.5, 4.7, Add/remove, all-games firewall | with fakeservers and a fake clock: ports avoid Valheim's and other instances'; memory short gives `action: "memory"` with data; `stop:<id>` stops then starts; auto-stop after N minutes at count 0 and not with presence none or unproven log presence; last player leaves at T → the game stops at T+60, the computer sleeps right after, the resume set is empty; idle sleep blocked by a presence-none game and by a foreign one; Sleep stops all and records the resume set; a game wake cancels the resume; the resume waits for the network (4.7's two cases); the KVM packet resumes; a hidden Valheim is never started |
| **D** Discord | `mg/discord` | P0', VM | `internal/manager/{discord.go,records.go,reminder.go,ask.go}`, `internal/discord/**` | 6.3, 4.6 | Valheim's texts golden; a failing channel doesn't delay another's posts; daily posts per game; a new game's nil Discord posts in the main channel; auto-stop texts at 30, 60 and 120 minutes; the "still playing?" rule with a stand-in Discord API (messages endpoint, 403: the other wording) and a fake clock, ending with one "Turning off" post |
| **WK** waker | `mg/waker` | P0', VM | `internal/waker/**`, `internal/wol/**` (except SV's `game.go`), `internal/jetkvm/**`, `cmd/valheim-waker/**`, `internal/manager/{waker.go,wakesignal.go}` | 6.4 | 6.4's tests; bot tests: commands from `Games`, channel check, per-game waking, the 10-minute text; an old config still loads (`TestLoadConfig` unchanged); `wakerSetup` still comparable; `fakekvm` install with games; the guild set with Valheim's Discord off; a star change while `wk.busy` retries |
| **N** router ports | `mg/router` | P0', VM, P | `internal/routerports/**`, `internal/manager/router.go` | 6.5 | stand-in IGD (SSDP + SOAP), NAT-PMP and PCP servers: map, renew, unmap; a 718 conflict whose entry points at this computer counts as open; NAT detection; the manual-steps text filled in |
| **API** | `mg/api` | P0' | `internal/web/*.go`, `internal/web/devfixtures/**` | 6.2 | `TestLegacyRoutes` (with `discord/test {webhook}`); every new route tested against the manager with fakeservers; partial settings merge; `guard` on every write |
| **UI** | `mg/ui` | P0', API | `internal/web/ui/**` except `assets/games/*.js`; deletes the old page modules in a last commit, merged after UIV | 6.1 | `node --check` on every module; with the dev fixtures and then the real app with fake servers: layout A, stars and both lists remembered, Overview, Add a game (greyed "Needs Windows" on a Mac, the star checkbox), a game's tabs, the memory dialog; a Go test loads the index and every module |
| **UIV** Valheim pages | `mg/ui-valheim` | P0', UI | `internal/web/ui/assets/games/valheim.js` and new helper modules it needs under `assets/games/valheim/` | today's Valheim-only panel parts (difficulty and mods on world cards, the Mods tab, the join-code card, tutorial, mods help) as the Valheim module, moved before UI deletes the old modules | the Valheim pages do what today's pages do |
| **T** rename, tray, docs | `mg/rename` | P0', A; merged after UI, UIV, API, CR and WK | `internal/tray/**`, main.go (visible name, tray wiring, `serverStopper`), `README.md`, `docs/READ-ME-FIRST*.txt`, `macapp/Info.plist` (`CFBundleDisplayName`, `CFBundleName`), `winres/winres.json` (visible fields only), and, in one commit agreed with their owners, the visible-name strings in index.html, the manifest, the panel texts, `internal/web/web_test.go`, build.yml's smoke tests, the two macOS prompts, `internal/worlds/library.go:606` and `internal/manager/waker.go:554` | 1.2's visible rename (every place it lists), 6.6 | identity tests still green; the goldens holding these texts updated in the same commit; tooltip cut is rune-safe; the tray has exactly the three decided items; Mac reopen texts name "Valheim Easy Server"; no "tunnel" in the docs; `plutil -lint` passes |
| **CR** real-server CI | `mg/ci-real` | F (first commit, merged right after the R2 cut); P0', A, G-minecraft (the rest) | `.github/workflows/real-server.yml` (rewritten in place, 7.4), `.github/workflows/build.yml` (R3 changes: 7.3 steps 4-6 and the R1 → R3 run), `internal/manager/managertest/**` | 7.4, 7.3's R3 parts | first commit: a dispatch on `multigame` runs Valheim's cells green through the matrix (from VM's `internal/games/valheim/realserver.json`, 5.1); the matrix comes from the `realserver.json` files (with `setup`); a push runs only changed cells. The rest: Minecraft's manager-level cell green; the upgrade steps run with a second fake game |

All phase-5 streams run in parallel. Merge order the lead follows: API before UI and UIV; UIV before UI's last commit; A before CR; G-minecraft before CR; T last of the panel and CI streams; each G stream whenever it's accepted (stage preview until its real-server check is green).

---

## 10. The owner's answers and open questions

### 10.1 Answered on 2026-11-26 (decisions.md), and where this spec applies them

- **The "still playing?" check when the bot can't read the channel, or there's no bot:** same rule; the question still goes out (worded for the control panel), Keep it on counts as a reply, and with no reply the game turns off 2 hours later (4.6).
- **Any other game without a usable join/leave signal:** the same rule as Windrose (4.6). Note: Core Keeper, which was the likeliest, turned out to have join and leave lines (5.8), so it is expected to get the normal auto-stop once a real log proves them.
- **Project Zomboid:** Build 42, Steam's default branch (5.6).
- **R2** reaches the owner as a normal auto-update; R3 waits for the owner's OK after the soak (8.2).
- **The R3 Mac checklist** runs on the owner's real data folder (7.5, which now says how to run the candidate).
- **Windows tray menu:** Open the control panel, Stop all games, Quit; no per-game entries (6.6).

### 10.2 Still open

- **Q1. Releases between the gates.** decisions.md allows releases only at the R1/R2/R3 gates. May these also go out, each one only with your OK: (a) R2.x fixes found during the soak; (b) a no-change R2.x, made only if R2 arrived while Valheim wasn't running, so the soak still covers an auto-update while Valheim runs; (c) after R3, R3.x releases that turn one game from preview to ready, or fix it? Without (c), games that are preview at R3 stay preview until the next round.
- **Q2. A "still playing?" game with no Discord channel at all** (no main channel and no own one): the plan is the same clock without a post: at 4 hours the control panel shows the question with Keep it on, and 2 hours later the game turns off. Is that right?
- **Q3. Adding a game stars it** (it shows under Active and gets its /command in Discord) unless you untick "Star it" in Add a game. Should the box be ticked by default?
- **Q4. "Quit" in the tray** closes the app and leaves running games running, as today's "Close app (server keeps running)" does; "Stop all games" stops them. Is that what you meant?
- **Q5. Windrose's server is a 3 GB download** (round one's 35 GB was the recommended disk space). You chose on-demand real-server checks for it because of its size. Keep it on demand, or check it like the other games?
- **Q6. Minecraft on the Mac Pro.** Today's newest Minecraft runs there (its Java needs macOS 11). If a later Minecraft needs a Java that won't start on macOS 12, the app would install the newest Minecraft that does run, and tell friends to use that version. Is that right, rather than showing Minecraft as unavailable on the Mac?

### 10.3 Good to know (no answer needed)

- **The app's name on the Mac:** updates replace only the program, so installed Macs keep showing "Valheim Easy Server" in Finder, Spotlight and Login Items; the panel says "Easy Game Server" and sends you to "Valheim Easy Server" in Applications when it needs to (1.2).
- **Vintage Story needs a password** on the server (5.5): since 1.20 its servers are invite-only, and this round has no allow-list screen for it.
- **Console games after an app update or restart** (Terraria, Necesse, Vintage Story): the app saves them before it closes and waits with automatic updates while people play them; if one is stopped later by the copy of the app that took it over, the stop can't type a save command, so anything since its last autosave is lost (Terraria saves every 10 minutes and when the last player leaves) (2.11).
