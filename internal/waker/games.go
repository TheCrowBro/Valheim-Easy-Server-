package waker

// One slash command per game (spec 6.4). An R3 app writes the commands it
// wants in Config.Games; a waker that finds Games there registers exactly
// those and ignores Query and Name. An app from R2 or before never writes
// Games, and a waker then registers /valheim from Query and Name as it
// always did, so either side's older version keeps working.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"valheim-easy-server/internal/discord"
)

// GameCommand is one game's slash command.
type GameCommand struct {
	ID          string `json:"id"`              // the instance id, sent in the wake packet after the tag (wol.PacketFor)
	Command     string `json:"command"`         // "minecraft": the command's name, lowercase [a-z0-9_-]{1,32}
	Description string `json:"description"`     // "Wake the server computer and start Minecraft"
	Name        string `json:"name"`            // the server's name, for replies
	Channel     string `json:"channel"`         // the only Discord channel it works in; never empty
	Query       string `json:"query,omitempty"` // its A2S address, when the game answers A2S queries (Valheim)
}

// slash is a command the bot registers and answers.
type slash struct {
	GameCommand
	// legacy is the /valheim of a settings file from an app of R2 or
	// before, which has no Games: it keeps today's texts, works in any
	// channel, and its wake packets name no game (today's packet), which
	// every app reads as a request for Valheim.
	legacy bool
}

// reCommand is what Discord takes as the name of a command (lower case
// only: Discord refuses capitals in a slash command's name).
var reCommand = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// maxDescription is the longest description Discord takes for a command.
const maxDescription = 100

// commands are the slash commands to register, from the settings file:
// the Games an R3 app wrote, or /valheim from Query and Name when an older
// app wrote it. A command Discord wouldn't take, a second one with the
// same name, or one without its game or its channel is left out (skipped
// says why), so one bad entry never costs the others theirs.
func (c Config) commands() (cmds []slash, skipped []string) {
	if c.Games == nil {
		return []slash{{GameCommand: GameCommand{Command: commandName, Description: command["description"].(string),
			Name: c.Name, Query: c.Query}, legacy: true}}, nil
	}
	seen := map[string]bool{}
	for _, g := range *c.Games {
		why := ""
		switch {
		case !reCommand.MatchString(g.Command):
			why = "isn't a name Discord takes for a command"
		case seen[g.Command]:
			why = "is there twice"
		case strings.TrimSpace(g.ID) == "":
			why = "doesn't say which game it starts"
		case strings.TrimSpace(g.Channel) == "":
			why = "doesn't say which channel it works in"
		}
		if why != "" {
			skipped = append(skipped, fmt.Sprintf("/%s %s", g.Command, why))
			continue
		}
		seen[g.Command] = true
		g.Description = describe(g)
		cmds = append(cmds, slash{GameCommand: g})
	}
	return cmds, skipped
}

// describe is the command's description as Discord takes it: never empty,
// at most 100 characters.
func describe(g GameCommand) string {
	d := strings.TrimSpace(g.Description)
	if d == "" {
		d = "Wake the server computer and start " + strings.TrimSpace(g.Name)
		d = strings.TrimSpace(d)
	}
	if utf8.RuneCountInString(d) > maxDescription {
		d = string([]rune(d)[:maxDescription-1]) + "…"
	}
	return d
}

// discord is the command as Discord's API takes it.
func (s slash) discord() map[string]any {
	if s.legacy {
		return command
	}
	return map[string]any{"name": s.Command, "description": s.Description, "type": 1}
}

// title is how replies name the game: its server's name in bold, or the
// command's name when the app didn't give one.
func (s slash) title() string {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		if s.legacy {
			return "The server" // as always
		}
		name = strings.ToUpper(s.Command[:1]) + s.Command[1:]
	}
	return "**" + discord.Escape(name) + "**"
}

// label names the commands in log lines and errors: "/valheim",
// "/valheim and /minecraft", or "its commands" when there are none.
func label(cmds []slash) string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = "/" + c.Command
	}
	switch len(names) {
	case 0:
		return "its commands"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Texts of a game's command (not the legacy /valheim, whose texts are in
// bot.go and stay as they were). They name the game's server.

func wakingText(s slash) string {
	return fmt.Sprintf("⏰ Waking the server computer and starting %s. It posts here when it's online.", s.title())
}

func stillWakingText(s slash, by, ago string) string {
	return fmt.Sprintf("⏰ %s is already being started: %s asked %s.", s.title(), discord.Escape(by), ago)
}

func wrongChannelText(s slash) string {
	return fmt.Sprintf("🔒 /%s works in <#%s>.", s.Command, s.Channel)
}

const wrongServerText = "🔒 This only works in the Discord server the game servers belong to."

// timeoutText is the answer's last change when a game that answers
// queries (Valheim) still doesn't after waitFor. The app posts in the same
// channel when it can't start a game, so this points there.
func timeoutText(s slash, waited string) string {
	return fmt.Sprintf("⚠️ %s didn't answer within %s. If the app couldn't start it, it says why above; otherwise someone may need to look at it through the KVM.", s.title(), waited)
}
