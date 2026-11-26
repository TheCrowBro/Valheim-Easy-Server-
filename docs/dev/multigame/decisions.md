# Multi-game: the owner's decisions

These are requirements. They were collected with the owner on 2026-11-21 (design mockups: the "Several Games Design" page) and confirmed again on 2026-11-26. Where this file and any spec disagree, this file wins.

## Games (all built in one round)

| Game | Server | On macOS |
|---|---|---|
| Valheim | existing; must keep working exactly (install folder, firewall rule, mods, join code, worlds, settings, takeover of a running server across an update) | offered |
| Minecraft: Java Edition | vanilla Mojang server.jar; Java only (no Bedrock/Geyser); mods in a later round; allow list on once reachable from the internet (friends added on the Players tab, or with one click when someone is turned away) | offered |
| Terraria | vanilla | offered |
| Necesse | dedicated server | offered |
| Vintage Story | plain (mods in a later round) | "Needs Windows" |
| Project Zomboid | dedicated server | "Needs Windows" |
| Enshrouded | Windows-only server; built and tested now (CI on Windows) | "Needs Windows" |
| Core Keeper | Windows/Linux server | "Needs Windows" |
| Palworld | Windows/Linux server | "Needs Windows" |
| Satisfactory | Windows/Linux server | "Needs Windows" |
| Windrose | Windows-only server | "Needs Windows" |

Games whose server can't run on this computer are listed in "Add a game" but greyed out ("Needs Windows").

## Name
- New visible name: "Easy Game Server". Only what people see changes. Release asset names, the repo, the data folder (including the Downloads/Desktop subfolder "Valheim Easy Server"), the update channel, the single-instance lock, launch at login and firewall/JetKVM names that installed copies depend on keep working.

## Layout (option A, with stars)
- Sidebar: Overview at the top; the games in two collapsible ("twirl-down") lists:
  - **Active (n)**: starred games, plus any game that is running (a running game without a star shows here, without a filled star, only while it runs).
  - **Inactive (n)**: the rest.
  - Each list opens and closes; the state is remembered. Active is open and Inactive closed by default.
  - Each game row: a letter tile in the game's colour, the name, a live player count while it runs, and a star button.
- "+ Add a game".
- APP group at the bottom: Discord, Sleep and waking, App settings. Footer "Server computer: NAME".
- **Stars** mean "what we're playing now". Several games can be starred at once. The star is saved per game.
- **Overview**: starred games first (cards with status, world, join info, players, Start/Stop/Copy), then running unstarred games, then a collapsible "Inactive games (n)" list with Start buttons; a memory bar showing what each game uses.
- **A game's page**: tabs Home, Worlds, Players, Stats, Mods (where the game has mods), Settings, Log. Home has a "How friends join" card specific to the game.
- Top bar: "N games online · M playing" and Sleep.
- Icons: simple letter tiles in a per-game colour (no official logos).

## Servers
- One server per game (each game: many worlds, one running at a time). Built so a second server of the same game could be added later without a redesign.
- Several games run at once. Ports are picked automatically with no clashes and shown on the game's page. Ports the owner chose are never moved silently.
- "Add a game": shows whether it runs on this computer, the download size, the memory needed and what friends need; installs the server and any runtime it needs (Java, .NET, ...); asks for server name, password and world (new, or imported from the owner's own game). Each game then has its own Start and Stop. A game can be removed; its worlds are kept as a backup.
- Memory check before starting a game from the panel: warn and let the owner choose (stop another game and start / start anyway / cancel).
- Memory short when a game is started from Discord: don't start; reply that there isn't enough memory and list what's running.
- Auto-stop: a game with nobody on for 1 hour stops itself. On by default for ALL games, starred or not; a per-game setting can change or turn it off.
- Windrose, only if the real-server check finds no usable player join/leave signal: after 4 hours running, post in Windrose's Discord channel asking whether it should stay on. If nobody sends a message in that channel within another 2 hours, announce that it is being turned off and stop it. Any message restarts the 4-hour clock.

## Discord and waking
- A Discord channel per game for its online/stopped/records posts. A game without its own channel uses the main channel.
- The JetKVM bot has one slash command per game (/valheim, /minecraft, /vintagestory, ...). Only STARRED games get their command; starring or unstarring updates the bot's commands. A game's command works only in that game's channel.
- Waking the computer from Discord for one game starts only that game.
- The KVM's own Wake-on-LAN button (no Discord command) starts the games that were running before the computer slept; if none were, it just wakes.
- Players, records and the daily "most deaths" post are per game. The Overview shows who's online in every game.

## Sleep
- The computer sleeps only when nobody is playing any game for the idle time and nothing is downloading or backing up. Sleep only; hibernation stays off.

## Friends outside the home
- The app opens router ports itself (UPnP IGD / NAT-PMP / PCP) and checks reachability where it can. When that fails it shows manual router steps.
- NO tunnel of any kind (no playit.gg).
- Valheim with crossplay OFF also gets automatic router ports. With crossplay on (join code), nothing changes.

## Releases and process
- Releases in steps:
  - R1 "bridge": safety nets only, no visible change.
  - R2 "core": the invisible rebuild with Valheim only and the old panel. The owner runs it for 3+ days of normal play, including an auto-update while Valheim runs, sleep and wake, /valheim and a takeover.
  - R3 "games": the new panel and the games, after the owner's short Mac Pro checklist (Valheim, Minecraft, Terraria, Necesse).
- Work in progress goes to the non-releasing branch "multigame" (owner-approved). Only pushes to the default branch ccr-949cf1ef-eeo1jr release; releases happen only by merging into it at the R1/R2/R3 gates.
- Satisfactory (~15.5 GB) and Windrose (~35 GB) get real-server checks on demand rather than on every build.

## Answers to the round-two spec's questions (2026-11-26)
- Windrose's "still playing?" check when the bot can't read its channel (or there's no bot): same rule; it still posts the question, a "Keep it on" button in the panel counts as a reply, and with no reply it turns off 2 hours later.
- Any other game without a usable join/leave signal (Core Keeper most likely) uses the same rule as Windrose.
- Project Zomboid: Build 42 (the current default branch on Steam).
- R2 reaches the owner as a normal auto-update (that is how the update and takeover get tested for real); R3 waits for the owner's OK.
- The R3 Mac checklist test copy runs on the owner's REAL data folder (it takes over as the final update would; backups are kept).
- Windows tray menu: Open the control panel, Stop all games, Quit. No per-game entries.
