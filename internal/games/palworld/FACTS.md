# Palworld: what the adapter relies on, and where each fact comes from

Checked on 2026-11-26 unless a line says otherwise. The newest server then was
**v1.0.5.102999**, Steam build **25247047** (published 2026-09-11). The download is
about 4.7 GB, so it was not downloaded here: the facts come from the publisher's
documentation, Steam's app information, small pieces of the publisher's own
container image, and logs and files people published. What only the real server can
settle is checked by `TestRealServer` (realserver_test.go), which prints a line
starting `FACTS:` for each; those results go into the last section when the lead has
run it.

## Sources

1. **Steam app info** for app 2394010, `https://api.steamcmd.net/v1/info/2394010`
   (fetched 2026-11-26).
2. **The publisher's server guide**, `https://docs.palworldgame.com/` (pages for
   version 1.0.4, fetched 2026-11-26): Requirements, Deploy dedicated server,
   Connect to the server, About server, Configure the server (arguments),
   Configuration parameters, Commands, and the REST API pages (info, players, save,
   shutdown, stop, announce, metrics). The API pages draw their examples with
   script; the same examples (the `curl` lines with their URLs) were read from
   Context7's copy of the guide, `https://context7.com/websites/palworldgame/llms.txt`.
3. **The publisher's container image** `ghcr.io/pocketpairjp/palserver:v1.0.5.102999`
   (built 2026-09-11): its config (the build steps) and the first 40 MB of the layer
   that holds the server, read straight from the registry, which hold
   `/pal/Package/DefaultPalWorldSettings.ini` (3,880 bytes, Windows line ends,
   SHA-256 `eaf49b2615f3d97ff71e510aff14443c87b92f69e862d2c72a3b254d9fa4c75a`,
   copied unchanged to `default/DefaultPalWorldSettings.ini`) and
   `Manifest_NonUFSFiles_Linux.txt`.
4. **The publisher's container repository**,
   `https://github.com/pocketpairjp/palworld-dedicated-server-docker` (README and
   `compose/compose.yaml`, fetched 2026-11-26).
5. **thijsvanloef/palworld-server-docker**, the most used community image
   (`scripts/start.sh`, `scripts/helper_functions.sh`, `scripts/player_logging.sh`,
   `scripts/rest_api.sh`, `scripts/init.sh`, `README.md` on its main branch, fetched
   2026-11-26). It can run the Windows server too, under Wine.
6. Steam Community, "How to fix your Dedicated Server - SIMPLE (Windows)",
   `https://steamcommunity.com/app/1623730/discussions/0/4353366080397432573/`
   (posts of 2024-04-04).
7. pelican-eggs/eggs issue 2915, `https://github.com/pelican-eggs/eggs/issues/2915`
   (comment of 2024-05-13).
8. CubeCoders/AMPTemplates issue 731, `https://github.com/CubeCoders/AMPTemplates/issues/731`
   (2024-04-19): a real join and leave.
9. thijsvanloef/palworld-server-docker issue 538,
   `https://github.com/thijsvanloef/palworld-server-docker/issues/538` (2024-04-21): a
   real container log with a join.
10. Supercraft's "Palworld Server Docker: Tested Compose File and Real RAM Numbers",
    `https://wiki.supercraft.host/wiki/palworld/docker_server_setup/` (updated
    2026-10-06, tested with v1.0.5.102999): the real console lines of a start, a REST
    save and a REST shutdown, and real REST answers.
11. A Windows setup guide by a hosting service with sample console output from
    **v1.0.3.101283** (2026-08): the 1.0 "connected the server" and "joined the
    server … Player id" lines. Its address is left out on purpose: its domain is a
    word the owner's decisions keep out of this tree (invariant I10). The sample's
    player name was the site's own name; testdata uses "Alex" instead.
12. TaYaKi71751-save-files/Palworld_Dedicated_Config, `GameUserSettings.ini`,
    `https://github.com/TaYaKi71751-save-files/Palworld_Dedicated_Config` (a real
    dedicated server's file, 2024; copied to
    `testdata/ini/GameUserSettings-dedicated.ini`).
13. Steam Community, `https://steamcommunity.com/app/1623730/discussions/0/595136312034525894`
    (2024-12-27): changing DedicatedServerName resets the map.
14. Loafhosts, "Palworld save file location on your server",
    `https://loafhosts.com/guides/palworld-save-file-location/` (2026), and HytHost,
    `https://hythost.com/guides/palworld-transfer-save-to-dedicated-server` (2026).
15. Winheberg, "How to reset player data on your Palworld server",
    `https://winheberg.com/en/docs/palworld/reset-palworld-player-data` (2026-08-11).
16. Ghosting, "Transfer a Palworld World to a Dedicated Server (1.0)",
    `https://ghosting.gg/blog/how-to-transfer-a-palworld-world-to-a-dedicated-server` (2026).
17. Microsoft Learn, "Latest supported Visual C++ Redistributable downloads",
    `https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist`
    (updated 2026-08-04), and "Redistribute Visual C++ files",
    `https://learn.microsoft.com/en-us/cpp/windows/redistributing-visual-cpp-files`
    (updated 2026-04-15).
18. microsoft/STL issue 4730, `https://github.com/microsoft/STL/issues/4730` (2024):
    programs built with Visual Studio 2022 17.10 crash on runtimes older than 14.40.
19. abspwgm/absolute-palworld-server pull request 16,
    `https://github.com/abspwgm/absolute-palworld-server/pull/16` (2026-09-22, v1.0.5).

## The download

- App 2394010, anonymous, `+app_update 2394010 validate` [2]. Windows and Linux only:
  `oslist` is `windows,linux`, and the macOS depot is empty [1]. So macOS is "Needs
  Windows" (decided as well).
- Windows depot 2394011: **4,672,921,664 bytes to download, 5,748,666,401 bytes
  installed**; Linux depot 2394012: 4,503,130,048 and 4,820,786,528 [1]. Info says
  4700 MB to download; SteamCMD's free-space check asks for 6 GB.
- Steam's Windows install also pulls depots 228989 and 228990 from Steamworks Common
  Redistributables (app 228980), the installers Steam runs itself [1]. SteamCMD does
  not run them, which is why the app installs the Visual C++ runtime itself (below).
  The Windows community image installs "vcrun2022" for the same reason [5].
- Public build on 2026-11-26: 25247047, updated 2026-09-11 [1]; the publisher's image
  of the same day is tagged v1.0.5.102999 [3, 4].

## Starting it

- Steam's Windows launch is `PalServer.exe -useperfthreads -NoAsyncLoadingThread
  -UseMultithreadForDS`; a second launch option adds `-publiclobby` ("Open and start
  as a community server"); Linux runs `PalServer.sh` [1].
- `PalServer.exe` is a small launcher that starts
  `Pal\Binaries\Win64\PalServer-Win64-Shipping-Cmd.exe` [6]; the name has been that
  since the 0.2 update of April 2024, when it changed and firewall rules had to follow
  [6]. The community image's Windows mode starts exactly
  `Pal/Binaries/Win64/PalServer-Win64-Shipping-Cmd.exe Pal <args>` [5]
  (`PalworldServerBinaryPath`, and `STARTCOMMAND+=("Pal")`), and the publisher's Linux
  script does the same with the Linux program: `PalServer-Linux-Shipping Pal "$@"` [5]
  (the arm64 rewrite quotes the line), with `Pal/Binaries/Linux/PalServer-Linux-Shipping`
  and `PalServer.sh` listed in the publisher's own manifest [3]. So the app starts the
  `-Cmd` program directly with `Pal` first; the launcher sets no environment of its
  own.
- `-Cmd` is Unreal Engine's console build of the program: it runs in a console window,
  so Ctrl+C can reach it (spec 2.7). Whether it saves on Ctrl+C is for the real check.
- Start it from its own folder: the Steam library it loads reads `steam_appid.txt`
  from the working folder; a host's server that players couldn't join worked once it
  was started from `Pal/Binaries/Win64` [7], and that folder holds `steam_appid.txt`
  (the community image's clean-up list names it there [5]). Plan.Dir is that folder.
- Arguments [2]: `-port=8211` (the listen port), `-players=32` (the player limit),
  `-publiclobby` (community server), `-publicip`, `-publicport` (community server
  only; "does not change the port number itself that the server uses"),
  `-logformat=text`, `-NumberOfWorkerThreadsServer`. "In v1.0 and later, leaving
  [`-useperfthreads -NoAsyncLoadingThread -UseMultithreadForDS`] unset may improve
  performance." The app passes `Pal -port=<game> -players=<n>`.
- Requirements [2]: 4 cores; 16 GB memory recommended, "8GB is also bootable, but
  increases the possibility of server crashes"; UDP 8211; Windows or Linux 64-bit; an
  SSD ("Low-performance storage may corrupt saved data"). Info.MemoryMB is 10240.

## Ports

- Game: **UDP 8211**, the only port the requirements name and the only one the
  publisher's compose file publishes [2, 4]. Public.
- REST API: **TCP 8212** (`RESTAPIPort`, off by default) [2, 3]. "These APIs are not
  designed to be exposed directly to the Internet … It is recommended that they be
  used within the LAN" [2]. Private: no firewall rule, no router mapping, a new
  password every run (spec 2.5).
- Steam's query port (UDP 27015 by default, `-queryport`) matters only for the
  community list [5]; the app doesn't pass `-publiclobby` and gives it no role. The
  real check records whether 27015 is open anyway.
- RCON (`RCONPort` 25575) is off by default and stays off [3]: the REST API does
  everything the app needs, and the community image calls RCON deprecated [5].

## Settings files

- `PalWorldSettings.ini` in `Pal/Saved/Config/WindowsServer/` (LinuxServer on Linux):
  "Copy the default configuration file and use that", from
  `DefaultPalWorldSettings.ini` in the install folder; "editing
  DefaultPalWorldSettings.ini will not affect the changes"; "The directories will only
  create once the server has been started" [2].
- One section, `[/Script/Pal.PalGameWorldSettings]`, one line
  `OptionSettings=(…)` with 122 options in 1.0.5, written `Key=Value` with quoted text
  (`ServerName="Default Palworld Server"`), bools as `True`/`False`, decimals as
  `1.000000`, an empty value (`DenyTechnologyList=`) and a bracketed list
  (`CrossplayPlatforms=(Steam,Xbox,PS5,Mac)`) [3]. The publisher's README shows a file
  with only two options, so options left out take their defaults [4].
- 1.0.5's defaults the app relies on [3]: `RESTAPIEnabled=False`, `RESTAPIPort=8212`,
  `RCONEnabled=False`, `AdminPassword=""`, `ServerPassword=""`,
  `ServerPlayerMaxNum=32`, `LogFormatType=Text`, `AutoSaveSpan=30.000000`,
  `bIsUseBackupSaveData=True`, `bIsShowJoinLeftMessage=True`, `PublicPort=8211`,
  `DeathPenalty=Item`.
- The app sets `ServerName`, `ServerPassword`, `AdminPassword` (new every run),
  `ServerPlayerMaxNum`, `RESTAPIEnabled=True`, `RESTAPIPort`, `RCONEnabled=False`,
  `LogFormatType=Text` (so the Monitor's lines can't turn into JSON, [2]'s
  `LogFormatType`), and the world options below; it keeps every other option and
  line. Not `PublicPort`, as decided.
- World options (spec 5.9's "verify keys"): **settled** against 1.0.5's file [3] and
  the 1.0 guide's list [2]: `DeathPenalty` (`None`, `Item`, `ItemAndEquipment`,
  `All`), `ExpRate`, `PalCaptureRate`, `PalSpawnNumRate` ("Impacts performance"),
  `DayTimeSpeedRate`, `NightTimeSpeedRate`. `Difficulty` is still in the file
  (`None`) but the 1.0 guide no longer lists it, so the app leaves it alone.
- A world's own `WorldOption.sav` is read in preference to `PalWorldSettings.ini`
  [16]. The server's own worlds have none; one copied from the game would override the
  app's settings, its admin password included, which is why a refused REST password
  says so.
- `GameUserSettings.ini` in the same folder: section
  `[/Script/Pal.PalGameLocalSettings]`, mostly the game's graphics leftovers, and
  `DedicatedServerName=<32 upper-case hex digits>` [12]; the community image reads it
  with `DedicatedServerName=([0-9A-Z]+)` [5].
- The adapter writes both files only before a start, in one step (a temporary file
  renamed over the old one), with the file's own line ends.

## Worlds

- A world is a folder `Pal/Saved/SaveGames/0/<id>/`, the id 32 hexadecimal digits
  ("C2777061EB1F49F18BFE6364B6481A9A") [14, 15]. It holds `Level.sav` ("the world
  itself"), `LevelMeta.sav`, `Players/` with one `<player id>.sav` per player, maybe
  `WorldOption.sav`, and `backup/` [14, 15, 16].
- `DedicatedServerName` picks the folder the server loads [5, 14]. **A name with no
  folder makes a new world**: "If that name has no folder, the game makes a new world
  about a minute after the server is up" [14]; changing the name and restarting
  "will reset map exploration and quest progression" [13].
- So for a new world the app chooses the id itself (32 upper-case hex digits, as the
  server writes them) and writes it as `DedicatedServerName` before the first start.
  It knows the folder before anything is saved, and the name is kept in State as
  `world.<id>` = name; `planned` holds the id of the last start. Spec 5.9's "empty
  DedicatedServerName, then record the GUID after the first save" (a verify) is not
  needed. As a safety net, if the planned folder never appears while
  `GameUserSettings.ini` names another folder that has a world and no name, the next
  plan gives that folder the name. The real check confirms which happens.
- The folder appears at the first save, about a minute after start [14]; the
  autosave comes every `AutoSaveSpan` seconds, 30 by default [3]. A world counts as
  there once `Level.sav` is.
- With `bIsUseBackupSaveData` (on by default) the server keeps its own copies in the
  world folder: "5 save data per 30 sec, 6 per 10 min, 12 per 1 hour, 7 per 1 day"
  [2]. The app's backups leave `backup/` out (it can hold dozens of copies); deleting a
  world removes its folder, those copies with it, after the app's own "deleted" backup.
- Import stays off (spec): a co-op world needs its host's player data rewritten for a
  dedicated server [16], which the app doesn't do this round.

## The REST API

- Base `http://localhost:8212/v1/api/<name>` [2] (the curl examples), basic
  authentication; the user is `admin` and the password `AdminPassword` [5]
  (`userpass="admin:${ADMIN_PASSWORD}"`). Answers 200, 400, 401 [2].
- `GET info` → `{"version", "servername", "description", "worldguid"}` [2]; a real
  1.0.5 answer: `"version": "v1.0.5.102999"`, `"worldguid":
  "36B882106A4F4EBCAA16940C167F791E"` [10] (`testdata/rest/info-v1.0.5.102999.json`).
- `GET players` → `{"players": [{name, accountName, playerId, userId, ip, ping,
  location_x, location_y, level, building_count}]}` [2]; a real empty answer is
  `"players": []` [10]. A player still loading in, or making their character, is
  listed with an all-zero playerId [5] (`player_logging.sh`), and may have no name
  yet; they count but aren't named (`testdata/rest/players-one-loading-written.json`
  is written from that note, not recorded).
- `POST save` saves; `POST shutdown` needs a body with `waittime` ("Must use with
  request body", `waittime` required) and takes a `message` [2]; `POST stop` force
  stops; `POST announce {"message"}`; POST answers are a short text, "OK" for
  announce [5]. A POST with no body should still send `Content-Length: 0`: a server
  may refuse one without [19].
- The community image stops a server with `save` then `shutdown 1` and calls the
  plain SIGTERM fallback one that "Does not save" [5]; a 1.0.5 run of the same
  sequence printed `[LOG] REST accessed endpoint /v1/api/save OK`,
  `[LOG] REST accessed endpoint /v1/api/shutdown OK` and `REST API stopped` [10]. The
  app's stop: save, then shutdown with waittime 1 (Wait 3 min), then Ctrl+C (Wait
  2 min). Guides say a save is best given time before a hard stop; the shutdown saves
  on its way out, so it is sent even when the save call runs out of time.

## The console log

Lines as the real servers printed them (testdata/monitor has each excerpt):

- Start [10]: `Setting breakpad minidump AppID = 2394010` and `[S_API FAIL] Tried to
  access Steam interface SteamUser021 before SteamAPI_Init succeeded.` (the Steam
  library; printed on every good start, so hidden), `Game version is
  v1.0.5.102999`, `REST API started on port 8212`, `Running Palworld dedicated server
  on :8211` (ready). A v1.0.3 start prints the same version and ready lines [11].
- Join and leave, added in 0.2.0.6 (2024-04-04) [8]:
  `[2024-04-19 19:26:37] [LOG] HunterAP joined the server. (User id: steam_<redacted>)`,
  `… [LOG] HunterAP left the server. (User id: steam_<redacted>)` [8];
  `[2024-04-21 15:53:31] [LOG] Tanaka Lyanne joined the server. (User id:
  steam_76561198379551257)` [9]. In 1.0 a player first "connected" and then
  "joined" with the player id too [11]: `[LOG] <name> <ip> connected the server.
  (User id: steam_…)`, `[LOG] <name> joined the server. (User id: steam_…, Player id:
  <32 hex>)`. The time before `[LOG]` is the server's local time, with the day not
  always two digits ("2026-08-1") [11].
- REST calls are logged: `[LOG] REST accessed endpoint /v1/api/save OK`, and
  `REST accessed endpoint / Unauthorized` for a refused one [10]. The app's own probe
  calls (players and info, every 30 s) are hidden from the Log page.
- No line is known for a death or a wrong password, so Deaths is false.
- Whether the Windows server's lines reach a file the app hands it as standard output,
  and how promptly (a program's standard output to a file is often written in
  blocks), is for the real check. Presence and readiness don't depend on it: the
  REST API is the authority (below), and a join or leave line older than the latest
  probe that disagrees with it is ignored.

## Presence

- ByName from the REST API's player list, probed every 30 s (spec 2.6); joins and
  leaves in the log only make changes show sooner. Ready when the API answers or the
  "Running" line shows, whichever comes first (`RunState.Extra["readyBy"]` says which).

## Friends

- They type the address and port "to form below the server list" under the game's
  multiplayer menu [2]. Cross-play is on by default for Steam, the Microsoft Store and
  Xbox, Mac and PS5 [2], but "If you want to join the server from the Xbox version or
  the PS5 version, you need to deploy it as a community server" [2], which the app
  doesn't, so FriendsNeed says PC or Mac. Their game must be exactly the server's
  version; the server updates at every start (decided), and Join names the version
  once the server has said it.

## The Visual C++ runtime

- Permalink of the newest x64 runtime: `https://aka.ms/vc14/vc_redist.x64.exe`
  ("Permalink for latest supported x64 version") [17]; it changes with every release,
  so there is no fixed checksum to pin. (Spec 5.9's `aka.ms/vs/17/release/…` is the
  older permalink.) Silent install: `/install /quiet /norestart` [17]; exit codes 0,
  3010, 1641 and 1638 count as installed (install.EnsurePrereq).
- Installed version: `HKLM\SOFTWARE\Wow6432Node\Microsoft\VisualStudio\14.0\VC\Runtimes\x64`,
  values `Installed`, `Major`, `Minor`, `Bld`, `Version` [17]. The adapter reads the
  32-bit view (that key) and the 64-bit one, and wants `Installed`=1 and at least
  14.40: "The version of the Redistributable installed on the machine must be the same
  or later than the version of the MSVC Build Tools used to create your application"
  [17], and programs built with 17.10 crash on older runtimes [18]. A Repair always
  runs the installer, which updates an older runtime.

## Spec 5.9's "verify" items

| Spec says | Now |
|---|---|
| `Program()` = `server/Pal/Binaries/Win64/PalServer-Win64-Shipping-Cmd.exe` (verify) | Right [5, 6]; started with `Pal` first, from its own folder [5, 7]. |
| World options "difficulty and rates; verify keys" | Settled: the keys above [2, 3]; no Difficulty (gone from the 1.0 guide). |
| "a new world: empty DedicatedServerName, and after its first save the new GUID is recorded (verify)" | Changed: the app picks the id and writes it [13, 14]; a net catches a server that picks its own. The real check logs which. |
| Launch the real process directly; the skipped launcher's environment into Plan.Env (2.4, verify) | Right: the launcher only starts the `-Cmd` program; no environment [5, 6]. |
| A stop ending in "interrupt" must reach a console program (2.7) | `-Cmd` is the console build; whether Ctrl+C saves: real check. |
| VC++ key `HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64` `Installed`=1 | The key is in the 32-bit view (WOW6432Node) [17]; also needs 14.40 or newer [17, 18]. |

## Still to confirm on the real server (TestRealServer prints these)

- That the server makes the new world in the planned folder (and how long after
  "online" `Level.sav` appears), and that REST info's `worldguid` is that id.
- Whether the console lines reach the captured log, and promptly; which came first,
  the "Running" line or the REST API.
- That the started process is `PalServer-Win64-Shipping-Cmd.exe` and holds UDP 8211
  and TCP 8212; whether UDP 27015 is open.
- That the REST stop saves `Level.sav`; how long it takes.
- What Ctrl+C does: whether it stops the server, how fast, and whether it saves.
- What the server prints, and whether it stops, when another program holds its game
  port (and so what ExitHint should match).
- With `EGS_CI_ADMIN=1`: that a Repair runs the Visual C++ installer silently.

## Results of the real check

Not run yet: the lead dispatches `real-server.yml` for this cell on `mg/g-palworld`.
