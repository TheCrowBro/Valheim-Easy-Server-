# Minecraft: Java Edition: facts the adapter relies on

Every fact below was checked on 2026-11-26. "Real run" means Mojang's own
server.jar for 26.3 (SHA-1 `33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c`) run
headless on Linux x64 with Eclipse Temurin 25.0.5.1+2, exactly as the plan
starts it (`java -Xms512M -Xmx2048M -jar server.jar --nogui --universe <saves>
--world <name> --port <port>`, working folder = the server folder). Players
were driven by a small offline-mode protocol client (protocol 777), so those
runs used `online-mode=false`; the lines that only appear in online mode come
from published logs, named where used. The real lines are in `testdata/`
(only the sandbox's own `Picked up JAVA_TOOL_OPTIONS` lines were dropped and
the scratch folder shortened to `/data/games/minecraft`).

Spec 5.2's "verify" marks, and what they turned out to be:

| Spec said | Settled as | Source |
|---|---|---|
| Monitor lines from 26.3's classes: join `<name> joined the game`, leave `<name> left the game`, deaths start with the name | Right words, but in 26.3 every broadcast is logged with a `System chat: ` prefix: `[19:40:16] [Server thread/INFO]: System chat: Alex joined the game`. The Monitor accepts the prefix or no prefix (older versions, and the 1.8 log below, have none). | real run (`testdata/session-26.3.log`) |
| Exit hint "Exception reading … level.dat" (verify) | No such line. With only level.dat damaged the server falls back to level.dat_old and starts. With both damaged: `[ServerMain/ERROR]: Failed to load world data. World files may be corrupted. Shutting down.` and exit code 0. The hint matches that line. | real run (`testdata/damaged-level-26.3.log`) |
| Ctrl+C after a takeover (2.7: verify the server is a console program on Windows) | Temurin's `java.exe` is a console program (PE subsystem 3; `javaw.exe` is GUI, subsystem 2), so Ctrl+C reaches it. On Ctrl+C/SIGINT the JVM's shutdown hook saves players, chunks and level.dat (file times change) but log4j has already closed, so **nothing** is logged; the exit code is 130. On SIGTERM sometimes `Stopping server` gets out. | PE header of `OpenJDK25U-jre_x64_windows_hotspot_25.0.5.1_2.zip`'s `bin/java.exe`; real run (SIGINT, then file times) |
| Launch the real server process directly (2.4, verify per game) | `java -jar server.jar` is the server: the jar is a "bundler" that unpacks `versions/26.3/server-26.3.jar` and `libraries/` into the working folder on its first start (`Unpacking …` lines) and then runs `net.minecraft.server.Main` in the same JVM (`Starting net.minecraft.server.Main`). One PID. No environment is needed. | real run |
| macOS 12 can run the newest server's Java | Temurin 25.0.5.1+2 macOS x64 JRE: `Contents/Home/bin/java` and `lib/server/libjvm.dylib` are x86_64 with `LC_BUILD_VERSION minos 11.0.0` (sdk 14.2). macOS 12 is fine. The installer still runs `java -version` after installing (decided insurance, Q6). | Mach-O header of `OpenJDK25U-jre_x64_mac_hotspot_25.0.5.1_2.tar.gz` |

## Versions and downloads

- `https://piston-meta.mojang.com/mc/game/version_manifest_v2.json`: `latest.release` = `26.3`, `latest.snapshot` = `26.4-pre-1`. The list's first entry is the snapshot, so the resolver looks the release up by id and checks its `type` is `release`. Each entry has the version file's `url` and `sha1`; the resolver checks that SHA-1. Recorded (trimmed to five entries): `testdata/piston-meta-manifest.json`. Source: the URL itself.
- `…/v1/packages/8696584e8d2da5726487597e6c5777480387ec22/26.3.json`: `downloads.server` = `{"sha1": "33680f5f2ac32864d6d7cf5e56a705fdb3e05f4c", "size": 62294556, "url": "https://piston-data.mojang.com/v1/objects/33680f5f…/server.jar"}`, `javaVersion` = `{"component": "java-runtime-epsilon", "majorVersion": 25}`, `releaseTime` 2026-09-15T11:23:02+00:00. Recorded (trimmed to the fields read): `testdata/piston-meta-26.3.json`. The jar's own `version.json` agrees: `"id": "26.3"`, `"protocol_version": 777`, `"java_version": 25`.
- Versions are opaque: "26.3", "26.4-pre-1", "26.1.2". Never parsed.
- Java: Adoptium `GET /v3/assets/latest/25/hotspot?architecture=x64&image_type=jre&os={windows,mac,linux}&vendor=eclipse` answers the archive's link and SHA-256 together (install.Temurin uses that answer, so the checksum always belongs to the file; spec 5.2's separate `/v3/binary/latest` + `/v3/assets` wording is superseded, as stream I noted). jdk-25.0.5.1+2: Windows x64 zip 58,599,439 bytes, Mac x64 tar.gz 42,215,950 bytes (sha256 `fac7e81b…d7d639`), Linux x64 tar.gz 61,751,939 bytes (sha256 `2edef9e4…b17acf`). Hence `DownloadMB` 120.
- Disk: after a first start the server folder is 127 MB (jar 62 MB, unpacked libraries and versions), the Linux JRE 192 MB unpacked, a new world 16 MB. Hence `DiskMB` 500.
- Running the jar with Java 21: `java.lang.UnsupportedClassVersionError: net/minecraft/bundler/Main has been compiled by a more recent version of the Java Runtime (class file version 69.0)…`, exit 1. Real run (`testdata/java21-26.3.log`).

## Command line and files

- `--help` of 26.3 lists: `--bonusChest --demo --eraseCache --forceUpgrade --help --initSettings --jfrProfile --nogui --pidFile <Path> --port <Integer> --recreateRegionFiles --safeMode --serverId <String> --universe <String> (default: .) --world <String>`. Real run.
- `--port` wins over `server-port` but isn't written back (the file kept `server-port=25565` while the server listened on 25566). The plan writes both anyway, so they agree. Real run.
- The server reads `server.properties`, `eula.txt`, `whitelist.json`, `ops.json`, `banned-players.json`, `banned-ips.json` from its working folder, and rewrites `server.properties` at every start (keeping the values, sorting the keys, adding missing ones with defaults). So the plan's own key order doesn't survive; its values do. Real run.
- `--initSettings` writes the defaults (kept as `testdata/server.properties-26.3-init`): there is **no `pvp` key** (settled); `level-type=minecraft\:normal` (namespaced, colon escaped; the plan writes `minecraft:<type>` the same way); `white-list=true` and `enforce-whitelist=false` by default; `log-ips=true`; `pause-when-empty-seconds=60`; `management-server-*` keys exist (off). Real run.
- `level-name` names the world inside level.dat when it is made (`Saving chunks for level 'ServerLevel[world]'…` showed "world" while `--world MyWorld` chose the folder), so the plan sets `level-name` to the world too. `log-ips` is set to true because the allow list's "auto" reads the login address. Both are additions to spec 5.2's key list, for those reasons.
- `usercache.json` (every player who joined, with account id) lives in the **universe** folder (`--universe`, the app's `saves/`) in 26.3, not in the server folder. It is a file, so the world list (folders) ignores it. Real run.
- Without `eula=true`: `[ServerMain/INFO]: You need to agree to the EULA in order to run the server. Go to eula.txt for more info.`, exit 0. Real run (`testdata/eula-26.3.log`). The EULA link `https://aka.ms/MinecraftEULA` is the one in the file the server writes.
- A world folder (26.3): `level.dat`, `level.dat_old`, `session.lock`, `data/`, `datapacks/`, `dimensions/minecraft/{overworld,the_nether,the_end}/…` (regions under `dimensions/minecraft/overworld/region`), `players/{data,stats,advancements}/<uuid>…`. Real run.

## The log (26.3)

Format `[HH:MM:SS] [<thread>/<LEVEL>]: <message>`. Thread names may hold a slash (`RCON Client /127.0.0.1 #2`), so the Monitor matches the level, not "up to the first slash". Lines without the stamp: the bundler's `Unpacking …` and `Starting net.minecraft.server.Main`, the JVM's `WARNING: …` (JNA, Unsafe), stack traces. All real run, in `testdata/`:

- version: `Starting minecraft server version 26.3`
- ready: `Done (8.343s)! For help, type "help"`; RCON starts just after it (`Starting remote control listener`, `Thread RCON Listener started`, `RCON running on 0.0.0.0:25575`), a few milliseconds later, so a probe right at "online" can be refused once.
- every start saves once just after loading: three `Saving chunks for level 'ServerLevel[<level-name>]'/minecraft:<dimension>` lines after "Done".
- login: `Alex[/127.0.0.1:53090] logged in with entity id 5 at (105.5, 136.0, -27.5)`, then `System chat: Alex joined the game`.
- online mode only: `[User Authenticator #1/INFO]: UUID of player <name> is <uuid with dashes>` before the login (from the login thread). Sources: Paper 26.1.2's log at https://mclo.gs/zN6VHE7 (Paper keeps vanilla's login lines; `testdata/online-paper-26.1.2.log`), and vanilla 1.8.1-pre2's log attached to Mojang's bug MC-78020 (https://bugs.mojang.com/browse/MC-78020, `testdata/online-1.8-mc78020.log`), which also has a login from a public address.
- renamed: `System chat: Alex (formerly known as OldAlex) joined the game`, when the user cache knew the account by another name. Real run (`testdata/renamed-26.3.log`; the cache entry was edited to an old name).
- leave: `Alex lost connection: Disconnected`, then `System chat: Alex left the game`.
- deaths: `System chat: Alex hit the ground too hard`, `… was slain by Zombie`, `… was killed`, `… drowned`, `… blew up`, `… fell out of the world`, `… died` (`testdata/deaths-26.3.log`). The words are the 106 `death.*` entries of `assets/minecraft/lang/en_us.json` inside `server-26.3.jar`, copied as they are into `deaths.json`, which the Monitor embeds (the spec said "kept in testdata"; it is runtime data, so it sits beside the code and `TestDeathMessages` reads every entry). Two entries don't start with the player: `death.attack.sulfurCubeHot.player`/`.item` (`%2$s showed %1$s that not just the floor is lava`), and `death.attack.message_too_long` wraps a cut-short copy; `death.attack.badRespawnPoint.link` is only a piece of another message.
- not deaths: player chat `[Not Secure] <Alex> Alex fell from a high place` (no "System chat"), the console's `say`: `[Not Secure] [Rcon] hello from the console`, advancements `System chat: Alex has made the advancement [Stone Age]`, `tellraw` (not logged at all).
- turned away (allow list on, name not on it): `Disconnecting Bob (/127.0.0.1:52238): You are not white-listed on this server!`, `Bob (/127.0.0.1:52238) lost connection: You are not white-listed on this server!`, `[Server thread/WARN]: handleDisconnection() called twice`. Spec 5.2's pattern matches the second line exactly.
- wrong client version: nothing is logged at all (the client is told `multiplayer.disconnect.incompatible` "Incompatible client! Please use 26.3"). So there is no "bad version" event. Real run (client sent protocol 770).
- save: RCON `save-all` answers `Saving the game (this may take a moment!)Saved the game` (two messages run together) and logs `System chat: [Rcon: Saved the game]` (with `broadcast-rcon-to-ops=true`, the default); typed at the console, 26.3 logs command answers as `System chat: …` too (a 26.3 Snapshot 5 log on Mojang's tracker shows `System chat: Stopping the server` after a console `stop`: https://bugs.mojang.com/api/issue-attachment-get?attachmentId=588230). There is no "All dimensions are saved" line.
- autosaves log nothing: seven minutes with a player on, no line. Real run.
- `Server empty for 60 seconds, pausing` after a minute with nobody on (`pause-when-empty-seconds`). Real run.
- stop (RCON `stop`, answer `Stopping the server`): `System chat: [Rcon: Stopping the server]`, `Stopping server`, `Saving players`, `Saving worlds`, three `Saving chunks for level …` lines, `Thread RCON Listener stopped`; exit 0. With a player on, the stop logged no leave for them (`testdata/stop-player-on-26.3.log`), so End() makes them leave.
- game port taken: `[Server thread/WARN]: **** FAILED TO BIND TO PORT!`, `The exception was: io.netty.channel.unix.Errors$NativeIoException: bind(..) failed with error(-98): Address already in use`, `Perhaps a server is already running on that port?`, an `IllegalStateException: Failed to initialize server`, `Stopping server`, `Saving worlds`; exit 0. A new world's folder was already made. (`testdata/port-in-use-26.3.log`)
- RCON port taken: `[Server thread/WARN]: Unable to initialise RCON on 0.0.0.0:25575` with `java.net.BindException: Address already in use`; the server keeps running without RCON. The Monitor reports `rcon_failed`. (`testdata/rcon-port-in-use-26.3.log`)
- Minecraft exits with code 0 after each of these failures, so exit hints read the last lines, never the code.

## RCON and the lists

- `list`: `There are 0 of a max of 10 players online: ` (note the space after the colon), `There are 1 of a max of 10 players online: Alex`. Real run.
- `whitelist add X` → `Added X to the whitelist` / `Player is already whitelisted`; `whitelist remove X` → `Removed X from the whitelist` / `Player is not whitelisted`; `whitelist on` → `Whitelist is now turned on` / `Whitelist is already turned on`, and it writes `white-list=true` into server.properties itself; `op X` → `Made X a server operator`; `deop X` → `Made X no longer a server operator`; `ban X reason` → `Banned X: reason`; `pardon X` → `Unbanned X`; an unknown command → `Unknown or incomplete command. See below for error<cmd><--[HERE]`. Real run. Failures from 26.3's en_us.json: `That player does not exist`, `Nothing changed. The player is already an operator` (and the like).
- `whitelist add <name>` looks the name up at Mojang when the server can reach it: in an offline-mode test the server still put Steve's real account id (`8667ba71-…`) on the list, so the offline Steve was turned away. The list matches by account id. Real run.
- `whitelist on` doesn't throw out players already on while `enforce-whitelist` is false, which is what the plan writes while the list is off; so turning it on live keeps everyone playing.
- File shapes (two-space JSON): `whitelist.json` `[{"uuid", "name"}]`; `ops.json` adds `"level": 4, "bypassesPlayerLimit": false`; `banned-players.json` adds `"created": "2026-11-26 19:50:12 +0000", "source": "Rcon", "expires": "forever", "reason"`; `usercache.json` `[{"uuid", "name", "expiresOn": "2026-12-26 19:40:16 +0000"}]`. Real run.
- Mojang's profile API `GET https://api.mojang.com/users/profiles/minecraft/Notch` → 200 `{"id": "069a79f444e94726a5befca90e38aaf5", "name": "Notch"}` (32 hex digits, no dashes; the list files need 8-4-4-4-12); an unknown name → 404 `{"path": …, "errorMessage": "Couldn't find any profile with name …"}`. `https://api.minecraftservices.com/minecraft/profile/lookup/name/Notch` answers the same. Recorded: `testdata/mojang-profile-*.json`.
- Offline account ids are name-based UUIDs of `OfflinePlayer:<name>` (Alex → `36532b5e-c442-3dbb-a24c-c7e55d0f979a`, matching the real server); the stand-in uses them.

## Decisions taken here (for the lead)

- EULA: `Saved.State["eula"]` = when the owner agreed (RFC 3339); Plan refuses without it and writes eula.txt with that date.
- Allow list "auto": on before the first start unless `Saved.State["cgnat"] == "true"` (to be set by the router code, spec 6.5); the first time the list goes on, everyone in the user cache who isn't banned is put on it, and `State["allowListOn"]` remembers it, after which the list is the owner's. A login from a public address (not private, loopback, link-local or 100.64.0.0/10) while "auto" is off makes the Monitor add everyone playing and send `whitelist on` over RCON (on its own goroutine), with an `allow_list_on` event.
- The run record's `Extra` holds `allowList` (on/off at start) and `allowListMode` (the setting).
- `Recover` reads the RCON port and password back from server.properties.
