// Package discord posts messages in a Discord channel through a webhook: a
// link made in the channel's settings that lets an app post there without
// a bot.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Prefix is how webhook links start.
const Prefix = "https://discord.com/api/webhooks/"

var (
	reWebhookPath = regexp.MustCompile(`^/api(?:/v\d+)?/webhooks/\d+/[\w-]+/?$`)
	hosts         = map[string]bool{
		"discord.com": true, "ptb.discord.com": true, "canary.discord.com": true,
		"discordapp.com": true, "ptb.discordapp.com": true, "canary.discordapp.com": true,
	}
)

// Problem returns why link isn't a webhook link, or "". People paste the
// channel's own link or an invite by mistake, so those get their own hint.
func Problem(link string) string {
	if link == "" {
		return "Paste the webhook link from Discord."
	}
	notWebhook := "That isn't a Discord webhook link. Those start with " + Prefix
	u, err := url.Parse(link)
	if err != nil {
		return notWebhook
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "discord.gg" || (hosts[host] && strings.HasPrefix(u.Path, "/invite/")):
		return "That's an invite link. Paste the channel's webhook link: it starts with " + Prefix
	case hosts[host] && strings.HasPrefix(u.Path, "/channels/"):
		return "That's a link to the channel. Paste its webhook link: it starts with " + Prefix
	case !hosts[host] || u.Scheme != "https" || !reWebhookPath.MatchString(u.Path):
		return notWebhook
	}
	return ""
}

// Escape stops Discord from reading s as formatting, so a name with
// underscores or stars shows as typed.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\*_~`|[<", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Error is a post that didn't work. Temporary ones may work if tried again
// later; the others need a new webhook link.
type Error struct {
	Msg       string
	Temporary bool
}

func (e *Error) Error() string { return e.Msg }

// Post sends a message to the webhook at link. Mentions in it never ping
// anyone and links don't get previews. client may be nil.
func Post(ctx context.Context, client *http.Client, link, content string) error {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	u, err := url.Parse(link)
	if err != nil {
		return &Error{Msg: "The webhook link isn't valid."}
	}
	q := u.Query()
	q.Set("wait", "true") // otherwise a message Discord drops isn't reported
	u.RawQuery = q.Encode()
	body, err := json.Marshal(map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
		"flags":            1 << 2, // SUPPRESS_EMBEDS
	})
	if err != nil {
		return err
	}
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/TheCrowBro/Valheim-Easy-Server-, 1)")
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Msg: "Couldn't reach Discord. Check that this computer is connected to the internet.", Temporary: true}
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			// Discord asks clients to slow down when they post too often.
			wait := retryAfter(resp.Header, b)
			if try == 0 && wait <= 30*time.Second {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return &Error{Msg: "Discord asked the app to wait before posting again.", Temporary: true}
		}
		return statusError(resp.StatusCode, b)
	}
}

func statusError(code int, body []byte) error {
	var reply struct {
		Message string `json:"message"`
	}
	json.Unmarshal(body, &reply)
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound:
		return &Error{Msg: "Discord says the webhook link doesn't work. The webhook may have been deleted: make a new one and paste its link in Settings."}
	case code >= 500:
		return &Error{Msg: fmt.Sprintf("Discord had a problem (error %d).", code), Temporary: true}
	case reply.Message != "":
		return &Error{Msg: fmt.Sprintf("Discord didn't accept the message (%s).", strings.TrimSuffix(reply.Message, "."))}
	}
	return &Error{Msg: fmt.Sprintf("Discord didn't accept the message (error %d).", code)}
}

// retryAfter reads how long Discord wants the app to wait.
func retryAfter(h http.Header, body []byte) time.Duration {
	var reply struct {
		RetryAfter float64 `json:"retry_after"` // seconds
	}
	secs := 1.0
	if json.Unmarshal(body, &reply) == nil && reply.RetryAfter > 0 {
		secs = reply.RetryAfter
	} else if v, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil && v > 0 {
		secs = v
	}
	return time.Duration(secs * float64(time.Second))
}
