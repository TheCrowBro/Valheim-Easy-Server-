# Satisfactory dedicated server: facts the adapter relies on

Each fact has its source and the date it was checked. "Settled" means
checked against the publisher's documentation, Steam's app info, a real
server's output or the game's own source headers. "To confirm" means the
real-server check (`TestRealServer`, on demand, spec 7.4) prints a
`FACTS:` line for it; copy the answer here when it has run.

Sources, all read on 2026-11-26:

- **Wiki**: the official wiki's Dedicated servers pages,
  https://satisfactory.wiki.gg/wiki/Dedicated_servers,
  .../Dedicated_servers/HTTPS_API and .../Dedicated_servers/Configuration_files,
  and its save format page https://satisfactory.wiki.gg/wiki/Save_files.
- **API docs**: the publisher's `CommunityResources/DedicatedServerAPIDocs.md`,
  shipped with the game, as excerpted verbatim (game version 1.2.4.0) at
  https://raw.githubusercontent.com/Sour-Dev-Home/satisfactory-dash/main/docs-vault/raw-sources/dedicated-server-api.md,
  and the OpenAPI description at https://satisfactory-oas.github.io/spec.
- **Real answers**: a real 1.2.4.0 server's answers captured by the same
  project (testdata/api, testdata/README.md).
- **Real log**: a real 1.0.0 (CL 365306) server's start
  (testdata/monitor/v1.0.0-CL-365306-linux-start.log).
- **Steam**: https://api.steamcmd.net/v1/info/1690800.
- **Headers**: the game's own C++ headers for its server, which Coffee Stain
  publishes for modding, at
  https://github.com/satisfactorymodding/SatisfactoryModLoader/tree/master/Source/FactoryGame/Public/Server
  (FGDSGameplayTypes.h, FGDSAuthenticationTypes.h, FGDSSharedTypes.h,
  FGTLSTypes.h, FGDSQueryTypes.h), and its save header as the
  satisfactory-3d-map project mirrors it
  (libsave/include/SatisfactorySave/GameTypes/FactoryGame/FGSaveManagerInterface.h).
- **Docker**: the widely used robtme/satisfactory-server image's scripts,
  which start, claim and stop real servers: run.sh and healthcheck.sh at
  https://github.com/robtme/satisfactory-server.

## The download and the program

- Settled (Steam): app 1690800 "Satisfactory Dedicated Server", free to
  download (anonymous), `oslist` windows,linux, 64-bit. Public build
  26728027, built 2026-11-17. The game is at 1.2.5.1 (CL 520140) by then
  (SatisfactoryModLoader issue 432, 2026-11-19).
- Settled (Steam): the Windows depot 1690801 is 15.53 GB installed and a
  3.62 GB download; the Linux depot 1690802 is 2.80 GB installed. Depot
  1004 (Windows) is Steamworks' shared redistributable (`depotfromapp`
  1007), 68 MB: Steam's own client library. So the adapter says
  DownloadMB 3700 and DiskMB 17000, and SteamCMD's free-space check asks
  for 16 GB. **The spec's "download ~15.5 GB" is the installed size**: the
  download is 3.6 GB.
- Settled (Steam): Steam's Windows launch is `FactoryServer.exe -log
  -unattended`; Linux runs `FactoryServer.sh`.
- Settled (wiki, and the real log's `LogInit: ExecutableName:
  FactoryServer-Linux-Shipping` and base directory): the process that runs
  and listens is `Engine\Binaries\Win64\FactoryServer-Win64-Shipping-Cmd.exe`
  (Linux: `Engine/Binaries/Linux/FactoryServer-Linux-Shipping`). The
  launcher passes the project name `FactoryGame` first (Unreal's standard
  launcher; the runhelper tool starts the `-Cmd` program the same way:
  https://github.com/Trevrosa/runhelper runner/src/games/satisfactory.rs).
  `-Cmd` is the console build, so Ctrl+C reaches it (spec 2.7).
- Settled (wiki): `-log` shows the log in the console; `-unattended`
  suppresses dialogs, so the server never waits on a message box nobody
  sees.
- To confirm: whether the Windows server needs Microsoft's Visual C++
  runtime installed separately. No source says it does, and SteamCMD
  brings only Steam's library (depot 1004). The real check reports which
  runtime files are in the install and whether Unreal's prerequisite
  installer is there; ExitHint suggests the runtime when the server stops
  before writing a line.
- To confirm: the API's documentation ships with the game client
  (`CommunityResources/DedicatedServerAPIDocs.md`); the real check reports
  whether the server install has it too.

## Command line and ports

- Settled (wiki): `-Port=<port>` sets the game port, default 7777, UDP for
  the game and TCP for the HTTPS API; `-ReliablePort=<port>` sets the
  reliable messaging port, default 8888, TCP. Ports 15000 and 15777 are no
  longer used since 1.0; the reliable port came in 1.1. The Docker image
  starts real servers with exactly these flags
  (`./FactoryServer.sh -Port=… -ReliablePort=… -ExternalReliablePort=…`),
  and the real log's command line shows `-Port=7777`.
- Settled (wiki): external and internal port numbers must match (no
  remapping), so `-ExternalReliablePort` isn't needed: the app's router
  mapping keeps the same number.
- Settled (wiki): "If in use, the server increments to the next free port"
  for the game port. The Monitor reads Unreal's `LogNet: GameNetDriver …
  listening on port N` line (its wording from a community post the wiki
  cites) and warns ("port_moved") when N isn't the planned port. To
  confirm: the line's exact wording and the moving itself (the real check
  takes the port first and reports).
- To confirm: what a taken reliable port does. The wiki's ports table says
  the server "fails to initialize"; its configuration page says the
  reliable messaging factory tries 512 ports from 8888
  (`PortRangeBegin=8888`, `PortRangeLength=512`).
- Settled (wiki, Docker): the player limit is set on the command line with
  `-ini:Game:[/Script/Engine.GameSession]:MaxPlayers=N`; the Docker image
  also sets `-ini:GameUserSettings:[/Script/Engine.GameSession]:MaxPlayers=N`,
  and the adapter does both. The default is 4; the wiki warns more can hurt
  stability. The app allows 1 to 16. To confirm: QueryServerState's
  `playerLimit` follows it (the real check compares).
- Settled (wiki): 8 GB of memory, 16 GB for big saves or more than 4
  players: MemoryMB 12288, as the spec says.

## The log

- Settled (wiki, DJWoodZ's bot's default path, the real log): the server
  writes `FactoryGame/Saved/Logs/FactoryGame.log` in its install folder,
  and at each start renames the last one `FactoryGame-backup-<date>.log`
  (the wiki). The plan renames it first, the same way, so the app never
  reads the last run's lines as this run's. Output is GameLog.
- Settled (DJWoodZ's bot README): the log's times are UTC unless the server
  is started with `-LocalLogTimes`, `-LogTimeCode` or `-NoLogTimes` (the
  app passes none). Lines before the engine's clock starts have no time
  (real log).
- Settled (real log): `LogInit: Build: ++FactoryGame+rel-main-1.0.0-CL-365306`
  and `LogInit: Net CL: 365306`. The Monitor shows the version as
  "1.0.0 (build 365306)"; friends need the same.
- Settled (real log): the API's certificate fingerprint, `LogServer:
  Warning: SHA256:Re8Y8l7EB+699kPspYduzdfA9PewDxrATl2tZlL2kRQ=`, after
  "make sure the following Fingerprint matches with the Client one", and
  `LogServer: Display: Server API listening on '0.0.0.0:7777'
  (Standalone)`; a server that made a new certificate first says
  "Generated a new Self-Signed certificate for the Server API (old
  certificate invalid or expired)".
- Settled as Unreal Engine's own lines (World.cpp and NetConnection.cpp,
  relied on by three independent tools, testdata/README.md), to confirm
  with a real session: `LogNet: Login request: <url>?Name=<name> userId:
  <id> platform: <platform>`, `LogNet: Join succeeded: <name>`, and
  `LogNet: UNetConnection::Close: [UNetConnection] RemoteAddr: …, UniqueId:
  <id>, …`. Names come from these; the count from the API. CI has no game
  client, so the real check can't show a join (spec 2.6): a real session's
  lines go into testdata before the game is marked ready.
- To confirm: the save line. The Monitor takes `World Serialization (save)`
  (sharkoz's tool, an older version) or a LogGame line saying it saves; the
  real check prints the log's end after a save.
- Settled (Unreal Engine): `LogExit: Preparing to exit.` on the way out,
  and `*** INTERRUPTED *** : SHUTTING DOWN` on Ctrl+C.

## The HTTPS API

- Settled (API docs): every call is a POST to `https://<host>:<game
  port>/api/v1` of `{"function": <name>, "data": {…}}`; an answer is
  `{"data": …}` or `{"errorCode", "errorMessage", "errorData"}`. Statuses:
  200 (data or an error), 201, 202 (still processing: LoadGame,
  CreateNewGame), 204 (no data), 400 (not JSON), 401 (token missing,
  unreadable or expired), 403 (not allowed), 404, 415, 500. Content type
  JSON, UTF-8.
- Settled (real answers): answers are camelCase (`serverGameState`,
  `activeSessionName`, `numConnectedPlayers`, …), whatever the docs'
  tables say; parameter names are read without regard to case (the
  server's own error names the parameter `newGameData` for a request that
  sent `NewGameData`; the Docker image sends `MinimumPrivilegeLevel`,
  `ServerName`, `AdminPassword`, the OpenAPI description camelCase). The
  app sends camelCase.
- Settled (real answers): CreateNewGame's `newGameData` must hold every
  field, empty or not: `sessionName`, `mapName`, `startingLocation`,
  `bSkipOnboarding` (the C++ name, not the docs' `SkipOnboarding`),
  `advancedGameSettings`, `gameModeSettings` (undocumented, needed since
  2026 builds), `customOptionsOnlyForModding`; success is HTTP 202. The
  stand-in is as strict.
- Settled (API docs, headers): privilege levels NotAuthenticated, Client,
  Administrator, InitialAdmin, APIToken; tokens are "<base64 JSON with
  pl>.<hex fingerprint>". PasswordlessLogin works while the server is
  unclaimed (as InitialAdmin) or has no friends' password (as Client);
  otherwise `passwordless_login_not_possible`. PasswordLogin gives
  `wrong_password` for a password that isn't set.
- Settled (Docker): the claim is PasswordlessLogin
  `{"minimumPrivilegeLevel": "InitialAdmin"}`, then ClaimServer
  `{serverName, adminPassword}` with that token, whose answer is a new
  administrator token (`data.authenticationToken`, lower-case), then
  SetClientPassword `{password}` (success: an empty answer). An empty
  "Bearer" header is refused as malformed, so the app sends none without a
  token. HealthCheck answers `{"data": {"health": "healthy", …}}` without
  a token.
- Settled (API docs): error codes `server_claimed`, `server_not_claimed`,
  `password_in_use` (the same password for friends and admin, so Validate
  refuses it), `cannot_reset_admin_password`, `save_game_failed`,
  `save_game_load_failed`, `invalid_token` (401, from the real-server
  notes of the same project).
- Settled (API docs, OpenAPI): SetAdminPassword invalidates every earlier
  token and answers a new one; the app logs in again if none comes.
- Settled (headers, FGDSGameplayTypes.h): ServerGameState's fields, and
  that the auto-load session is "the session the most recent save of
  which will be automatically loaded by the server upon restart".
  SetAutoLoadSessionName changes it without loading anything; LoadGame
  `{saveName, enableAdvancedGameSettings}` and CreateNewGame load now, and
  the API doesn't answer while the server loads (API docs: "HTTPS API
  becomes temporarily unavailable until the blocking work on the server
  is finished").
- Settled (API docs): StartingLocation empty means a random start. The
  names `Grass Fields`, `Rocky Desert`, `Northern Forest`, `Dune Desert`
  are what two independent tools send (CorruptedVariant/server-forge,
  K3lvin4SY/prism). To confirm with the real check; should a version
  refuse the name, the setup asks again with a random start.
- Settled (API docs): third-party tools are advised to use application
  tokens (`server.GenerateAPIToken` in the server's console) rather than
  PasswordLogin. The app logs in with the admin password it set up,
  which the Docker image's flow and the game's Server Manager use too, and
  logs in again when a token stops working. The real check prints the
  shape of RunCommand's answer to `server.GenerateAPIToken`, should the
  app move to an application token later.
- Settled (API docs, wiki): the Lightweight Query API (UDP, no login)
  reports only the server's state and name, not who is on, so the probe
  uses QueryServerState.

## The certificate

- Settled (API docs): the API always uses TLS; without a certificate of the
  owner's (`FactoryGame/Certificates/cert_chain.pem` and `private_key.pem`)
  the server makes a self-signed one, and the game asks players to trust
  it the first time (Join's steps say so).
- Settled (wiki, real log): the server keeps its certificate in
  `ServerSettings.<port>.sav`, and makes a new one when the old one is
  invalid or expired, logging its fingerprint each start.
- Settled (headers, FGTLSTypes.h `FingerprintPublicKey`): the fingerprint is
  of the public key. The app's fingerprint is SHA-256 of the certificate's
  public key, written as the log writes it ("SHA256:" and base64). To
  confirm: that the two are equal (the real check compares the log's with
  the pinned key); the app also accepts the log naming the whole
  certificate's SHA-256.
- The app's rule (api.go): it pins the key in State the first time,
  before sending anything secret, and trusts a new key only when the
  program holding the API's port is the server process it started, or,
  where the system can't tell, when this run's log names that key.

## Saves and server settings

- Settled (wiki, real log): the server keeps its saves in the Windows
  user's `%LocalAppData%\FactoryGame\Saved\SaveGames\server`
  (Linux: `~/.config/Epic/FactoryGame/Saved/SaveGames/server`), whatever
  its install folder, and its own settings in `ServerSettings.<port>.sav`
  in the SaveGames folder beside it (the real log: "Failed to read file
  '/home/steam/.config/Epic/FactoryGame/Saved/SaveGames/ServerSettings.7777.sav'"
  on a first start). That file holds the claim, the server name, the
  passwords, the session to load at start and the certificate (wiki), so
  they are per game port: on a new port the server is unclaimed again,
  and the app claims it again with the same admin password. The app's
  note of the name and friends' password it gave the server belongs to
  the port of the claim, so on another port (new, or one used before) or
  after a new claim it gives both again (setup.go, forgetApplied).
- Settled (wiki): three rotating autosaves, `<session>_autosave_0.sav` to
  `_2`, every 300 seconds by default (`FG.AutosaveInterval`).
- Settled (headers via satisfactory-3d-map, wiki): a save's header starts
  with the header version (13 in 1.0, 14 from 1.1), save version, build,
  the save's name (14 and later), map name, map options, session name,
  played seconds and the save time in .NET ticks; strings are Unreal
  FStrings (length with the closing zero; negative for UTF-16). The app
  reads the session name from it to group saves into worlds.
- Settled (wiki, Save files page): the game itself (Steam or Epic) keeps a
  player's saves in `%LocalAppData%\FactoryGame\Saved\SaveGames\<account
  id>`, beside the server's `server` folder; the Worlds page offers those
  for import (Format.LocalIn, Windows only).
- The app's choice for import: the save is copied into the server's saves
  folder, which is where the server loads saves from; the API's
  UploadSaveGame would do the same but only while the server runs. The
  world is then listed under the session inside the save; starting the
  name it was imported under loads it, and the selection moves to the
  session's name as that start is planned, so that "restart to apply"
  isn't shown for it while the server runs (worlds.go, plan.go,
  setup.go).
- The app's choices: a world is a session; the app's own save is named
  after the session (`<session>.sav`, SaveGame `{saveName}` with the
  session's name); a new world is saved right after it is made, so it is
  on disk at once; removing the game leaves the saves where they are
  (spec 2.8) but renames `ServerSettings.<port>.sav` aside, so that, if the
  game is added again, the app can claim the server again (the admin
  password went with the game's settings).

## Stopping

- Settled (wiki): the HTTPS API's Shutdown is the best way to stop the
  server; on Windows, Ctrl+C in its console also stops it; closing the
  window kills it without cleaning up. The Docker image stops servers with
  SIGINT.
- The stop chain: a call (QueryServerState, SaveGame of the session being
  played, Shutdown), waiting 3 minutes, then Ctrl+C once, waiting 2
  minutes. To confirm: whether Shutdown saves by itself, and whether
  Ctrl+C saves (the real check compares the saves before and after); until
  then the Ctrl+C step's text says what was built since the last autosave
  (every 5 minutes) may be lost, and the stand-in doesn't save on Ctrl+C.

## Friends

- Settled (wiki): friends add the server in the game's Server Manager by
  address and port, then join; no join code. The game asks them to trust
  the server's self-signed certificate (API docs); Join shows the
  fingerprint the server logged so they can check it.
- To confirm: that friends on Steam and on Epic can both join (the API
  docs' player id header names both stores; Info.FriendsNeed says "Steam
  or Epic").
- Settled (wiki): friends outside need the game port (UDP and TCP) and the
  reliable port (TCP) forwarded.
