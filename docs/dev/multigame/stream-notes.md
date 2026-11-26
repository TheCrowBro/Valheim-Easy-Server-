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
