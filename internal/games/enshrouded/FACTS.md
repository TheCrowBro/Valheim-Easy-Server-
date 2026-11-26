# Enshrouded dedicated server: the facts the adapter relies on

Every fact below has its source and the date it was checked. Checked on
2026-11-26 unless it says otherwise. Spec 5.7 marked some facts "verify";
section 12 lists each one and how it was settled. A few things only the
real server on Windows can settle. The real-server check (`TestRealServer`,
build tag `realserver`, Windows) prints them as `FACTS:` lines (section 13).

The server could not be run here: it is a Windows-only program, and this
stream works on Linux. Nobody can join a server in CI, so the log lines
come from real logs that people posted (section 0). They are copied into
`testdata/real/`, and the Monitor tests read them.

## 0. Sources

Keen Games, the publisher (help centre at enshrouded.zendesk.com; each
article read through the help centre's own API on 2026-11-26):

- **[K1]** "Dedicated Server Configuration" (article 16055441447709, edited 2025-02-21).
- **[K2]** "Dedicated Server FAQ" (16056312924957, edited 2025-02-21).
- **[K3]** "Dedicated Server Installation on Steam" (16051370691485, edited 2024-02-23).
- **[K4]** "Recommended Server Specifications" (16055628734109).
- **[K5]** "Favorite Server via IP" (16454683440541).
- **[K6]** "Server Roles Configuration" (19191581489309, edited 2025-11-10).
- **[K7]** "Server Gameplay Settings" (20453241249821, edited 2025-11-10).
- **[R]** `enshrouded_server_readme.txt`, which Steam installs beside the server. It says "Version: 1.0.0.0" and is 22,588 bytes, sha256 `187bbb490ff1b9138bc46ede8655a769c42a690c9c4ee2ffae7a84cc529bf33d`. Downloaded with SteamCMD (anonymous, `+@sSteamCmdForcePlatformType windows`) on 2026-11-26, build 26430253. Its default settings file is copied byte for byte into `testdata/real/enshrouded_server-1.0.0.0-readme-default.json`.

Steam:

- **[S]** `https://api.steamcmd.net/v1/info/2278520` (2026-11-26).

The server's own program:

- **[X]** `enshrouded_server.exe` from that download: 22,786,048 bytes, sha256 `f0d824be7fcd48ebfe2458ff5c28da26060c2074e3ec1fcac587e5a1d508aeb2`, PE timestamp 2026-11-04 11:17:18 UTC. Read on Linux (its PE header, and its strings with `strings -a`); never run.

Real logs, as people posted them:

- **[L1]** CompaBot (github.com/Alorbieski91/CompaBot), `tests/test_server_control.py` at commit `5e366b8` (2026-11-18). It is introduced as "Trimmed from a real enshrouded_server.log (11/7/2026)", from a Windows server (CompaBot drives `C:\EnshroudedServer`). The lines up to "Removed peer" are real; the author says the lines after them are copies. Only the real ones are in `testdata/real/compabot-2026-11-07-join-leave.log`, taken from a git clone (exact bytes).
- **[L2]** jsknnr/enshrouded-server issue 79, a comment by Gaetann18 on 2024-11-07: a whole start and a join on Game Version (SVN) 602428, under Proton. The comment was copied line by line from the issue page into `testdata/real/jsknnr-79-2024-11-07-start-join.log`. A page fetch can't promise every space came through exactly (the indent of the permission lines, for example).
- **[L3]** mbround18/enshrouded-docker issue 68, by Stuleo on 2026-10-30 (1.0): a stop by Ctrl+C. It is in `testdata/real/mbround18-68-2026-10-30-ctrl-c.log`.
- **[L4]** Steam discussions, "Save Data stops working on dedicated Server still", by King_Maxi95 on 2024-01-31 (the raw page was downloaded). Its save lines read "(I 02:45:04,770) (server) Saved". The forum turns square brackets into formatting, so the poster used parentheses. The Monitor test uses one of these lines, with the square brackets put back, and says so.

Hosting projects (source code read from git clones on 2026-11-26) and hosting guides:

- **[H1]** mbround18/enshrouded-docker: `cli/src/main.rs`, `cli/src/utils/extract_player_name.rs`, `cli/src/utils/shutdown.rs`, and pull request 66 (2026-10-22).
- **[H2]** mornedhels/enshrouded-server: `scripts/default/common`, `scripts/wine/enshrouded-server`, `scripts/default/enshrouded-backup`, and issue 265 (2026-11-03).
- **[H3]** briggleman/kraken: `internal/panel/catalog/bundled/enshrouded.yaml`, `internal/agent/roster_test.go`, and pull request 295 (2026-09-14).
- **[H4]** jsknnr/enshrouded-server: `container/entrypoint.sh` and `container/enshrouded_server_example.json`.
- **[H5]** zephxs/enshrouded_dedicated: `enshrd_query`, `a2squery.py`.
- **[H6]** Shakes63/palisade: its Enshrouded notes (`apps/web/lib/game-docs.generated.ts`) and `apps/api/src/players/sightings.service.ts`.
- **[G1]** WDC GameGrid knowledge base, "The Enshrouded Console and Server Log" (October 2026, "last verified 2026-10-01").
- **[G2]** Connect Hosting, "Enshrouded 1.0: What Changed for Server Owners" (updated 2026-10-16).
- **[G3]** Connect Hosting, "How to Move Your Enshrouded World to a Dedicated Server" (2026-09-28).
- **[G4]** EmeraldHost, "How to Join Your Enshrouded Server" (updated 2026-07-30), and "How to Read the Server Log" (2026-08-13).

## 1. The server

- Steam app **2278520**, "Enshrouded Dedicated Server". It downloads anonymously (`freetodownload` 1) and installs into `EnshroudedServer`, with a single public branch [S][G2]. Public build 26430253, built 2026-11-04 [S].
- **Windows only.** The macOS depot is empty and the Linux depot holds only Steam's libraries (depot 1006, 111 MB). The game itself is depot 2278521, Windows: 8,047,349,315 bytes, 3,602,183,216 to download. With Steam's Windows libraries (depot 1004, 68 MB) the install was 8,102,178,370 bytes [S]. Keen: "For now, only Windows is supported. Linux support is planned for later." [K3]. Hosting projects run the Windows server under Wine or Proton [H1-H4].
- Steam's launch setting is `enshrouded_server.exe` with no arguments [S]. Keen recommends running the exe rather than Steam's Launch button [K3].
- Enshrouded 1.0 came out on 2026-10-15; hotfix 1.0.0.1 came the same day, as game build "1267041" [G2]. The installed readme says 1.0.0.0 [R].
- **Memory:** "In idle state the server process uses ~4.4GB of physical RAM. Each player needs up to 100MB of additional RAM. In total the server should not use more than 6GB" [K2]. One host measured about 0.9 GB idle on 1.0 with no players [G2]. `MemoryMB` stays 6144, Keen's ceiling.
- **Disk:** "We recommend to have a free space of 30GB. The uncompressed server files are currently ~13GB" [K2]. Steam's figure is 8.1 GB [S]. A save is 1 to 100 MB [K2]. `DownloadMB` is 8100 and `DiskMB` 13000.
- **Players:** "The slot count must be a number between 1 and 16" [K1][R].

## 2. The program

- `enshrouded_server.exe` is a **console program**: its PE optional header has subsystem 3 (`IMAGE_SUBSYSTEM_WINDOWS_CUI`), x86-64 [X]. Keen: "Can the server be run headless? Yes, but there is a DOS window." [K2]. A console program gets a console of its own from CREATE_NO_WINDOW, so the app's Ctrl+C can reach it (spec 2.7).
- It imports `SetConsoleCtrlHandler` and handles console events by name: its strings include `Ctrl_C`, `Ctrl_Break`, `Close`, `Logoff` and "[app] Trigger gameflow shutdown, exit: %s" [X]. Real logs show "[app] Trigger gameflow shutdown, exit: Ctrl_C" after a SIGINT under Wine or Proton [L3][H1][H2].
- Those four names sit together in its strings, followed by `[%u]`, most likely how it prints any other event, by number [X]. They are the only exit reasons the program names. Windows shutting down has no name of its own among them.
- `Shutdown` and `SaveDataIssue` are in its strings too, but not as exit reasons. They are values of `GameMessageLoginErrors` (`Authentication`, `Version`, `Full`, `Shutdown`, `SaveDataIssue`, `Generic`): the reason the server gives a player it drops or turns away. They go with its lines "[server] Send Shutdown Message to Player '%k'" and "[server] Send Save Data Error Shutdown Message to Player '%k'" [X]. No source shows "exit: Shutdown" or "exit: SaveDataIssue".
- It is the server itself, not a launcher [S], so the app starts it directly (spec 2.4) with no arguments, in its own folder.
- It reads `enshrouded_server.json` from "the same folder as the enshrouded_server.exe" [R]. Crash dumps also go there [K2]. Its strings include a `--config` option [X]. The adapter doesn't use it, because nothing documents it.

## 3. The settings file (`enshrouded_server.json`)

- If it is missing, the server makes one at start, with four user groups (Admin, Friend, Guest, Visitor) that have random passwords [R][K6].
- 1.0's keys [R]: `name`, `saveDirectory` ("./savegame"), `logDirectory` ("./logs"), `ip` ("0.0.0.0"), `queryPort` (15637), `slotCount` (16), `tags`, `voiceChatMode`, `enableVoiceChat`, `enableTextChat`, `gameSettingsPreset`, `gameSettings` (applied only when the preset is "Custom"), `userGroups`, and `bannedAccounts`. The readme's text calls the bans `bans`, but its default file and the program use `bannedAccounts` [R][X]. The program also knows `useAlternativeP2P` [X].
- **There is no `gamePort`** in 1.0: the string isn't in the program at all, while `queryPort` is [X]. That agrees with [H3], [L1]'s author and the spec.
- `saveDirectory` and `logDirectory` may be absolute paths ("relative and absolute paths are supported") [H2].
- The server **writes the file back**. Its program has "Internal Error: Failed to write updated config file" [X], and at shutdown it rewrites the file, "adding tags, gameSettings and bannedAccounts if they were missing" [G2]. Bans made in the game are kept there [R]. So the Plan changes only the keys it owns and keeps every other key as it was.
- A file that isn't valid JSON is replaced with a default one (four groups with new random passwords), according to [H3]. [G1] says a refused settings file stops the server before its log opens. The Plan always writes valid JSON. A file it can't read is kept beside the new one as `.unreadable-<time>`.
- **The top-level `password`** "is no longer used", but if it is set, the server "will set up a new userGroup called 'default' which uses the password" [K6][R]. The Plan takes the key out, so no second way in appears.
- **User group rules** the server checks (its program's strings [X]): "user groups require a name", "user groups names must be unique", "user groups passwords must be unique", "Users that are allowed to Edit the Base But not the World ?" (`canEditBase` needs `canEditWorld`), "Only one user group can be without password", "Game role without password has more rights than a password protected one", "There are more reserved slots than overall slots", and "To many user groups max is %k". The stand-in refuses the same ones.
- **The rights** [R]: `canKickBan` lets a player kick and ban. `canAccessInventories` lets them open chests and other containers in bases. `canEditWorld` lets them terraform or destroy outside bases. `canEditBase` lets them build, terraform and remove in bases. `canExtendBase` lets them add, remove and upgrade Flame Altars. `reservedSlots` keeps slots for the group.
- 1.0's own groups [R]: Admin has every right. Friend has everything but kick/ban and `canExtendBase`. Guest can only edit the world. Visitor has nothing. The app's single group, **Friends**, has every right but `canKickBan`. Everyone shares one password, so one player mustn't be able to kick or ban the rest. `canExtendBase` is on because with only one group, nobody else could place a Flame Altar, and without one there is no base. That settles 5.7's "verify the rights": canKickBan false, canAccessInventories true, canEditBase true, canExtendBase true, reservedSlots 0, plus canEditWorld true (1.0 added it, and the server needs it with canEditBase).
- **Difficulty presets** (`gameSettingsPreset`): Default, Relaxed, Hard, Survival, Custom [K1][K7][R]. "'gameSettings' will not be applied Preset is not set to '%k'" [X]. The adapter offers the four presets, plus "custom", which keeps the `gameSettings` the owner put in the file by hand.

## 4. Ports

- One UDP port, **15637** by default (`queryPort`) [K1][K2]. "By default, we are using: queryPort: 15637. Additionally, Steam is using the ports described in their Required Ports for Steam article" [K2]. Players join on it, and Steam's server list asks it [H3] ("the json calls it queryPort but it is the port players join on"), [L1]'s author ("Current Enshrouded uses one UDP port, its query port, for joining and the server list"). The server uses IPv4 [K2].
- Some hosting guides still list a second port (15636), from before the server dropped `gamePort` [G2]. The 1.0 program has no setting for one [X].
- **verify, real check:** whether the server holds any TCP port, or any UDP port other than its query port. `TestRealServer` lists every port its PID holds (`netstat -ano`).

## 5. The log

- The server writes its log to `logDirectory/enshrouded_server.log` [H1][H3][G1]. At each start it moves the last one to `logDirectory/backup/enshrouded_server_<date and time>.log` [K1][G1]. Its program logs "[holistic] Backed up log to '%s'." and has a `cleanUpBackupFolder` step [X].
- **It writes next to nothing to standard output.** mbround18's code says "the server writes nothing to its own stdout, so every webhook trigger ... lands only in the game's own log" (under Proton) [H1]. [G1]: "If the game wrote nothing at all, it rejected its settings file before it opened its log. Enshrouded then prints the reason only to its standard output". Every hosting project reads the log file [H1][H2][H3][H4][G1]. So the adapter follows the file (Output `GameLog`), not standard output (spec 5.7 said Capture; section 14). **verify, real check:** what standard output holds on Windows (`TestRealServer/stdout` records it).
- The app reads the log while the server writes it. CompaBot reads `server\logs\enshrouded_server.log` on a running Windows server [L1]. **verify, real check:** that it opens with the app's sharing flags (`platform.OpenShared`), and whether it opens without them.
- Every line starts with a level letter (I, W or E) and **the time since the server started**: `[I 00:05:07,120]` [G1][L1][L2][L3]. The program's format is `%02u:%02u:%02u,%03u` [X]. The Monitor adds that time to the process's start time, so a log read back after a takeover counts each line at its own time.
- Before the Plan starts a run, it moves the last log aside itself. The app follows the log from its first line as soon as the program starts, and the server may move its old log later than that. The old log's "online" and join lines would then count for the new run.

## 6. The lines the Monitor reads

All of them are in the 1.0 program's strings [X], unless the entry says otherwise.

| What | Line | Sources |
|---|---|---|
| ready | `[Session] 'HostOnline' (up)!` and `[Session] finished transition from 'Lobby' to 'Host_Online' (current='Host_Online')!` (`[%s] '%s' (%s)!`, `[%s] %s transition from '%s' to '%s' (current='%s')!`) | Keen's FAQ [K2]; [L1] (1.0), [L2] (2024); [H1][H3][H6] |
| session closed | `[Session] 'HostOnline' (down)!` | CompaBot's code [L1] |
| build | `Game Version (SVN): 602428` (1.0.0.1 is 1267041 [G2]) | [L2][X] |
| join | `[server] Player 'Legiterately' logged in with Permissions:`, then one line per right (`\t - CanAccessInventories`) | [L1] (1.0), [L2] (2024); [H1][H3] ("Lines observed live 2026-09-14") |
| not a join | `[server] Machine '1': Player '0(0)' logged in` names a handle, not a player | [L1][L2]; CompaBot's code says so |
| leave | `[server] Remove Entity for Player 'Legiterately'`, then `[server] Remove Player 'Legiterately'` | [L1]; [H1][H3] |
| the player's Steam account | 1.0: `[online] Added peer 0(1) (steamid:76561198039777309)` before the login; 2024: `[online] Session accepted with peer ( id 76561198196714077 ).` then `[online] Added Peer #0.` | [L1], [L2]; [X] (`[online] Added peer %k (%s)`, `[online] Session accepted with peer (%llu)`) |
| connection gone | `[online] Removed peer 0(1)` (2024: `Removed Peer #0.`) | [L1], [L2] |
| server full | `[online] Session rejected with peer because it is full (%llu)` | [X] |
| stop asked | `[app] Trigger gameflow shutdown, exit: Ctrl_C`; the other console events give `Ctrl_Break`, `Close` or `Logoff` | [L3] (1.0); [H1][H2]; [X] |
| world shutting down | `[game_server] Switching state from Run to Shutdown after 730.20 s` | [L3]; [X] |
| couldn't save | `Shutting down Server Save System with pending changes`, `Error while shutting down Server Save System`, `[server] Failed to save %k`, `[server] Failed to Save. Retry %k` | [X] |
| quitting because it can't save | `[server] Send Save Data Error Shutdown Message to Player '%k'`, once per player on (a normal stop sends `[server] Send Shutdown Message to Player '%k'`) | [X]; no real log |
| port in use | `[online] Couldn't create dedicated server socket. error='%s'`, `[online] Couldn't bind dedicated server socket. error='%s'` | [X] |
| Steam | `[online] Server failed to connected to Steam %u`, `[online] Server lost connection to Steam` | [X]; jsknnr issue 16 (2024-01-25: "Server failed to connected to Steam 3") |
| settings refused | `Internal Error: …`, `Config Error: Failed to parse Ip '%k'` | [X] |
| world won't load | `[server] Failed to load Server Save Game` | [X] |

- Deaths: no line names a player's death [X][L1][L2], so `Deaths` is false.
- Chat isn't in the log [G1].
- The names are the players' Steam names [G1][H3]. Steam names can contain an apostrophe, so the Monitor's pattern takes everything up to the closing `' logged in with Permissions:`.

## 7. Players and the query port

- The query port answers Steam's **A2S_INFO** with the player count. Kraken checked it directly: "The game port does answer A2S_INFO: verified directly on 2026-10-12" [H3]. Hosting tools rely on it: mornedhels counts players with A2S before an update or restart [H2], zephxs takes `player_count` from A2S_INFO to decide restarts [H5], and CompaBot counts on 15637 [L1].
- CompaBot warns: "a server can answer the query with 0 while people are playing; the log's names win when it has any" [L1]. So the Monitor takes the A2S count when it is above the log's (it holds the computer awake), and never takes a named player off the list because of a 0.
- So presence is **by name** (from the log) with the A2S count as a cross-check. Spec 5.7 said "presence count ... names only if a real log has them"; the real logs [L1][L2] and the live observations [H1][H3] have them.
- A2S answers once the server has signed in to Steam; Steam's list shows the server then. If the log never says the server is online, the Monitor counts it as online after the query port has answered for a minute. That covers a log the app can't read and a reworded line. **verify, real check:** the A2S answer on Windows (`TestRealServer` prints it).

## 8. Saving and stopping

- "Save game files will be created every 5 minutes." [K2] One host measured every 10 minutes on a current build [G3].
- **1.0 logs no line when it saves.** The 2024 lines `(server) Start Saving` and `(server) Saved` [L4] are not in the 1.0 program, and neither is any other "Saved" or "Saving" line [X]. The Monitor still counts a `[server] Saved` line if one comes, but in 1.0 the only save it can know of is the one at a clean stop. After a stop someone asked for, once the world thread has switched to Shutdown and nothing said the save failed, the end of the process counts as the save.
- **Ctrl+C saves the world and exits.** The server handles Ctrl+C [X][L3]. It "lets the game save" and stopped 2 to 3 s after SIGINT under Proton [H1, pull request 66]. Hosting tools stop it with SIGINT for that reason [H1][H2][H4]. [H6]: "The dir stays empty until the first autosave tick (log `[server] Saved`) or a graceful stop". CompaBot's Windows stop script "saves it with Ctrl+C" [L1]. **verify, real check:** that Ctrl+C on Windows changes the world's files, also after a takeover (Conformance checks the files changed; `TestRealServer/facts` checks again after a takeover).
- The server has no console commands, no RCON, no telnet and no web API [G1] ("accepts no input at all"). Ctrl+C is its only gentle stop. The stop chain: interrupt, wait 4 minutes, send again every 90 seconds (spec 5.7).
- After repeated failed saves, the server shuts itself down ("On dedicated servers, the server will shut down automatically in case many consecutive attempts to save were unsuccessful", Hotfix 6, quoted in [L4]). No posted log shows that shutdown, so its lines come from the program's strings [X]. Each failed attempt logs `[server] Failed to Save. Retry %k`. Then the server tells each player on why they are dropped: `[server] Send Save Data Error Shutdown Message to Player '%k'`. With nobody on, only the retries are left to show it. Whether an `[app] Trigger gameflow shutdown, exit:` line follows, and with what reason, nobody knows: the program names only the four console events (section 2).
- So the Monitor warns on every failed save and once on the save-error message. It takes only the four console events as a stop someone asked for, so a server that stopped by itself looks like a crash to the manager, which says so (and starts it again when "Start it again if it crashes" is on). The exit hint says the server couldn't save when the end of its log has a retry or the save-error message. A stop for a reason nobody has seen (Windows shutting down, if it prints a number) is also reported as a crash; that costs little.
- **Not settled by any check:** the real lines and their order when the server gives up saving. The real-server check can't make its saves fail. If an owner's log ever shows it, copy those lines into `testdata/real/` and check the Monitor and the exit hint against them.

## 9. Worlds

- "There is a file per game-world." [K2] The server keeps the world in its `saveDirectory`. 1.0 changed the format and file names: "a new save data format which results in smaller save game files and a simpler data structure in the save data folder", and it migrates older saves in place on its first start ("[savedata2-migration] Found file '...'") [G2]. mornedhels sees `world0.ksd` files in 1.0, and `3ad85aea…` files before it [H2].
- The world is the whole folder: "Copy the whole folder, not a file you think is the important one. Keen documents the directory, not its internal layout" [G3]. So each of the app's worlds is a folder, `saves/<world>/`, given to the server as its `saveDirectory`. An empty folder makes a new world.
- **Import: no** (spec: "import no (verify)"). Keen documents the server's folder only, not where the owner's own game keeps its worlds or how to move them [G3]. The client keeps characters, and the worlds it hosts, in its own place and format [K2][G3]. A server's own savegame folder can still be uploaded as a world: worldstore's folder upload does that.
- **verify, real check:** that a world folder whose name is in no Windows code page ("Second world ✓") works on Windows (`TestRealServer/facts`).

## 10. Joining

- Dedicated servers are listed in the game's Join menu once "Show dedicated server" is ticked in the filter [K2].
- Friends can also search the list by address: "Press T (shown at the bottom right) to open the search field. Enter the IP address and Query Port in the format IP address:Query Port" [G4]. Spec 5.7 said friends can't type an address; that was right before this search existed.
- If a server isn't listed, Keen's way is to add it in Steam: View → Game Servers → Favorites → +, typing the IP and query port [K2][K5].
- The password a player types picks their user group [K6][R].
- The server is in the public list for anyone to see: "Every player can see the server and can access it, if no password is set" [K6]. The password keeps strangers out. So `Join.Public` is true.

## 11. Text the owner sees

The adapter writes its own texts: the Help lines in the schema, the Join steps, the exit hints and the feed texts. None of them names the app.

## 12. Spec 5.7's "verify" items, settled

| Spec 5.7 says | Settled as | How |
|---|---|---|
| presence count; names only if a real log has them; `[online] Added Peer`/`Removed Peer` as count events (verify) | presence by name from `Player '…' logged in with Permissions:` and `Remove Entity for Player '…'`, with A2S's count winning when higher. Peer lines are connections, including failed joins [L2], so they only give the player's Steam account | [L1][L2][H1][H3][X] |
| whether TCP is needed too | not in any source; **real check** lists the ports | [K2][X]; `TestRealServer` |
| userGroups rights (verify) | canKickBan false, canAccessInventories true, canEditWorld true (new in 1.0, needed with canEditBase), canEditBase true, canExtendBase true, reservedSlots 0 | [R][X] |
| Output Capture ("the console shows the log lines") | **GameLog**: the server writes its log only to its file | [H1][G1]; **real check** records standard output |
| ready lines | settled as spec says | [K2][L1][L2] |
| saves every 5 minutes | Keen's figure; 10 measured by one host; 1.0 logs no save line | [K2][G3][X] |
| Probe: A2S on the query port (verify it answers) | it answers, with the count | [H3][H2][H5][L1]; **real check** prints the answer |
| Stop: Ctrl+C should reach it (verify it saves) | a console program that handles Ctrl+C and saves on the way out. Its log names the stop's reason: `Ctrl_C`, `Ctrl_Break`, `Close` or `Logoff`, the only reasons the program names (`Shutdown` and `SaveDataIssue` are reasons it gives players, section 2) | [X][L3][H1][H6][L1]; **real check** checks the files |
| (not in 5.7) the server stopping by itself when it can't save | read from its retries and its save-error message to players, not from an exit reason; no real log shows it (section 8) | [L4][X]; no check can make it happen |
| import no (verify) | no | [G3][K2] |
| friends can't type an address | they can search the list by address (press T); Steam favourites stay as the fallback | [G4][K5] |

## 13. What only the Windows real-server check settles

`TestRealServer` (Windows, from `realserver.json`) prints `FACTS:` lines for these. Copy them in here when it has run:

- the PE subsystem of the installed program (it fails if the program is no longer a console program);
- the install's Steam build, size and readme version, and the Game Version (SVN) in the log;
- how long a new world takes to come online;
- that the log says HostOnline (it fails if the online event only came from the query port), and whether the log opens without sharing flags while the server runs;
- every UDP and TCP port the server's PID holds;
- the A2S answer (up, count 0, version);
- the memory it uses with nobody on;
- how long Ctrl+C takes, the log's last lines after it, the world's files after the first stop, and that a stop after a takeover saved again;
- the settings file's keys after the server rewrote it, that the app's group and the name "Real check ✓" survived, and what is in the log's backup folder;
- the lines that show the second start loaded the world;
- whether the server touched the owner's own `Saved Games\Enshrouded`;
- what standard output holds when it goes to a file, and the exit code after Ctrl+C.

The real check can't settle the lines of a server that gives up saving (section 8). Its log after Ctrl+C does show the reason the program gives for that stop (it should be `Ctrl_C`).

## 14. Where the adapter departs from spec 5.7

- **Output `GameLog`, not `Capture`**, following `logDirectory/enshrouded_server.log` (`<logs>/game/` as the spec says). The reason is in section 5. Before each start the Plan moves the last log into `backup/` and keeps the newest 10 there.
- **Presence `names`, not `count`** (section 7).
- **The Join steps** say friends can search by address (section 10).
- **The top-level `password` key is taken out**, not left empty. Either way the server ignores it; taking it out also removes a non-empty one a hand edit left.
- **`canEditWorld: true`** is added to the Friends group (section 3).
- **Additions:** a world-scope `difficulty` (the presets, plus "custom", which keeps hand-made `gameSettings`), and advanced `textChat` (on) and `voiceChat` (off). The password must be 5 to 32 characters, which is the app's own rule: the server takes any password.
