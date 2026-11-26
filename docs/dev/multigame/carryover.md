# What the first design round concluded

A first round of research and design ran on 2026-11-21: a code map, game research with fact-checks, three independent architectures merged by a judge into one spec, and the start of an adversarial review. Its files were lost when the session's temporary workspace was cleared, so this page records what survived, to seed the second round. Everything here should be re-checked against the code and primary sources.

## The architecture the judge chose

**Base.** The "safety of existing users" design was the base, because its claims held up against the code at every point checked. These were confirmed in the code (bed004b):
- main.go builds the Downloads/Desktop data subfolder from `appName`, so renaming `appName` would orphan the data of copies kept there. Keep "Valheim Easy Server" through its own constant (e.g. `legacyFolderName`).
- `selfupdate.Swap` removes `exe.old` before renaming, so any helper process must run from its own copy of the binary, not from `exe` or `exe.old` (on Windows a running `.old` would make the next update fail).
- `adoptRunning` uses runtime.json, then OpenProc, then a ±2 s start-time check, then FindByExe.
- The old Windows firewall script blanket-removes `'Valheim Easy Server - *'` rules and deletes rules by program, so new games' rules need another prefix (e.g. `Easy Game Server - `).
- `backfillRecords` and `pruneLogs` scan `<data>/logs/server-*.log` as Valheim logs, so other games' logs must live elsewhere.
- `wakerSetup` is compared with `==`, so new fields must stay comparable (no slices/maps).
- `manager.New` runs before the web server listens, and the updater waits about a minute for the new build to answer, so migrations must be O(1) before Listen.
- `config.Load` fills Defaults then `fillMissing`: an old build reading a settings.json with Valheim fields missing would generate a new random password and the world "Midgard". Never leave Valheim's fields empty.
- `discordQ` holds 16 posts; `consoleMu` makes each Windows Ctrl+C take about 1 s (stops serialise).

**Invariants kept from the safety design:**
1. Valheim never moves: its paths always come from `config.ResolvePaths` (server, saves, backups, logs, uploads, ServerDir/SaveDir overrides). No migration copies, moves or renames a Valheim file.
2. The legacy files (settings.json, runtime.json, players.json, stats.json, history.json, records.json, lastplayed.json, modscodes.json, jetkvm.json, update.json, .instance.json) keep their names, shapes and meanings for Valheim and the app. New data goes into new files (`servers.json`, `games/<id>/...`); additive `omitempty` fields only where old builds ignore them.
3. settings.json's Go shape is frozen (golden test); it's written so unknown top-level keys survive (but the writer owns maps, so deleted map entries don't come back).
4. runtime.json stays the Valheim run file, so a new build adopts a running Valheim exactly as today, and an old build after rollback still can.
5. The update handshake keeps working: every existing flag accepted, `GET /api/status` keeps its top-level fields, `.instance.json` holds `{port}`. No new command-line flags (an old build would exit on an unknown flag); new developer behaviour uses `EGS_DEV_*` environment variables.
6. Identity unchanged: asset names `ValheimEasyServer.exe` / `ValheimEasyServer-mac` (+ `.sha256`), repo id 1430331409, data folder `ValheimEasyServer`, the single-instance id, Run value, LaunchAgent label `com.thecrowbro.valheimeasyserver`, Mac binary name, Valheim firewall rule names, JetKVM waker program name/paths (`valheim-waker`, `S99valheim-waker`).
7. Old API routes keep working as aliases of the Valheim instance.
8. Kill switches: a per-game registry stage (hidden / preview / ready), `app.disabledGames` in servers.json, and a "Valheim-only safe mode" when servers.json is damaged or from a newer version.
9. Files holding passwords or tokens are 0600.

**Taken from the "delivery" design:**
- Stars and the Active/Inactive lists (remembered by the app), starred-first Overview, slash commands only for starred games.
- Contract-first phase 0: freeze the `game` interfaces, API shapes, a `web.Backend` interface and the test harnesses first, so game streams and UI streams can start in parallel.
- One stand-in server binary (`fakeserver`) whose "personality" is picked from argv, so it can stand in for java, dotnet and any server exe; `fakegame` and `managertest` helpers; a `FakeBackend`/panel demo for UI work.
- One file per game for registration, the CI matrix and the UI module, so no stream edits a shared list.
- The non-releasing integration branch `multigame` (now approved by the owner).
- R1 as cheap insurance for rollbacks.

**Taken from the "model" design:**
- A declarative settings schema with server / world / new-world scopes, drawn by one generic form in the panel.
- A Plan / Monitor / Prober / Controller split per game, with Reattach for adoption.
- Stop chains that fall through on "unsupported" (e.g. RCON → stdin → Ctrl+C → kill).
- Probes (RCON, REST, query) whose counts are authoritative and reconcile with the log.
- Presence kinds: names, count only, or none.
- Support levels per OS: yes / experimental / no (with the owner's "Needs Windows" label).
- RunRecord as a strict superset of the Valheim runInfo.
- Frozen-struct compat tests and a test that fails if game ids are hard-coded in the manager/web/UI shell.
- Owner-chosen ports never move; removing a game keeps legacy folders.

**Fixes the judge made:**
1. Keep the "Valheim Easy Server" Downloads/Desktop subfolder (see above).
2. Never write a changed meaning into a legacy settings field.
3. Use a "keeper" helper instead of Windows handle duplication / Unix FIFOs (see below).
4. Preserve only unknown top-level keys in settings.json; the writer owns maps.
5. The waker gets outcome replies from the app (app → JetKVM, UDP with HMAC), so no new inbound firewall rule is needed on the server computer.
6. Ports are role specs (with an optional tie to the game port), not base-plus-offset, so Satisfactory's 8888 fits.
7. A port clash gives a "pick ports" action instead of moving ports silently (moving breaks port-scoped firewall rules and friends' saved addresses).
8. `EGS_DEV_*` environment variables instead of new flags.
9. "Starred means registered" for Discord commands.
10. Auto-stop for the migrated Valheim is switched on in R3, where the new panel can show and change it.
11. R2 keeps a virtual Valheim instance so the old panel's Install button still works on a fresh install.
12. Minecraft can use its management protocol over websocket (`golang.org/x/net/websocket` is already in go.mod).
13. Firewall rules are program-wide for private game exes, but port-scoped for shared runtimes (java, dotnet), so RCON/REST ports stay closed to the LAN.
14. A before-update backup is taken after SteamCMD finishes and before launch.
15. history.json stays the machine/Valheim history; other games keep their own rings.
16. The "no hard-coded game ids" test covers the manager, web Go code and UI shell only.
17. Later work hooks in through observer hooks rather than editing server.go everywhere.

**The keeper.** Some servers save only on a console command typed on stdin (Terraria `exit`; Vintage Story, Necesse, Zomboid and Minecraft prefer stdin commands). An adopted process (after the app restarts for an update) has no stdin. Plan: a copy of the app binary at `<data>/bin/keeper-<sha8>[.exe]`, run as `keeper-<sha8> --egs-keeper <spec.json>` (checked in main before flag parsing; the keeper is only ever started by new builds). It starts the game with a stdin pipe and its output appended to a log file, passes the game a duplicate of the pipe's write end (so the game never sees EOF even if the keeper dies), listens on 127.0.0.1 with a token (`AUTH`, `LINE <text>`, `PING`), writes keeper.json (0600) with both PIDs, start times, port and token, and exits after the game. Unix: Setsid, ignores SIGINT/SIGHUP. Windows: CREATE_NO_WINDOW; it must never call `SetConsoleCtrlHandler(NULL, TRUE)` before starting the game (inherited: the game would ignore Ctrl+C). After adoption the app reconnects through keeper.json; if that fails, sending console lines reports "no console" and the stop chain moves on to signals.

**Release plan.**

| Release | Contents | Gate |
|---|---|---|
| R1 bridge (to the default branch, early) | safety nets and compat tests; settings.json keeps unknown top-level keys; awareness of other games' live run files (keep awake, block idle sleep, warn; manual Sleep asks first); the identity split (constants for names that must not change); an upgrade-from-real-release CI job | normal CI |
| R2 core | servers.json and migration; the manager refactor into instances with per-instance jobs and adoption; the Valheim adapter; the generic world store; platform additions and the keeper (unused by Valheim); registry lists Valheim only; old panel | upgrade/rollback CI green on Windows and Mac; all existing tests green; the owner runs it 3+ days with real Valheim |
| R3 games | new panel and stars, rename, Add a game, per-game Discord and waker, auto-stop on, memory check, router ports; games marked ready only when their real-server checks are green | real-server matrix for every ready game, the owner's Mac checklist, upgrade R2→R3 and rollback R3→R2 CI |
| R3.x | stage flips and fixes, one game per build | that game's checks |

## The review's blockers (to bake into the new spec)
1. No tunnel anywhere (packages, routes, UI, settings, release items).
2. The owner's Windrose Discord rule, instead of "can't stop itself" (only if the real-server check finds no join/leave signal). The JetKVM bot can see that a message was posted in a channel (GUILD_MESSAGES intent, no message content needed) and relay it to the app; or the app can poll the channel with the bot token.
3. Vintage Story and Project Zomboid are "Needs Windows" on macOS: no Mac install paths, no Mac CI cells, not on the Mac checklist.
4. Start conditions: the baseline is the fresh head at start (still bed004b on 2026-11-26), recorded here; nothing is pushed to the default branch except at the release gates.

## Game facts found in round one (re-verify each)
- **Minecraft Java**: server.jar from Mojang's piston-meta version manifest; Java from the Adoptium (Eclipse Temurin) API for Windows/macOS x64/arm64; EULA, server.properties; RCON or the management protocol; allow list; TCP 25565; ready line `Done (...)! For help, type "help"`; "joined the game" / "left the game"; stop with `stop` on stdin.
- **Vintage Story**: version from `https://api.vintagestory.at/stable.json` (or `lateststable.txt`); server zip `vs_server_win-x64_<v>.zip` / `vs_server_linux-x64_<v>.tar.gz` on cdn.vintagestory.at (no login); no Mac server package (only inside the 600 MB client archive), hence "Needs Windows"; needs .NET 10 (portable runtime from builds.dotnet.microsoft.com); extract each version into a fresh folder; start `dotnet VintagestoryServer.dll --dataPath <dir>`; serverconfig.json; port 42420 TCP+UDP, needs forwarding (UPnP ~50%); whitelist on by default since 1.20 (use a password or whitelist); players must own the game (auth3.vintagestory.at check); ready `Dedicated Server now running on Port`; joins `... joins.`; leaves `Player X left.` or `Player X got removed. Reason: ...`; stop with `/stop` on stdin; saving `World saved!`, done `Stopped the server!`; mods from mods.vintagestory.at (players' games download them automatically). Don't install pre-releases.
- **Terraria**: `GET https://terraria.org/api/get/dedicated-servers-names` then `GET https://terraria.org/api/download/pc-dedicated-server/<name>`; zip has Windows/, Mac/, Linux/ (no exec bits stored; chmod after extracting); Windows exe is 32-bit .NET Framework 4 and needs the XNA Framework 4.0 redistributable (silent msiexec, needs elevation); Mac build is x86_64 MonoKickstart (runs on Intel macOS 12; Apple silicon needs Rosetta); on macOS the working directory becomes the app bundle's Resources folder, so use absolute paths; serverconfig.txt (world, autocreate, worldname, difficulty, size, password, maxplayers, port, worldpath...); port 7777; client and server versions must match exactly ("You are not using the same version as this server"), so update promptly; save and stop with `exit` on stdin (only saves on exit or when the last player leaves); UPnP is ON by default in the server.
- **Necesse**: SteamCMD app 1169370, anonymous; Server.jar with a bundled Temurin 17 JRE (macOS build x86_64, needs macOS 11+, runs on the Intel Mac Pro); start `-nogui`; on Windows its data folder comes from %APPDATA% (keep APPDATA in the child's environment); port 14159 UDP, needs forwarding.
- **Project Zomboid**: SteamCMD 380870, anonymous; about 8 GB RAM (-Xmx8g); ports 16261/16262 UDP; RCON (players, save, quit, servermsg); `-adminusername/-adminpassword` to skip the first-run prompt; servertest.ini, SandboxVars.lua; "Needs Windows" on macOS.
- **Core Keeper**: SteamCMD 1963720, anonymous, Windows/Linux; friends join by Game ID (no router setup); needs a working graphics device (don't pass -nographics).
- **Palworld**: SteamCMD 2394010, anonymous, Windows/Linux (about 5.75 GB); REST API on 8212 (info, players, save, shutdown, announce); game port 8211 UDP, needs forwarding; 8-16 GB RAM; players must match the server version exactly, so compare SteamCMD buildids before each start; Windows needs the VC++ 2015-2022 x64 runtime.
- **Satisfactory**: SteamCMD 1690800, anonymous, Windows/Linux (about 15.5 GB); HTTPS API on 7777 (PasswordlessLogin, ClaimServer, SetAdminPassword, SetClientPassword, CreateNewGame, QueryServerState, SaveGame, Shutdown...) so the app can claim and set up the server; ports 7777 TCP/UDP and 8888 TCP; 8-16 GB RAM.
- **Windrose**: SteamCMD 4129620, anonymous, Windows only; invite code in R5/ServerDescription.json, works without router setup; no RCON/REST/console; status from `R5/Saved/Logs/R5.log`, which has account connect/disconnect lines (e.g. `UR5CoopProxyServer::OnAccountDisconnected`, periodic "Connected Accounts" dumps); about 12 GB RAM for 4 players; stop with Ctrl+C.
- **Enshrouded**: SteamCMD 2278520, anonymous, Windows only; enshrouded_server.json with userGroups (roles with passwords); UDP 15637 (query) needs forwarding.
- **Router ports**: UPnP IGD v1/v2 (WANIPConnection/WANPPPConnection AddPortMapping...), NAT-PMP, PCP; detect carrier-grade NAT (100.64.0.0/10) and double NAT by comparing the router's external IP with the public IP; reachability checks where possible (e.g. Steam's server-by-address lookup for Steam game servers; Minecraft status APIs).
