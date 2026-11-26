# Review of the round-two spec: findings and verdicts

Four adversarial reviews (requirements, code, games, simplicity) of [spec.md](spec.md), checked on 2026-11-26 against the code at bed004b, [decisions.md](decisions.md), build.yml, and the real server files and publishers' documentation (Terraria's 1.4.6.1 zip, Minecraft 26.3's jar and manifest, Steam's app info, the Vintage Story and .NET metadata were re-read for this check). Verdicts: **accept** (changed as proposed, or with a small adjustment noted), **partly** (part of it, or a different fix), **reject**. The spec's "Changes after review" section summarises the result.

Totals: 83 findings; 71 accepted, 11 partly, 1 rejected.

| ID | Lens | Severity | Problem | Verdict | Note |
|---|---|---|---|---|---|
| F01 | requirements | major | Unstarring everything still leaves /valheim; a starred game with no channel works everywhere | accept | 6.4: R3 always writes `games` (`[]` = no commands); channel-less games get no command; WK tests |
| F02 | requirements | major | `auto` allow list only fires on the app's own mapping, so a hand-forwarded Minecraft stays open | accept | 5.2: written into server.properties before start unless CGNAT is found; a public-address login turns it on; the proposed fix's conditions, read literally, meant "always on", so it was simplified |
| F03 | requirements | major | Waker and firewall work moved into the app job slot changes what the owner sees | accept | 4.2: app slot holds only Sleep; waker keeps `wk.busy`, firewall stays synchronous; Remove is the game's own job; R2 gate test |
| F04 | requirements | major | Auto-stop's stop job resets the idle clock, so the PC sleeps after 2 h, and resume depends on which timer won | partly | 4.5/4.7: auto-stop runs first in a tick and its stop and exit don't reset the idle clock; resume set skips games past their deadline; manual stops still reset it as today |
| F05 | requirements | major | A 30 s resume timer can start every game before the Discord packet is heard | partly | 4.7: the resume waits for the network (listener bound, gateway answers) then 30 s; a late tagged packet cancels unstarted games; asking the waker over UDP isn't possible once that channel is cut (SFP-07) |
| F06 | requirements | major | Valheim's start can wait an hour behind another game's SteamCMD download | accept | 2.3: update-on-start uses TryLock and starts the installed build with today's warning; installs still wait; I test |
| F07 | requirements | major | Several visible "Valheim Easy Server" strings have no owner; Mac texts would name an app that isn't there | accept | 1.2 lists every place (winres, macOS prompts, library.go, waker.go, CFBundleName); Mac texts name the real app; `build-N` tags added to identity |
| F08 | requirements | major | R2.x, the no-change soak release and R3.x are releases the owner never approved; R3 could ship games hidden | accept | Owner question Q1; `release/r2` defined; R3 ships every game at least preview, hidden only after a failed check, named to the owner |
| F09 | requirements | major | A plain release merge fast-forwards; the note becomes a `[skip ci]` commit and nothing is released | accept | 8.2: `git merge --no-ff -m "<note>"`, pre-push and post-push checks (same as CT-10) |
| F10 | requirements | major | No way stated to run the build-0 candidate on the Mac without stranding the Mac Pro on it | accept | 7.5 "Running the candidate": bare binary from Terminal, real data folder, then reopen the installed app |
| F11 | requirements | major | Windrose's presence test in CI can't see joins (no client) | accept | 5.11: decided from a real session log; ships `Unknown` until then |
| F12 | requirements | major | Windrose's posts: a false second stop post, "send any message" when unread, an invented 6 h stop | accept | 4.6: `stopAsked` with one line; a panel-worded question when unreadable; no-channel case uses the decided 4 h + 2 h clock and is owner question Q2 |
| F13 | requirements | major | The memory refusal in Discord depends on an unverified UDP path | partly | Superseded by SFP-07: the UDP path is gone and the app posts refusals through the webhook; no JetKVM acceptance item needed |
| F14 | requirements | major | If Minecraft's Java won't run on macOS 12, the spec greys Minecraft out on the Mac | partly | Settled: Java 25 needs macOS 11, so it runs now; fallback to the newest runnable release kept as insurance (5.2), owner question Q6 |
| F15 | requirements | minor | A new game's nil Discord settings have no stated meaning | accept | 3.2: nil = on, main channel, stopped and daily, no password; D test |
| F16 | requirements | minor | Auto-stop texts always say "an hour" | accept | 4.5: built from the minutes, tested at 30/60/120 |
| F17 | requirements | minor | Adding a game stars it without the owner asking | partly | 6.1: a "Star it" checkbox, ticked by default so a new game doesn't vanish into the closed Inactive list; owner question Q3 |
| F18 | requirements | minor | A hidden Valheim can still be started from Discord, start-with-app or a resume | accept | I1, 3.2, 4.3, 6.4: hiding stops, unstars and excludes it; `TestHiddenLegacy` |
| F19 | requirements | minor | App-wide "start with app" doesn't say which games it starts | accept | 2.9: Valheim keeps settings.json's meaning; other games get a per-game setting, default off |
| F20 | requirements | minor | Hand-forwarded Valheim ports show a UPnP conflict and needless manual steps; never tested on a real router | partly | 6.5: a 718 conflict pointing at this PC, or SteamSees listing it, counts as open; the Mac checklist step is optional (needs a friend outside) |
| F21 | requirements | minor | R2 ships ten hidden adapters it doesn't need | accept | 8.1: R2 registers Valheim only; gate checks `game.All()` has one entry |
| F22 | requirements | minor | A copy that skips R2 updates R1 → R3 untested | accept | 7.3/8.2: R1 → R3 and bed004b → R3 at the R3 gate; 3.6 row added |
| CT-01 | code | blocker | Build 0 on branches breaks the self-update test and 7.3 on multigame | accept | 1.4/7.3: tests build numbered copies; only uploaded artifacts are build 0; S2's acceptance fixed |
| CT-02 | code | major | `Status.job` drops a finished Sleep job, so the old panel and tests never see its result | accept | 4.2: the higher id of the app slot's and legacy slot's current-or-last job; `sleptSince` reads the app slot |
| CT-03 | code | major | Waker install and firewall fix as app jobs break synchronous APIs and tests | accept | Same change as F03 |
| CT-04 | code | major | LegacyStore too narrow: tests inject the installer and mods URL after New | accept | 5.1: `Installer()`, `ModsURL()`, `ModVersions()` read at each call; Progress converted |
| CT-05 | code | major | No locking contract between adapter calls and `m.mu` | accept | 2.2 locking note; LegacyStore snapshots via atomic pointers; Monitor called under the lock; `-race` Status test |
| CT-06 | code | major | Adoption loses the mods-code rerun and the Posted fix-up | accept | `Afterer.Pending`; 4.4 step 4; `TestAdoptRerunsAfter` |
| CT-07 | code | major | `played` in the adapter's Extra, but the manager owns reminders | accept | 2.2: typed `Record.Played` (no omitempty); `{}` for non-legacy mods |
| CT-08 | code | major | Cached installed/build makes a Start after install fail | accept | 4.3/4.8: install job refreshes the cache before finishing; legacy keeps today's synchronous reads |
| CT-09 | code | major | Keeper: Setsid with Setpgid fails, SIG_IGN is inherited, the Windows console text is wrong | partly | The keeper is cut (SFP-06); the parts that apply to `platform.Start` are in 2.11 and P's acceptance |
| CT-10 | code | major | Gate merges fast-forward onto a `[skip ci]` head | accept | Same change as F09 |
| CT-11 | code | minor | `discord/test` body key is `webhook`, not `link` | accept | 6.2: `{webhook, game?}`; legacy body in `TestLegacyRoutes` |
| CT-12 | code | minor | Exact-key goldens would need edits in R2/R3 | accept | 7.2: bed004b keys with types plus an explicit allow-list of extras |
| CT-13 | code | minor | Tests override `readyWait`/`codeWait` and use runInfo/codePending; not accessor-only | accept | `valheim.ReadyWait`/`CodeWait` exported; 4.1 lists the allowed replacements |
| CT-14 | code | minor | The old release updates itself again during the "back" step | accept | 7.3 step 5: stop publishing, autoUpdate off, assert the build number |
| CT-15 | code | minor | `TestStatusIsQuick` < 5 ms is flaky on Windows and under -race | accept | 4.8: LAN addresses cached (R3); counting hook and loose bound (with SFP-18) |
| CT-16 | code | minor | Guild lock depends on Valheim's Discord on/off | accept | 6.3: guild from `discordWebhook` whenever valid; tested with Valheim's posts off |
| CT-17 | code | minor | SteamCMD lock misses `ensureSteamCMD`; success line hard-wired to Valheim | accept | 2.3: lock covers all of Install; regexp from the instance's app id; TryLock on start |
| CT-18 | code | minor | Games without public ports get no firewall rule and trigger the pop-up | accept | 2.12: `AnyPort` program-wide rule; golden test |
| CT-19 | code | minor | The new matrix drops Valheim's Mac doorstop build step | accept | 7.4: `setup` per OS in realserver.json |
| F1 | games | major | Terraria's zip has a version folder at the top, so Program() never exists | accept | 5.3: lift the top folder; dedupe the list; "1461" → "1.4.6.1"; ~57 MB; golden test (zip re-checked) |
| F2 | games | major | Terraria on macOS needs the script's `DYLD_LIBRARY_PATH` | accept | 5.3: Dir and Env set as the script does; real check must accept a connection (script re-checked) |
| F3 | games | minor | Several Terraria verify marks can be settled; 1.4.6 may use UDP | accept | 5.3: difficulty 0-3, `upnp=0`, strings, XNA URL; UDP port checked in the real check |
| F4 | games | major | `pvp` is no longer a server.properties key; versions are now "26.3" | accept | 5.2: `pvp` dropped; versions opaque (jar and manifest re-checked) |
| F5 | games | minor | Minecraft log strings differ from the spec's | accept | 5.2: any-thread regex, renamed join, save lines, case-insensitive bind failure, turned-away line |
| F6 | games | minor | Mac Temurin's java is under Contents/Home; Mojang UUIDs come without dashes | accept | 2.3/5.2: use Temurin's returned path; dashes added; facts recorded |
| F7 | games | major | Necesse's java is at `jre/Contents/Home/bin/java` on macOS | accept | 5.4: Program per OS; ~180 MB; arch checked with `file` |
| F8 | games | major | Necesse verify marks have documented answers | accept | 5.4: `-datadir`, ready/end lines, `exit`, no seed, `.zip` worlds |
| F9 | games | major | Download checks only sha1/sha256, but VS publishes md5 and .NET sha512 | accept | 2.3: typed `Hash`; 5.5 resolver details |
| F10 | games | minor | Vintage Story strings, whitelist values and config rewrite on shutdown | partly | 5.5: strings and edit-only-while-stopped accepted; whitelist always Off with a required password, since "on without a password" would lock friends out with no allow-list screen |
| F11 | games | major | Zomboid's Windows launch is StartServer64.bat, not the JSON; size ~7 GB | accept | 5.6: parse the .bat; JSON fallback; ~7.1 GB; ready substring; Build 42 (Steam info re-checked) |
| F12 | games | major | Enshrouded is ~8 GB / 13 GB unpacked; one port; no address join; groups format | accept | 5.7 rewritten from the publisher's FAQ (Steam info re-checked) |
| F13 | games | major | Core Keeper writes GameInfo.txt, and has join/leave lines | accept | 5.8: GameInfo.txt with fallback; ByName once proven; ~420 MB |
| F14 | games | major | Satisfactory keeps saves in LocalAppData; API is camelCase | accept | 2.8, 3.1, 5.10: world store points there; camelCase; log path |
| F15 | games | major | Windrose is ~3 GB, not 35 GB; nested config; worlds by WorldIslandId | accept | 5.11 rewritten; size raised with the owner (Q5) (Steam info re-checked) |
| F16 | games | minor | Palworld args include flags v1.0 advises against; PublicPort misused | accept | 5.9 |
| F17 | games | minor | Private ports can't bind to 127.0.0.1; RCON exposed on the Mac LAN | accept | 2.5: protection stated; Mac setup checklist note |
| F18 | games | minor | Valheim's download is ~2 GB, not ~1 GB | accept | 5.1: DownloadMB ~2000; legacy sentence kept in `Texts.Exact` |
| F19 | games | minor | Section 10 still asks questions the owner answered; tray differs from the decision | accept | Section 10 rewritten; 6.6 tray has the three decided items |
| SFP-01 | simplicity | major | Streams merge before Windows/Mac CI has run on them | accept | 9.1: push `mg/<stream>`, green on all three before merging; `-race` locally |
| SFP-02 | simplicity | major | Windows-only adapters can't be tested on Linux, the agents' only OS | accept | 2.2/7.1: fake mode ignores `Info.OS`; Conformance on Linux too |
| SFP-03 | simplicity | major | Every R3 lifecycle feature needs call sites in M3's files | accept | 4.1/9.3: one observer list frozen in phase 0' |
| SFP-04 | simplicity | major | The real-server matrix on every push mostly gets cancelled and costs a lot | accept | 7.4: changed cells on push, nightly, dispatch; gate runs not cancelled |
| SFP-05 | simplicity | major | Real-server checks wait for the manager and Add a game | partly | 7.1/7.4: `gametest` `Real`; only Valheim and Minecraft go through the manager; presence can't come from CI at all, so it comes from real logs (F11) |
| SFP-06 | simplicity | major | The keeper is the hardest part for three games no decision covers | accept | 2.11: cut; the app holds the stdin pipe, saves before exiting, waits with updates while people play; Terraria autosaves every 10 min (its changelog) |
| SFP-07 | simplicity | major | The app → JetKVM HMAC reply channel is a new protocol for four messages | accept | 6.4: replies posted through the game's webhook; no key, no JetKVM listener |
| SFP-08 | simplicity | major | Wrong join patterns would auto-stop or sleep games with players on | accept | 2.6: log-only presence counts as `none` until the first join; ready needs a real join/leave excerpt |
| SFP-09 | simplicity | major | Real servers never gate a release | accept | 7.4: release job requires a green real-server run on `HEAD^2` |
| SFP-10 | simplicity | major | A new build that crashes after a minute is never caught or fixed | accept | 7.3 step 4: 3 minutes alive, then N → N+1 with Valheim running |
| SFP-11 | simplicity | major | No branch for R2.x once R3 work lands | partly | `release/r2` accepted (8.2); holding back every inert stream isn't needed, since unlinked packages aren't in the binary; G registrations wait for the cut |
| SFP-12 | simplicity | major | V's checks can't run as written; V then M is a long serial path with two copies | accept | bed004b goldens recorded in R1; V and M merged into VM; side pieces in SV |
| SFP-13 | simplicity | minor | Answered questions still open; tray and Mac checklist contradict decisions | accept | Same changes as F19 and F10 |
| SFP-14 | simplicity | minor | Build 0 breaks the self-update test; S2's checks contradict | accept | Same change as CT-01 |
| SFP-15 | simplicity | minor | Game resolvers in the shared install stream can't be fixed by game streams | accept | 2.3: resolvers live in `games/<id>`; install keeps generic helpers |
| SFP-16 | simplicity | minor | The rename spans four streams' files, so CI goes red until all merge | accept | 1.2/9.3: T does every visible-name change in one commit, merged last |
| SFP-17 | simplicity | minor | FakeBackend duplicates ~85 methods and can drift | accept | 6.2: no Backend interface; dev fixtures, then the real app with fake servers |
| SFP-18 | simplicity | minor | Wall-clock tests under -race gate releases | accept | I5/4.8: counting hooks; loose bounds skipped under -race |
| SFP-19 | simplicity | minor | Rosetta and the `intel` level serve Macs nobody uses | accept | 2.10: OS map by goos/goarch; "Needs an Intel Mac" on arm64 |
| SFP-20 | simplicity | minor | Build Windrose's Discord rule only if a game turns out to need it | reject | It is needed anyway: Windrose ships `Unknown` until a real session log exists (F11), and SFP-08's safety net uses the same rule |
| SFP-21 | simplicity | minor | Several Needs/Owns entries are wrong or missing | accept | 9.3: G needs P0; no K; P owns additions.go; S1 owns the constant edits; CR needs G-minecraft; UIV before UI's deletions; MemoryMB no longer from stats.json |
| SFP-22 | simplicity | minor | servers.json writes and safe-mode machinery in R2 for little benefit | partly | 3.4: R2 neither reads nor writes it; damaged → backup, missing → rebuild; no restore route; unknown-key preservation kept (cheap, and R3.x builds write the file) |
| SFP-23 | simplicity | minor | The combined firewall script is never run before release | accept | 2.12/7.2: `TestFixFirewallAllReal` on the Windows runner |
