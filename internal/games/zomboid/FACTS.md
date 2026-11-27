# Project Zomboid: the facts the adapter relies on

Each fact says where it comes from and when it was checked. "Settled" means
checked on 2026-11-26 against Steam's own data, the developers' own
announcements, the server's code, or a real server's files and logs that
someone published. "Real check" marks what only `TestRealServer`
(realserver_test.go, Windows) can settle; it prints a `FACTS:` line for each,
to copy here once the lead has dispatched it.

The full download is about 7 GB, too big for the machine this adapter was
written on, so nothing here comes from a download made for this stream. The
first real-server check ran on GitHub's Windows runner on 2026-11-27 and the
server never started; what it showed is in "The first real-server check"
at the end, marked **[run 1]**. The sources are:

- **[steam]** Steam's app info for 380870, `https://api.steamcmd.net/v1/info/380870`, fetched 2026-11-26 (change number 41532876).
- **[news]** The developers' Steam announcements, `https://api.steampowered.com/ISteamNews/GetNewsForApp/v2/?appid=108600`, fetched 2026-11-26.
- **[code]** The server's own code, Build 42.20.2, as decompiled and published at `https://github.com/Ketum-Git/PZ-Javacode` (folder `PZ_42.20.2/zombie/`), read 2026-11-26. It is a third party's decompilation, not The Indie Stone's source, but it is the game's own logic; file names are given below.
- **[pepenotti]** Measurements on a real 42.20.4 server on Windows (2026-09-23), published with their raw files at `https://github.com/pepenotti/zomboid-server` (`docs/verification-log.md`, `fixtures/b42/`). `testdata/real/` holds copies of their console logs and settings files.
- **[manifest]** A real Windows install's file list (2026-09-29) with each launch file's size, SHA-256 and contents, at `https://github.com/mj8724/gameserver` (`docs/acceptance/TARGET-MANIFEST-windows.md`).
- **[zwarden]** A research report (2026-09-10, build 42.20.4) at `https://github.com/MCrank/ZWarden` (`docs/research/project-zomboid-runtime.md`), which quotes the official wiki's Dedicated server, Startup parameters, Server settings and Java pages (`https://pzwiki.net/`, which refuses plain fetches).
- **[run 1]** The first `TestRealServer` run: GitHub Actions run 45921304914, job 140252501724, `real-server.yml` on `multigame` at f86a110, `windows-latest` (Windows Server 2025 10.0.26100, image windows-2025-vs2026 20261123.321.1), 2026-11-27 01:13 to 01:52 UTC. Only its job log survives (the test removed its folder); `testdata/real/start-failed-windows-ci.log` is the server's last 60 lines from it.

## Version and branch

- **Build 42 is Steam's default branch** of the dedicated server (decisions.md asks for Build 42). Settled [steam]: branch `public` is build id **26486687** (built 2026-11-06), the same build as `pre-release`; `legacy41` is "Build 41.78.21"; `42.19` and `42.21` are fall-backs. No branch is named in the install.
- **The current version is 42.22.2**, released to the stable branch on 2026-11-09. Settled [news] ("42.22.2 STABLE Released"). A real 42.22.2 server prints `version=42.22.2 3d39148846 integrity=pending demo=false` (a log posted on 2026-11-26 at `https://github.com/Chuckleberry-Finn/moddingAlertSystem/issues/3`); 42.20.4 printed `version=42.20.4 b0bbce05d5 demo=false` [pepenotti]. The Monitor takes the first word after `version=`.
- Multiplayer has been in Build 42 since 42.13 (December 2025) and in the stable branch since 42.20 (29 July 2026) [zwarden, quoting the developers' blog].

## Download, disk, memory

- **Sizes.** Settled [steam]: depot 380871 (shared) 6,849,662,987 bytes, depot 380874 (Windows) 206,697,858 bytes, so about **7.1 GB installed**. [run 1]: SteamCMD's own count on 2026-11-27 was 7,124,223,981 bytes (a build newer than the one measured the day before), downloaded, checked and written in about 4 minutes on GitHub's runner. Steam sends them compressed: 1,888,501,536 + 55,662,416 bytes, about **1.9 GB downloaded**. `Info.DownloadMB` is 7100, the depots' size, as spec 5.6 settles it and as the other games give theirs (spec 2.1 does the same for Valheim), so Add a game compares like with like; the compressed 1.9 GB is what actually crosses the network. `DiskMB` is 10000 (the install, worlds, the app's backups and the server's own startup backups). `SteamApp.SizeGB` is 7.1 (the disk-space message).
- **A macOS depot exists** (380872, 202 MB) but Steam's launch settings have no macOS entry and the wiki documents only Windows and Linux [steam, zwarden]. decisions.md: "Needs Windows" on a Mac.
- **Memory.** The shipped `StartServer64.bat` gives Java `-Xms16g -Xmx16g` [manifest]; the Windows `ProjectZomboid64.json` says `-Xmx3072m` [manifest]; the Linux one `-Xmx8g` [pepenotti]. There is no official requirement [zwarden]. The app leaves out the .bat's -Xms/-Xmx and gives `-Xmx` from its "Java memory" setting, default 6144 MB (the wiki's own example is 6 GB [zwarden]); `Info.MemoryMB` is 8192, the heap plus Java's own needs.

## Launching the server

- **Steam starts `StartServer64.bat`** on 64-bit Windows (and `StartServer32.bat`, `start-server.sh` on Linux). Settled [steam], launch entries 0 to 2.
- **The Build 42 .bat** is, byte for byte, `testdata/launch/StartServer64.bat`: 367 bytes, SHA-256 `e9d76cad…92af82`, CRLF line endings. Settled: rebuilt from the contents quoted in [manifest], and its size and SHA-256 match the ones [manifest] recorded on a real install (`TestLaunchFilesAreTheRecordedOnes` checks the sums). It reads:

  ```
  @setlocal enableextensions
  @cd /d "%~dp0"
  SET PZ_CLASSPATH=java/;java/projectzomboid.jar
  ".\jre64\bin\java.exe" -Djava.awt.headless=true -Dzomboid.steam=1 -Dzomboid.znetlog=1 -XX:+UseZGC -XX:-CreateCoredumpOnCrash -XX:-OmitStackTraceInFastThrow -Xms16g -Xmx16g -Djava.library.path=natives/ -cp %PZ_CLASSPATH% zombie.network.GameServer -statistic 0 %1 %2
  PAUSE
  ```

  So the spec's `-Djava.library.path=natives/;natives/win64/;.` is Build 41's; Build 42's is `natives/`. The app reads the .bat at every start (`parseBat`), so a later change to it is picked up: `%~dp0` (the .bat's own folder) becomes `.\`, since the server runs in that folder, and every `SET` before the Java line goes into the plan's environment (`PZ_CLASSPATH`), as spec 2.4 asks of a skipped script. The real check prints the installed files' sums and contents right after the install, before anything starts ([run 1] printed them only once the server was online, which it never was, so the .bat of the current build is still unseen).
- **`StartServer64_nosteam.bat`** (361 bytes, SHA-256 `bced99f5…1d02db`) differs only in `-Dzomboid.steam=0` and having no `%1 %2`. Settled the same way. The app doesn't use it: Steam mode is what lets Steam friends join.
- **`ProjectZomboid64.json`** on Windows is `testdata/launch/ProjectZomboid64.json` (468 bytes, tab-indented, SHA-256 `403263cf…6eca52`, matching [manifest]): main class `zombie/network/GameServer`, class path `["java/.", "java/projectzomboid.jar"]`, options for all Windows versions plus `-XX:+UseZGC` for Windows 10 (`-XX:+UseG1GC` for 7). It is the fallback when the .bat can't be read, and the first choice on Linux. Windows' server install has no `ProjectZomboid64.exe` [manifest].
- **The Linux .json** (`testdata/launch/ProjectZomboid64-linux.json`) is written from the options [pepenotti] measured (`-Xmx8g`, `-Djava.library.path=linux64/`, `-Djava.security.egd=file:/dev/urandom`, …) with the Windows class path, which every Linux copy seen has (for example `https://github.com/VPradoB/project-zomboid`); not byte for byte. Linux isn't offered to the owner; this is for CI.
- **`start-server.sh`** (Linux) sets `LD_LIBRARY_PATH` to `linux64`, `natives`, the install folder and the JRE's lib folder, and preloads `libjsig.so` before running `ProjectZomboid64`; it does not `exec`, and always exits 0 [pepenotti, and the script's text at `https://github.com/uka-it/pzserver-manager`]. The plan sets the same variables on Linux (`linuxEnv`), adding `jre64/lib` for Java 25's layout. Real check (Linux only, no cell).
- **`Program()` is `jre64\bin\java.exe`**, the Java the .bat runs. Settled: the .bat [manifest]; the install has `jre64/bin/java.exe` (49,760 bytes) and its `java -version` is `openjdk version "25.0.1" 2025-10-21 LTS` [manifest]; Build 42 runs on Java 25 since 42.13 [zwarden, quoting the wiki's Java page]. java.exe is a console program, so Ctrl+C reaches it; the real check reads its PE header to confirm.
- **The working folder is the install folder**: the class path and `natives/` are relative to it, and the .bat starts with `cd /d "%~dp0"` [manifest]. The plan gives it, and `-cachedir`, with every short "8.3" name spelt in full (`longPath`). The owner's data folder never has one (it comes from `%LOCALAPPDATA%`), but [run 1] ran from `C:\Users\RUNNER~1\AppData\Local\Temp\…` (GitHub's runner's TEMP, the short name of `runneradmin`), while the server itself printed its files as `C:\Users\runneradmin\…`. Whether that is what broke its start isn't known yet; the real check now tries a start from short names as a fact once the server works.
- **The Java option variables** `_JAVA_OPTIONS`, `JAVA_TOOL_OPTIONS` and `JDK_JAVA_OPTIONS`, which every Java on the computer reads extra options from (`_JAVA_OPTIONS` wins over the command line), are set empty in the plan's environment, so another program's `-Xmx512m` can't undo the "Java memory" setting. Java then notes `Picked up _JAVA_OPTIONS: ` with nothing after it, as [pepenotti]'s real Windows log shows; the Log page hides that note.
- **The server's own arguments** (after the main class), settled [code: `network/GameServer.java`, `main`] and [zwarden, quoting the Startup parameters page for 42.20.4]:
  - `-cachedir=<path>` (one argument, with "=") makes `<path>` itself the server's data folder: `Server/`, `Saves/`, `db/`, `Logs/`, `server-console.txt` all go there, with no `Zomboid` folder inside it (`ZomboidFileSystem.setCacheDir`). Without it the folder is `%USERPROFILE%\Zomboid`, where the owner's own game keeps its saves. [pepenotti] ran a real server with `-cachedir=C:\pz-data` and lists that layout. The real check confirms nothing appears in `%USERPROFILE%\Zomboid`.
  - `-servername <name>` picks the settings set and the world: `Server/<name>.ini`, `Saves/Multiplayer/<name>/`, `db/<name>.db`. Default `servertest`. The name is trimmed; a name with ".." is refused when the settings are saved.
  - `-adminusername <name>` (default `admin`) and `-adminpassword <password>`: on a world's first start the admin account is made with that password, and on every later start its password is set to it again ("admin password changed via -adminpassword option", seen at every start by [pepenotti]). Without `-adminpassword`, a new world's start stops and asks on the console: `User 'admin' not found, creating it`, `Enter new administrator password:`, `Confirm the password:` (`ServerWorldDatabase.java`; [pepenotti]'s real log is `testdata/real/first-start-admin-prompt-42.20.4.log`). When the console ends instead, Java's line reader throws and the server ends (`Scanner.nextLine`). An empty `-adminpassword` ends the server at once.
  - `-port <n>` and `-udpport <n>` set the game and direct ports over the settings file's.
  - `-statistic 0` is the .bat's; the 42.20.2 code doesn't read it (it logs "unknown option"). The app passes it as Steam's launcher does.
- **`-Drconlo`**, a Java system property with any value, makes RCON listen on 127.0.0.1 only; without it RCON listens on every address (`RCONServer.java`: `System.getProperty("rconlo") != null`; [zwarden]). The app passes `-Drconlo=true`, so the RCON port is never reachable from the home network (spec 2.5 assumed Project Zomboid couldn't do this). Real check: it tries the RCON port on the runner's other addresses.
- **`-Duser.language=en -Duser.country=US`** make the comments the server writes into its settings files, and its translated messages, English; otherwise they follow Windows' language. The lines the Monitor reads are English either way [pepenotti]. The app passes both.
- **The server prints its whole log on standard output** and also into `<cachedir>/server-console.txt` (`GameServer.main` sets `System.out` to a stream that writes both). So `Output` is `Capture`.

## The settings file, Server/<world>.ini

- Settled [code: `config/ConfigFile.java`, `network/ServerOptions.java`] and [pepenotti]:
  - `Key=Value` lines, `# …` comment lines, blank lines between keys; UTF-8; CRLF on Windows; no newline after the last line. The real 42.20.4 file is `testdata/real/servertest-42.20.4.ini` (144 keys).
  - The server reads each line trimmed, skips comments, and splits it at every "=": the value is what comes between the first and the second one. So a value can't hold "=" (it would be cut there) or a line break, and leading and trailing spaces are lost. The settings refuse those.
  - At every start the server reads the file and writes it back whole, with its own comments, keeping the values it read and dropping keys it doesn't know. A file that lacks keys is completed with their defaults ([pepenotti] verified a partial file). So a new world's file holds only the app's keys, and the server fills in the rest. Real check: the key count after the first start.
  - It is not rewritten when the server stops [pepenotti].
- The keys the app sets, with the server's defaults (`ServerOptions.java`):
  - `PublicName` (default "My PZ Server", at most 64 characters), `Password` (""), `MaxPlayers` (32, from 1 to 100), `PVP` (true), `PauseEmpty` (true): from the settings.
  - `Open` (true): friends make their own account the first time they join; with false the admin must make each one.
  - `Public` (false): not listed in the game's public server list.
  - `UPnP` (**true**): the server would try to map the router itself, and its log warns "If the server hangs here, set UPnP=false". The app maps the router itself (spec 6.5), so it sets false.
  - `DefaultPort` (16261), `UDPPort` (16262), `RCONPort` (27015), `RCONPassword` ("": RCON is off when it is empty, or when the port is 0).
  - `Seed` (a random string of letters, made at the first start) for a new world when the owner gives one; `-seed=` on the command line does the same.
- Not changed: `SaveWorldEveryMinutes` (0: no timed save of the whole world; the map is saved as it goes and on the way out), `BackupsOnStart` (true: the server zips a backup into `<cachedir>/backups/startup/` at every start, keeping `BackupsCount`=5), `Map`, `Mods`, `WorkshopItems`.

## The sandbox file, Server/<world>_SandboxVars.lua

- Settled [code: `SandboxOptions.java`] and [pepenotti]: `SandboxVars = { VERSION = 6, Key = value, …, ZombieConfig = { … }, … }`, one level of nested tables, four-space indents, comments as `-- …` lines. The server reads it at every start (missing keys keep their defaults: each option is read only when the table has it) and writes it back whole. Values edited while it is stopped survive [pepenotti]. `testdata/real/servertest_SandboxVars-42.20.4.lua` is the real file's start, one nested table and ZombieConfig. A file the server can't load ends it: "Exiting due to errors loading …".
- **Zombies** (1 Insane, 2 Very High, 3 High, 4 Normal, 5 Low, 6 None; default 4) is the menu's preset. The population itself is `ZombieConfig.PopulationMultiplier`; the file's own comment says it is "Set by the 'Zombie Count' population option … Insane = 2.5, Very High = 1.6, High = 1.2, Normal = 0.65, Low = 0.15, None = 0.0", default 0.65. Editing the file, the server changes only what it is told ([pepenotti]'s file has Zombies = 3 with the multiplier still 0.65), so the app sets both, as the menu does. Real check: both values are still there after the server rewrote the file.

## World layout

- Settled [pepenotti, code]: under the cachedir, a world named N is `Saves/Multiplayer/N/` (its map: `map_t.bin`, `players.db`, `vehicles.db`, `map/`, `chunkdata/`, … about 5.8 MB fresh), `db/N.db` (SQLite: accounts with bcrypt passwords, roles, bans), `Server/N.ini`, `Server/N_SandboxVars.lua`, `Server/N_spawnregions.lua`, `Server/N_spawnpoints.lua` (made at the first start). The server's own logs are in `Logs/`, its startup backups in `backups/`.
- **Import: no.** Settled: a single-player save keeps its character in another table (`localPlayers`, not `networkPlayers`) and players report that copying one into the multiplayer folder doesn't work (Steam discussions `https://steamcommunity.com/app/108600/discussions/1/846243771540628848/`; the hosting guide `https://shockbyte.com/help/knowledgebase/articles/how-to-convert-single-player-save-data-to-multiplayer-save-data-and-back`, which edits the database by hand).
- World names are limited to ASCII letters, numbers, spaces, "-" and "_": the name is on the command line, which Java on Windows gets in the computer's old code page (see below), and in four file names.

## Ports

- Settled [zwarden, quoting the wiki's Dedicated server page for 42.20 and the 41.77 changelog]: **16261 UDP** (the game port; Steam's queries come here too, there is no separate query port) and **16262 UDP** (the direct connection; without it the game falls back to Steam's relay and warns "PERFORMANCE MAY BE SEVERELY AFFECTED"). Both must be forwarded. RCON is **27015 TCP** and stays private. SteamPort1/SteamPort2 (8766/8767) are gone since 41.77.
- Friends connect on the game port ("Clients should use 16261 port for connections", `GameServer.startServer`).
- **A taken UDP port** fails the start: RakNet's start-up fails and the server logs `java.net.ConnectException: Connection Startup Failed. Code: <n>` (`core/raknet/UdpEngine.java`), then its Java ends. Whether it ends at once or lingers is a real-check question; the stand-in ends with code 0.
- **A taken RCON port** doesn't stop the server: it logs `RCON: error creating socket on port <n>` and runs without RCON (`RCONServer.java`). The Monitor reports it (`rcon_failed`), since the app then can't count players or stop it gently.
- Real check: `PortInUse` for the two UDP ports and the RCON port.

## The console and the Monitor

- **Log lines** look like `LOG  : Network      f:0 st:34,907,676> *** SERVER STARTED ****`: the level padded to five, ": ", the part of the game padded to twelve, the frame, then optionally ` t:<ms since 1970>`, ` st:<a millisecond clock>`, ` at <caller>`, then "> " and the message (`debug/DebugLog.java formatLogStringForConsole`; [pepenotti]'s real lines). `st:` doesn't start with the server: [run 1]'s two servers, separate Javas started 19 minutes apart, printed `st:680,281` and `st:1,821,529` about 30 s after their launches, so it is most likely the time since the computer started. Only `t:` is a time of day. Lines from `System.out.println` have the same header in 42.20 ([pepenotti]'s console log). The rest of a multi-line reply, Java's own notes, `java.util.logging`'s two-line `SEVERE:` messages and stack traces have none.
- **Ready**: `*** SERVER STARTED ****`, logged right after the UDP ports opened (`GameServer.startServer`). Settled [code, pepenotti]. It came 40 to 76 seconds after launch on a fresh world (Windows, [manifest] and [pepenotti]). RCON opens about 50 ms later: `RCON: listening on port 27015` [pepenotti]. Real check: the time to online.
- **Saves**: a whole save logs `SaveAll took N ms`, `Saving players`, `Saving worldgen params`, `Saving GlobalModData`, `Saving finish`, `Saving took N ms` [pepenotti's real log, `testdata/real/console-session-42.20.4.log`]. The Monitor's `saved` is "Saving finish".
- **Quit**: `quit` (console or RCON) answers `Quit`, then Java's shutdown handling runs: `Shutdown handling started`, `Server exited`, the save lines, `Shutdown handling finished`; the exit code is 0, about 9 seconds in all (it waits 5 seconds for clients) [pepenotti; code: `commands/serverCommands/QuitCommand.java`, `ServerMap.QueuedQuit`].
- **Shutdown handling is not a stop request**: it runs after a quit, Ctrl+C, a crash (for example the 42.22.2 log above: an exception, then `Server exited`) and a failed start alike. The Monitor marks a stop as asked for only on a `quit` typed at the server's console (`command entered via server console (System.in): "quit"`); the app's own RCON quit isn't echoed on the console [pepenotti], and the manager knows it asked for that one. So a crash stays a crash (the Necesse stream's review finding).
- **Joins and leaves aren't on the console** in Build 42. Settled [code]: `"<name>" fully connected (x,y,z)` and `"<name>" disconnected player (x,y,z)` are written by the "user" logger and connection events by the "connections" logger, and both are made without console output (`core/logger/LoggerManager.getLogger` → `ZLogger(name, false)`), into `Logs/<start time>_user.txt` and `_connections.txt` only. So who is on comes from RCON `players` (below). [pepenotti] saw no connections file before anyone joined, consistent with this.
- **Deaths**: no console line (no player-death log in `GameServer.java`; the console never names one). `Deaths` stays false. The real check has no client, so it can't show a join or a death.
- **The admin prompt** (`Enter new administrator password`) means the server is waiting on its console for good; the Monitor reports it (`admin_prompt`). The app always gives the password, so it shouldn't happen. [run 1] never got as far as making the admin account, so it says nothing about the prompt either way.
- **`Server Terminated.`** is what `GameServer.main` logs after an error it can't get past while starting, and the Java does not necessarily end. [run 1]: an error in `doMinimumInit`, then `Server Terminated.`, and the server went on loading the world; a second error there, `Server Terminated.` again, then 15 minutes of silence with the Java still running, no game port open and no RCON, until the test killed it. A normal quit doesn't log it ([pepenotti]'s session). So the Monitor reports it once (`load_failed`, with words saying the program may still be running and to stop it) and counts nothing after it as online, not even an RCON answer; `ExitHint` names it when the Java does end, and `Required Animation Asset not found`, the error behind the failed run's, has its own hint.
- The console's command loop reads standard input; when it ends (the app gives no console), it queues one unknown command and stops reading; the server carries on (`GameServer.launchCommandHandler`).

## RCON

- Settled [code: `network/RCONServer.java`; pepenotti; zwarden]: Source RCON over TCP. On with a non-empty `RCONPassword` and a non-zero `RCONPort`. A login answers an empty packet then the login answer; long replies are split into 4096-byte packets with the same id; an empty response packet sent after a command is echoed (so `internal/rcon`'s end marker works); at most 5 connections at once; commands run one at a time on the server's main loop. RCON and the console run the same commands with full admin rights.
- **`players`** answers `Players connected (N): ` then one `-name` line per player, every line ending in a newline (`PlayersCommand.java`); a real answer with nobody on was `Players connected (0): ` and an empty line [pepenotti]. `players` isn't written to the server's log, so polling it every 30 s doesn't fill the log. Names may have spaces; the probe takes off only the dash.
- **`save`** answers `World saved` and saves on the next tick (`SaveCommand.java`; [pepenotti]).
- **`quit`** answers `Quit`, and the reply goes out before the server stops (it stops on the next tick) [code; pepenotti verified quit over RCON].
- **Ctrl+C**: the server's shutdown hook runs `ServerMap.QueuedQuit`, which saves the whole world (`QueuedSaveAll(true)`) before Java ends (`GameServer.shutdownHook`). The developers discouraged stopping by signal in Build 41, when it didn't save [zwarden, quoting the wiki]. So the stop chain is RCON `quit`, then Ctrl+C. Real check: whether Ctrl+C saved ("Saving finish" after "Shutdown handling started", and the map's files changed).

## Friends

- Friends join by IP and port in the game's Join screen, with the server's password if it has one, and a username and password of their own (with `Open=true` the server makes their account on their first join) [zwarden, quoting the wiki: "Enter port (default: 16261)"]. Their game must be the same version as the server (42.21 added a notice for a different version [news]).

## Characters Java can't get on Windows

- Java on Windows gets its command line in the computer's ANSI code page; a character the code page lacks arrives as "?". The Necesse stream found this for its Java (its FACTS.md); it is how Java's Windows launcher reads arguments. The app's world names and passwords on the command line are ASCII, but `-cachedir` holds the data folder's path, which holds the Windows user's name, so Plan refuses a path with such a character (`folderProblem`). The settings file is read as UTF-8 (Java 18 and later read files as UTF-8; [pepenotti] saw `PublicName=Prueba Ñandú` kept), so the server name and password may have any character but "=".

## The first real-server check [run 1]

It failed after 39 minutes: "conformance" in 1168 s, "facts" in 1150 s. Each part took about 20 minutes because:

- **Each part downloaded the server again.** gametest gives every session a folder of its own, so each part ran its own SteamCMD into its own folder: a fresh SteamCMD updating itself, a first `app_update` failing with `ERROR! Failed to install app '380870' (Missing configuration)` (a fresh SteamCMD's known first-run failure; the install package's retry covers it), then 7.1 GB in about 4 minutes, `Success! App '380870' fully installed.` The check now installs once into a folder every part shares (`sharedGame`), and each part's own install is SteamCMD finding nothing new.
- **The server never started, and its Java never ended.** About 30 seconds after launch, `GameServer.doMinimumInit` failed in `ModelManager.create` (`ModelManager.java:206`) at `getAnimationAssetRequired` (`ModelManager.java:1890`): a required animation wasn't loaded. That stops the rest of `doMinimumInit`, which is where the server's own Lua loads, so what followed failed in turn: `Looking in these map folders:` listed none, and `media/maps/Muldraugh, KY/WorldGenOverride.lua:4` hit `attempted index: biomes of non-table: null` because the world generator's Lua tables were never made, then `WorldGenChunk` threw a `NullPointerException` in `IsoWorld.init` (`GameServer.java:829`). Each failure logged `Server Terminated.`; the Java then stayed running without a port or RCON, so the wait for `*** SERVER STARTED` sat out its whole 15 minutes (the new world's 10 plus the manager's 5). The second part's server did exactly the same.
- **Not the reasons suspected.** The ready line was never written, so its detection wasn't tested; RCON never answered because the server never got to opening it; there was no admin prompt (the failure came before the admin account is made); the .bat's Java options weren't recorded, because the check recorded them only once the server was online.
- **Why the models didn't load is not settled.** The job log holds only the server's last 60 lines, and the log files went with the test's folder. The 60 lines begin with the stack trace of the first error, not its message. Very likely the same failure was reported for a vanilla 41.78.16 local host, where the messages read `Failed to load asset: AssetPath{ "Skinned/MaleBody" }` and then `Required Animation Asset not found: bob/bob_idle`, and a patch fixed it then (`https://theindiestone.com/forums/topic/92893-local-server-crashes-on-startup/`, seen through a search summary; the forum refuses plain fetches). What this run had that the published working Build 42 runs didn't: a working folder and `-cachedir` under a short "8.3" name (`RUNNER~1`) while the server printed long ones; the runner's environment (PATH full of other programs' libraries, whatever Java option variables it sets); a newer build than 42.20.4. The adapter now gives the server full names and empties the Java option variables, both harmless on the owner's PC.

### What the next run should show, and how long it should take

The check now records the install, the launch files (with their sums and contents), the bundled Java, the native libraries, the Java option variables and any library on PATH named like one of the server's, all before anything starts; then:

- **If the server starts** (about 15 minutes in all): the install about 4.5 minutes and a second SteamCMD run with nothing new, "facts" with `a new world was online …` (expected 40 to 80 s), the ports, RCON on 127.0.0.1 only, the settings files, the saves, the stops by RCON and Ctrl+C, and `started from … with -cachedir=… (short names), the server …`, which says whether the short names were what broke [run 1]; then "conformance". Copy the `FACTS:` lines here and settle every "Real check" item.
- **If it doesn't** (about 10 minutes): "facts" stops as soon as the server writes `Server Terminated.` (or ends, or asks for an admin password), shows the server's whole log (its first 150 shown lines, then every error with the lines after it), stops it, and starts the same install twice more: through `StartServer64.bat` itself as Steam runs it, and with the app's command line but a plain environment. Then "conformance" is left out. The logs go to `%TEMP%\TestRealServer-zomboid-logs\`, which the workflow uploads. Reading the two extra starts: the .bat works but the app's line doesn't, in either environment: the app's command line is the trouble; the plain environment works: the runner's environment is; neither works: the server (or this kind of computer) is, and the log says where.

## Still open (the real check, or a real session)

- Every "Real check" item above, and why [run 1]'s server couldn't load its animations.
- A real join and leave (no client in CI): the `players` answer with players on is settled from the code only.
- What a real server prints, and how it ends, when its UDP port is taken.
