package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Webhook asks Discord which Discord server and which channel the webhook at
// link posts in, and returns their IDs (spec 4.6 and 6.4: the "still
// playing?" rule reads that channel, and a game's slash command works only
// there). It is Guild with the channel too; client may be nil.
//
// Added in phase 0' for streams D and WK, who own this package from then on.
func Webhook(ctx context.Context, client *http.Client, link string) (guild, channel string, err error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", "", &Error{Msg: "The webhook link isn't valid."}
	}
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/TheCrowBro/Valheim-Easy-Server-, 1)")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", &Error{Msg: "Couldn't reach Discord. Check that this computer is connected to the internet.", Temporary: true}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return "", "", statusError(resp.StatusCode, b)
	}
	var hook struct {
		GuildID   string `json:"guild_id"`
		ChannelID string `json:"channel_id"`
	}
	if json.Unmarshal(b, &hook) != nil || hook.GuildID == "" {
		return "", "", &Error{Msg: "Discord didn't say which Discord server the webhook is in."}
	}
	if hook.ChannelID == "" {
		return "", "", &Error{Msg: "Discord didn't say which channel the webhook posts in."}
	}
	return hook.GuildID, hook.ChannelID, nil
}
