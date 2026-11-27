# Terraria: facts the adapter relies on

Each fact says where it comes from and when it was checked. "Real run" means
the official 1.4.6.1 server from terraria.org, run headless on Linux x86_64
on 2026-11-26 (its Linux build: a Mono and FNA build like the Mac's, the
same size but not the same bytes; Windows has its own .NET and XNA build),
with a FIFO as its console. To make it
print joins, leaves and refusals, a small throwaway Terraria protocol client
(not part of this repo) connected to it and sent the client's first messages
(connect request, player info, world request, spawn). The logs it printed
are in `testdata/real/` (see the end of this file).

Every "verify" mark of spec 5.3 is settled in section 9.

## 1. Version and download

- **The list of servers** (2026-11-26): `GET https://terraria.org/api/get/dedicated-servers-names`
  answered `["terraria-server-1461.zip","terraria-server-1461.zip"]` (the same
  name twice). Recorded in `testdata/dedicated-servers-names.json`.
- **The download**: `GET https://terraria.org/api/download/pc-dedicated-server/terraria-server-1461.zip`
  answered 200, `content-type: application/zip`, `content-length: 57392309`,
  `last-modified: Tue, 24 Nov 2026 18:43:27 GMT`. SHA-256 of the file
  downloaded on 2026-11-26: `0fb586dfbf2bcee448612c90768648d89532eed6ab345aadbbb0829f9d447476`.
  terraria.org publishes no checksum, so the adapter downloads over HTTPS
  without one.
- **Re-published under the same name.** The entries inside the zip are dated
  2026-11-24 (the 1.4.6.1 changelog says "Fixed Linux Server not starting
  due to missing libraries"), so Re-Logic replaced the zip without a new
  name. The adapter compares versions by name, so such a fix arrives only
  with Repair, which always downloads again.
- **Version from the name**: one digit per part, "1461" = 1.4.6.1, "145" =
  1.4.5, "1449" = 1.4.4.9. The banner of the real server says
  `Terraria Server v1.4.6.1`; its changelog's headings use the same versions.
- **Protocol release** of 1.4.6.1 is 329 (the real server accepted only
  "Terraria329" from the test client; the world file's header also starts
  with 329). Not used by the adapter; the real-server check connects as
  release 0 and only waits for an answer, so a new release can't break it.

## 2. The zip

- One top folder named after the version, `1461/`, holding `Linux/`, `Mac/`
  and `Windows/` (87 entries, 180 MB unpacked). The full list, with sizes,
  is `testdata/terraria-server-1461.entries`.
- Made on Windows: every entry's "created by" system is 0 (MS-DOS/FAT), so
  no Unix permissions are stored. The adapter marks `TerrariaServer.bin.osx`,
  `TerrariaServer.bin.x86_64` and the scripts (`TerrariaServer`, `Terraria`,
  `open-folder`) runnable itself.
- `Windows/`: `TerrariaServer.exe`, `serverconfig.txt` (an example, see 4),
  `start-server.bat` (runs `TerrariaServer.exe -config serverconfig.txt` in
  a loop), `GNS_Wrapper.dll`, `steamnetworkingsockets.dll`, `ReLogic.Native.dll`,
  `OpenNAT.dll`, `changelog.txt`. No XNA files.
- `Mac/Terraria Server.app/Contents/MacOS/`: `TerrariaServer.bin.osx` (the
  program), the scripts `TerrariaServer` and `Terraria`, and `osx/` with
  `libGNS_Wrapper.dylib`, `libSDL3.0.dylib`, `libFNA3D.0.dylib`,
  `libFAudio.0.dylib`. `Contents/Resources/` holds `TerrariaServer.exe` (the
  game code, FNA build), Mono's assemblies and `FNA.dll.config`.
- `Linux/`: `TerrariaServer.bin.x86_64`, the `TerrariaServer` script,
  `lib64/` (libGNS_Wrapper, libSDL3, libFNA3D, libFAudio, libssl, libcrypto,
  libprotobuf) and the assemblies beside the program.
- The adapter keeps only this system's folder, as `server/Windows`,
  `server/Mac` or `server/Linux` (see 10 for why not `server/<version>/`).

## 3. What each system needs

### Windows

- `TerrariaServer.exe` is a PE32 i386 .NET assembly, **console** subsystem
  (3), CorFlags `0x3` (IL only, 32-bit required), targeting
  `.NETFramework,Version=v4.0` (runtime v4.0.30319). It references
  `Microsoft.Xna.Framework`, `.Audio`, `.Content`, `.Game`, `.Graphics`,
  `.Input` and `.Xact`, which the zip doesn't include. (Read from the file's
  PE and CLI headers and its metadata strings, 2026-11-26.) .NET Framework 4
  comes with Windows 10 and 11.
- Being a console program, a Ctrl+C sent to its own hidden console reaches
  it (spec 2.7). What it then does on Windows is for the Windows real-server
  cell to show (see 11); on Linux it ends at once without saving (6).
- **Microsoft XNA Framework 4.0 Redistributable**:
  `https://download.microsoft.com/download/A/C/2/AC2C903B-E6E8-42C2-9FD7-BEBAC362A930/xnafx40_redist.msi`,
  7,054,848 bytes, SHA-256 `47260420773a20443fb1e38a89b7f39c5237a80a842de598e6c8f7f90a3bbd6d`
  (downloaded 2026-11-26). Read with msitools from the package itself:
  ProductName "Microsoft XNA Framework Redistributable 4.0", ProductVersion
  4.0.20823.0, `ALLUSERS=1`, a launch condition `Privileged` (it needs
  administrator rights: one admin prompt), Template `Intel;1033` (a 32-bit
  package), and in its Registry table, root 2 (HKLM), key
  `Software\Microsoft\XNA\Framework\v4.0`, values `Installed` = `#1` (DWORD
  1), `ProductVersion` = `4.0.20823.0`, `NativeLibraryPath`. The components
  that write them have attributes 4 (no 64-bit flag), so on 64-bit Windows
  the key is in the 32-bit view: `HKLM\SOFTWARE\WOW6432Node\Microsoft\XNA\Framework\v4.0`.
  The adapter reads `Installed` there through `registry.WOW64_32KEY`. It
  installs with `msiexec /i xnafx40_redist.msi /quiet /norestart`.
- **Repair puts XNA back even when the key is there.** A start is refused
  while the key is missing, so the XNA exit hint ("Click Repair") can only
  appear when Windows lists XNA but it doesn't work. The usual install
  would see the key and do nothing, and a quiet `msiexec /i` of a package
  that is already installed changes nothing either. So a repair with the
  key present runs `msiexec /i xnafx40_redist.msi REINSTALL=ALL
  REINSTALLMODE=vamus /quiet /norestart` (Windows Installer's documented
  reinstall properties: every feature; run from this package and cache it
  again, every file whatever its version, the HKLM and HKCU registry
  entries, the shortcuts), then checks the key again. Without the key a
  repair is the usual install, since REINSTALL only reinstalls what is
  already there. A repair therefore asks for administrator rights once
  more on Windows, whatever it was clicked for. Seen only in the docs so
  far; the Windows real-server cell checks it (11).
- The Terraria wiki's Server page lists ".NET Framework 4.0" and "XNA
  Framework" as the Windows server's requirements
  (https://terraria.wiki.gg/wiki/Server, read 2026-11-26).

### macOS

- `TerrariaServer.bin.osx` is a Mach-O **x86_64-only** executable,
  `LC_VERSION_MIN_MACOSX` 10.9, with **no code signature**, and one rpath,
  `@executable_path/osx`. It is MonoKickstart (Mono embedded).
- The dylibs in `osx/` are universal (x86_64 and arm64). Their x86_64 slices
  need macOS 10.9 (FNA3D, FAudio), 10.13 (SDL3) and **11.0
  (libGNS_Wrapper)**, so the Mac server needs macOS 11 or later: fine on the
  owner's macOS 12. `libGNS_Wrapper.dylib` is signed, without
  the hardened runtime (CodeDirectory flags 0).
- An x86_64-only program needs an Intel Mac: Info says `darwin/amd64` yes,
  `darwin/arm64` no ("Needs an Intel Mac"). Unsigned x86_64 programs run on
  Intel Macs; a program the app downloads itself carries no quarantine flag,
  so Gatekeeper doesn't stop it.
- **The bundle's scripts and the working-directory quirk.** The bundle's
  `TerrariaServer` script is:

  ```sh
  cd "`dirname "$0"`"
  ...
  ./fixDylibs.sh                      # not in the zip
  export DYLD_LIBRARY_PATH=$DYLD_LIBRARY_PATH:./osx/
  ./TerrariaServer.bin.osx $@
  ```

  and the bundle's entry point `Terraria` opens Terminal on it. So the
  official way to run it changes the working directory to
  `Contents/MacOS` inside the bundle, and finds the libraries through a
  path relative to it. The wiki's instructions do the same (cd into
  `Terraria\ Server.app/Contents/MacOS`, then `./TerrariaServer -config
  ../../myConfig.txt`). Anything relative (the config, the ban list, which
  defaults to "banlist.txt" in the working directory per the example config,
  the worlds) would then land inside the bundle, which the next update
  replaces. The adapter therefore starts `TerrariaServer.bin.osx` itself
  with Dir = `Contents/MacOS`, `DYLD_LIBRARY_PATH=<absolute>/Contents/MacOS/osx`
  (Mono loads the libraries by bare name: `FNA.dll.config` maps `SDL3` to
  `libSDL3.0.dylib` and so on, and the program loads `GNS_Wrapper` the same
  way), and every path in the config absolute, `banlist=` included. A
  program started directly with DYLD_LIBRARY_PATH keeps it (it isn't a
  system binary, and has no hardened runtime). Whether it all starts on a
  real Mac is for the Mac real-server cell (11).
- `fixDylibs.sh`, which the script runs first, isn't in the zip, so the
  script would print an error there and carry on; skipping the script
  loses nothing.

### Linux (tests only)

- `TerrariaServer.bin.x86_64` is an x86_64 ELF; its libraries are in
  `lib64/`. Started directly from its folder, with no environment of its
  own, it ran (real runs). The script also sets `MONO_IOMAP=all`, which it
  didn't need.

## 4. serverconfig.txt and the command line

- Keys the 1.4.6.1 server reads (the `key=` strings in `TerrariaServer.exe`):
  `world`, `port`, `worldrollbackstokeep`, `maxplayers`, `priority`,
  `password`, `motd`, `lang`, `language`, `worldpath`, `worldname`, `seed`,
  `banlist`, `difficulty`, `autocreate`, `secure`, `upnp`, `gns`,
  `slowliquids`, `npcstream`, plus `journeypermission_*` and `seed_*`.
  The example config in the zip documents the same (and `difficulty`
  0 classic, 1 expert, 2 master, 3 journey; `autocreate` 1 small, 2 medium,
  3 large; `maxplayers` 1 to 255; `#upnp=1` "Automatically forward ports
  with uPNP").
- **No key or switch for the world's evil** (corruption or crimson). Only the
  interactive "new world" menu asks (`CLI.ChooseEvil`); `autocreate` picks
  one at random (the real server printed `Evil: -1`). So the adapter has no
  "evil" new-world setting (the spec listed one).
- A value is everything after the first `=`: the real server read back
  `password=a=b#c d` as `Password: a=b#c d` (its `password` command). So the
  adapter only refuses control characters (a line break would end the value)
  and leading or trailing spaces in a password.
- **Seeds are cut to 40 characters** (a 66-character seed was printed and
  stored as its first 40). World names are not cut: a 40-character
  `worldname` was stored whole in the world file. The adapter caps seeds at
  40 and world names at 40.
- `language=` changes every console line, the banner included
  (`language=de-DE` gave `Terraria-Server v1.4.6.1`, `Zuhören auf Port …`,
  `Server gestartet`). Without the key, under `LANG=de_DE.UTF-8` on Linux,
  it stayed English. The assembly holds the texts in 12 languages. The
  adapter always writes `language=en-US`, so the Monitor can read it.
- `upnp=0` turns off the server's own port forwarding (the app maps the
  router itself); the example config's commented-out line, `#upnp=1`,
  shows forwarding is on unless told otherwise.
- Command-line switches in the assembly: `-config`, `-port`, `-players`,
  `-maxplayers`, `-pass`, `-password`, `-world`, `-autocreate`, `-worldname`,
  `-secure`, `-noupnp`, `-nogns`, `-lobby`, `-ip`, `-forcepriority`,
  `-seed`, `-lang`, `-language`, `-motd`, `-banlist`, `-worldrollbackstokeep`,
  `-disableannouncementbox`, `-announcementboxrange`, and others. The
  adapter uses only `-config <absolute path>`.
- The example config sets `priority=1` (high: "0:Realtime, 1:High, …
  3:Normal"). The adapter writes no `priority` line, so the server isn't
  asked to raise its priority above the other games'.

## 5. Network

- The real server listened on **TCP and UDP** of its port (both bound on
  0.0.0.0, from /proc/net/tcp and /proc/net/udp; and in the real-server
  check, `platform.PortInUse` said both "tcp" and "udp" were held by the
  server's own PID). The UDP side is most likely crossplay's
  GameNetworkingSockets (an inference from the `gns=` key, the `-nogns`
  switch, the GNS libraries and 1.4.6's "Added Crossplay!"; not checked by
  turning it off). Either way the game role's Protos are `["tcp","udp"]`.
- With the **TCP port** taken by another program, the server printed
  `Listening on port 17777`, `Type 'help' for a list of commands.`, the
  `: ` prompt, and **quit with code 0 without a word** (no "Server started").
  The same config with the port free and the same console reached "Server
  started". The assembly has `Tried to run two servers on the same PC` and
  `Failed to start server`, which may be what Windows prints; the exit hint
  knows both.
- With the **UDP port** taken, the server still started and ran.
- Default port 7777 (the example config; the wiki: "The server runs on TCP
  port 7777 by default").

## 6. Console: what it prints and does (real runs)

- The first line is `Error Logging Enabled.` with four byte order marks
  (U+FEFF, the bytes `EF BB BF` four times) before it. Then
  `Terraria Server v1.4.6.1`.
- While it waits for a command it writes the prompt `: ` with no line end,
  so the next line starts with `: ` (`: Server started`, `: Backing up world
  file`, `: Saving before exit...`). The Monitor strips both.
- **Ready**: `Server started` (always after `Listening on port N` and the
  help hint).
- Making a world (`autocreate`) prints `Creating world - Seed: egs-test,
  Width: 4200, Height: 1200, Evil: -1, Difficulty: 0` and then about 30,000
  progress lines (`Resetting game objects 1%`, `0.4% - Adding sand - 0.0%`,
  …), and saves without printing that it does. A small world took well under a minute
  on the test machine. Loading prints `Resetting game objects N%`,
  `Loading world data: N%`, `Settling liquids N%`. The Monitor shows the
  first line of each step and hides the rest.
- **Join**: `<ip>:<port> is connecting...`, then `<name> has joined.` once the
  player spawns (not at the player-info message). Names may hold spaces
  (`Big Bob has joined.`).
- **Leave**: `<name> has left.` A client that leaves before joining gives
  `<ip>:<port> lost connection...`.
- **Chat**: `<Alex> hello there`. A player typing "Alex has joined." gives
  `<Big Bob> Alex has joined.`, so lines starting with `<` are never read as
  joins or leaves.
- **Deaths: nothing.** A player's death message (PlayerDeathV2, message
  118) reached the server, which relayed it to another connected client,
  and printed no line. So `Deaths` is false.
- **Refusals** (`<ip>:<port> was booted: <reason>`): `Incorrect password`
  (no full stop), `You are not using the same version as this server.`,
  `This server is full right now, please try again later.` (with no
  "is connecting" line before it), `Empty name.`, `Name is too long.`
- **Saving** (`save`, the 10-minute autosave, the last player leaving):
  `Saving world data: N%` lines (not always up to 100%), `Validating world
  save: N%` lines, `Backing up world file`. The world file is written before
  it is validated (its time is before the `.bak`'s), so the Monitor counts
  the save at the first "Validating" line, or at "Backing up" when no
  validation line came.
- **Autosave every 10 minutes**: with a player on, the save lines came
  exactly 10 minutes after the start (19:35:02 → 19:45:03); changelog 1.4:
  "Multiplayer dedicated servers now save every 10 minutes, rather than
  every morning".
- **Save when the last player leaves**: after `Watcher has left.` (the last
  of two) the save lines followed; after `Alex has left.` with Watcher still
  on, none. Changelog 1.4.5: "Servers now save the world when the last
  player leaves."
- **`exit`**: prints `Saving before exit...`, saves silently (the world and
  its backups were rewritten) and ends with **code 0**. With a player on it
  prints no `has left.` line for them.
- **Ctrl+C** (SIGINT, Linux): ended at once with code 130, **no save** (the
  world files kept their times), no output.
- `playing`: `Alex (127.0.0.1:56582)` per player, then `1 player connected.`
  / `No players connected.`; `version`: the banner; `port`: `Port: 17777`;
  `help`: the command list (help, playing, clear, exit, exit-nosave, save,
  kick, ban, password, version, time, port, maxplayers, say, motd, dawn,
  noon, dusk, midnight, settle, seed).
- **A damaged world**: `Load failed!  No backup found.`, a blank line, a
  `System.IO.EndOfStreamException` with its stack, and code 0.
- **A missing world without autocreate**: `Load failed!  No backup found.`
  and a `System.IO.FileNotFoundException`, code 0 (no interactive menu, so
  no hang). The adapter's Plan refuses this case itself with a clearer
  message.
- The English texts are in the assembly's own language tables:
  `"19": "{0} has joined."`, `"20": "{0} has left."`, `"49": "Saving world data:"`,
  `"50": "Backing up world file"`, `"51": "Loading world data:"`,
  `"73": "Validating world save:"`, `"ServerSavingOnExit": "Saving before exit..."`,
  `"ServerStarted": "Server started"`, `"ClientWasBooted": "{0} was booted: {1}"`,
  `"ServerIsFull": "…"`, `"1": "Incorrect password"`, `"4": "You are not using the same version as this server."`.

## 7. Worlds

- A world is `<name>.wld` in the folder `world=` names, plus `<name>.wld.bak`
  (the previous save) and, by default, `<name>.wld.bak2` (seen after a few
  saves in the real runs). Changelog 1.4.0.5: rolling backups, "one
  additional backup save" by default, "up to a maximum of 9"
  (`worldrollbackstokeep`). The adapter counts `.wld`, `.wld.bak` and
  `.wld.bak2` to `.wld.bak9` as one world's files.
- The world's own name is inside the file (the 40-character name was found
  there after the length byte); the file name doesn't change it. So Rename
  is refused (spec 5.3); an import under a new file name works, and keeps
  the name inside.
- The owner's own worlds: Windows `Documents\My Games\Terraria\Worlds`
  (community posts; the adapter asks Windows where Documents is, as OneDrive
  can move it), macOS `~/Library/Application Support/Terraria/Worlds` and
  Linux `~/.local/share/Terraria/Worlds` (the wiki's Server page), and
  Steam Cloud worlds under `<Steam>/userdata/<account>/105600/remote/worlds`
  (Steam community posts: https://steamcommunity.com/app/105600/discussions/0/1727575977530036119
  and https://steamcommunity.com/discussions/forum/1/3114770913746985336;
  105600 is Terraria's Steam app id).

## 8. Friends joining

- 1.4.6's PC multiplayer menu adds servers by address and port: its texts
  include "Add Server" and "Invite others to join this crossplay session by
  adding a server at address {0} and port {1}", and its own invite text is
  "The server address is {0}. The server port is {1}. The password is {2}."
  (the assembly's language tables). The older "Join via IP" text is still
  there. The Join steps follow the game's own words.
- Friends need exactly the server's version (the refusal above), so the
  steps name it and "update on start" is on by default.

## 9. The spec's "verify" marks, settled

| Spec 5.3 / 2.x says | Settled |
|---|---|
| deaths no (verify whether the console prints deaths) | It doesn't (6). Deaths stays false. |
| UDP too if the real check finds the server listening on it | It listens on TCP and UDP of its port (5). Protos `["tcp","udp"]`. |
| XNA registry key `HKLM\SOFTWARE\WOW6432Node\Microsoft\XNA\Framework\v4.0` `Installed`=1 (verify the key) | Right: from the MSI's own Registry and Component tables (3). |
| version from the banner `Terraria Server v1.4.6.1` (verify its form) | Exactly that line (6). |
| a real log still has to show the exact join and leave lines with names (2.6) | `Alex has joined.`, `Big Bob has left.` etc. in `testdata/real/` (6). |
| Terraria on macOS: launched directly with the script's DYLD_LIBRARY_PATH (2.4, verify per game) | The script's content and the program's rpath and library loading (3). A real Mac start is the Mac cell's (11). |
| SaveLine `save` (2.11) | `save` saves without stopping and prints the save lines (6). |
| saves every 10 minutes and when the last player leaves (2.11, Say text) | Seen in real runs and in the changelog (6). |
| Ctrl+C after a takeover (2.11) | Ends at once, no save (6); the world from the last save loads again (the local real-server check). |

## 10. Where the adapter departs from the spec, and why

- **Install layout**: `server/Windows/TerrariaServer.exe` (and
  `server/Mac/…`, `server/Linux/…`), not `server/<version>/Windows/…`. A
  version folder would give the program a new path at every update, so the
  firewall rule made for the old path (Windows' rules are per program; the
  rule names carry a hash of the path) would no longer match, and a start
  right after an update, perhaps from Discord with nobody at the computer,
  would wait for an admin prompt before friends could connect. The update
  still unpacks the new server completely before it moves the old one
  aside, and puts the old one back if the swap fails.
- **No "evil" new-world setting**: the server can't take one (4).
- **`banlist=` in the config**, absolute, in `server/` (outside the folder an
  update replaces); the spec's list didn't have it (3, macOS).
- **A `server_full` event** (a game's own warning kind, like
  `wrong_password`) for "This server is full…".
- **Join.Address is the bare IP**: Terraria asks for the port separately.

## 11. What only Windows and macOS CI can confirm

The realserver-tagged `TestRealServer` (run by the matrix from
`realserver.json`) installs, makes a small world, checks the server answers
on its TCP port and logs `PortInUse` for TCP and UDP ("FACTS:" lines), saves
with the console, stops with `exit` (the world file newer), starts again,
takes over and stops with Ctrl+C, and loads the world again. On Windows it
then takes XNA's main file out of the global assembly cache and repairs XNA
(the "XNA repair" step). It passed on Linux on 2026-11-26 against the real
1.4.6.1 server (Linux build). Still to see on the runners:

- Windows: XNA's silent install through `install.EnsurePrereq`, the server
  starting with it, the Ctrl+C step ending it (and whether it saves), the
  console lines being the same as on Linux, `PortInUse` for TCP and UDP,
  and the repair's reinstall over an installed XNA (msiexec's exit code,
  the key still there, the removed file back; "FACTS:" lines).
- macOS (Intel): the program starting with Dir and DYLD_LIBRARY_PATH as
  above, the GNS library loading, and `PortInUse` for TCP and UDP.

## 12. The logs in testdata/real

All from the real runs on 2026-11-26 (ports 17777 to 17779; the addresses
are the test client's, on 127.0.0.1), copied byte for byte apart from the
cuts noted:

- `new-world.log`: the start of a new small world: the first 30 lines, a few
  from the middle of world generation, and the last 39 (the ~30,000 lines
  between are left out), then the first `save`.
- `load-world.log`: a start on that world, ended by Ctrl+C (which printed
  nothing).
- `console-and-version-mismatch.log`: `help`, `version`, `playing`, `port`,
  then clients of the wrong release being turned away.
- `join-leave-save.log`: refusals for names, then a join, a leave and the
  save that follows.
- `chat-two-players-exit.log`: chat that looks like a join, two players, the
  save after the last one leaves, and `exit`.
- `password-full-exit.log`: a server with a password and one slot: a wrong
  password, a join, the server full, the save after leaving, then `exit`
  with a player on.
- `autosave-10min.log`: the save 10 minutes after the start with a player on.
- `port-in-use.log`: a start with the TCP port taken; it ends with the
  prompt `: ` and no line end, as the server left it.
- `damaged-world.log`: a start on a cut-short world file.
