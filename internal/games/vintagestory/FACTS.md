# Vintage Story: the facts the adapter relies on

Each fact has its source and the date it was checked. "Run here" means the
real 1.22.7 **Linux** dedicated server, downloaded from Vintage Story's own
site and run headless on 2026-11-26 with Microsoft's .NET 10.0.15 runtime,
in a scratch folder (the console it printed is in `testdata/real/`, with the
scratch folder's path shortened to `/srv/vs`). The Windows server is the
same .NET program: `VintagestoryLib.dll`, which holds the server's code and
every line it prints, is byte for byte the same in both downloads (MD5
`54478e7cadc3a04096075fcbb61aba7f`), as are the game's mods; only the small
starter, `VintagestoryServer.dll`, differs. So what the program itself does
carries over; what only Windows can show is listed
under "Only the Windows CI cell can confirm" at the end, and
`realserver_test.go` checks it there.

## The download (settled)

- **The version list** is `https://api.vintagestory.at/stable.json`, a JSON
  object from version to its downloads, newest first: 53 versions on
  2026-11-26, newest **1.22.7**, whose every download carries `"latest": 1`
  (recorded in `testdata/api/stable.json`). Versions up to 1.17.12 name their
  server `"server"`; from then on there are `"windowsserver"` and
  `"linuxserver"`. `https://api.vintagestory.at/lateststable.txt` says
  `1.22.7` too (`testdata/api/lateststable.txt`); it has no checksum, so the
  app uses it only when stable.json can't be read. Pre-releases
  (`1.22.0-rc.10`) are only in `unstable.json`, which the app never reads.
- **The Windows server** is `vs_server_win-x64_1.22.7.zip` at
  `https://cdn.vintagestory.at/gamefiles/stable/vs_server_win-x64_1.22.7.zip`,
  64,354,058 bytes (listed as "61.4 MB"), MD5
  `c5bcd15a558041533ed3b88f707bb19e`: the listed MD5 matched the download
  (`md5sum`, 2026-11-26). The Linux server, `vs_server_linux-x64_1.22.7.tar.gz`,
  53,871,049 bytes, MD5 `3031212903b1c283084d5fe0cda6bce6`, matched too.
- **No Mac server.** The list's Mac entries (`mac-x64`, `mac-arm64`) are the
  608-614 MB game; there is no Mac server package. Hence "Needs Windows"
  (decisions.md).
- **Layout:** the zip has no folder at the top: `VintagestoryServer.dll`,
  `VintagestoryServer.exe` (a .NET launcher), `VintagestoryServer.runtimeconfig.json`,
  `VintagestoryServer.deps.json`, `VintagestoryAPI.dll`, `VintagestoryLib.dll`,
  `Lib/`, `Mods/` (the game's own three mods) and `assets/`: 10,653 files,
  about 330 MB unpacked (`unzip -l`, `du`). There is no start script in the
  Windows zip; the Linux one has `server.sh`, which runs
  `dotnet VintagestoryServer.dll --dataPath "<data>"` from the server folder.
- **A fresh folder per version.** The server refuses old files: its
  `VintagestoryLib.dll` holds "Your Server installation still contains old
  files from a previous game version, which may break things. Please fully
  delete the /assets folder and then do a full reinstallation. Shutting down
  server." So each version is unpacked into `server/<version>/`, and
  `.egs-build.json` names the one that runs.
- **.NET:** `VintagestoryServer.runtimeconfig.json` (in `testdata/`) names
  `"tfm": "net10.0"` and `Microsoft.NETCore.App` `10.0.0`: channel **10.0**.
  Microsoft's `https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json`
  gave `latest-runtime` **10.0.15** (released 2026-11-10), with
  `dotnet-runtime-win-x64.zip` (36,980,907 bytes) and its SHA-512; the Linux
  tarball (36,659,264 bytes) matched its SHA-512 and unpacks to 80 MB. The
  server starts on it ("C# Framework: .net 10.0.15" in its first lines).
  With a runtime it can't use, .NET's host prints "You must install or update
  .NET to run this application." and exits with 150
  (`testdata/real/dotnet-wrong-runtime.log`); with the dll missing it exits
  with 155 (`dotnet-missing-dll.log`).

## The command line (settled, run here)

- `dotnet VintagestoryServer.dll --help` lists `--dataPath` ("default is
  Environment.SpecialFolder.ApplicationData"), `--logPath`, `--port`,
  `--maxclients`, `--ip`, `--genconfig` ("Generate a new default
  serverconfig.json and exit. Warning, this deletes any existing config."),
  `--setconfig`, `--withconfig`, `--standby` and others; `--version` prints
  `1.22.7`.
- With `--dataPath <saves>` everything the server keeps goes there:
  `serverconfig.json`, `servermagicnumbers.json`, `Saves/`, `Logs/`,
  `Playerdata/`, `Backups/`, `BackupSaves/`, `Cache/`, `Macros/`, `Mods/`,
  `WorldEdit/`. Nothing went to `~/.config/VintagestoryData` (the default).
- The app runs `<private dotnet> server/<version>/VintagestoryServer.dll
  --dataPath <saves>` from `server/<version>/`, as `server.sh` does, so the
  one process is the server.

## serverconfig.json (settled, run here; two corrections to spec 5.5)

- **A file with only the app's keys stops the server at once** (correction):
  `[Server Fatal] You have configured a default group code suplayer but no
  such group exists! Killing server`, exit code 0
  (`testdata/real/minimal-config.log`). The player roles must be in the file.
  So when there is none, the app runs `--genconfig` first: it wrote the
  server's whole default file in 0.4 s and exited with 0, printing "Config
  generated." (`genconfig.log`; the file is
  `testdata/serverconfig-genconfig-1.22.7.json`, ConfigVersion 1.7, which the
  server raises to 1.10 at its next start). The app then sets only its keys
  and keeps every other one, in its place (`testdata/golden/`).
- **When the server writes it** (correction: the spec said at shutdown): at
  the end of its start, when it says it is running (the file's time was the
  ready line's, 21:34:47), and when `/serverconfig` changes something; not
  when it stops (the file's time stayed 21:34:47 after the 21:36:42 stop). So
  the app writes it only before a start, and the server keeps the app's
  values when it writes it back (checked by `TestRealServer`).
- **WhitelistMode** (spec's values settled): `/serverconfig whitelistmode
  on|default|off` wrote `2`, `0` and `1`, and with `1` the server answered
  "Current WhitelistMode is Off". With `0` (the default since 1.20) the ready
  line is followed by "Please be aware that as of 1.20, servers default
  configurations have changed - servers no longer register themselves to the
  public servers list and are invite-only (whitelisted) out of the box…"; with
  `1` it isn't. `OnlyWhitelisted` (false) is the older key; the app sets it
  false too.
- **Password:** `/help serverconfig` says "Sets a password when connecting to
  the server. Cannot use spaces." So the app refuses a password with spaces.
- **The world:** `WorldConfig.SaveFileLocation` is the file the server opens,
  or makes, with `WorldName`, `Seed` and `PlayStyle` for a new one ("Using
  world seed: 12345", "Create new save game data. Playstyle:
  surviveandbuild", "Savegame …/Saves/Riverbend.vcdbs loaded"). An existing
  world keeps its own style and seed whatever the file says: an exploration
  world started with `PlayStyle: surviveandbuild` and `Seed: null` in the file
  said "Loaded existing save game data. Playstyle: exploration" and kept its
  rules (`/worldconfig microblockChiseling` = all). So the app writes those
  keys only for a new world.
- **Play styles** are the game's own presets, in the `worldconfig` its mods
  carry (`Mods/VSSurvivalMod.dll`, `Mods/VSCreativeMod.dll`): `surviveandbuild`
  (shown as "Standard", langCode `preset-surviveandbuild`), `exploration`,
  `wildernesssurvival`, `homosapiens` (all world type `standard`) and
  `creativebuilding` (world type `superflat`). Run here: an `exploration`
  world had `microblockChiseling` all and `temporalStorms` off; a
  `creativebuilding` world with `WorldType: superflat` had `gameMode` creative
  and `worldClimate` superflat (`exploration-world.log`, `creative-world.log`).
- A world name with accents and an emoji worked ("Café Wörld ☕.vcdbs",
  `unicode-world-stop.log`).
- A broken file (a stray comma): "Failed to read serverconfig.json" with
  Newtonsoft's exception, then "Stop reason: serverconfig.json read error",
  exit 0 (`damaged-config.log`). The app sets such a file aside
  (`serverconfig.json.damaged-<time>`) and has the server write a new one.
  It does the same with a file whose `DefaultRoleCode` (`suplayer` when the
  key is missing, as in `minimal-config.log`) isn't one of its `Roles`, and
  with one whose `WorldConfig` isn't an object: the server would stop on
  those at every start. A value Go reads but the server's reader
  (Newtonsoft) refuses, such as a word where a number belongs, isn't caught
  by the app (not run here; it would take the server's whole format), so
  ExitHint for "Failed to read serverconfig.json" names the file and tells
  the owner to undo the change or move the file out of the saves folder.

## The port (settled, run here)

- **42420, TCP and UDP**, on every address: `/proc/net/tcp` and
  `/proc/net/udp` both had `00000000:A5B4` while it ran, and it said
  "Dedicated Server now running on Port 42420 and all ips!". Port 42421 from
  the file was used as given.
- **Port in use:** a second server on the same port printed its crash report
  ("Critical error occurred", "System.Net.Sockets.SocketException (98):
  Address already in use", "at Vintagestory.Server.ServerMain.startSockets()"),
  wrote `Logs/server-crash.log` and exited with **0** (`port-in-use.log`). The
  line names no port; ExitHint says which game it is. Windows says "(10048):
  Only one usage of each socket address…" instead (ExitHint knows both).

## The console (settled, run here and from public logs)

- **Line format:** `{0:d.M.yyyy HH:mm:ss} [{1}] {2}` (a format string in
  `VintagestoryLib.dll`), for example
  `26.11.2026 21:34:47 [Server Event] Dedicated Server now running on Port 42420 and all ips!`.
  On the console the kind reads "Server Event", "Server Notification",
  "Server Debug", "Server Warning", "Server Error", "Server Fatal"; in the
  server's own `Logs/server-main.log` the same lines read "[Event]",
  "[Notification]"… Lines without a time are the rest of the line before
  (stack traces, `/help`'s answer).
- **Version:** "Game Version: v1.22.7 (Stable)".
- **Ready:** "Dedicated Server now running on Port <p> and all ips!", 30 s
  after the start for a new world and 18 s for an existing one, on 4 cores at
  2.1 GHz. It always says so, so the Monitor needs no Tick fallback.
- **Join** (spec's "verify which part is the name" settled): `{0} {1} joins.`,
  where `{0}` is the player's name and `{1}` the address they came from:
  "Keelaesh [::ffff:198.51.100.30]:64570 joins." (a 1.20.12 server's
  server-main.log in issue
  [anegostudios/VintageStory-Issues#7431](https://github.com/anegostudios/VintageStory-Issues/issues/7431),
  29.10.2025) and "Teskey [::ffff:192.0.2.60]:54896 joins." (a 1.22.3
  console in [#9655](https://github.com/anegostudios/VintageStory-Issues/issues/9655),
  4.6.2026). The line before names the account: "Client 56 uid
  MadeUpUid3AAAAAAAAAAAAAA attempting identification. Name: Keelaesh". Names
  match `^(\w|-){1,16}$` (the server's own rule, in its dll, with "{0} can't
  join (invalid Playername: {1})."), so they have no spaces.
- **Leave:** "Player Keelaesh left." (#7431), or, when the server removes
  someone, "Player Teskey got removed. Reason: The Players client crashed"
  (#9655) and "Player … got removed. Reason: Threw an exception at the server"
  ([#9914](https://github.com/anegostudios/VintageStory-Issues/issues/9914),
  1.22.6, names hidden by the reporter). Both count as leaving.
  These excerpts are in `testdata/issues/`; GitHub's page view was read on
  2026-11-26 and long lines were put back together from it, so spacing inside
  a line may differ from the original file. The players' addresses, uids and
  auth token there are other people's, so the copies (and the quotes here)
  have documentation addresses (192.0.2.x, 198.51.100.x, 203.0.113.x),
  made-up uids of the same length and letters, and "(removed)" instead; the
  Monitor doesn't depend on any of them.
- **Wrong password:** "{0} fails to join (invalid server password)." (in the
  dll; no public log shows it, and nobody can join a server here without a
  game account). The Monitor makes it a `wrong_password` warning.
- **Deaths** (spec's "verify" settled: no): the dll has "Player {0} died.",
  "Player {0} got killed by {1}" and "{0} killed {1}, with item (if any):
  {2}", but no public server log shows any of them on the console or in
  server-main.log (searched: issues mentioning deaths, #7430, #9709), and a
  death can't be made here without a client. So `Deaths` stays false.
- **Saves:** "World saved! Saved 0 chunks, 0 mapchunks, 16 mapregions." after
  `/autosavenow` (with "Handling Console Command /autosavenow" before it and
  "Autosave completed" after), by itself every 5 minutes (`ServerAutoSave`
  300 in servermagicnumbers.json; it came at 21:51:04 after a 21:46:00 start
  with nobody on, `autosave-sigterm.log`), and on every stop.
- **Stop someone asked for:** "Server stop requested, begin shutdown
  sequence. Stop reason: Shutdown via server command" after `/stop`, or
  "External close event SIGINT received" / "… SIGTERM received" after a
  signal; then the save, "Stopped the server!", and exit code 0 within about
  a second. **Stops the server makes by itself** have other reasons
  ("serverconfig.json read error", "Failed opening savegame"), and crashes
  print "Critical error occurred" or "Caught unhandled exception in thread …"
  (in the dll); every one exits with 0, so the Monitor marks only the
  reasons above as asked for, and the manager sees the rest as crashes.
- **A damaged world:** "Failed opening savegame, possibly corrupted. Error
  Message: Failed opening savegame.. Will exit server now." after "SQLite
  Error 26: 'file is not a database'." (`corrupt-world.log`).
- Busy computers print "Server overloaded. A tick took 829ms to complete."
  every few seconds; the Log page hides it.

## Stopping (settled on Linux; Windows in CI)

- `/stop` on the console saves and stops (`first-run-no-config.log`).
- **Ctrl+C saves** (spec's "verify whether it saves" settled on Linux): SIGINT
  gave "Server termination event SIGINT received. Shutting down server", the
  full save and "Stopped the server!", and the world file changed
  (`existing-world-sigint.log`). The server listens for SIGINT itself (its
  process caught it, `/proc/<pid>/status` SigCgt). SIGTERM does the same
  (`new-world-sigterm.log`). So a stop after a takeover, which can only send
  Ctrl+C, loses nothing.
- With its console at end-of-file (`< /dev/null`) the server keeps running
  and doesn't spin (all its threads asleep after the start), so a console
  that goes quiet after an app restart does no harm.

## Worlds (settled)

- A world is one SQLite database, `Saves/<name>.vcdbs` ("Main game save file,
  in sqlite3 format", the wiki's VintagestoryData page). While the server
  runs it has `<name>.vcdbs-wal` and `<name>.vcdbs-shm` beside it; after a
  clean stop only the `.vcdbs` is left (run here). The store keeps all three
  as one world, so a backup taken while it runs has the -wal and a restore
  never leaves a stale -wal beside a restored file.
- **The owner's own worlds** (spec's "verify" settled):
  `%APPDATA%\VintagestoryData\Saves` on Windows (the wiki's
  [VintagestoryData folder](https://wiki.vintagestory.at/VintagestoryData_folder)
  page, last edited 20 January 2026: Windows `%APPDATA%\VintagestoryData`,
  macOS 1.21+ `~/Library/Application Support/VintagestoryData`, Linux
  `~/.config/VintagestoryData`; "Saves: Holds the world save files").

## Friends (settled)

- The game's menu, from its English language file (`assets/game/lang/en.json`
  in the 1.22.7 download): "Multiplayer" (`mainmenu-mp`), "Add New Server"
  (`multiplayer-addserver`), "Host / IP Address" (`multiplayer-address`),
  "Server Password (if any)" (`multiplayer-serverpassword`).
- Friends log in with their own game account: the server checks each one
  with Vintage Story's auth server ("Client uid …, mp token …: Verifying with
  auth server", #9655; `VerifyPlayerAuth: true` in the default file).

## Memory

- 1.3 GB resident with a new world and nobody on (`top`, run here).
  `MemoryMB` 3072 leaves room for a few players.

## The real-server check, run here

`go test -tags realserver -run TestRealServer ./internal/games/vintagestory/`
passed on Linux on 2026-11-26 in 122 s, with the adapter's own installer
(the real 1.22.7 Linux download, its MD5 checked, and .NET 10.0.15 through
`install.DotNet`): every Conformance check against the real server (a new
world, online, `/autosavenow` saved, `/stop` saved, the same world again, a
takeover stopped with Ctrl+C, a backup restored), then the details. It logged:

- installed 1.22.7, the server says 1.22.7, the program is
  `runtime/dotnet-10.0.15/dotnet`;
- `PortInUse("tcp")` and `PortInUse("udp")` on the game port: in use, by the
  server's own PID;
- serverconfig.json after the start has ConfigVersion 1.10 and still the
  app's name, password, port, player limit and `WhitelistMode` 1, and no
  invite-only notice was printed; the new world was an exploration world with
  seed 424242;
- after `/autosavenow` the world has a -wal; after `/stop` it is gone and the
  world file is newer;
- the Ctrl+C stop after a takeover took 1.7 s and saved the world ("Server
  termination event SIGINT received. Shutting down server", "World saved!",
  the -wal folded in);
- `~/.config/VintagestoryData/Saves` (the game's own saves) was not touched.

## Only the Windows CI cell can confirm

`realserver_test.go` (`TestRealServer`, from `realserver.json`) checks these
on `windows-latest`, and logs "FACTS:" lines to copy here:
- that Ctrl+C (CTRL_C_EVENT through the server's own console) saves there as
  SIGINT does on Linux (.NET turns it into the same signal; the test fails if
  the world isn't saved);
- that the private `dotnet.exe` from the Windows zip starts the server, and
  `platform.PortInUse` sees it on TCP and UDP;
- what console encoding a captured `dotnet.exe` uses for names with accents
  (a player "Jürgen" might reach the log as "J?rgen"; the Monitor still
  takes the name as it comes);
- the port-in-use text on Windows (ExitHint knows the usual wording).
