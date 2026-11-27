# Backup of the multi-game work (2026-11-27)

The owner paused the work on 2026-11-27 at about 03:36 UTC and asked for a backup of everything. This branch, `multigame-backup`, holds all of it. It never releases: only pushes to the default branch release, and this commit says `[skip ci]`.

Its tree is the multigame branch at `732bd85` plus this folder. Every unpushed branch and every unfinished worktree is one of the backup commit's parents, so all of their history is on GitHub.

## Restoring

```sh
git fetch origin multigame-backup
git log --format='%H %s' -1 origin/multigame-backup   # the backup commit
git show --no-patch --format=%P origin/multigame-backup # its parents, listed below
git branch mg/r3-integrate <sha from the table>          # recreate any branch
```

## What is here

### Branches that were only on this computer

Each of these is 2 commits ahead of multigame (one stream commit and one follow-up), except mg/r3-int.

| Branch | Commit | What it is |
|---|---|---|
| mg/manager-r3 | 0a6db8a | M3: the manager's R3 core (instances, several games at once) |
| mg/lifecycle | 2e39620 | A: ports, memory check, firewall, auto-stop, idle sleep, Add a game / Remove |
| mg/discord | 24fa231 | D: a Discord channel per game, per-game posts and records, the "still playing?" rule |
| mg/waker | 2b80eae | WK: the JetKVM bot's one command per starred game, wake for one game |
| mg/router | 4e9d355 | N: automatic router ports (UPnP / NAT-PMP / PCP) |
| mg/api | 01f7355 | API: the panel's 6.2 routes and the dev fixtures (recorded answers). mg/ui and mg/ui-valheim start here |
| mg/r3-int | 420b18a | the R3 integration so far: multigame f86a110 + M3 + A + D + WK + N. mg/r3-integrate starts here |
| mg/r1-int | ec66d7e | R1's old integration branch (already released as build-33; kept for completeness) |

### Unfinished work in the stopped jobs (snapshots)

Each snapshot is one commit on top of its branch. It holds the worktree's uncommitted edits as they were when the job was stopped, new files included.

| Branch | Snapshot | On top of | What was in progress |
|---|---|---|---|
| mg/r3-integrate | a98fd81 | 420b18a | INT: making the integration build, vet and pass `go test -race` (test helpers shared between streams, tests moved to M3's instance model), then wiring the cross-stream items |
| mg/ui | 7b1d99f | 01f7355 | UI: the new panel: sidebar with Active/Inactive lists and stars, top bar, Overview, Add a game, a game's page with tabs, App pages |
| mg/ui-valheim | 86f8b70 | 01f7355 | UIV: Valheim's pages as a module of the new panel (worlds, mods, join code, players, settings) |
| mg/g-fix-vintagestory | 53f4f26 | f86a110 | Vintage Story's Windows console: how the server reads typed lines, the fix and a Windows facts test |
| mg/g-fix-enshrouded | cdbe9d5 | f86a110 | Enshrouded's query port that never answered A2S on the real Windows run |

UIG (the other 10 games' panel modules) had not started.

### Already on GitHub

- `multigame` at `732bd85`: everything merged so far, plus the real-server fixes for the shared harness, Terraria, Necesse and Project Zomboid.
- Every other stream branch (`mg/g-*`, `mg/p0`, `mg/p0r3`, `mg/ci-real`, `mg/install`, `mg/platform`, `mg/fakeserver`, ...) is contained in multigame.
- `release/r2` and the default branch: R2, released as build-40.
- GitHub runs started by the last push: real-server.yml 45935038820 (games changed) and build.yml 45935038754. Read their results first when resuming.

### Files in this folder

- `README.md`: this file.
- `multigame-streams.js`: the workflow script that runs each stream (build, independent review, follow-up). Its arguments are `{base, streams: [{key, branch, release, ask}]}`.

## Picking the work up again

1. Read the two GitHub runs above. Send each failure to its game's fix or to the list below.
2. Finish INT from the mg/r3-integrate snapshot. Then merge mg/api, the remaining real-server fixes (Vintage Story, Enshrouded) and multigame's new commits. Then the panel (UI, UIV, then UIG), then T (rename, tray, docs) and the rest of CR. Then the R3 gate.
3. R3 ships only after the owner says step 2 (build-40) has been fine.

### Lead follow-ups collected so far

1. Contract: `Plan.ConsoleEncoding` ("" or "utf-16le"). Terraria's Windows server reads its piped console as UTF-16LE. The manager's console writes (the save line before the app exits, "console" stop steps) and gametest's typeLine must honour it. Terraria sets it for the real Windows server only. Vintage Story may need it too.
2. Terraria on Windows ignores Ctrl+C, so after a takeover only Force stop ends it. Lead's call (owner told): on Windows, automatic updates wait while such a game runs. Terraria stops by itself after an hour with nobody on. A manual update warns. Record this in decisions.md.
3. internal/manager/realserver_test.go: use `gametest.DataDir(t)` so Valheim's logs are collected.
4. steamcmd:
   - try a third time;
   - treat "Missing configuration" and a crash during self-update (exit 134) as worth another try;
   - filter the macOS "Missing String for EOSType" spam;
   - fix the Linux colour-code bug that hides "Success!".
5. Satisfactory realserver_test.go:47: measure `gametest.DataRoot()`, not `os.TempDir()`.
6. Spec 7.1 / signatures_test.go: add `gametest.DataDir`, `DataEnv` and `DataRoot`.
7. The manager's probe loop: give each Probe call 3 s (spec 2.2).
8. Approve Terraria's departures:
   - `-savedirectory <server folder>`;
   - on Windows, the Ctrl+C step waits 15 s and its text points to Force stop.
9. gametest:
   - spell Windows temp folders in full (no `RUNNER~1`);
   - maybe a shared-install option for big games.
10. Manager: an event for "the server gave up loading but still runs" (Zomboid's "Server Terminated."). It should end the start with an error and offer Stop.
11. Minecraft and Necesse: empty `_JAVA_OPTIONS`, `JAVA_TOOL_OPTIONS` and `JDK_JAVA_OPTIONS`, as Zomboid now does.
12. Spec 5.6: describe Zomboid's launch as built:
    - the .bat's SET lines go into Plan.Env;
    - `%~dp0` is handled;
    - folders are spelt in full;
    - the Java option variables are emptied.
13. Minecraft's stand-in: its RCON "stop" sometimes exits before the reply reaches the client (an occasional takeover-stop failure in TestConformance).
14. Privacy, before R3: Core Keeper's and Enshrouded's testdata, FACTS.md and tests carry real players' Steam IDs from public logs (e.g. corekeeper's github-2024-10-29 log, enshrouded's 1.0 and jsknnr logs). Replace them with made-up IDs and names, and scan every game's testdata again.
