package waker

// One slash command per game (spec 6.4). An R3 app writes the commands it
// wants in Config.Games; a waker that finds Games there registers exactly
// those and ignores Query and Name. An app from R2 or before never writes
// Games, and a waker then registers /valheim from Query and Name as it
// always did, so either side's older version keeps working.
//
// Phase 0' made the type and the field; stream WK makes the bot use them.

// GameCommand is one game's slash command.
type GameCommand struct {
	ID          string `json:"id"`              // the instance id, sent in the wake packet after the tag (wol.PacketFor)
	Command     string `json:"command"`         // "minecraft": the command's name, lowercase [a-z0-9_-]{1,32}
	Description string `json:"description"`     // "Wake the server computer and start Minecraft"
	Name        string `json:"name"`            // the server's name, for replies
	Channel     string `json:"channel"`         // the only Discord channel it works in; never empty
	Query       string `json:"query,omitempty"` // its A2S address, when the game answers A2S queries (Valheim)
}
