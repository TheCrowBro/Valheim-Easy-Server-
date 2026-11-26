# Core Keeper: the facts the adapter relies on

Each fact says where it comes from and when it was checked. "The real
server" means Core Keeper Dedicated Server 1.3.0.7 (its log: "Game version:
1.3.0.7-5554"), Steam app 1963720, public branch build 26331994 (built
2026-10-30), downloaded anonymously with SteamCMD and run on Linux under
Xvfb on 2026-11-26. Its logs are in `testdata/real/`, byte for byte except
that this machine's folders were replaced by the app's layout
(`/home/owner/EGS/games/corekeeper/...`). The Windows download of the same
build was fetched with SteamCMD (`+@sSteamCmdForcePlatformType windows`) and
its files inspected, not run: only the Windows real-server cell runs it.
The real-server check itself (`realserver_test.go`: Conformance against the
real server, then the Game ID, a world with a mode and seed, a restart and a
takeover) passed here on Linux on 2026-11-26, with Xvfb and the server
installed beforehand (`EGS_CK_INSTALLED`).

Spec section 5.8's "verify" marks are settled in the sections that name
them below. Where this file and the spec disagree, the code follows this
file, and "Departures from the spec" lists each case.

## The download

- **Steam app 1963720, anonymous.** Source: `api.steamcmd.net/v1/info/1963720`
  (2026-11-26): "Core Keeper Dedicated Server", `"freetodownload": "1"`,
  `oslist windows,linux` (no macOS), public build 26331994. SteamCMD's
  `+login anonymous +app_update 1963720 validate` installed it here at the
  first try. Older builds are kept as branches `1.2.1.5`, `1.2.0.7`,
  `1.1.2.10`, `1.1.2.0` ("Legacy build"); the app uses the public branch.
- **Sizes** (installed, from the app info and the installed manifests):
  Windows: depot 1963721 417 MB (163 MB to download) plus Steam's own
  libraries, depot 1004 (68 MB, 24 MB to download): 485 MB on disk.
  Linux: 481 MB + 111 MB. Hence `DownloadMB: 490` and `SteamApp.SizeGB: 0.5`.
- **Layout** (Windows): `CoreKeeperServer.exe`, `UnityPlayer.dll`,
  `CoreKeeperServer_Data/`, `MonoBleedingEdge/`, `D3D12/`, the Steam DLLs,
  `Launch.bat`, `Launch.ps1`, `README.txt`, `ARGUMENTS.txt` and
  `steamapps/appmanifest_1963720.acf` (the build `install.SteamBuild` reads).
  Core Keeper's own version is in no file Steam writes; the server logs it.
- **The server's Steam app id is the game's**: "Initializing Steamworks with
  AppID 1621690". No `steam_appid.txt` or environment is needed.

## The program

- **Steam's launch settings** (app info, `config.launch`): Windows
  `Launch.bat`, Linux `launch.sh`. `Launch.bat` is one line,
  `powershell -executionpolicy unrestricted -File %0\..\Launch.ps1 %*`, and
  `Launch.ps1` starts `CoreKeeperServer.exe -batchmode -logfile
  CoreKeeperServerLog.txt` with `Start-Process`, waits for `GameInfo.txt` and
  prints it. Linux's `_launch.sh` starts Xvfb and runs `./CoreKeeperServer
  -batchmode -logfile CoreKeeperServerLog.txt` with `LD_LIBRARY_PATH`
  including `linux64/`. So `Program()` is `server/CoreKeeperServer.exe`,
  started directly (spec 2.4), and the Linux plan sets `LD_LIBRARY_PATH`.
- **Never `-nographics`.** README.txt (both downloads): "you need the
  -batchmode parameter (NOT -nographics since part of the procedural
  generation is done on the GPU)".
- **Windows: CoreKeeperServer.exe is a windowed ("GUI") program**: its PE
  header's subsystem is 2 (IMAGE_SUBSYSTEM_WINDOWS_GUI), read from the
  Windows download on 2026-11-26. Ctrl+C can only reach a console program
  (spec 2.7), so the stop can't be Ctrl+C on Windows; see "Settled: how it
  stops". The Windows real-server cell logs the subsystem again.
- **Unity's identity**: `CoreKeeperServer_Data/app.info` says "Pugstorm",
  "Core Keeper", the same as the game's, and the server keeps Unity's player
  settings under that name. On Linux it wrote
  `~/.config/unity3d/Pugstorm/Core Keeper/prefs` (window size 1024x768,
  fullscreen mode 1, a session count) and `Sentry/` crash-report files
  there, outside its data folder, with every `-datapath` run. On Windows
  Unity keeps the same settings in the registry
  (`HKCU\Software\Pugstorm\Core Keeper`), shared with the owner's own Core
  Keeper; not checked on a real PC. Pugstorm's own launcher does the same.
  The Windows real-server cell records whether
  `%USERPROFILE%\AppData\LocalLow\Pugstorm\Core Keeper` changes.

### Settled: graphics (spec 5.8, "needs a graphics device")

- On Linux with Xvfb (`Xvfb :97 -screen 0 1x1x24`, as `_launch.sh` does) and
  no graphics card, Unity made an OpenGL 4.5 device on Mesa's software
  renderer ("Renderer: llvmpipe (LLVM 20.1.2, 256 bits)"), said "Checking
  system requirements: PASS" and made and saved worlds normally.
- **With no display at all** (no Xvfb, `DISPLAY` unset) the server went no
  further than "Desktop is 0 x 0 @ 0 Hz" (`testdata/real/no-display-hangs.log`):
  no session after 90 seconds, and it didn't answer Ctrl+C (it was killed).
  The Linux real-server check starts Xvfb itself.
- The server's own check of the computer is "Checking system requirements:
  PASS" or "FAIL", then "System requirements not met: Game will probably not
  run correctly (missing/broken visuals etc.)" (its code's strings). The
  Monitor warns on FAIL and ExitHint names the graphics driver.
- **Windows' basic display adapter** (the GitHub runner has no graphics
  card): settled by the Windows real-server cell, which needs the server to
  make, save and load a world.

## The command line

- **`ARGUMENTS.txt`** (both downloads, 2026-11-26): `-world INDEX` (0 to 29),
  `-worldname NAME` ("the name that connecting clients will see"),
  `-worldseed SEED` ("Only takes effect if set when the world is first
  created. Omit to generate a random seed."), `-hashedworldseed`,
  `-worldmode Normal|Hard|Creative|Casual` ("Changing between creative and
  non-creative modes after the world has been created is not supported"),
  `-season`, `-activatecontent`, `-activateallcontent`, `-datapath PATH`
  ("the save file location"), `-gameid ID`, `-password` (direct connections
  only: "The password is not used when connecting using game ID, because the
  game ID alone provides all capabilities a password would"), `-port`
  (direct connections; "If omitted, the server will instead use the Steam
  relay network"), `-ip`, `-maxplayers COUNT`, `-allowonlyplatform`.
- **The server writes its settings back.** At each start it writes
  `<datapath>/ServerConfig.json` from its command line (keeping the file
  before as `ServerConfig.json.pugbackup`), and uses that file for whatever
  the command line leaves out (README: "Most arguments can also be found and
  changed in the ServerConfig.json file"). A second start with another
  `-worldname`, `-worldseed`, `-worldmode` and `-maxplayers` wrote all four
  into the file (`testdata/real/ServerConfig-after-two-starts.json`), while
  the world kept its seed ("Using seed 1391267135").
- **The player limit defaults to 100**, not the README's 8: a fresh data
  folder started without `-maxplayers` wrote `"maxNumberPlayers": 100`. So
  the Plan always gives `-maxplayers` (default 8).
- **No seed**: "No seed string provided, using random string: 920313824799
  hashed to 299130195", and that string is written into ServerConfig.json.
  So a new world without a seed gets a random one, as long as the data
  folder is new (it is: each world has its own, below).
- **Values with spaces, quotes and emoji get through**: `-worldname "Crow's
  Cave ✓ 🏡 a long name with forty-plus characters in it ok"` (63
  characters) was kept whole in ServerConfig.json, and `-datapath
  ".../saves/Crow's Cave ✓"` was used as given
  (`testdata/real/own-gameid-unicode-folder.log`). Unity reads its command
  line as Unicode on Windows, so unlike Java there is no code page to worry
  about there (not checked on a real PC; the Windows cell's world folder
  has a space and its server name a "✓").

## Settled: the Game ID (spec 5.8)

- **The log says it when the session is up**: "Started session with info:
  Ek8c5udkq9vH3cf" in 1.3.0.7 (`testdata/real/`), "Started session with
  Game ID bvpvPP6a3VjMucnq5Cu8HifZQ73C" in 1.0.0.14 (the GitHub log below).
  The bare Game ID is the line before it. The Monitor takes the Game ID
  (`RunState.Code`) from it.
- **The world comes after the session**: the server loads or makes the
  world only after that line ("no existing save file, starting with empty
  world" or "successfully loaded world file into SerializeWorld", then the
  seed and content lines), and then pauses its clock until someone joins:
  "timescale = 0" is the first line after the world is up in all four real
  logs (1.3.0.7 new and saved worlds, 1.0.0.14 new world). So "online" is
  that first "timescale" line after the session line (spec 5.8 said the
  session line), with a fallback from Tick two minutes after the session
  line in case a later version words it differently. Asked to quit between
  the two, the real server quit at once without saving: "no existing save
  file, starting with empty world", "Got quit request", "Running quit
  handlers", no "Quit blocked", no world file (seen in the real-server
  check here on 2026-11-26).
- **`GameInfo.txt` and `GameID.txt` are written next to the program** at
  each start ("GameInfo path: .../CoreKeeperServer_Data/../GameInfo.txt"),
  about 20 seconds after the start here. GameInfo.txt held "No GameID found.
  Creating new GameID for the session." then "GameID: Ek8c5udkq9vH3cf" at the
  first start, and only "GameID: Ek8c5udkq9vH3cf" later
  (`testdata/real/GameInfo-*.txt`); GameID.txt holds the bare Game ID. With
  direct connections it also has "Steam GameID: ", "Local IP: ", "Public IP:
  ", "Port: " and "Password: " lines (the server's code; escapingnetwork's
  log parser reads both "GameID:" and "Steam GameID:"). The Plan deletes
  both files before a start, as `Launch.ps1` deletes GameInfo.txt, so an old
  one isn't mistaken for this run's.
- **It is kept between starts**: the server saves it in ServerConfig.json
  and says "Old and new server gameid is the same" at the next start.
- **The server takes a Game ID it is given**: `-gameid
  Ab3dEfGh9JkLmNpQrStUvWz12345` (28 characters) was used as given, in the
  log, GameInfo.txt and ServerConfig.json. Its rules (ARGUMENTS.txt): 15 to
  28 letters and digits, not Y, y, x, 0 or O; otherwise it makes a random
  one ("Provided GameID ... was too short/too long/contained invalid
  characters. Proper GameID was created instead.", its code's strings).
- **The app makes the Game ID** (28 characters from those allowed, with
  crypto/rand) the first time a world starts, keeps it in the instance's
  settings (`State["gameId"]`) and gives it on every start, because each of
  the app's worlds has a data folder of its own (below) where the server
  would otherwise make a new one. So friends keep one Game ID for the
  instance, Join can show it while the server is stopped, and the Monitor's
  `Code` (from the log) confirms it while it runs.

## Joining

- **Friends join by Game ID, through Steam**: in Core Keeper, "Join Game",
  paste the Game ID into the box, "Join" (nodecraft.com, "How to Join Your
  Core Keeper Server", updated 2024-10-06). README.txt, "Limitations": "The
  dedicated server is currently only available via Steam" unless direct
  connections (`-port`) are set up, which needs router setup and shares IP
  addresses. The app never gives `-port`: Steam's relay needs no router
  setup (Join.Router false), and there is no password (the Game ID is it).
- Patch 1.1.2.4 notes say "the game currently does not support crossplay
  between PC and consoles" (gameserverkings.com, read 2026-11-26). Friends
  need the same version as the server (the same page).

## Settled: how it stops (spec 5.8, 2.7)

- **On Linux, Ctrl+C (SIGINT) is a quit request, and the server saves.**
  Three runs (`testdata/real/*-ctrl-c.log`, `own-gameid-unicode-folder.log`):
  "Got quit request", "Exit blocked by ECSManager", "Quit blocked" while it
  saves, then "Got quit request", "Running quit handlers"; exit code 0, 4
  seconds after the signal. The world file was written in the quit handlers
  (`0.world.gzip` rewritten, the one before kept as `0.world.gzip.pugbackup`;
  for a new world the file appeared only then). Reclyptor's toolkit measured
  the same for SIGTERM (github.com/Reclyptor/CoreKeeper SPEC.md, "Measured:
  save on SIGTERM").
- **On Windows the program is windowed, so the stop copies what Pugstorm's
  launcher does when the owner presses q** (Launch.ps1, 2026-11-26):
  `taskkill /pid $ckpid.Id` (no `/F`), then `Wait-Process`. Without `/F`,
  taskkill asks the program's windows to close; Unity takes that as the
  same quit request. A Unity issue ("taskkill command is ignored when the
  Player is running in Batch mode with Graphics API Vulkan", Unity issue
  tracker; the search summary says the second taskkill works) shows a
  batch-mode player normally answers taskkill. So on Windows the stop
  chain is one "call" step that runs `taskkill /pid <pid>` (hidden, after
  checking the process still has the run's start time); on Linux, and for
  the stand-in everywhere (a console program with no window), it is one
  "interrupt" step (Ctrl+C, SIGINT). It is sent again every minute and
  waits 3 minutes in all; the server ignores a second request while it
  saves ("Save is still pending", "Quit blocked"). It uses only the run
  record's process id, so it works the same after a takeover. **Not run
  here**: the Windows real-server cell settles it (its stop must end the
  server with the world saved, and the check fails if `Launch.ps1` stops
  using `taskkill /pid`).
- **After a takeover nothing is lost**: the server has no console, so the
  app that took it over stops it exactly as the one that started it.
- The log has no line once the world file is written. The Monitor counts a
  run whose world was up and that went on to "Running quit handlers" (and
  didn't say "Skipping world save due to ...") as saved when the process
  ends.
- **Autosave**: none was seen in 7 minutes with nobody on: the world is
  paused while empty ("timescale = 0"; "timescale = 1" when a player joins,
  the GitHub log below), and the server has a `-disableautosave` switch and
  an autosave warning for players ("not sending autosave warning since no
  connections", its code's strings). How often it saves with players on is
  not known; the quit request saves either way.

## Settled: the worlds (spec 5.8)

- **The server's layout**: `<datapath>/worlds/<slot>.world.gzip`, slot 0 to
  29 (`-world`), written only when the server saves, with the previous save
  as `<slot>.world.gzip.pugbackup`. Beside it the server writes
  `ServerConfig.json`, `Admins.json`, `PlayerBans.json` (each with a
  `.pugbackup`), `modloader/config.json` and `mods/README.txt` ("MODS ARE NOT
  LOADED FROM HERE"). Without `-datapath` it uses
  `%USERPROFILE%\AppData\LocalLow\Pugstorm\Core Keeper\DedicatedServer`
  (Windows) or `~/.config/unity3d/Pugstorm/Core Keeper/DedicatedServer`
  (Linux) (ARGUMENTS.txt; gameserverkings.com; corekeeper.atma.gg Save_data).
- **The app gives each world its own data folder**, `saves/<world>/`, and
  always runs slot 0 there. A world is then a folder, which the shared world
  store handles whole (backups and restores carry the world's settings and
  lists with it, renames are folder renames, no slot numbers to keep in
  step), and there is no limit of 30. See "Departures".
- **The owner's own game keeps its worlds** in
  `%USERPROFILE%\AppData\LocalLow\Pugstorm\Core Keeper\Steam\<steam id>\worlds\<slot>.world.gzip`
  (Linux: `~/.config/unity3d/Pugstorm/Core Keeper/Steam/<id>/worlds/`), with
  each world's name, seed and mode in `worldinfos/<slot>.worldinfo`
  (corekeeper.atma.gg, Save_data, read 2026-11-26; hosting guides from
  sparkedhost.com and xgamingserver.com say the same, and that a world copied
  to a server must be put in the slot the server runs).
- **Importing**: worlds from the owner's own game are single files named
  by slot, in a folder of many. Offering them (worldstore's `Detect`/`Place`
  or `LocalIn`) needs `worlds.Candidate`, which adapters may not import
  (spec 2.1; the same gap G-minecraft and G-necesse reported). So
  `Info.Import` is false. An upload of a world folder (an app backup or a
  server's data folder) imports as it is, and an upload holding one
  `<slot>.world.gzip` becomes that world's slot 0 at its next start
  (`tidyWorld`, which refuses to guess between several).

## The log lines (settled with one real join; spec 5.8, 2.6)

- **Where the strings come from**: the user-string heap of the 1.3.0.7
  server's `CoreKeeperServer_Data/Managed/Pug.Other.dll` (read on
  2026-11-26): "Started session with info: ", "[userid:{0}] player {1}
  connected islocalplayer={2}", "] is using new name ", "Disconnected from
  userid:" + " with reason ", "Rejecting player with userid:{0} since player
  limit has been reached", "Got quit request", "Quit blocked", "Save is
  still pending", "Running quit handlers", "Skipping world save due to
  ...", "Checking system requirements: FAIL", "Not initializing Steam
  network since no Steam API available", "Successful authentication from
  userid: {0}". The Windows and Linux servers are the same Unity code, so
  the same lines come on both.
- **A real join and leave** (the only real log with a player found, since a
  CI run has no game client): a player's log in GitHub issue
  escapingnetwork/core-keeper-dedicated#66 ("World save folder not found",
  2024-10-29, version 1.0.0.14-34ff), lines in order in
  `testdata/forum/github-2024-10-29-v1.0.0.14-join-leave.log`: "Connected to
  userid:76561198056526695", "Successful authentication from
  userid:76561198056526695", "[userid:76561198056526695] player Gonaeng
  connected islocalplayer=False", "[userid:76561198056526695] is using new
  name Gonaeng", then "Disconnected from userid:76561198056526695 with
  reason App_Min". The issue page was read through a summarising fetch, so
  the file holds the lines it listed from that post, in order: its three
  start lines ("Logged in", "Listening on SteamID", "Started session"), then
  every line from "no existing save file" to the last "Quit blocked"; the
  lines between those two parts were left out. It is not a byte-for-byte
  copy of the post.
  escapingnetwork's own log parser (`scripts/logfile-parser.sh`, read
  2026-11-26) matches the same three lines for its Discord join, leave and
  start messages.
- **So**: join = the "player ... connected" line, with the Steam account;
  the leave line names only the account, so the Monitor keeps who each
  account is; the "is using new name" line renames. `Presence: ByName`,
  which the manager treats as unknown until this install has seen a join
  (spec 2.6). There is no death line, so `Deaths: false`.
- **The relay**: in this sandbox, which only lets HTTPS out, the server
  logged in to Steam ("Logged in with userid:...") and started its session,
  but logged "SteamNet Error: Unable to communicate with ANY of 33 Steam
  Datagram routing cluster" every few seconds. Friends couldn't have joined.
  The Monitor warns once and hides the repeats.

## Memory and time

- The Linux server used about 1.4 GB (VmRSS 1,415,404 kB) with nobody on, a
  minute after starting a new world, with Mesa's software renderer, and
  1325 to 1377 MB in the real-server check here. Hence `MemoryMB: 2048`.
  The Windows cell logs its figure.
- A new world was ready about 20 seconds after the start (21 to 26 s in the
  real-server check here), and a saved one too; the quit request ended it
  2.6 to 4 seconds after it was sent. `ReadyWait` is 10 minutes for a new
  world and 5 for a saved one, for slower computers.

## Departures from the spec

- **Worlds: one data folder per world, always slot 0**, instead of 5.8's
  `saves/worlds/<index>.world.gzip` with names in `State` (`world.<index>`).
  With slots, a deleted world's backup restores into a slot another world
  may have taken since, an import has to pick a slot through a `Place` the
  adapter can't write, and renames and backups have to keep a separate list
  in step. With a folder each, the world store does it all itself.
- **The app makes the Game ID and gives it with `-gameid`**, instead of
  reading the server's from GameInfo.txt: the per-world folders would give
  each world its own Game ID otherwise. GameInfo.txt is still deleted before
  each start, and the real-server check reads it to confirm the Game ID.
  Join reads no file (spec 4.8), so it takes the Game ID from the log
  (`RunState.Code`) or the settings.
- **`-worldname` is the server name**, not the world's name: ARGUMENTS.txt
  says it is "the name that connecting clients will see", which is what the
  owner types as the server's name in Add a game; hosting panels map it the
  same way (Xylona's definition gives `-worldname {{SERVER_NAME}}`). The
  world's own name names its folder.
- **"Online" is the world being up** (the first "timescale" line after the
  session line), not the session line itself, which comes before the
  world exists.
- **On Windows the stop is a "call" step** (taskkill, as the shipped
  launcher does), not "interrupt", because Ctrl+C can't reach the Windows
  server. Elsewhere, and for the stand-in, it is "interrupt".
- **`Import: false`**, until adapters can name `worlds.Candidate` (above).
- **`DownloadMB: 490`**, the server's depot and Steam's libraries, rather
  than 420 for the depot alone.
- **New world settings**: `mode` (normal, hard, casual, creative) and
  `seed`, asked only when a world is made; the server keeps both in the
  world and its folder's ServerConfig.json, so later starts leave them out.
