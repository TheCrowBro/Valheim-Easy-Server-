# Notes from the streams for later work
What each stream told the lead when it finished (decisions it made where the spec was silent, and what later streams must know). The lead accepted these unless marked otherwise.

## RC (mg/protocols)

1. RCON calls now give up after 10 s when ctx has no deadline. The frozen comment only said "ctx bounds…"; I added this so a hung server can't stall a stop chain forever, and it is in the doc comments. A slow command, such as save-all on a big world, needs a longer ctx deadline. Undoing it is one variable (defaultTimeout).
2. For the stop chain (VM/M3, G-minecraft, G-zomboid):
   - If the server hangs up after it starts answering (Minecraft `stop`, Zomboid `quit`), Exec returns the reply with a nil error, and the Client is closed.
   - If it hangs up before answering, Exec returns an error wrapping io.EOF or a connection reset. For a stop or quit step, the server may well be stopping, so the chain should wait the step's Wait. Only a refused connection should jump straight to the next step.
   - After any error mid-command (timeout, cancel, ErrAuth, broken connection) the Client is closed and the caller must Dial again. A command refused before sending (a zero byte, too long, ctx already ended) leaves the connection usable.
3. RCON commands and passwords are limited to 1446 bytes, because Minecraft reads one 1460-byte packet. A longer command is refused before anything is sent.
4. For stream F (fakeserver):
   - TestServer runs handle for one command at a time, and handle must not call TestServer.Close, because Close waits for a command in progress.
   - TestResponder's info must not call Close, for the same reason.
   - A nil handle or info is allowed.
   - TestServer.Addr and TestResponder.Addr come from the listener, so ":25575" becomes "[::]:25575".
5. For adapters: a2s.Info.Players is whatever the server reports, and Bots is separate, so an adapter decides whether to subtract bots. Query refuses answers that come in several pieces: I believe Steam game servers fit A2S_INFO in one packet, but I didn't check that here.
6. Nothing outside my two folders needed changing.

## SV (mg/side)

Departures from the spec text, all inside internal/servers. Please confirm or tell me to change them:
1. Discord also gets an Unknown map, so it keeps keys it doesn't know, like the other levels. This follows 3.2's "loses nothing a newer one put there". The catch is that servers.Discord can no longer be compared with ==; DiscordRules() returns a copy.
2. 3.4 says the backup is "written back on the next change". Open writes it back right away instead, after moving the damaged file aside. Otherwise every start would set the file aside and warn again, and Update, which per spec refuses unless OK, could never save that next change.
   - Exception: a servers.json that exists but can't even be read (a permission or a lock) is left in place. The backup is used for that run, and Opened.WriteErr explains why changes won't save.
3. Rules the spec doesn't spell out:
   - A missing servers.json with a valid backup restores the backup whether or not games/ exists.
   - A missing file whose backup is from a newer version means safe mode, because a new list would soon overwrite that backup.
   - A rebuilt list stars the legacy game, as a first start does.
   - An empty games/ folder or a damaged backup counts as a sign that a list existed, so the result is Rebuilt (with the warning), not Created.
   - A folder named after the legacy id is never rebuilt, since Valheim's files never live under games/.
4. Additions beyond the spec's signatures: Open, Opened (with Warning), New, ValidID, ErrNewer, the default helpers, and Update's refusal to add bad, duplicate or second-legacy ids. Load does not treat those id problems as damage, because a same-schema newer list must never be set aside.

For M3 (R3):
- Call servers.Open(dataDir, game.Legacy().Info().ID, func(id string) bool { _, ok := game.Get(id); return ok }, time.Now()) in manager.New.
- A non-nil error or SafeMode means Valheim-only safe mode.
- Add Opened.Warning() as a warning activity when it isn't empty, and log Opened.Kept and WriteErr.
- Pick ports for Opened.Found.
- Open reads only servers.json in the normal case. It reads servers.json.bak and lists games/ only when the file is missing or damaged. There is no file-access hook, so TestNewIsQuick's counting hook has to allow for this.

For WK and VM:
- The waker should send wol.PacketFor(mac, tag, id) with its random 6-byte tag. wol.Send still builds the plain Packet, so WK (owner of wol.go) will need a send path that takes a built packet or an id.
- R2's wake check (VM): GameOf returns "" or the legacy id for packets that may start Valheim. It returns UnreadableGame or any other id for packets to ignore.

My reading of the brief's "legacy id unchanged": a packet with no id reads as the legacy game, and the "valheim" id round-trips. PacketFor(mac, tag, "valheim") does add the suffix, as 6.4 describes. If you meant the legacy id to produce exactly today's packet, that is a one-line change, but wol would need to be told the legacy id.

## W (mg/worldstore)

- Tell the game streams that use worldstore (Minecraft, Terraria, Necesse, Vintage Story and the rest) to make the store once, for example in Game.New, keep it on the server value, and return it from Worlds(). Writing `func (s *server) Worlds() game.Worlds { return worldstore.New(...) }` would make every upload fail as expired and stop changes running one at a time. This is now in the comment on New.
- Small change to an error message: when an import can't put the old world back, it now names the backup to restore, for example "Automatic copy before it was replaced", instead of "the world's backups".
- No files outside internal/worldstore/** were touched.

## VM (mg/manager)

**Changes to existing tests and files outside my own:**
1. **Test changes.** Existing tests change only by 4.1's replacements, with one exception. In `legacy_golden_test.go`, `legacyRunInfo()` has `NoCrossplay`, `ModsCode` and `CodeMods`, which `game.Record` doesn't have. They now go in `Extra: valheim.RecordExtra(...)` (plus imports).
2. **Old helper names kept in a test file.** The tests call old helpers that now live in the adapter. Rather than editing the tests, I added a new test-only file, `internal/manager/legacy_names_test.go`, that maps those names onto the adapter's code: `argsKey`, `onlineMessage.text`, `m.exitHint()`, `eventsFromLog`, `remind`, `dailyText`, `m.library()`, `savedCode`. Please confirm this is acceptable.
3. **Web test file.** I added `internal/web/games_test.go`, which only does `import _ games/all`. The web tests make their own manager, and the manager needs Valheim registered. No route or JSON changed.

**Places the frozen contract fell short, all worked around inside my own files:**
1. **Replay timing.** `Monitor.Line` has no notion of reading a log back. The manager uses two local optional interfaces, `Replaying(bool)` and `FromLog()`, which Valheim's Monitor implements. They should probably become optional interfaces in `internal/game`.
2. **Adapter errors with a next step.** An adapter can't return the manager's `ActionError`. Valheim returns its own `ActionError` with a `SuggestedAction()` method, and `actionOf()` converts it. A `game.ActionError` type would be cleaner.
3. **Activity messages from Plan.** `PlanRequest` has nowhere to add activity-feed entries (mod installs, "Couldn't check for server updates"). I added `LegacyStore.Note`. R3 games will need this too.
4. **LegacyStore differs from spec 5.1:**
   - `Update(change func(*config.Settings) error)` replaces `Save(config.Settings)`, so a change is a single atomic step under `m.mu`.
   - `ModVersions` and `SetModVersions` are gone: the adapter keeps the installed mod versions itself, keyed by the server folder.
   - `Note` is added.
5. **Discord escaping.** Adapters may not import `internal/discord`, so `games/valheim` has a copy of `discord.Escape`, with a test that keeps the two the same. Consider allowing `internal/discord` for adapters.
6. **Late join code.** The Monitor's `RunState.Extra` also carries `"codeLate"`, which today's "late" join-code problem needs.

**Waiting on other streams:**
- **Wake packet (stream SV):** once `internal/wol/game.go` lands, change the single call site `var wakePacketGame` in `internal/manager/wakesignal.go` to `wol.GameOf`. `TestWakePacketForOtherGame` replaces that variable, so it keeps working.
- **SteamCMD (stream I):** Valheim still installs through `LegacyStore.Installer()` (`steamcmd.Installer`). Its update on start uses today's path, and an error shows today's "Couldn't check for server updates…" warning. When I lands, `games/valheim` `Plan` should use I's skip-if-busy/`TryLock` update mode, and the default `steamcmd.SteamCMD{}` in `manager.New` keeps an empty AppID, which means Valheim.
- **R3 (M3):** not done here because they are R3 work: 4.9's full `GameStatus` (R2 has a subset), the observer hooks, the `Capture`/`GameLog` outputs (Valheim uses `LogArg`), console and RCON stop steps (they return `ErrNoConsole`), and the servers.json-backed Store.

After review:

Nothing is blocking, and no files outside this stream's ownership were needed. One thing you might choose to do: TestOneGameRegistered in internal/manager is now the only test that counts the games in the shipped registry (internal/games/all). Every R3 game stream (G-*) adds a file to internal/games/all, so the first one merged after the R2 cut must change or remove this test, as intended. You may want to tell the M3 or G streams that this is expected.

## I (mg/install)

1. **Valheim's update-on-start needs one change by stream VM.** Valheim's Plan (today's manager server.go:240 path) must call LegacyStore.Installer().Install with steamcmd.SkipIfBusy(ctx). Other adapters use install.SkipIfBusy(ctx) when req.Update is set. Without the marker, a start's update waits behind another game's download instead of skipping. In R2 only Valheim uses SteamCMD, so nothing changes until R3, but VM should wire it now. The manager's warning text needs no change.

2. **Added to the frozen install contract:** SkipIfBusy, ErrBusy, BusyError (an alias of steamcmd's), FindJava and FindDotNet. They are pinned in internal/install/signatures_test.go; nothing existing changed.

3. **Added to internal/steamcmd:** the AppID, Branch, Name and SizeGB fields, InstalledBuildFor, SkipIfBusy, ErrBusy, BusyError, and the test-only package internal/steamcmd/steamcmdtest.

4. **Fakes don't take the shared lock.** steamcmd.Fake (used by --dev-fake-server and the manager tests) was left alone on purpose so VM's tests behave as before, and stream F's install.Fake can't take the lock without a new export. If R3 manager tests should make fake games compete for SteamCMD, say so and I'll add a small hook.

5. **Spec 5.2 wording differs from what Temurin does.** Spec 5.2 says to download from /v3/binary/latest and fetch the checksum separately from /v3/assets/latest. Temurin instead uses the package link and checksum from the single /v3/assets/latest answer, so the checksum always belongs to the file downloaded. The Minecraft stream should know; 5.2 could be updated.

6. **Runtime layout for adapters:** each runtime goes into dir/temurin-<release>/ or dir/dotnet-<version>/, with a note in .egs-java.json or .egs-dotnet.json. Use FindJava and FindDotNet for Program(). Installing a version that's already there downloads nothing, so a Repair that wants a fresh runtime must remove the runtime folder first.

7. **EnsurePrereq depends on stream P's RunElevatedWait.** It needs the installer's exit code back, and 1223 (as the code or as an error) when the owner declines the prompt. Until P lands, the stub's ErrUnsupported gives "<Name> isn't installed, and the app can't install it on this computer."

8. **Download's progress text is "Downloading <file name>…".** Callers that want the game's name rewrite p.Step, as Temurin, DotNet and EnsurePrereq already do.

## P (mg/platform)

1. **build.yml (not mine):** the Windows job must set `EGS_CI_ADMIN: "1"` on its platform test step (`go test -v -count=1 ./internal/platform/`). Without it, TestFixFirewallAllReal and TestRunElevatedWaitReal skip, and spec 7.2 and P's acceptance need the first one to run. Never set it on the Mac job or on anyone's own computer: the firewall test removes every "Valheim Easy Server - *" and "Easy Game Server - *" rule first, just as the app's own fix does.

2. **File names outside my list:** firewall_games.go has no system suffix, but it can't hold code for each system on its own. So I also created firewall_games_windows.go, firewall_games_darwin.go and firewall_games_linux.go, plus their tests firewall_games_test.go and firewall_games_windows_test.go. Please confirm these count as P's files; they're used only by this feature.

3. **Different from the spec's wording:**
   - The `<sha8>` in a game's rule name covers the program path and the rule's ports, not just the program. Checking a rule by its name then works in any Windows language, and notices a port change after "pick ports".
   - Rule names: `Easy Game Server - <instance> TCP|UDP (<sha8>)`, or `Easy Game Server - <instance> (<sha8>)` with AnyPort (the spec gave no name for that case).
   - The games' script removes all old rules before adding any new ones, instead of remove-then-add per game. The result is the same, and it's safe when two games share a program.

4. **For M3 (stdin pipe):** after Start, both ends of the console pipe are in blocking mode on Unix, because os/exec calls Fd() on the files it hands over. On Windows pipes block anyway. Write deadlines therefore don't work, and typing a line to a game that has stopped reading will hang. Type lines from a goroutine that is allowed to stay stuck. This is written up in the StartOptions doc in additions.go.

5. **Visible text (for stream T):** macOS FixFirewallAll uses a new prompt, "Easy Game Server wants to let friends through the macOS firewall to reach your game servers." It appears only when there are game rules, so only from R3; with no games, the prompt is today's FixFirewall text. RunElevatedWait's macOS prompt doesn't name the app. Please add the firewall prompt (firewall_games_darwin.go) to T's list of visible names.

6. **For callers:**
   - The macOS and Linux PortInUse can only name processes belonging to this user. On macOS another user's port shows as in use with PID 0.
   - DefaultGateway returns IPv4 only.
   - For a GameRule with no ports and AnyPort false, GameFirewallReady says true and FixFirewallAll adds nothing for it.

After review:

1. Spec 9.3 accepts stream P only when TestFixFirewallAllReal passes on the Windows runner, so the reviewer's should-fix still needs build.yml's owner (the lead, or CR). In the windows job, change only the "Windows features" step. Do not set it for the whole job, and never on the Mac job, because the test removes every "Valheim Easy Server - *" and "Easy Game Server - *" firewall rule:

      - name: Windows features (Ctrl+C to the server, CPU/GPU/RAM counters, power)
        run: go test -v -count=1 ./internal/platform/
        env:
          # The runner gives admin rights without a prompt, so the real
          # firewall and elevation tests can run. Only on this step: they
          # replace the app's firewall rules, and "All tests" would run them twice.
          EGS_CI_ADMIN: "1"

2. Then push mg/platform and re-run CI. Before accepting P, check two things. First, the Windows platform step's log shows "--- PASS: TestFixFirewallAllReal" and "--- PASS: TestRunElevatedWaitReal", not SKIP. Second, the run's summary page has no "did not run" warning. Until the variable is set, every Windows run shows that warning, which is on purpose.

## F (mg/fakeserver)

1. **Spec and brief differ on how the stand-in picks its game.** The spec's fakeserver.json example has "game", but `install.Fake`'s frozen signature can't know the id. So, as your brief says, the game comes from the command line through each game's `Match`, and "game" in fakeserver.json is an optional override. Each G stream's `cmd/fakeserver/<id>.go` must have a Match that no other game's command line also matches (Minecraft, Necesse and Zomboid all run java; their jar or main class tells them apart). The stand-in exits with code 2 and names the games when two match.
2. **The RCON stop step needs a convention the manager must share.** A `"rcon"` StopStep only carries the command, so gametest reads the port from `Record.Ports["rcon"]`, the password from `Record.Secrets["rcon"]`, and connects to 127.0.0.1. M3's stop chain must do the same; please confirm it in the spec. The demo's API kind keeps its password in `Secrets["rest"]` and makes its own calls.
3. **Additions to gametest's API** (frozen signatures unchanged, signatures_test.go untouched and green):
   - `Session.Server()`, `Env()`, `Record()`, `Plan()`, `StartAgain()` and `Console(line) error`.
   - A new subpackage, `internal/game/gametest/demo`, which M3 can use for "a console demo game saves on Quit; after a takeover, stops through Ctrl+C".
4. **Where install.Fake's tests live.** I own only internal/install/fake.go, so its tests are in internal/game/gametest/installfake_test.go, which also pins its signature. If you'd like them in an internal/install/fake_test.go, that file needs assigning (stream I owns the rest of that package).
5. **The demo reads .egs-build.json itself**, in the same format as stream I's WriteBuild/ReadBuild, so it doesn't depend on stream I. Real adapters should use `install.ReadBuild` / `SteamBuild`, which work with what install.Fake writes.
6. **A test exists for each RC-dependent piece.** TestDemoRCONAndA2S (cmd/fakeserver) and the demorcon kind in TestConformanceDemo skip while RC is a stub and run with no change once RC merges. Both passed against mg/protocols.
7. **Conformance is stricter than spec 7.1's list in a few places G streams should know about:**
   - Fingerprint must equal the plan's, and must change exactly when a Restart-marked field changes.
   - A console plan needs a SaveLine whose save produces a "saved" event.
   - Every stop step must get through, except a console step after a takeover.
   - The server-name field is found as a server-scope text field with "name" in its key or label; Validate's error may be keyed by the bare key or `server.<key>`.
   - Real mode skips the scripted-player checks.
8. **Two behaviours to know.**
   - Stand-ins listen on 127.0.0.1 unless fakeserver.json sets "host", so dev mode never raises a firewall prompt.
   - BuildFakeserver builds once per test binary inside go test's own temporary folder, and reads the stand-in's sources so go test re-runs cached results when they change.

After review:

Nothing blocking, and every change is in F's own files.

1. Once stream RC (mg/protocols) merges, TestDemoProbeDuringStop and TestDemoRCONAndA2S stop skipping and run with no change.
2. A rule for whoever writes the RCON personalities (Minecraft, Zomboid): an RCON "stop" handler should return its reply and queue the stop with s.post, as demo.go does. If it calls stop or Exit inside the handler, the reply never goes out and the exit waits 250 ms. The RCON doc in fw_net.go now says this.
3. Stream RC's TestServer and TestResponder are correct as written; their docs already warn that handle/info must not call Close. No change is needed there.

## P-fix (mg/platform-fix)

1. **A file outside my list.** The comma bug lives in internal/platform/winparse_games.go (a 4-line change plus a comment), with its goldens in winparse_games_test.go. Spec 9.3 gives both to stream P, but your brief didn't list them. I edited them because the fix is in the script text and nothing else touches those files in R2. If you'd rather they went another way, drop that part of the commit. Without it, the "egsci UDP" rule will keep failing.
2. **Spec text.** Spec 2.12 (spec.md line 822) still shows `localport=<ports>` unquoted; it should read `"localport=<ports>"`.
3. **Missing programs.** If CI's log says netsh made no rule for "a program that isn't there", then R3's manager (stream A's firewall.go) must leave games that aren't installed yet out of FixFirewallAll's list. Otherwise their rules never appear, and GameFirewallReady keeps offering the fix.
4. **A risk CI can't see.** The temp-file startup command (pscommand.go, not mine) looks like a common malware pattern: `-WindowStyle Hidden -EncodedCommand` that reads a file and runs it as a script block, as admin. Windows Defender is off on GitHub runners, so CI can't tell whether Defender's script scanning (AMSI) blocks it on the owner's PC. With games this route is the normal one for FixFirewallAll. Today, Valheim alone only uses it with very long folder paths. It should be checked on the owner's Windows PC before R3.
5. **Next step if the launch is to blame.** If TestRunElevatedPowerShellReal fails while TestRunElevatedWaitReal passes, the fix belongs in power_windows.go's runElevated (plain ShellExecute, no COM, no "wait" flag), which I don't own. One option is to start it the way RunElevatedWait does, in its own goroutine, so it still returns before the prompt is answered.
6. **Untouched.** Nothing pushed or tagged; no other branch touched.

## F-fix (mg/fakeserver-fix)

1. **Separate macOS manager failure.** The same macOS job also failed internal/manager TestSettingsSaveDuringStart: "timed out waiting for server online", "The server stopped while starting", and "TempDir RemoveAll cleanup: directory not empty". It is not this port race: cmd/fakevalheim never opens a port, and the manager tests' random Valheim port is never bound. It needs its own look by VM/M3; I did not touch the manager.
2. **"Text file busy" in the manager (M3).** The same Linux race could hit the manager's tests once they run several fake games at once: one game being installed by copying the stand-in while another is started. If M3 sees "text file busy" on Linux CI, the same short retry on syscall.ETXTBSY around starting the program fixes it.
3. **Note for G-stream agents** (stream-notes, which I don't own):
   - Conformance and WaitEvent now retry a start up to 3 times on new ports when another program took a port gametest chose, and say so in the test log.
   - A personality or adapter whose "port in use" message should be recognised needs the port number in that line, or the port named earlier in the log, plus one of the usual wordings.
   - A test that wants to see a real "port in use" must give the port itself through Options.Change; those ports are never changed or retried.
4. **Port ranges for test helpers.** gametest picks from 20000-29989 and cmd/fakeserver's tests from 30000-31999. A future test helper that picks ports should stay outside both ranges, or share the same claim list.

After review:

1. For the owner of internal/manager tests (the reviewer suggests the VM or M3 stream): the wake-port tests pick a UDP port the same way that broke on macOS.
   - Where: internal/manager/waker_test.go:50-57 (freeUDPPort), which wakerOptions uses at waker_test.go:64 and which waker_test.go:78, waker_test.go:230 and r2_test.go:477 call; and internal/manager/wakesignal_test.go:36-42.
   - Why it can fail: each opens udp4 127.0.0.1:0, reads the port the system gave it, closes it, and the manager later listens on ":port" (internal/manager/wakesignal.go:35). Ports from port 0 come from the system's ephemeral range, which outgoing UDP such as DNS lookups by any program also uses. If the bind fails, the manager only logs "can't listen for wake signals", so the test then times out waiting for a wake instead of failing with a clear message.
   - Suggested fix: pick the wake port below 32768, the lowest system range. Use a range the other test programs don't use, since gametest has 20000-29999 and cmd/fakeserver has 30000-31999; 32000-32767 is free, for example. Check each pick by opening it. Or keep the probe socket open and let the manager take it over.
2. Lower risk, also not mine: internal/jetkvm/jetkvm_test.go:210, internal/rcon/rcon_test.go:593 and internal/platform/platform_windows_test.go:197 close a port from the system and then expect nothing to be listening there. If another program took it in between, they would see a different error. That is rare, and none of them blocks waiting, so they don't need changing now.
3. Items still open from my earlier report: the separate macOS TestSettingsSaveDuringStart failure, and the "text file busy" risk wherever other tests copy and then run a program.

## VM-fix (mg/manager-fix)

1. The same macOS job (/tmp/claude-0/-home-user/41f121b4-97ec-5588-a00f-1184231def86/scratchpad/j140125553130.log, lines 329-342) also failed TestConformanceDemo/demoapi in internal/game/gametest (stream F's code, not mine). The demo server hit "can't open port 49326: ... address already in use", which looks like a race between picking a free port and the server binding it. That job needs this fix and that one to go green.

2. For information, not a fix: in R2, handleLine always takes m.mu (the Monitor runs under it) and then handleEvents takes it again. Build 33 skipped noise lines before taking the lock and took it once per real line. So a start now goes through the lock roughly 1.5 to 2 times as often. When nothing else holds the lock this costs almost nothing. It only showed here because a back-to-back save loop held the lock on a slow disk. I did not change production code for it.

3. A side effect seen only under the injected 1 s save delay: the power loop, held up for 22 s, logged "woke up after 22s", which it takes as the computer waking from sleep. It did not disturb the start. I'm noting it only in case someone ever holds m.mu for a long time in real use.

After review:

Nothing to decide. This commit changes only internal/manager/r2_test.go, which this stream owns. setup's shared cleanup in manager_test.go has the same weakness: after ForceStop it waits only for the state to leave "online", not for the server process to exit. That only matters for tests that end while a server is still starting. I left it alone because it was outside this finding.
