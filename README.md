# Valheim Easy Server

Run a Valheim dedicated server on a spare Windows PC, such as an old Mac Pro running Windows 10 through Boot Camp, and look after it with a simple app on that computer. Friends who help run it use the same app through an IP KVM or remote desktop. Nobody needs to touch a command line.

![The control panel's home page](docs/screenshots/home.png)

## What it does

- **Start and stop with one button.** Stopping always saves the world first, the same way the official guide does it (Ctrl+C), just without the console window.
- **A tutorial for friends.** The **Tutorial** button, right next to Start server, explains the basics in five short steps with pictures of the real buttons: starting the server, joining from Valheim, stopping, switching worlds and hibernating.
- **How to join, always visible.** The panel shows the crossplay join code, the address and the password, with copy buttons. Crossplay codes work without setting up your router.
- **Several worlds.** Switch worlds with one click, create new ones (with Valheim's difficulty presets and world modifiers, Fire hazard included), rename old ones, and delete worlds. Deleted worlds keep a backup.
- **Add a world by dropping it in.** Drop a world onto the app (a Valheim 1.0 world folder, an older `.fwl` + `.db` pair, or a zip of either) and it goes into the server's worlds folder by itself. The app only asks when the server already has a world with that name: replace it (keeping a backup) or keep both. Replacing the world that's running restarts the server with the new copy. Any world can be downloaded as a zip, and "Import from this PC" copies worlds from the Valheim game installed on the server computer.
- **A mod per world.** Each world's card has a switch for RandyKnapp's Advanced Portals (portals that let metal through). The app downloads it with BepInEx and Jötunn from Thunderstore when that world starts and keeps it up to date; worlds without it run plain Valheim. Players get step-by-step instructions on the Home page.
- **Backups.** Valheim's own automatic backups and the app's backups are listed together, and each one can be restored or downloaded. The app makes a backup before anything is replaced, restored over or deleted.
- **Hibernate or sleep, and wake with Wake-on-LAN.** Saves the world, stops the server and hibernates the computer (or puts it to sleep). Wake it from an IP KVM's Wake-on-LAN, or with the power button, and the server starts again. It can also rest on its own when nobody has played for a while.
- **Stats.** CPU, memory, GPU and network use for the Valheim server and the whole computer, over 15 minutes to 24 hours. A breakdown by number of players estimates what each extra player costs.
- **Players.** See who's online and everyone who has played, with total play time. Make someone an admin or ban them with one click, without hunting for Steam IDs.
- **Looks after itself.** It updates the server through Steam before each start, so friends never get a "version mismatch". It restarts the server after a crash and keeps Windows from falling asleep while people play. Closing the window doesn't stop the server, and the app picks it back up when it starts again.
- **Only on this computer.** The control panel only works on the server computer itself, so there's no password to set up and nothing for strangers to reach. Friends use it through an IP KVM or remote desktop, the same way they'd use the computer.

| Tutorial | Worlds | Adding a world | Stats | Players |
|---|---|---|---|---|
| ![Tutorial](docs/screenshots/tutorial.png) | ![Worlds](docs/screenshots/worlds.png) | ![Adding a world](docs/screenshots/add-world.png) | ![Stats](docs/screenshots/stats.png) | ![Players](docs/screenshots/players.png) |

## Getting it

1. Open the [Actions tab](../../actions), click the newest successful **Build and test** run, and download **ValheimEasyServer-windows** at the bottom. Or use a [release](../../releases) if there is one.
2. Unzip it. You get `ValheimEasyServer.exe` and `READ-ME-FIRST.txt`.

## Setting up the server computer (once)

1. Make a folder such as `C:\Valheim Server` and put `ValheimEasyServer.exe` in it. Everything goes in that folder: the server, worlds, backups and logs. To move or back up the whole setup, copy the folder.
2. Double-click `ValheimEasyServer.exe`. If Windows shows **"Windows protected your PC"**, click **More info → Run anyway**. The app isn't code-signed, so Windows doesn't recognise it yet.
3. The control panel opens in its own window. Click **Install the server**. It downloads the free Valheim dedicated server from Steam with Valve's SteamCMD tool, about 1 GB. You don't need to own Valheim on this PC.
4. Go to **Settings** and set the server name and password. A password is generated for you, and friends need it to join.
5. Already have a world? Drop it on **Add a world** on the Worlds page (see [Worlds](#worlds)). Otherwise the server creates a new one.
6. Click **Start server**. When it says **Online**, friends can join.
7. Work through **Setup on this computer** on the Home page:
   - **Allow it through Windows Firewall.** Windows asks for permission; click Yes. If Windows showed its own firewall pop-up earlier and it was dismissed, this also fixes that.
   - **Turn on hibernation**, which the Hibernate button needs.
   - **Turn on Wake-on-LAN**, so the IP KVM can wake the computer (see [below](#hibernate-sleep-and-waking-up)).
   - **Start this app with Windows**, so everything comes back after a reboot.

The app lives next to the clock as a shield icon. Click the icon to open the panel; right-click it for Start, Stop, Hibernate and Close.

## How friends join the game

The **How friends join** card on the Home page has everything:

- **Crossplay on (recommended, the default).** Friends enter the 6-digit **join code** in Valheim under *Join Game*, then the password. This works for Steam, Xbox, PlayStation 5, Switch 2 and Game Pass players, and needs **no router setup**. The code changes each time the server restarts. Valheim doesn't accept local network addresses for crossplay servers, so people in the same house use the code too.
- **Crossplay off (Steam only).** Friends join by address. Friends outside your home need **UDP ports 2456–2457 forwarded** on your router to the server computer.

Everyone needs the same Valheim version as the server. The app updates the server before each start, so friends just need to update their game.

## Looking after the server through an IP KVM

The app is made to be used on the server computer itself, so anyone who can see its screen and use its keyboard and mouse can run it, including friends connected through an IP KVM or remote desktop.

- The control panel is the **Valheim Easy Server** window. If it was closed, click the shield icon next to the clock or double-click `ValheimEasyServer.exe` again.
- New to the panel? Click **Tutorial** on the Home page, next to **Start server**.
- Files you drop on **Add a world** must be on the server computer. Bring them over on a USB stick, through the KVM's virtual drive if it has one, or by downloading them there from a chat or cloud drive.
- After **Hibernate** (or **Sleep**), wake the computer with the KVM's Wake-on-LAN. The address it needs is in **Settings → Resting and waking up**.
- **Download** saves files on the server computer, because that's the computer the KVM shows. It asks where to save: choose a cloud-drive folder (such as OneDrive) or a USB stick, so you can get the file on your own PC.

## Worlds

- **Play a world:** on the Worlds page, click **Play this world**, or **Switch to this world** while the server runs. The server saves, restarts with that world, and is back in about a minute.
- **New world:** click **New world**, pick a name and optionally a difficulty. The server creates it with a random seed when it starts. For a specific seed, create the world in Valheim on your own PC and add it.
- **Difficulty:** each world's ⋯ menu has **Difficulty**, with Valheim's presets (Casual, Hard, Hammer and so on), the combat, death penalty, resources, raids and portals modifiers, and the switches No build cost, Raids based on each player's progress, Passive enemies, No map and **Fire hazard** (fire spreads to wood everywhere, not just in the Ashlands, so buildings can burn down). Changes apply the next time the server starts that world. Valheim keeps switches in the world, so once a switch that was on is unticked, the server resets that world's difficulty each time it starts (`-resetmodifiers`) and then applies what the dialog shows. Boss progress isn't affected.
- **Add a world from someone's PC:** on that PC, press <kbd>Windows</kbd>+<kbd>R</kbd>, paste `%userprofile%\AppData\LocalLow\IronGate\Valheim\worlds_local` and press Enter. In Valheim 1.0 each world is a **folder** there. Older worlds are two files, `Name.fwl` and `Name.db`, which the server converts to the 1.0 format the first time it loads them. Bring the folder, the two files or a zip of either to the server computer, then drop it anywhere on the app, or use **Choose a world folder** under **Add a world**. The app finds the world, even inside a whole `worlds_local` folder full of Valheim's backups, and puts it in the server's worlds folder. Worlds saved in Steam Cloud must first be moved to local saves in Valheim (*Manage saves*).
- **Same name as a world on the server:** the app asks whether to **replace the server's copy** or **keep both** (older-format worlds only; the new one gets a name like "Midgard 2"). It shows when each copy was last saved. Replacing makes a backup of the server's copy first. If that world is running, the server saves, stops, swaps in the new copy and starts again.
- **First world on a new server:** it becomes the world the server plays.
- **Download a world:** the **Download** button on its card saves it as a zip named after the world and the date, such as `Midgard 2026-10-23.zip`. A Save window asks where to put it: a USB stick, a cloud-drive folder or the Desktop. The zip contains a short note on how to play the world in Valheim on your own PC, and it can be dropped on **Add a world** on another server. If the server is playing that world, the zip has it as of the last save (the server saves every 15 minutes by default).
- **Backups:** each world's ⋯ menu has **Backups**, where you can restore or download any backup. Restoring keeps a copy of the current world first.

## Mods

Each world's card on the Worlds page has a switch for one mod: [Advanced Portals](https://thunderstore.io/c/valheim/p/RandyKnapp/AdvancedPortals/) by RandyKnapp. It adds three portals that let metal through: the Ancient Portal takes copper and tin, the Obsidian Portal iron, and the Black Marble Portal anything. A new world can get it in the **New world** window too.

- Mods belong to a world. When the server starts a world with the switch on, the app downloads the mod from Thunderstore, with the two things it needs (the BepInEx mod loader and the Jötunn library), into the server folder, laid out the way mod managers do it. With **Update the server before it starts** on, it keeps them up to date too. BepInEx's console window is turned off, because the server runs without a window.
- A world with the switch off runs plain Valheim: the mod loader is switched off when it starts. The files stay, so switching back is quick. Portals built with the mod don't work while it's off.
- **Everyone who plays a modded world needs the mod too.** Valheim can't send mods to players when they join, so each player installs it once:
  1. Install a free mod manager: [r2modman](https://thunderstore.io/c/valheim/p/ebkr/r2modman/) or [Gale](https://thunderstore.io/c/valheim/p/Kesomannen/GaleModManager/).
  2. Choose Valheim, search for **Advanced Portals** and install it (**Download** in r2modman). BepInEx and Jötunn come with it.
  3. Start Valheim from the mod manager (**Start modded** in r2modman), then join as usual.

  If joining fails because the versions don't match, the player updates the mod in their mod manager: the server keeps it up to date.

  While a modded world is selected or running, the **How friends join** card on the Home page has a **Which mod do I need?** button. It opens a pop-up with the mod, its version on the server, its download page and these steps. A world card's **Player instructions** link opens the same pop-up, and so does switching a mod on. The mod checks this when someone joins and turns away players who don't have it. Xbox, PlayStation, Switch and Game Pass players can't use mods, so they can't join a modded world.
- Reports differ on whether crossplay join codes work with a modded server. If friends on PC can't join with the code, turn **Crossplay** off in Settings and join by address (friends outside your home then need UDP ports 2456–2457 forwarded on your router).
- The mod's own settings (portal costs and which items each portal allows) are in `BepInEx\config\randyknapp.mods.advancedportals.cfg` in the server folder. They're synced to players, and an admin can also change them in the game.

## Hibernate, sleep and waking up

**Hibernate** (top right on every page, or the tray menu) saves the world, stops the server and hibernates the computer, which then uses no power. If the server was running before, it starts again by itself about 20 seconds after the computer wakes up. The app always asks first.

**Waking it with Wake-on-LAN** (for example from an IP KVM):

1. Connect the computer to the router with an Ethernet cable. Wake-on-LAN doesn't work over Wi-Fi.
2. Open **Settings → Resting and waking up**. It shows the network address to wake (the MAC address, like `3C:07:54:12:34:56`) with a copy button.
3. Click **Turn on Wake-on-LAN** and accept the Windows prompt. This lets the network adapter wake the computer when it receives a Wake-on-LAN packet, and only then, so ordinary network traffic doesn't wake it. Nobody playing is disconnected.
4. Enter the MAC address in your IP KVM's Wake-on-LAN settings.
5. Test it once: click **Hibernate**, wait a minute, then send Wake-on-LAN from the KVM.

Some computers can't be woken over the network from hibernation, because their firmware turns the network port off. It's not known whether a 2013 Mac Pro running Windows can be, so test it. If it doesn't wake, choose **Sleep** in **Settings → Resting and waking up**. Sleep keeps a little power on, wakes in seconds, and Wake-on-LAN can wake most computers from it. The button then says **Sleep**. The power button always works too.

**Settings → Hibernate when nobody is playing** (or **Sleep when nobody is playing**) rests the computer automatically after a set time without players, but never while someone is using its keyboard or mouse (including through an IP KVM).

If the button says hibernation is off, click **Turn on hibernation** and accept the Windows prompt. Most Boot Camp Macs can hibernate in Windows.

## Stats

The **Stats** page shows the Valheim server's own CPU and memory use next to the whole computer's, plus GPU, network and players over time. **Show as table** lists the same numbers as a table. **Usage by number of players** averages the readings for each player count and estimates the cost of each extra player, including what a full server of 10 would need.

The dedicated server runs without graphics, so GPU usage stays low; that's normal. GPU readings need a reasonably recent graphics driver (WDDM 2.0 or later). If the GPU tile says "not available", the driver is too old to report it.

## Troubleshooting

| Problem | What to do |
|---|---|
| Friends see "Incompatible version" | Everyone (and the server) needs the latest Valheim. Make sure **Update the server before it starts** is on and restart the server; friends update their game in Steam. |
| Friends can't connect by address | Use the crossplay join code, or forward UDP 2456–2457 to the server computer, and click **Allow through Windows Firewall**. |
| "Valheim rejected the server password" | The password can't be part of the server name, the world name or the world's seed. Pick another in Settings. |
| The server stopped unexpectedly | The Home page offers **See what happened**, and the **Log** page shows the server's own output. With **Restart the server if it crashes** on, it restarts itself. It gives up after 3 crashes in 15 minutes. |
| A world doesn't load | Restore a backup from the world's **Backups**. |
| The panel window doesn't open | Click the shield icon next to the clock, or double-click `ValheimEasyServer.exe` again. |
| A dropped world isn't found | Drop the world's own folder (the one with `_main` files inside), its `.fwl` and `.db` files, or a zip of either. A zip inside a zip doesn't work. |
| Windows Security blocks or removes the app | Antivirus tools sometimes raise a false alarm for new programs that aren't signed. Allow it under *Windows Security → Virus & threat protection → Protection history*. |
| Shutting down or restarting Windows | Stop the server first (or use Hibernate), so the world is saved. Windows doesn't give the server time to save when it shuts down. |
| Wake-on-LAN doesn't wake the computer | Check that **Settings → Resting and waking up** says the network adapter may wake the computer, that the KVM uses the MAC address shown there, and that the computer is on a cable, not Wi-Fi. If it still won't wake from hibernation, choose **Sleep**, and restart the computer once. |
| Mods (BepInEx) | Install them into the server folder (**Settings → Folders → Open**); the app starts the server the same way. Use **Repair server files** to undo. |

The app's own log is in `logs\app.log` inside its folder, and the server's logs are next to it.

## How it works

`ValheimEasyServer.exe` is a single program with no installer and nothing else to download. It:

- runs `valheim_server.exe` hidden in the background with the settings from the panel. To stop it, it sends Ctrl+C to the server's hidden console, which is how Valheim expects to be stopped, so the world is saved;
- reads the server's log to see who joins, leaves and dies, the join code, saves and errors;
- installs and updates the server with Valve's SteamCMD (`app_update 896660`);
- understands both Valheim world formats: 1.0 world folders (`_main.N.fwl2`, `.db2`, `.chunk` files) and the older `.fwl` + `.db` pairs;
- serves the control panel from inside the app to this computer only (127.0.0.1) and opens it in a Microsoft Edge app window. It turns away requests that other websites open in a browser could make: it checks the address the browser used, the browser's own "which site is asking" header, and an anti-forgery header on every change;
- reads CPU, GPU, memory and network usage from Windows' performance counters, the same source Task Manager uses;
- hibernates with `shutdown /h` or sleeps through Windows' own sleep call, and sets up Wake-on-LAN with `powercfg /deviceenablewake` and the network adapter's power settings (magic packet on, pattern wake off).

Its folder holds `settings.json`, `server\` (the Valheim server), `saves\worlds_local\` (worlds), `saves\adminlist.txt` and the other player lists, `backups\`, `logs\` and `steamcmd\`.

## Building from source

You need [Go](https://go.dev/dl/) 1.24 or newer.

```sh
go test ./...
GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui" -o ValheimEasyServer.exe .
```

For the program icon, run `go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64` before building. GitHub Actions does all of this on every push: it runs the tests on Linux and Windows, including stopping a server with Ctrl+C on real Windows, and publishes the `.exe`.

To try the app without the real server, use the stand-in server in `cmd/fakevalheim`. It writes Valheim-style logs and world folders and simulates players:

```sh
go build -o fakevalheim ./cmd/fakevalheim
FAKE_VALHEIM_SIM=1 go run . --data ./dev-data --dev-fake-server ./fakevalheim
```
