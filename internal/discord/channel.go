package discord

// Reading a channel with a bot. A webhook can only post, so the "still
// playing?" rule (a game with no way to tell whether anyone is on asks in
// its channel whether it should stay on) reads the channel with the
// waker's bot token instead. Only who sent each message is read, never
// what it says, so the bot needs no special permission: View Channel and
// Read Message History in that channel are enough.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// API is where Discord's REST API is, for a bot.
const API = "https://discord.com/api/v10"

// Bot reads channels with a bot's token.
type Bot struct {
	Token  string
	API    string       // Discord's API; API when empty (tests use a stand-in)
	Client *http.Client // nil: one that gives up after 20 s
}

// Message is a message in a channel, as far as the app needs it: its id
// and who sent it.
type Message struct {
	ID        string `json:"id"`
	WebhookID string `json:"webhook_id,omitempty"` // set when a webhook posted it (this app's own posts)
	Author    struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot,omitempty"`
	} `json:"author"`
}

// FromPerson reports whether someone typed the message, rather than a bot
// (the waker answering a slash command) or a webhook (this app's own posts).
func (msg Message) FromPerson() bool { return msg.WebhookID == "" && !msg.Author.Bot }

// Messages reads up to limit messages (1 to 100) posted in the channel
// after the message with the id after (a snowflake; SnowflakeAt makes one
// for a time). Discord gives the ones straight after it; their order isn't
// relied on. A channel the bot isn't allowed to read answers with an
// *Error whose Status is 403.
func (b Bot) Messages(ctx context.Context, channel, after string, limit int) ([]Message, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	q.Set("limit", strconv.Itoa(min(max(limit, 1), 100)))
	var out []Message
	if err := b.get(ctx, "/channels/"+url.PathEscape(channel)+"/messages?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ChannelName is the channel's name ("windrose", shown as #windrose), for
// the control panel's "The bot can't read #windrose".
func (b Bot) ChannelName(ctx context.Context, channel string) (string, error) {
	var ch struct {
		Name string `json:"name"`
	}
	if err := b.get(ctx, "/channels/"+url.PathEscape(channel), &ch); err != nil {
		return "", err
	}
	return ch.Name, nil
}

func (b Bot) get(ctx context.Context, path string, out any) error {
	if b.Token == "" {
		return &Error{Msg: "There's no Discord bot to read the channel with."}
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	api := strings.TrimRight(b.API, "/")
	if api == "" {
		api = API
	}
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, nil)
		if err != nil {
			return &Error{Msg: "That isn't a Discord channel."}
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Authorization", "Bot "+b.Token)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Msg: "Couldn't reach Discord. Check that this computer is connected to the internet.", Temporary: true}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch code := resp.StatusCode; {
		case code/100 == 2:
			if json.Unmarshal(body, out) != nil {
				return &Error{Msg: "Discord's answer couldn't be read.", Temporary: true, Status: code}
			}
			return nil
		case code == http.StatusTooManyRequests:
			// Reads are rare (every few minutes), so one short wait is enough.
			if wait := retryAfter(resp.Header, body); try == 0 && wait <= 10*time.Second {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return &Error{Msg: "Discord asked the app to wait before reading again.", Temporary: true, Status: code}
		case code == http.StatusUnauthorized:
			return &Error{Msg: "Discord didn't accept the bot's token.", Status: code}
		case code == http.StatusForbidden:
			return &Error{Msg: "The bot isn't allowed to read that channel.", Status: code}
		case code == http.StatusNotFound:
			return &Error{Msg: "Discord doesn't know that channel. It may have been deleted.", Status: code}
		case code >= 500:
			return &Error{Msg: fmt.Sprintf("Discord had a problem (error %d).", code), Temporary: true, Status: code}
		default:
			return &Error{Msg: fmt.Sprintf("Discord didn't let the bot read the channel (error %d).", code), Status: code}
		}
	}
}

// discordEpoch is when Discord's ids begin: the first moment of 2015, in
// milliseconds since 1970.
const discordEpoch = 1420070400000

// SnowflakeAt is the smallest id a message made at t can have. Discord's
// ids (snowflakes) start with the time they were made, so "the messages
// after SnowflakeAt(t)" are those posted after t, without first reading
// the channel to find its newest message.
func SnowflakeAt(t time.Time) string {
	ms := t.UnixMilli() - discordEpoch
	if ms < 0 {
		ms = 0
	}
	return strconv.FormatUint(uint64(ms)<<22, 10)
}

// Later reports whether the id a is newer than the id b. An id that isn't
// a number is never newer, and any id is newer than one that isn't.
func Later(a, b string) bool {
	x, err := strconv.ParseUint(a, 10, 64)
	if err != nil {
		return false
	}
	y, err := strconv.ParseUint(b, 10, 64)
	return err != nil || x > y
}
