# Windrose dedicated server: the facts the adapter relies on

Each fact has its source and the date it was checked. "Settled" means
checked against the real server's files, Steam's app info, the publisher's
documentation or a real server's log; "verify" means only the real-server
check on Windows (realserver_test.go) or the owner's first real session can
settle it, and says which.

The server checked is Steam build 24913903 (updated 2026-08-24), Windrose
0.10.0.9.32 (its DeploymentId, `0.10.0.9.32-22d39a16`).

## 1. Sources

| # | Source | What it is | Checked |
|---|---|---|---|
| S1 | `https://api.steamcmd.net/v1/info/4129620` (change 39649761) | Steam's app info | 2026-11-26 |
| S2 | The server itself, installed with SteamCMD (`+@sSteamCmdForcePlatformType windows +login anonymous +app_update 4129620 validate`), files inspected, never run | The real install | 2026-11-26 |
| S3 | `DedicatedServer.md` and `SaveWorkflow.md`, shipped in the install's root (S2) | The publisher's own documentation | 2026-11-26 |
| S4 | `https://playwindrose.com/dedicated-server-guide/` ("Last updated: 18 May 2026, game version 0.10.0.5.120") | The same guide on the web, plus a FAQ | 2026-11-26 |
| S5 | Log formats in `R5/Binaries/Win64/WindroseServer-Win64-Shipping.exe` (S2), read with `strings -e l` | Every log line's wording | 2026-11-26 |
| S6 | Docker Hub `windroseserver/windroseserver` (the publisher's image): tags and image history | Versions | 2026-11-26 |
| S7 | `github.com/RunOnFlux/windrose-server-flux` README.md and CLAUDE.md, commit 5829be9 (2026-11-18): "measured on the real game, 2026-11-18" | A hosting image, stop and log behaviour measured | 2026-11-26 |
| S8 | `github.com/strayer/game-server-deployment-discord-bot` windrose_todos.md and tests (from "live log on windrose-vm, 2026-06-14") | A hosting tool, ready line measured | 2026-11-26 |
| S9 | `github.com/crunchydude/windrose-bot` bot.py (its parser and the self-test lines it checks at start) | A Discord bot running against a production server | 2026-11-26 |
| S10 | `github.com/jasonrundell/nitrado-discord-bot` src/ftpLogPlayers.ts and its tests (from its `samples/R5.log`, a Nitrado server's log) | A bot for the publisher's partner host | 2026-11-26 |
| S11 | `github.com/CubeCoders/AMPTemplates` windrose.kvp (config version 7) | The AMP panel's Windrose template | 2026-11-26 |
| S12 | `github.com/shipstuff/windrose-self-hosted` README.md, server.py, scripts/entrypoint.sh (commit 04a3142, 2026-06-16) | A hosting project | 2026-11-26 |
| S13 | `github.com/paulsena/windrose-server-monitor` windrose_monitor.py | A player monitor reading R5.log | 2026-11-26 |
| S14 | `github.com/Danealor/WindowsGSM.Windrose` Windrose.cs | The WindowsGSM plugin (Windows) | 2026-11-26 |
| S15 | `github.com/indifferentbroccoli/windrose-server-docker` issues #6 (2026-04-15), #41 (04-27), #45 (04-29), #66 (05-06), #76 (06-05), #70, #71 | Real logs posted by server owners | 2026-11-26 |
| S16 | `github.com/ubidzz/Synix-Control-Panel` windrose.game.json | Another panel's ready line | 2026-11-26 |

Real excerpts are in `testdata/real/`, with every account id, session id,
player name, address and token replaced by made-up ones of the same shape
(the repository is public): `ib*.log` from S15, `crunchydude-*.log` from
S9's self-test lines, `nitrado-*.log` from S10's test. They keep each
line's wording, spacing and order. `testdata/session-assembled.log` is a
whole session put together from those lines and S5's wordings (verify:
against the owner's first real session).

## 2. Install

- Steam app 4129620, "Windrose Dedicated Server", type Tool, free to
  download, anonymous login (S1, S2: the anonymous install worked). Settled.
- Windows only: Steam's launch entry is `WindroseServer.exe -log` for
  `oslist windows` (S1); the publisher: "Can I run on Linux / headless?
  Currently Windows-only" (S4). The game depot 4129621 is marked
  "windows,macos" but has only the Windows program, and the Linux depot
  4129622 has no public build (S1). The publisher's Docker image is a
  native Linux build that isn't on Steam (S6). So macOS (and Linux) show
  "Needs Windows". Settled.
- Size: depot 4129621 is 3,036,697,175 bytes and Steam's libraries (depot
  1004) 67,863,136 (S1); SteamCMD reported `SizeOnDisk` 3,104,560,311 and
  downloaded 893,493,120 bytes (S2). `DownloadMB` is the installed size,
  3105, as the other SteamCMD games' (stream notes, G-zomboid); the
  publisher asks for 35 GB of disk (S3, S4), which is `DiskMB`. Settled.
- Memory: "RAM: 8 GB" for 2 players, 12 GB for 4, 16 GB for 10 (S3).
  `MemoryMB` 12288 and `MaxPlayers` 4. Settled.
- The program that runs: `WindroseServer.exe` (260 KB) is Unreal Engine's
  BootstrapPackagedGame (its strings: "BootstrapPackagedGame-Win64-Shipping.pdb",
  "This program is used for packaged games and is not meant to be run
  directly", `CreateProcess() returned %x`). It checks
  `SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64`, offers to install
  `Engine\Extras\Redist\en-us\vc_redist.x64.exe` (version 14.42.34438) and
  starts `R5\Binaries\Win64\WindroseServer-Win64-Shipping.exe` (S2). The
  shipped `StartServerForeground.bat` starts the shipping program itself:
  `start /abovenormal R5\Binaries\Win64\WindroseServer-Win64-Shipping.exe -log`
  (S2); so do AMP (S11), WindowsGSM (S14) and the hosting images (S7, S12).
  `Program()` is the shipping program and the command line is `-log`.
  Settled.
- The shipping program links `MSVCP140.dll` and `VCRUNTIME140.dll` (its
  import table, S2), so skipping the launcher means the app installs the
  Visual C++ 2015-2022 x64 runtime itself (Microsoft's permanent link, as
  the Palworld adapter does), and accepts 14.42 or newer, the one the
  server ships. Settled from the files; verify (Windows CI) that the
  install and the first start work on a runner.

## 3. ServerDescription.json

- `R5/ServerDescription.json`, one per install; the fields and their
  meanings are S3's list (also in S4). The server "creates a default
  ServerDescription.json and a first world ... start and stop the server
  once so that the files are generated, and then edit them"; "This file can
  be edited manually only when the server is shut down. Any field may be
  automatically changed by the server in the event of an issue" (S3).
  Settled.
- `IsPasswordProtected` "should be `true` if a password is set and `false`
  if the `Password` field is empty. Otherwise it may cause unexpected
  behavior" (S3). The plan always sets both together. Settled.
- `InviteCode`: "Allowed characters: 0-9, a-z, and A-Z. Must contain at
  least 6 characters. Case sensitive" (S3); the server makes one at its
  first start (8 hexadecimal digits in every sample: `d6221bb7`, `f1014dc1`
  in S3, `fba3990a` in S7). The app keeps the server's code. Settled.
- `PersistentServerId` is the server's identity with Windrose's online
  service: "Do not edit it" (S3); a server that loses its file gets a new id
  and its worlds lose their link to it (S12, "Why identity matters"). The
  plan rewrites only its own keys and keeps every other one, in its place.
  Settled.
- The file is written with tabs and Windows line ends (S7: "has CRLF and
  tabs") and the publisher's example has the nested object's brace on a
  line of its own (S3); `testdata/ServerDescription-publisher-example.json`
  is S3's example, and the plan writes the same style
  (`testdata/golden/ServerDescription-planned.json`). Settled.
- `UserSelectedRegion`: `SEA`, `CIS`, `EU` ("EU covers both EU and NA"),
  empty to let the server choose (S3). The app's "Connection region"
  setting. Settled.
- `P2pProxyAddress` "IP address for listening sockets" (S3); it is offered
  to friends' games as the address to try first (S12: "advertises this
  value verbatim ... as its ICE host candidate"; 0.0.0.0 there makes
  friends "silently bounce back to the main menu"). The server fills it in
  with the computer's LAN address (S3's example `192.168.31.49`). A home
  computer's address can change, so the plan replaces an address this
  computer no longer has with the one it reaches the internet from, and
  leaves any other value alone. This is the app's own rule; verify on the
  owner's PC that the server keeps working after the change.
- `UseDirectConnection`: true makes the server listen for direct
  connections and the invite code stops working (S3, S12). The app leaves
  it as it is (spec 5.11: keep every other key) and Join says so when it is
  on. Settled.
- `CanLaunchMultipleServerInstances` false keeps a second copy from running
  from the same folder (S3); the server then logs "Another instance of
  server is running from the same folder" (S5). Settled.

## 4. The log

- `R5/Saved/Logs/R5.log` (S7, S8, S11, S12). Unreal renames the last one to
  `R5-backup-<date>.log` at every start (S7, S8), so the file holds only the
  current run; the plan renames it first, the same way, so the app never
  reads the last run's lines as this run's. Output `GameLog`. Settled.
- The format (S15 logs, S5): `[2026.06.05-03.40.53:314][347]R5LogCoopProxy:
  <padding>[026308] <function, at most 47 characters, cut with "...">
  <padding><message><padding>[D:\Source\...\File.cpp:613]`. Times are UTC:
  issue #6's log line at 02:07:48 came 20 minutes before the issue was
  opened at 02:28Z, with `TZ=America/New_York` set (S15). A message of
  several lines goes on below with no time; its source is at the end of its
  last line, alone on a line when the message ends with a line break.
  Settled.
- Ready: `Host server is ready for owner to connect` (R5LogCoopProxy,
  SetIsReadyForHostOwnerConnect), which "fires after the save loads and
  listeners are up"; the `IpNetDriver listening` line comes earlier, for
  the lobby, and appears twice (S8). Also `Server. Change state
  LoadedIslandData => WaitingForFirstAccount` once the world is loaded
  (S7). The Monitor takes either; three minutes after `Server registration
  finished successfully` (the ready line of S16) it says online anyway.
  Settled from S7, S8 (measured on real servers) and S5.
- The invite code and the password: `>= Server Connection Info`,
  `> InviteCode : {}`, `> Password : {}` (S5; S3: "you should see an
  invite code" in the console). The Log page hides the password, and the
  `"Password"` line of the settings file the server prints at its start
  (`Saved server description. FilePath '...'. Description` and the JSON,
  S15 #41). Settled from S5 and S15.
- Version: the `"DeploymentId": "0.10.0.3.104-256f9653"` line of that JSON
  (S15 #41); `0.10.0.9.32-22d39a16` is the current one (S6, S7). Settled.

## 5. Who is playing (the presence decision)

decisions.md: if the log's account lines give a reliable join/leave
signal, presence is by name (or count) with the normal one-hour auto-stop;
only if they don't, presence is none and the "still playing?" rule applies.

What the log has (all settled from real logs, except where it says):

- A join: Unreal's own `LogNet: Join succeeded: <name>` (S9, S10), the
  account line `Account connected. AccountId <32 hex digits>`
  (R5LogDataKeeper, OnPlayerStateReplicateAccountId, S9), and
  `ServerAccount. AccountName '<name>'. AccountId <id>. PlayerState ...`
  (IR5DataKeeperStateLogger::LogSetState, S10, S11's join pattern). The
  join line and the account line come in either order and each may come
  twice (S9: "observed both sequences in production").
- A leave: `Account disconnected. Inform Cm. AccountId <id>. ...`
  (UR5CoopProxyServer::OnAccountDisconnected), then `Account was
  disconnected. AccountId <id>` (OnAccountBLDisconnected) for a dropped
  connection (S15 #66 and #76, the same lines a month and a version
  apart), and `...::MoveAccountToListOfDisconnected Account disconnected.
  AccountId <id>` (S11's leave pattern). A dropped connection also writes an
  "R5Ensure" block ("Unexpected BL disconnect"), which isn't a second leave.
- The lists of accounts: `Connected Accounts`, `Reserved Accounts` (still
  loading in), `Disconnected Accounts` (every past session, the same player
  more than once), one line per account: `N. Name '<name>'. AccountId
  '<id>'. State '<state>'. ... TimeInGame ... FarewellReason ...` (S15 #6
  and #45, S12, S13). They come below an account's connect and leave
  messages (S11's leave pattern ends the line at the account id; S5:
  "Account connected. AccountId {}{}"), below "Server is ready to continue
  initialization" when the first player arrives (S15 #45), and when the
  server stops (S7). S12 and S13 read them as the list of who is on.
- No query port, no RCON, no console (S7, S12: "No RCON"; S11: no writable
  console). The log is the only signal.

Decision: presence by name (`game.ByName`). The account lines are what
three tools that report joins and leaves from production servers rely on
(S9, S10, S11), real logs show them for clean leaves and for dropped
connections (S15), and the lists of accounts give a second, independent
account of who is on, which the Monitor uses to correct itself. Until this
install has seen a join, the manager still treats presence as unknown and
applies the "still playing?" rule (spec 2.6), so a wording that changed
can never stop a game silently. S7 decided against counting players on the
grounds that it had never seen a client connect; the samples above are
servers with players. verify: the owner's (or a friend's) first session on
the Windows PC, with its R5.log excerpt added to `testdata/real/`, before
the stage goes to ready (spec 8.2).

## 6. Stopping

- The server is a windowed program: both `WindroseServer.exe` and the
  shipping program are PE subsystem 2 ("Windows GUI"; the world updater is
  3, a console program) (S2). Windows gives such a program no console, so
  Ctrl+C can't reach it the usual way (spec 2.7).
- With `-log` the server opens a console of its own for its log: "The
  server runs in a separate command-line window where you can see game
  logs" (S4, about StartServerForeground.bat, which is `start ... -log`);
  S14: "UE allocates its own log console when given -log". The app starts
  it hidden, and the console Unreal opens takes the hidden window state
  from the start (verify on Windows CI and the owner's PC: no window
  appears, also with Windows Terminal as the default terminal).
- Ctrl+C is the graceful stop. Unreal's console handler (S5: `*** INTERRUPTED
  *** : CTRL-C TO FORCE QUIT`, `ConsoleCtrl RequestExit`) asks the engine to
  exit; Windrose then writes its exit backup: "SIGINT to the game process
  is the graceful stop ... `Engine exit requested (reason: ConsoleCtrl
  RequestExit)`, the exit backup (`Save backup has finished successfully`,
  fresh `*_Latest.zip`), `Log file closed` in under a second" (S7, under
  Wine, which delivers SIGINT as Ctrl+C). On Windows, S14 stops it by
  sending Ctrl+C to that console. "Never send SIGINT twice. The log says
  `CTRL-C TO FORCE QUIT`: a second one aborts the exit backup" (S7). "The
  game ignores Ctrl+C while it is still starting" (S7: not honoured in 15 s
  on a fresh first boot; 4.9 s into loading a world it exited without
  saving, nothing being loaded). So the stop is one Ctrl+C, never sent
  again, with 4 minutes to finish. Settled from S5, S7, S14; verify (Windows
  CI) that the app's Ctrl+C reaches the hidden console and the world saves.
- The publisher's own ways: StartServerForeground.bat, "To shut down, just
  close the window", and `WindroseServer.exe` without `-log` runs "as a
  background process. To close it, open Task Manager and end the process
  there" (S4), which saves nothing since the last backup. When Ctrl+C can't
  reach the server (no console: someone started it without -log), the
  app's stop asks its window to close (`taskkill /pid` without `/F`), the
  same as closing it by hand; Windows gives a closing program only a few
  seconds, so it may save less. Settled.
- SIGTERM and killing save nothing (S7).
- Exit lines: `Start on exit sync backups`, `Save backup has finished
  successfully`, the lists of accounts, `Log file closed` (S7, S5).

## 7. Joining

- Friends: Play, Connect to Server, paste the invite code; the same version
  as the server: "If the versions differ, players may experience connection
  issues ... or be unable to join the server at all" (S3, S4). A client on
  another version shows as `DeploymentId mismatch. AccountId {}.
  ServerDeploymentId '{}'. ClientDeploymentId '{}'` (S5), a wrong password
  as `Client sent wrong password. Disconnect it. ClientPassword {}.
  ServerPassword {}` (S5; the Log page hides both). Settled.
- "Ports are dynamically assigned via NAT punch-through. Ensure your router
  supports UPnP" (S4). No router setup: `Join.Router` false, and the manual
  steps add the UPnP sentence. The firewall rule is program-wide (spec
  2.12). Settled.
- The game is on Steam, the Epic Games Store and Stove (S4); the invite code
  works the same from each (S4's instructions are the same for all three).

## 8. Worlds

- A world is `R5/Saved/SaveProfiles/Default/RocksDB_v2/<game version>/Worlds/<world id>/`,
  holding its RocksDB database and `WorldDescription.json` (`IslandId`,
  `WorldName`, `WorldPresetType`, ...) (S3). The id is 32 hexadecimal
  digits (S3, S15). `WorldIslandId` selects the world a start loads (S3).
  Settled.
- Since 0.10.0.5, `RocksDB_v2` is "runtime state ... regularly overwritten
  and deleted during gameplay"; `RocksDB_v2_Backups` holds the server's own
  backups, "created by the game at certain intervals and every time you
  exit a world", zips, "The most recent backup is marked with a _Latest
  suffix", and "At startup, the game ... loads the database from that
  archive, overwriting the contents of RocksDB_v2" (S3, SaveWorkflow.md).
  S7 finds the backups under `RocksDB_v2_Backups/.../Worlds/<id>/` and saw
  one every 60 s. So the app's backup of a world holds both folders, and is
  made only while the server is stopped (spec 5.11; a copy of a live
  database would mix moments). Settled from S3 and S7; verify the folder
  layout on Windows CI (the check logs the saves folder).
- Names: the app keeps its own names in State (`world.<id>`), as the
  server's default WorldName is the same for every world; a world without
  one shows its WorldName, else its id. Renaming isn't offered: the
  publisher says to change WorldDescription.json only with the shipped
  `R5WorldDescriptionUpdater.exe` while stopped (S3), and the server
  rewrites the file from its database at every start (S15 #69, #74).
- A new world: the first start makes the first one (S3). For another, the
  plan gives `WorldIslandId` a new id. S15 #70 says the server then makes a
  fresh world and writes its own id into the file; S15 #41 (0.10.0.3) shows
  a server stopping with `Cannot load island` for an id it couldn't load.
  The adapter handles both the server using the app's id and the server
  using its own (State's `planned`); verify which on Windows CI (the check
  logs it).
- Importing the owner's own worlds isn't offered: the publisher's transfer
  steps need the database and backups folders by hand (S4), and a world's
  link to the online service depends on the server that owns it (S12).

## 9. Departures from spec 5.11, and what other streams need

- The first start, not the install, makes ServerDescription.json and the
  first world (firstrun.go): the server listens as soon as it starts, and
  its firewall rule names the installed program, so the rule can be in
  place before the first start but not before the install ends; a
  settings file lost later is made the same way. For stream A: if the
  firewall fix isn't made before a game's first start, Windows may ask
  about the hidden server then (its block rule from a "Cancel" is removed
  by the next fix anyway, spec 2.12).
- Presence is by name, not by count (section 5).
- The real-server check runs like the other games' (decisions.md Q5), not
  on demand.
- The Windows stop is one "call" step (Ctrl+C to the server's console, or
  its window closed when it has none) rather than "interrupt" then a window
  close: a close after a delivered Ctrl+C is Unreal's "force quit".
- Settings added: "Connection region" (advanced). The Visual C++ runtime is
  installed. P2pProxyAddress is refreshed (section 3).
- For M3: Output `GameLog` (`R5/Saved/Logs/R5.log`); `Recover` gives that
  log for a server found without its run file. State keys: `world.<id>`,
  `planned`, `inviteCode`, `directConnection`. Record.Extra: `inviteCode`,
  `worldId`. Event kinds: `code`, `saved` (no feed text: it comes every
  minute), `no_backend`, `load_failed`, `wrong_password`, `bad_version`,
  `save_failed`. RunState.Extra: `worldId`, `backend` ("failed"). The
  Windows stop is a "call" step and needs a working context.
