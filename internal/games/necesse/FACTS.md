# Necesse: the facts the adapter relies on

Each fact says where it comes from and when it was checked. "The real
server" means Necesse Dedicated Server 1.3.3, Steam app 1169370, public
branch build 24926481 (built 2026-08-25), downloaded anonymously with SteamCMD
and run headless (`-nogui`) on Linux on 2026-11-26. Its console output is in
`testdata/real/` (two lines Java printed because of the test machine's own
environment, "Picked up JAVA_TOOL_OPTIONS/_JAVA_OPTIONS", were removed, and
the data folder's path was replaced with `/home/owner/EGS/games/necesse/saves`;
everything else is byte for byte, colour codes included). The Windows and
macOS downloads of the same build were fetched with SteamCMD
(`+@sSteamCmdForcePlatformType`) and their files inspected, not run.

Spec section 5.4's "verify" marks are settled in the section that names them
below ("Settled" headings). Where this file and the spec disagree, the code
follows this file, and the "Departures from the spec" section lists each case.

## The download

- **Steam app 1169370, anonymous.** Source: `api.steamcmd.net/v1/info/1169370`
  (2026-11-26): `"freetodownload": "1"`, `oslist windows,macos,linux`, public
  build 24926481. SteamCMD's `+login anonymous +app_update 1169370 validate`
  installed it here (first attempt on a fresh SteamCMD: "ERROR! Failed to
  install app '1169370' (Missing configuration)", the second succeeded;
  internal/steamcmd already retries).
- **Sizes** (depots of build 24926481, app info; download is compressed):
  Windows: depot 1169373 72.7 MB download / 180.1 MB installed, plus Steam's
  shared libraries (depot 1004 from app 1007: steamclient*.dll, 24.3 MB /
  67.9 MB); about 248 MB on disk in all. macOS: depot 1169374 67.0 MB /
  163.1 MB (no Steam libraries). Linux: 75.9 + 36.7 MB / 302 MB. Hence
  `DownloadMB: 100` and `SteamApp.SizeGB: 0.3`.
- **Layout** (all three downloads): `Server.jar` (20.5 MB, Main-Class
  `StartDesktopServer`), `lib/*.jar`, `locale/*.lang`, the bundled Java in
  `jre/`, the start scripts, and `steamapps/appmanifest_1169370.acf` (the build
  `install.SteamBuild` reads). Necesse's own version number (1.3.3) is in no
  file Steam writes; the server prints it.

## The program

- **Steam's launch settings** (app info, `config.launch`): Windows
  `jre\bin\javaw.exe -jar Server.jar` and, "Launch server from terminal",
  `jre\bin\java.exe -jar Server.jar -nogui`; macOS
  `jre/Contents/Home/bin/java -jar Server.jar &`; Linux `./jre/bin/java -jar
  Server.jar`. So `Program()` is `server/jre/bin/java.exe` on Windows and
  `server/jre/Contents/Home/bin/java` on a Mac.
- **The bundled Java is Eclipse Temurin 17.0.19+10** (`jre/release` in each
  download).
- **Windows: `java.exe` is a console program** (PE32+, subsystem 3,
  `file`: "PE32+ executable (console) x86-64"); `javaw.exe` is the windowed
  ("GUI") one. Spec 2.7 needs a console program for Ctrl+C, so the app runs
  java.exe. The Windows real-server cell checks the subsystem again
  (`javaFacts`).
- **The start scripts** (`StartServer-nogui.bat/.command/.sh`) only change to
  the server folder and run Java with G1 settings: `-XX:+UnlockExperimentalVMOptions
  -XX:+UseG1GC -XX:+ExplicitGCInvokesConcurrent -XX:G1NewSizePercent=20
  -XX:G1ReservePercent=20 -XX:MaxGCPauseMillis=50 -XX:G1HeapRegionSize=32M -jar
  Server.jar -nogui`. They set no environment. The Plan does the same itself
  (Dir = the server folder, the same Java options), adding `-Xmx`.

### Settled: the Mac server's architecture and the oldest macOS (spec 2.10, 5.4)

- `file jre/Contents/Home/bin/java` on the macOS download: "Mach-O 64-bit
  x86_64 executable". `libjvm.dylib`, `libjli.dylib`, `libnet.dylib` and
  `libnio.dylib` are x86_64 too. Not universal, no arm64.
- Their `LC_BUILD_VERSION`: minos 11.0.0, sdk 14.2.0. So it runs on macOS 11
  and later: the owner's Mac Pro on macOS 12 (Intel) is fine.
- Hence `OS: darwin/amd64 yes, darwin/arm64 no ("Needs an Intel Mac")`. The
  Mac real-server cell logs `file` and the minimum macOS, and fails if the
  Java stops being x86_64.

## The command line

- **`-help` output of the real server** (`testdata/real/help-and-world-prompt.log`):
  `-nogui`, `-settings <file>`, `-world <name>`, `-port`, `-slots`, `-owner`,
  `-motd` ("Use \n for new line"), `-password` ("blank for no password"),
  `-pausewhenempty <1/0>`, `-giveclientspower`, `-logging <1/0>` ("a log file
  for each session, defaults 1"), `-logs`, `-zipsaves <1/0>`, `-language`
  ("only used for occasional messages in log"), `-ip`, `-datadir <path>`
  ("where cache, latest log, saves etc. are stored. Defaults to Necesse folder
  in appdata on different platforms"), `-localdir`. "Parameters not given will
  be loaded from server settings file." There is no seed option.
- Without `-world` the server asks on its console ("Type a name for the world
  to create") and waits. The app always gives `-world`. Given `-world` for a
  world that isn't there, it makes it at once, asking nothing, with a seed of
  its own choosing (`worldSeed = lu6KF` in the new world's `world.dat`); only
  the interactive questions asked without `-world` ("Custom server options?
  (y/n)", 2021 forum log below) could set world options. Hence no new-world
  settings.
- **Values starting with "-" are ignored**: `-password -abc` started a server
  with no password at all (the ready line had no "with password";
  `testdata/real/dash-password-ignored.log`). So Validate refuses a server
  name, password or world name starting with "-".
- **An empty value works**: `-password ""` started a server with no password.
  Spaces inside a value work (`-motd "Necesse at home"`, world "My World's
  #1"). The Windows real-server cell starts with a password containing a
  space and checks the server took it.
- **`server.cfg` is never rewritten from the command line** (after a start
  with `-port 14159 -motd …` it still held its defaults), and the server falls
  back to it for anything not given. Its defaults (written on the first
  start): `port 14159, slots 10, password empty, pauseWhenEmpty = true,
  logging = true, language = en, zipSaves = true`, MOTD empty. So the Plan
  gives every option it cares about, `-password` even when empty.
  `pauseWhenEmpty` is left to server.cfg's `true` (see autosave below).

### Settled: the data folder, and the "APPDATA" question (spec 5.4)

- Without `-datadir` the server used `~/.config/Necesse` on Linux (cfg/,
  cache/, logs/, latest-server-log.txt; checked, then removed). The wiki
  (necessewiki.com/Multiplayer, 2026-11-26) gives `%APPDATA%\Necesse` on
  Windows; hosting guides give `~/Library/Application Support/Necesse` on a
  Mac. That is the same folder the owner's own Necesse game uses, so a server
  without `-datadir` would share the owner's settings and worlds.
- **With `-datadir <dir>` everything goes under `<dir>`**: `cfg/` (server.cfg,
  settings.cfg, banned.cfg, auth), `cache/version.cache`,
  `latest-server-log.txt`, `saves/worlds/`. On Linux the default folder was not
  created by any `-datadir` run (checked after a dozen runs, and by the
  real-server check, which compares the default folder before and after). So
  no APPDATA environment override is needed. The Windows and Mac real-server
  cells make the same before/after comparison of `%APPDATA%\Necesse` and
  `~/Library/Application Support/Necesse`, and fail if either changes.
- Two lines come before the server's own log starts, without a time: "First
  time launched" and "Cache version was not correct, deleted version cache
  files." (a fresh data folder); their state is also in the data folder.
- Not checked (only the owner's PC can): a data folder path with characters
  outside Windows' ANSI code page. Java's launcher on Windows reads its
  arguments in that code page, so such a `-datadir` could arrive mangled.
- A crash writes `latest-crash.log` in the working directory (the server
  folder): "Printing crash log to C:\steamcmd\steamapps\common\Necesse
  Dedicated Server\latest-crash.log" (Steam forum, 2022, below).

## The ports

- **The game port is UDP only.** While the server ran with `-port 14159`
  (0.0.0.0) nothing listened on TCP; `/proc/net/udp` showed the game port and
  the LAN socket. The real-server check logs `PortInUse("udp")` true with the
  server's pid and `PortInUse("tcp")` false. Wiki: "Protocol: UDP. Default
  port: 14159".
- **The LAN socket** ("Started lan socket at port 55169") is UDP 55169, or
  another free port when that one is taken (two servers at once: 55169 and
  51868). It only answers LAN discovery, so it isn't a public port.
- **With the game port taken** the server prints the BindException
  (`(ERR) java.net.BindException: Address already in use`), then still prints
  "Started server using port …" and "Type help for list of commands.", then
  stops: "Stopped server on … with code: NETWORK_ERROR", exit code 0
  (`testdata/real/port-in-use.log`). So the Monitor doesn't count the ready
  line after a BindException, and ExitHint names the port.

## The console

- Each line starts with a colour code (`ESC[39m`; debug lines `ESC[34m`,
  errors `ESC[31m`), then the local time `[2026-11-26 20:17:45] `, then the
  message; debug lines and errors begin `(DEBUG) ` and `(ERR) `. Colours are
  written even into a pipe. Typed lines are echoed as `> stop`. Chat is
  `(name): message` (AMP template below), so the Monitor makes no event from
  a line that starts that way: players can't fake a join or a stop by typing.
- **Version**: "Loading dedicated server on version 1.3.3." and "Started
  server using port 14159 with 10 slots on world "RealCheck.zip", game version
  1.3.3."
- **The password is printed in clear**: `… on world "RealCheck.zip" with
  password "secret1", game version 1.3.3.` and in "Launched game with
  arguments: … -password secret1 …". The Monitor masks it in the lines the Log
  page shows (the run's log file keeps the original).

### Settled: ready, save and stop lines (spec 5.4)

- **Ready**: `Type help for list of commands.` after "Found 0 saved players."
  and "Local address: 127.0.0.1:14159". A new world took about 9 s, an existing
  one about 3 s here.
- **`save`**: "Starting world save", "Starting save...", "Completed world save".
- **`stop`, `exit`, `quit`**: "Starting world save", "Completed world save
  before stopping server", "Stopped server on 2026/11/26 20:18:19 with code:
  SERVER_STOPPED", four statistics lines, "Server has stopped", "Exiting in 2
  seconds...", exit code 0 about 3 s later. Each of the three saved the world
  (the zip changed), except once: on a fresh data folder, `exit` typed 15 s
  after the start printed "Starting world save" but no "Completed…" line and
  left the world zip byte for byte as it was
  (`testdata/real/first-launch-exit-no-save.log`); three more `exit` runs and
  every `stop` and `quit` run saved. So the stop types `stop`, and the Monitor
  counts a save only on a "Completed…" line.
- **Autosave**: with the world running (`-pausewhenempty 0`), "Running an auto
  save with backup...", "Starting world save", "Reloaded file system in 438.70
  us", "Copying backup to path: …/saves/worlds/LATEST_BACKUP1.zip", "Finished
  backup" one minute after the start, then "Auto save: Reloaded file system in
  2.90 ms" every minute, each one rewriting the world zip (its time changed
  each minute; `testdata/real/autosave-every-minute.log`, 16 minutes). With the
  default `pauseWhenEmpty = true` and nobody on, the world is paused: no
  autosave in 16 minutes and its world time didn't move
  (`testdata/real/paused-when-empty.log`). The Monitor counts "Reloaded file
  system in …" as a save, without a feed text (it comes every minute).

### Settled: Ctrl+C doesn't save (spec 5.4 "verify whether Ctrl+C saves")

- SIGINT (Ctrl+C): Java exits at once with code 130, prints nothing, and the
  world zip is unchanged (`testdata/real/load-then-ctrl-c.log`). SIGTERM: exit
  code 143, nothing printed, nothing saved. The Necesse wiki's Linux page
  (necessewiki.com/Multiplayer-Linux, 2026-11-26) says the same: "the
  dedicated server does not save on SIGTERM", send `stop` and wait for the
  save instead. The real-server check's takeover, whose stop goes to Ctrl+C, logs
  "Ctrl+C left the world file as it was (no save)" and took 1.2 s here.
- So after a takeover what was played since the last autosave (at most about
  a minute, as the world autosaves every minute while anyone is on) is lost;
  the stop step's `Say` text says so.
- When the console ends (the app gone, stdin at end of file) the server
  carries on without a word (`testdata/real/console-closed-odd-world-name.log`).

### Settled: join and leave lines (spec 5.4 "verify from a real log")

No player joined the servers run here: that needs the Necesse game. The
lines come from real logs players posted, and from the 1.3.3 server itself:

- Steam forum, "Hosted Server save state issue?", 2021-05-10, version 0.20.40
  (https://steamcommunity.com/app/1169040/discussions/0/3144053275979758824/,
  `testdata/forum/steam-2021-05-09-v0.20.40-join-leave.log`):
  `Client "76561198129445960" with address host.docker.internal:60944 is
  connecting with version 0.20.40.`, `Client "76561198129445960" connected on
  slot 1/10.`, `Creating new player: 76561198129445960`, …,
  `Player 76561198129445960 ("Robert") disconnected with message: Quit`.
  A player new to the world is named by their Steam account number when they
  get their slot (they have no character yet); the leave line gives the
  account and the character's name. The Monitor matches the leave to the join
  by name, account, or the account number the player joined under.
- Steam forum, "Server Crash after update when player connects", 2022-06-28
  (https://steamcommunity.com/app/1169040/discussions/0/3409803502275060174/,
  `testdata/forum/steam-2022-06-28-join-then-crash.log`): `Client "icytux"
  connected on slot 1/10.`, `Loading map for player: AUTH:76561198014526397`,
  `Loaded player: 76561198014526397`, then a crash: `(ERR) Stuck in crash loop,
  stopping server.`, `Stopped server on … with code: SERVER_ERROR`. A known
  player is named by their character; the account follows on "Loaded player".
- The 1.3.3 `Server.jar` still holds these texts: `Client "` … `" with address
  ` … ` is connecting with version `, ` connected on slot `, `Loaded player: `,
  `Creating new player: `, and in its disconnect packet `Player ` … ` ("` …
  `") disconnected with message: ` (read as text from the class files on
  2026-11-26). Older servers said "Saved all data." for a save; 1.3.3 has no
  such text, and the Monitor accepts both.
- CubeCoders' AMP template for Necesse (github.com/CubeCoders/AMPTemplates,
  `necesse.kvp`, 2026-11-26) matches joins with `^\[[\d\-]+ [\d:]+\] Client
  "(?<username>.+?)" connected on slot (.+)\.$`, leaves with `Player \d+
  \("(?<username>.+)"\) disconnected with message: .+$`, chat with
  `\((?<username>.+)\): (?<message>.+)$`, readiness with `Started server using
  port \d+ with \d+ slots on world ".+"( with password ".+")?, game version
  .+$`, and stops the server with `quit`.
- **Still open for stage ready** (spec 2.6, 8.2): a real join and leave
  excerpt of the current version, captured when the owner first plays.
  Until then the manager treats Necesse's presence as unproven (it is log-only).
- The server's `players` command answers "Players online: 0/10"; the Monitor
  turns it into a `count` event. A turned-away client of another version
  logs a line with "had wrong version (" (the jar's text; its exact form isn't
  in any log seen), which the Monitor reports as `bad_version`.

### Deaths (spec 5.4 "deaths no (verify)"): still no

- The 2021 forum log printed deaths: `Robert got humiliated by Zombie`. The
  1.3.3 language file (`locale/en.lang`, section `[deaths]`) has about 170
  death texts, some with the attacker first (`<attacker> humiliated <victim>`),
  and settlers die too. Without a 1.3.3 log of a player's death the adapter
  keeps `Deaths: false`; turning it on needs that log and a match against
  those templates (as Minecraft's adapter does with its language file).

## Worlds

### Settled: where worlds are (spec 5.4 "Local … (verify)")

- 1.x keeps each world as `<data>/saves/worlds/<name>.zip` ("Creating new world
  at …/saves/worlds/RealCheck.zip"). The zip holds `<name>/world.dat`,
  `worldSettings.cfg` and `levels/…`. A new world is an empty 22-byte zip until
  its first save (`NeverSaved`). `testdata/world/RealCheck.zip` is a world the
  real server made and saved once.
- Older guides give `%APPDATA%\Necesse\saves\` (pre-1.0); the owner's game
  is current, so Local looks in `<Necesse folder>/saves/worlds` only:
  `%APPDATA%\Necesse\saves\worlds` (Windows, through
  `platform.KnownFolder("appdata")`), `~/Library/Application
  Support/Necesse/saves/worlds` (Mac).
- **The server finds a world by its file name only**: a copy renamed
  `Renamed.zip` (still holding `RealCheck/` inside) loaded as the existing
  world and saved back keeping `RealCheck/` inside.
- **`LATEST_BACKUP1.zip`**: the first autosave copies the world to
  `saves/worlds/LATEST_BACKUP1.zip`, beside the worlds. The world list leaves
  out names starting with `LATEST_BACKUP`, and Validate refuses them as world
  names.

## Join

- The game's menus (1.3.3 `locale/en.lang`): Multiplayer → "Join a server" →
  "Direct join" (or "Add server"), with boxes "IP address:" and "Port
  number:". Client and server must be the same version ("Wrong game
  version"). Port forwarding of UDP 14159: wiki. Steam's "Join a friend"
  needs the friend to host from the game, not a dedicated server (forum).

## Departures from the spec (5.4)

- **Stop with `stop`, not `exit`** (one `exit` didn't save; see above).
- **Worlds are in `<saves>/saves/worlds/`**, not `<saves>/saves/`.
- **Download about 100 MB** (compressed), not 180 MB (that is the installed
  size of the Windows depot alone).
- **More options on the command line**: the G1 Java options of the official
  scripts, `-zipsaves 1` and `-language en` (so server.cfg can't move the
  worlds or change the console's language), and `-Xmx` from a new advanced
  setting `memoryMB` (default 2048; the spec's `-Xmx<mem>M` named no setting).
- **No values starting with "-"** for the server name, password and world.
- **Worlds can't be renamed** in the app (`ErrCantRename`): the world list
  must leave out the server's backup copies, which needs worldstore's `List`,
  and worldstore renames only worlds it lists itself. The server itself
  would accept a renamed zip.
- **Uploads**: any `.zip` in an upload is offered as a world (worldstore's
  default for "files" worlds). A `Detect` that checks for `world.dat` would
  return `worlds.Candidate`, and adapters may not import internal/worlds
  (TestImportRules), so there is none for now.

## Not settled here (only the CI cells or the owner can)

- That the Windows and Mac servers behave as the Linux one did (the same
  Server.jar; the real-server cells run the same checks on each).
- The Windows firewall: the server also listens on the LAN socket (UDP 55169
  or another port) besides the game port; whether Windows asks about it when
  the app's rule for java.exe covers only the game port.
- A data folder path outside Windows' ANSI code page (above).
