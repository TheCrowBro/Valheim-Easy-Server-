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

## G-minecraft (mg/g-minecraft)

**Decisions or files I need you for:**
1. **Contract gap.** worldstore.Format's Detect and Place use worlds.Candidate, but adapters may not import internal/worlds (TestImportRules fails if they do). So Minecraft uses worldstore's default folder detection instead of 5.2's "folders holding level.dat". To get the stricter Detect, either add internal/worlds to the adapters' allowed imports or add `type Candidate = worlds.Candidate` to worldstore. The other game streams will hit the same gap.
2. **Q6 needs a runtime hook.** Info.OS can't change at runtime, so the panel can't grey Minecraft out on a Mac whose Java won't start. The adapter saves State["unsupported"] and refuses to start with "Needs Windows: the newest Minecraft needs a newer macOS". Consider an optional interface the panel can ask for the support level and reason.
3. **State keys other streams must set.**
   - State["eula"] holds the time the owner ticked the EULA (RFC 3339). The UI module, Add a game or the API must write it; Plan refuses to start without it.
   - State["cgnat"]="true" must come from the router code (6.5). Without it, "auto" puts the allow list on from the first start.
   - State["allowListOn"] is the adapter's own.
4. **New event kinds for the manager.** "allow_list_on" (with feed text) and "rcon_failed" (a warning). turned_away carries the account id; Allow is `WriteList(ctx, rec, "allowed", current + name)`.
5. **Choice to confirm: what happens when someone comes in from the internet while "auto" is still off.** The adapter adds everyone playing at that moment to the list, then turns it on over RCON, so nobody playing is thrown out. That includes the newcomer, who may be a stranger. The first time the list goes on before a start, everyone in Minecraft's user cache who isn't banned is added once; after that the list is the owner's.

**Departures from 5.2's wording and things worth knowing:**
6. Plan also sets `level-name` (so the world's own name matches) and `log-ips=true` (the "auto" rule reads the login address).
7. deaths.json sits beside the code and is embedded, not in testdata, because the Monitor needs it at runtime.
8. 5.2's Temurin URL wording is superseded: the checksum comes from the same Adoptium answer as the link.
9. Minecraft rewrites server.properties at every start with default permissions (0644 on Unix). The RCON password, new each run, therefore ends up in a world-readable file even though the plan writes it 0600.
10. `broadcast-rcon-to-ops` is left at the server's default, so the app's list changes show in the log and to admins in game as "[Rcon: …]".
11. The manager must not treat exit code 130 after its own Ctrl+C as a crash; the real JVM and the stand-in both exit with 130.
12. The macOS setup-checklist line about the RCON port belongs to the UI module, which I didn't build.
13. Registration is still to do: add internal/games/all/minecraft.go after the R2 cut and change TestOneGameRegistered at the same time.

After review:

1. The pending allow-list switch is sent on the first Tick after the replay ends. That relies on the manager not calling Tick while it is reading back a taken-over server's log (manager/server.go tick() returns early when run.replaying is set). If a future manager ticks during the replay, the switch is sent early, but that is harmless: turnOnAllowList still does nothing when the list is already on.
2. Until the manager's checks loop first calls Build() after the app starts, a stopped Minecraft's Join steps say "the same Minecraft version as the server" instead of naming the version. Spec 4.8's checks loop calls Build() at startup and every minute, which fills it in.
3. On Windows or Linux, a new Java that fails its check is deleted, so a later start or Repair downloads it again (about 59 MB). I chose that so the Status shows not installed instead of installed with a Java that can't start. Say if you'd rather keep the failed Java and add a state flag instead.

Review findings (fixed in the follow-up unless it says otherwise):
- should-fix: Taking over a running server replays the log and turns the allow list on again, re-adding players the owner removed
- should-fix: Join reads files through Build() while the manager holds its lock (Status, spec 4.8)
- should-fix: A Java that fails its check on update-on-start has already replaced the old Java, yet the plan still starts "the version that's installed"
- should-fix: Join differs from the contract and spec 4.9: Public is true, LAN entries have no port, the port comes from the settings rather than the running server

## G-terraria (mg/g-terraria)

1. **Install layout differs from the spec.** The server goes in server/Windows, server/Mac or server/Linux, not server/<version>/. A version folder would give the program a new path at every update. The firewall rule for the old path would then stop matching, so a start right after an update (perhaps from Discord with nobody at the PC) would wait for an admin prompt before friends could connect. The update still unpacks fully before swapping, and puts the old server back if the swap fails. Please confirm, and update spec 5.3.
2. **No world "evil" setting.** The 1.4.6.1 server has no config key or switch for it; when it makes a world from the config file, it picks one at random. The spec's newWorld field "evil" was dropped. Please update 5.3.
3. **Other small departures:**
   - `banlist=` is written into the config as an absolute path in server/, because its default would land inside the Mac bundle that updates replace.
   - A new warning event kind, "server_full".
   - Join.Address is the bare IP, because Terraria asks for the port separately.
   - Default new-world size is medium.
   - Order is 3 and Stage is preview.
4. **Contract gaps:**
   - Plan has no way to add an activity-feed note when update-on-start fails; it only logs. This is the same gap VM met.
   - After "exit" the server prints nothing more, so Monitor.End sets LastSave when "Saving before exit..." was seen, without an event.
   - M3's console stop must give ErrNoConsole after a takeover, as gametest does.
5. **Files I didn't create:** I did not add cmd/fakeserver/terraria_test.go, since it isn't in my ownership. The stand-in is exercised through the adapter's Conformance and takeover tests.
6. **Registration:** the package registers itself in init(), as Valheim does. When the lead adds internal/games/all/terraria.go after the R2 cut, TestOneGameRegistered in internal/manager must change, as VM noted.
7. **realserver.json** lists windows-latest and macos-15-intel, a 60-minute timeout and no setup script. The test also runs on Linux.
8. **Re-published zip:** Re-Logic re-uploaded terraria-server-1461.zip on 2026-11-24 under the same name. An update compares versions by name, so a same-name fix only arrives through Repair.

After review:

1. **Order scheme across the game streams.** I used section 5.n gives (n-1)*10, because it matches Valheim's 0 and Minecraft's 10 with no change to Minecraft. If you choose another scheme (for example the reviewer's section × 10, which would make Minecraft 20 and Terraria 30), both branches need their value changed. Either way, the other game streams (Necesse 30, Vintage Story 40 and so on) should be told the scheme before you add internal/games/all/terraria.go.
2. **Repair now asks for administrator rights again on Windows.** Because a repair forces the XNA reinstall when XNA is listed, every Repair of Terraria shows a Windows permission prompt, whatever the owner clicked it for. If they click No, the repair reports an error after the server files are already downloaded again. I think that is right for an action the owner starts themselves, but tell me if you'd rather only force XNA when its exit hint was last shown.
3. **Worth knowing for other games.** I didn't change `install.EnsurePrereq` (stream I owns it). It always runs `msiexec /i`, and it returns early whenever Installed says yes, so it has no repair mode. I worked around that inside the adapter: no Installed check, plus REINSTALL properties. Another game whose Repair needs to reinstall a prerequisite will hit the same gap.

Review findings (fixed in the follow-up unless it says otherwise):
- should-fix: The XNA exit hint tells the owner to click Repair, but Repair never reinstalls XNA in the one case where that hint can appear
- should-fix: Info.Order 3 puts Terraria ahead of Minecraft once both streams merge

## G-necesse (mg/g-necesse)

1. **Registration.** Add internal/games/all/necesse.go after the R2 cut. Its first merge must change or remove TestOneGameRegistered, as VM noted.
2. **Contract gap.** worldstore.Format.Detect and Place use worlds.Candidate, but TestImportRules (spec 2.1) forbids internal/worlds in adapters other than Valheim. So no game can write a Detect. Minecraft's "a folder holding level.dat" needs one too. Please either allow internal/worlds for adapters or add `type Candidate = worlds.Candidate` (and friends) to internal/game. With that, Necesse could check uploads for world.dat and leave LATEST_BACKUP1.zip out of the owner's own worlds.
3. **internal/steamcmd, stream I's code.** On Linux, SteamCMD starts its lines with ESC[0m, so `^Success! App` and `^ERROR!` never match. Every install there ends "SteamCMD finished without confirming the install". This doesn't affect the owner (Linux isn't offered), and the Valheim Mac cell passing suggests macOS has no such codes. But it stops any SteamCMD real-server check running on Linux. A one-line fix would strip `\x1b\[[0-9;]*m` before matching. Also, a fresh SteamCMD's first app_update fails with "Missing configuration"; the existing retry covers it.
4. **Spec 5.4 corrections**, if you want them in the spec (all explained in FACTS.md): stop with "stop" not "exit"; worlds in `<saves>/saves/worlds`; download about 100 MB; a memoryMB setting for `-Xmx`; no leading "-" in values; Deaths stays false; Ctrl+C after a takeover loses up to about a minute of play.
5. **G-minecraft.** Its stand-in's Match must not accept Necesse's `java ... -jar Server.jar -nogui -datadir ...`. A case-insensitive check for "server.jar" would, and the stand-in would then exit with "could start minecraft or necesse". My TestStandInMatch catches this once both are merged.
6. **Visible text.** The Ctrl+C step's Say text and ExitHint texts are new visible strings. None contain the app's name.
7. **Not done:** the UI module (phase 0' isn't frozen yet).
8. **Side effects on this machine:**
   - I installed lib32gcc-s1, lib32stdc++6 and libc6-i386 with apt so SteamCMD could run.
   - I removed ~/.config/Necesse and ~/Steam, which my runs had created.
   - Downloads and logs are under the scratchpad folders gnec/ and gnec-scripts/.

After review:

- Not my stream, please route to stream I (internal/steamcmd). The current Linux SteamCMD (version 1794537082, 2026-11-26) prints its success line as "\x1b[0mSuccess! App '1169370' fully installed." successLine in internal/steamcmd/steamcmd.go is anchored `(?i)^Success! App '<id>'`, so every install fails with "SteamCMD finished without confirming the install. Last message: \x1b[0m", even though the files are installed. bed004b's shipped reSuccess has the same anchor. Whether Windows and macOS SteamCMD now print these codes into a pipe is unknown. If they do, installed copies' Valheim updates are affected too. A likely fix is to strip ANSI codes from each line before matching (I tried a local-only test edit to confirm it; it was not allowed, so I didn't make it).
- Possible follow-up, not done: the data folder check refuses to start Necesse when the app's data folder path has a character the code page lacks. GetShortPathName (8.3 names) could make it work in that case. I left it out because the 8.3 path is Windows-only and I can't test it here.
- An imported world whose file name has such characters will be listed but refused at start. Worlds can't be renamed in the app (ErrCantRename), so the owner would have to rename the file by hand. Like the dash rule, worldstore's import path doesn't apply the adapter's NameProblem.
- The manager's onExit counts any exit with Stopping false as a crash (its restart still needs run.ready). So a start that fails with "Invalid world path" or a taken port is now reported both by the start job ("stopped while starting" with the hint) and by a "crashed" activity. That is the same as any other game failing during start.

Review findings (fixed in the follow-up unless it says otherwise):
- must-fix: The Monitor marks a crash or a taken-port exit as a requested stop, so the manager reports it as a normal stop and never restarts it
- should-fix: World names, passwords and server names outside Windows' ANSI code page reach Java mangled

## F-win (mg/fakeserver-win)

1. Recommendation for internal/platform/proc_windows.go, which isn't mine to change. On Windows, after Proc.Close, Exited() returns false and Kill() fails silently because h=0. Close also writes p.h with no lock, so it races with any Exited or Kill still running. On Unix, Close does nothing and Exited keeps returning true. That difference caused this failure. The manager is safe today because it waits on its own done channel and closes the handle only after the process ends. But any R3 code that polls Exited() after a reaper may have closed the handle will hit the same trap, and only on Windows. Suggested platform fix: Close remembers that the process has ended (or Exited returns true once the handle is gone), Close is guarded by a mutex, or at least the Close docs say "never use a Proc after Close". Interrupt blocking for 1 s on Windows also makes timing differ by system.
2. The "fakeserver: exiting now (code N)" line goes to stderr. A future personality whose plan uses its own log file instead of Capture won't show it in gametest's tail, because gametest only captures output for Capture plans. G streams copying demo.go get the line for free. Their Monitors should ignore lines starting "fakeserver: ", as demo's does with Hide.
3. After a takeover on Linux and macOS, the new timing note says "exit code -1". platform can't know the exit code of a process it didn't start there; this is expected, not an error.
4. Nothing was pushed or tagged. The only change is one commit on mg/fakeserver-win, on top of e236b73.

After review:

Nothing blocking, and nothing outside this stream's files was needed. When you add F-win's entry to docs/dev/multigame/stream-notes.md (your file), use the corrected timing expectation from the summary, not the "~1.0 s" line from my first handoff. Nothing was pushed or tagged, and no other branch was touched.

## G-vintagestory (mg/g-vintagestory)

1. **Registration:** add internal/games/all/vintagestory.go after the R2 cut, and change TestOneGameRegistered in internal/manager at the same time.
2. **Spec 5.5 corrections:**
   - The server writes serverconfig.json when its start finishes and after /serverconfig commands, not at shutdown.
   - A file with only the app's keys kills the server, so the Plan runs `--genconfig` first.
   - The password can't contain spaces (the server's own rule).
   - Ctrl+C saves (5.5 had it as "verify").
   - The download is about 64 MB plus 37 MB of .NET.
   - A world is three files (.vcdbs, -wal, -shm).
3. **Firewall and .NET:** Program() is runtime/dotnet-<version>/dotnet.exe, a path from install.DotNet that includes the version. I keep the installed .NET until a new server needs another channel or the owner clicks Repair. A repair can move Program(), and the firewall fix (stream A) must follow. Please confirm this trade-off (security patches for .NET only arrive with Repair or a channel change).
4. **Event kinds:** the adapter uses `wrong_password` and `load_failed`. Its RunState.Extra is empty.
5. **Order:** 40, following the (n-1)*10 scheme from the Terraria notes.
6. **No cmd/fakeserver/vintagestory_test.go:** I don't own that file. The stand-in is tested through the adapter's Conformance and plan tests.
7. **Same gap as the other streams:** Plan can't add an activity-feed entry, so a failed update-on-start only shows as a job step and in the log. Also: only one Vintage Story instance can exist this round, so update-on-start takes no lock.
8. **realserver.json** lists windows-latest only, with a 60 min timeout and no setup script. The test also runs on Linux.
9. **Scratch files:** I removed the downloads and the Linux runs' data folders from the scratchpad; logs remain in /tmp/claude-0/-home-user/41f121b4-97ec-5588-a00f-1184231def86/scratchpad/gvs. Nothing was pushed or tagged.

After review:

- The third parties' addresses, uids and mp token are still in the earlier commit a655814 on this branch. I was told not to rewrite history, so only you can remove them: squash the branch when merging, or rewrite that commit, if they must not reach the default branch at all. The new commit only removes them from the tree.
- No spec gaps or contract questions. I only changed files this stream owns: internal/games/vintagestory/* and cmd/fakeserver/vintagestory.go.

Review findings (fixed in the follow-up unless it says otherwise):
- should-fix: Exit hint promises to replace a settings file that the adapter keeps using
- should-fix: Issue-tracker testdata copies third parties' IP addresses, player ids and an auth token

## G-zomboid (mg/g-zomboid)

1. **Registration:** add internal/games/all/zomboid.go after the R2 cut, and change TestOneGameRegistered at the same time.
2. **Spec 5.6 and 2.5 corrections:**
   - B42's .bat has `-Djava.library.path=natives/`; the `natives/;natives/win64/;.` form in 5.6 is Build 41's.
   - Project Zomboid CAN bind RCON to loopback with -Drconlo. Spec 2.5 says it can't; the adapter uses it, and the role stays private.
   - The download is about 1.9 GB compressed and 7.1 GB installed. I set DownloadMB 1950 and DiskMB 10000; the spec says "download ~7.1 GB".
   - Order 50 follows Terraria's (n-1)*10 scheme.
   - Short is "Project Zomboid".
3. **A stable admin password** is created on the first Plan and kept in State["adminPassword"] in servers.json. The server re-applies it at every start. Nothing shows it yet: the UI could, so the owner can log in as "admin" in game.
4. **New event kinds** for the manager: "admin_prompt" (a warning) and "rcon_failed" (Minecraft uses the same kind).
5. **Presence comes from the 30-second RCON probe only.** The B42 console has no join lines, so a join or leave shows within 30 s. The 2.6 rule counts presence as proven once a probe answers.
6. **The real check downloads the server twice** (two gametest sessions). If that is too slow, drop the "facts" subtest, or give gametest (stream F) a way to reuse an install.
7. **Necesse's TestStandInMatch** runs a Zomboid-like command line. After both are merged, that line starts this stand-in, which asks for an admin password, ends with exit 1 because stdin is empty, and writes only under that test's temp folder (./Zomboid). It should stay green.
8. **Server defaults I left alone, for you to decide:** BackupsOnStart=true (the server keeps up to 5 startup zips of its own, using disk) and SaveWorldEveryMinutes=0 (no timed full saves; the server saves on quit and Ctrl+C).
9. **Unverified wording:** the Join steps ("choose Join and add a server") follow the wiki, not a real B42 client screen. The owner's first real session should check them.
10. **Linux:** the SteamCMD ANSI-prefix bug the Necesse stream reported (stream I) would also break a Linux run of this check. The Windows cell isn't affected.
11. **Third-party testdata:** testdata/real holds console logs and settings files copied from a third-party GitHub repo (pepenotti/zomboid-server), with the source in FACTS.md. They are the game's own output, but tell me if you'd rather keep only excerpts.

After review:

1. forLead 2 from the first round is still open: you need to pick one convention for Info.DownloadMB across all adapters, either the depot (installed) size or the compressed bytes Steam sends. I followed the spec and the other SteamCMD adapters (depot size, 7100). If you choose compressed sizes instead, Project Zomboid's figure is 1950 and FACTS.md has the exact bytes.
2. Spec 5.6 names only the install message. Showing that same sentence when the update before a start fails to read the launch files is my reading of the plain-English rule. Tell me if you want different wording there.
3. Necesse's TestStandInMatch relies on the Zomboid stand-in using ./Zomboid when it gets no -cachedir. That behaviour is now tested on this branch.

Review findings (fixed in the follow-up unless it says otherwise):
- should-fix: Install failure message adds technical detail to the spec's exact text
- should-fix: DownloadMB uses Steam's compressed size, unlike spec 5.6 and the other SteamCMD adapters
- should-fix: TestStandInMatch's home-folder check can never fail

## G-enshrouded (mg/g-enshrouded)

1. Registration: add internal/games/all/enshrouded.go after the R2 cut, and change TestOneGameRegistered at the same time. I didn't create the file.
2. Departures from spec 5.7, for the spec text (FACTS.md section 14):
   - Output is GameLog on <logs>/game/enshrouded_server.log, not Capture. mbround18's code says "the server writes nothing to its own stdout", and every hosting tool reads the log file. M3's GameLog tail must reopen the file after rotation, use OpenShared, and copy lines into the run's own log, as spec 2.4 says.
   - Presence is ByName, not count. Real logs show "Player 'X' logged in with Permissions:" and "Remove Entity for Player 'X'". The A2S count is the cross-check.
   - Join: friends can search Enshrouded's list by address (press T, type IP:port). The spec says they can't.
   - The top-level password key is removed rather than left empty.
   - The Friends group gets canEditWorld true (new in 1.0; the server refuses canEditBase without it).
   - New settings: a world-scope difficulty, and textChat and voiceChat.
   - The stop chain stays as specified: interrupt, wait 4 minutes, resend every 90 seconds.
3. New event kinds the manager should show or ignore: server_full, save_failed, load_failed, port_in_use, no_steam, bad_config. A2S answers emit count events with Hold.
4. Monitor Line.Time and event times come from Record.Started plus the time in each line, so the manager must pass the real Started in the record given to Watch.
5. Real-server cell: one install of about 8.1 GB plus temporary files on windows-latest, 90-minute timeout. Please check the runner's free disk space the first time you dispatch it. After it runs, copy its FACTS: lines into FACTS.md section 13. If stdout turns out to carry the log, GameLog could go back to Capture; GameLog keeps working either way.
6. Not done: the UI module (out of scope, as the brief said).
7. In my scratchpad I downloaded the server with my own SteamCMD copy, then deleted its 8 GB of data files. Nothing outside my files was changed.

After review:

Files outside this stream that need a decision:
1. internal/game/gametest/demo/demo.go (the Demo Game adapter) has the same empty-world bug. Its Validate calls s.worlds.NameProblem(world) and reads sv.Worlds[world], and args/Fingerprint use world exactly as given. So Validate(sv, "") returns "Give the world a name.", and Fingerprint(sv, "") differs from the plan's. If the manager ever drives the demo through startGame or Status, it hits this. The fix is the same: treat "" as sv.World.
2. The contract doesn't say that "" means the selected world. The Validate and Fingerprint comments in internal/game/contract.go (spec section 2, around lines 278 and 286) should say so.
3. gametest.Conformance never passes an empty world, so it can't catch this. I suggest Conformance also check that Validate(sv, "") returns no errors when a world is selected, and that Fingerprint(sv, "") equals plan.Fingerprint after a start. That would cover every adapter.

Review findings (fixed in the follow-up unless it says otherwise):
- must-fix: Validate and Fingerprint treat world "" as a world named "", so the app refuses every Enshrouded start and always says "restart to apply"
- should-fix: "SaveDataIssue" and "Shutdown" are messages the server sends players, not shutdown reasons it logs; the real save-failure shutdown gets no exit hint
