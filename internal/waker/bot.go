// Package waker is the companion that runs on the IP KVM next to the server
// computer (a JetKVM, which is always on). It stays in Discord, and when
// someone types /valheim it wakes the server computer with Wake-on-LAN. The
// app on the server computer hears the same signal when it's already awake,
// and starts Valheim either way.
package waker

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/wol"
)

// Config is the waker's settings file, which the app writes.
type Config struct {
	Token string `json:"token"`           // the Discord bot's token
	MAC   string `json:"mac"`             // the server computer's network adapter
	Query string `json:"query,omitempty"` // where it answers Steam queries (game port + 1), to see whether it's up
	Name  string `json:"name,omitempty"`  // the server's name, for replies
	Guild string `json:"guild,omitempty"` // the only Discord server it answers in; any when empty

	// For tests: Discord's addresses, and where wake packets go instead of
	// the local network's broadcast addresses.
	API     string   `json:"api,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

// LoadConfig reads the settings file.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(c.Token) == "" {
		return c, errors.New("no Discord bot token in the settings")
	}
	if _, err := wol.ParseMAC(c.MAC); err != nil {
		return c, err
	}
	return c, nil
}

// Status is what the waker is up to, for the app to show.
type Status struct {
	Version   string     `json:"version"`
	Connected bool       `json:"connected"`          // to Discord
	Bot       string     `json:"bot,omitempty"`      // its name there
	Servers   int        `json:"servers"`            // Discord servers it's in
	LastWake  *time.Time `json:"lastWake,omitempty"` // when it last woke the server computer
	WokenBy   string     `json:"wokenBy,omitempty"`
	Error     string     `json:"error,omitempty"`
	Updated   time.Time  `json:"updated"`
}

// commandName is what people type: /valheim.
const commandName = "valheim"

var command = map[string]any{
	"name":        commandName,
	"description": "Wake the server computer and start the Valheim server",
	"type":        1,
}

// Bot is the waker in Discord.
type Bot struct {
	Config     Config
	StatusFile string // where to keep its status; none when empty
	Version    string

	log     *log.Logger
	http    *http.Client
	api     string
	gateway string
	send    func(mac net.HardwareAddr, tag []byte, targets []string) error
	query   func(context.Context, string) (Info, error)
	// How often wake packets go out, for how long, and how long to wait for
	// Valheim to answer.
	wakeEvery, wakeFor, waitFor time.Duration

	mu         sync.Mutex
	appID      string
	sessionID  string
	resumeURL  string
	seq        int64
	guilds     map[string]bool // where /valheim is set up (true) or being set up
	waking     time.Time       // when the current wake began; zero when none
	wokenBy    string
	queryWorks bool // Valheim has answered a query at least once
	status     Status
}

// New makes a bot with the settings.
func New(cfg Config, logger *log.Logger) *Bot {
	b := &Bot{
		Config: cfg, Version: "dev", log: logger,
		http:    &http.Client{Timeout: 20 * time.Second},
		api:     apiURL,
		gateway: gatewayURL,
		send:    wol.Send,
		query:   Query,
		guilds:  map[string]bool{},

		wakeEvery: 10 * time.Second,
		wakeFor:   3 * time.Minute,
		waitFor:   10 * time.Minute,
	}
	if cfg.API != "" {
		b.api = strings.TrimRight(cfg.API, "/")
	}
	if cfg.Gateway != "" {
		b.gateway = strings.TrimRight(cfg.Gateway, "/")
	}
	return b
}

type user struct {
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
}

func (u user) name() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// ErrToken is Discord not accepting the bot's token.
var ErrToken = errors.New("Discord doesn’t accept the bot token. Copy it again from the Discord Developer Portal (Bot → Reset Token)")

// Check asks Discord who the bot is, which also checks its token.
func (b *Bot) Check(ctx context.Context) (string, error) {
	var me user
	if err := b.rest(ctx, http.MethodGet, "/users/@me", true, nil, &me); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return "", ErrToken
		}
		return "", err
	}
	return me.name(), nil
}

// Application asks Discord for the bot's application ID, which the link
// that adds the bot to a Discord server needs.
func (b *Bot) Application(ctx context.Context) (string, error) {
	var app struct {
		ID string `json:"id"`
	}
	if err := b.rest(ctx, http.MethodGet, "/applications/@me", true, nil, &app); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return "", ErrToken
		}
		return "", err
	}
	if app.ID == "" {
		return "", errors.New("Discord didn’t say which application the bot belongs to")
	}
	return app.ID, nil
}

// Invite is the link that adds the bot to a Discord server: guild, when
// given, is the only one it offers. The bot needs no permissions, only
// to be there to add /valheim.
func Invite(appID, guild string) string {
	q := url.Values{"client_id": {appID}, "scope": {"bot applications.commands"}, "permissions": {"0"}}
	if guild != "" {
		q.Set("guild_id", guild)
		q.Set("disable_guild_select", "true")
	}
	return "https://discord.com/oauth2/authorize?" + q.Encode()
}

// Run stays in Discord until ctx ends, reconnecting whenever the connection
// drops.
func (b *Bot) Run(ctx context.Context) error {
	b.setStatus(func(s *Status) {})
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		wait := backoff
		name, err := b.Check(ctx)
		if err == nil {
			b.setStatus(func(s *Status) { s.Bot, s.Error = name, "" })
			started := time.Now()
			err = b.session(ctx)
			if ctx.Err() != nil {
				break
			}
			b.log.Printf("Discord connection ended: %v", err)
			if time.Since(started) > time.Minute {
				backoff = 2 * time.Second
			}
			wait = backoff
			if errors.Is(err, errReconnect) {
				wait = time.Second
			}
		} else {
			b.log.Printf("can't reach Discord: %v", err)
			if errors.Is(err, ErrToken) {
				wait = 10 * time.Minute // it won't work until someone changes the token
			}
		}
		b.setStatus(func(s *Status) {
			s.Connected = false
			if err != nil && !errors.Is(err, errReconnect) {
				s.Error = err.Error()
			}
		})
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
	b.setStatus(func(s *Status) { s.Connected = false })
	return nil
}

// dispatch handles an event from Discord. Work that takes a while gets its
// own goroutine, so heartbeats aren't held up; it lives on after a
// reconnect.
func (b *Bot) dispatch(ctx context.Context, event string, d json.RawMessage) {
	switch event {
	case "READY":
		var r struct {
			SessionID   string `json:"session_id"`
			ResumeURL   string `json:"resume_gateway_url"`
			Application struct {
				ID string `json:"id"`
			} `json:"application"`
			User user `json:"user"`
		}
		json.Unmarshal(d, &r)
		b.mu.Lock()
		b.sessionID, b.resumeURL, b.appID = r.SessionID, strings.TrimRight(r.ResumeURL, "/"), r.Application.ID
		b.mu.Unlock()
		b.setStatus(func(s *Status) { s.Connected, s.Bot, s.Error = true, r.User.name(), "" })
		b.log.Printf("in Discord as %s", r.User.name())
	case "RESUMED":
		b.setStatus(func(s *Status) { s.Connected, s.Error = true, "" })
	case "GUILD_CREATE":
		var g struct {
			ID string `json:"id"`
		}
		json.Unmarshal(d, &g)
		go b.register(ctx, g.ID)
	case "INTERACTION_CREATE":
		go b.interaction(ctx, d)
	}
}

// register sets up /valheim in a Discord server. That's instant, unlike a
// command for all servers at once.
func (b *Bot) register(ctx context.Context, guild string) {
	allowed := b.allowed(guild)
	b.mu.Lock()
	app := b.appID
	_, done := b.guilds[guild]
	if allowed && !done {
		b.guilds[guild] = false // under way
	}
	b.mu.Unlock()
	if done || app == "" || guild == "" {
		return
	}
	path := "/applications/" + app + "/guilds/" + guild + "/commands"
	if !allowed {
		// It may be there from before the app limited the bot to one
		// Discord server: take it away.
		if err := b.rest(ctx, http.MethodPut, path, true, []any{}, nil); err != nil {
			b.log.Printf("removing /valheim from a Discord server it isn't for: %v", err)
		}
		return
	}
	err := b.rest(ctx, http.MethodPut, path, true, []any{command}, nil)
	b.mu.Lock()
	if err != nil {
		delete(b.guilds, guild) // try again next time
	} else {
		b.guilds[guild] = true
	}
	n := 0
	for _, ok := range b.guilds {
		if ok {
			n++
		}
	}
	b.mu.Unlock()
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
			err = errors.New("Discord didn’t let the bot add /valheim: add it again with the link from the app")
		}
		b.log.Printf("setting up /valheim: %v", err)
		b.setStatus(func(s *Status) { s.Error = err.Error() })
		return
	}
	b.setStatus(func(s *Status) { s.Servers = n })
}

// allowed reports whether the bot answers in a Discord server.
func (b *Bot) allowed(guild string) bool {
	return b.Config.Guild == "" || guild == b.Config.Guild
}

// interaction answers /valheim.
func (b *Bot) interaction(ctx context.Context, raw json.RawMessage) {
	var in struct {
		ID            string `json:"id"`
		Token         string `json:"token"`
		Type          int    `json:"type"`
		ApplicationID string `json:"application_id"`
		GuildID       string `json:"guild_id"`
		Data          struct {
			Name string `json:"name"`
		} `json:"data"`
		Member *struct {
			User user   `json:"user"`
			Nick string `json:"nick"`
		} `json:"member"`
		User *user `json:"user"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Type != 2 || in.Data.Name != commandName {
		return
	}
	if !b.allowed(in.GuildID) {
		b.replyOnly(ctx, in.ID, in.Token, "🔒 This only works in the Discord server the Valheim server belongs to.")
		return
	}
	who := "someone"
	switch {
	case in.Member != nil && in.Member.Nick != "":
		who = in.Member.Nick
	case in.Member != nil:
		who = in.Member.User.name()
	case in.User != nil:
		who = in.User.name()
	}
	name := "The server"
	if b.Config.Name != "" {
		name = "**" + discord.Escape(b.Config.Name) + "**"
	}

	if info, ok := b.up(ctx); ok {
		b.reply(ctx, in.ID, in.Token, fmt.Sprintf("🟢 %s is already online%s.", name, playing(info)))
		return
	}
	b.mu.Lock()
	since, by := b.waking, b.wokenBy
	if since.IsZero() {
		b.waking, b.wokenBy = time.Now(), who
	}
	b.mu.Unlock()
	if !since.IsZero() {
		b.reply(ctx, in.ID, in.Token, fmt.Sprintf("⏰ The server computer is already waking up: %s asked %s.",
			discord.Escape(by), ago(time.Since(since))))
		return
	}
	b.reply(ctx, in.ID, in.Token, fmt.Sprintf("⏰ Waking up the server computer for %s… This message changes once Valheim is ready, usually within a few minutes.", discord.Escape(who)))
	b.wake(ctx, in.ApplicationID, in.Token, name, who)
}

func playing(info Info) string {
	switch info.Players {
	case 0:
		return ". Nobody’s playing yet"
	case 1:
		return ", with 1 playing"
	}
	return fmt.Sprintf(", with %d playing", info.Players)
}

func ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < 2*time.Minute {
		return "a minute ago"
	}
	return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
}

// up reports whether Valheim answers.
func (b *Bot) up(ctx context.Context) (Info, bool) {
	if b.Config.Query == "" {
		return Info{}, false
	}
	qctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	info, err := b.query(qctx, b.Config.Query)
	if err != nil {
		return Info{}, false
	}
	b.mu.Lock()
	b.queryWorks = true
	b.mu.Unlock()
	return info, true
}

// wake sends Wake-on-LAN packets for a few minutes (the computer may be
// asleep, or awake with the server stopped: the app starts it either way),
// then updates the reply once Valheim answers.
func (b *Bot) wake(ctx context.Context, appID, token, name, who string) {
	defer func() {
		b.mu.Lock()
		b.waking = time.Time{}
		b.mu.Unlock()
	}()
	now := time.Now()
	b.setStatus(func(s *Status) { s.LastWake, s.WokenBy = &now, who })
	b.log.Printf("waking the server computer for %s", who)
	mac, _ := wol.ParseMAC(b.Config.MAC)
	targets := b.Config.Targets
	if len(targets) == 0 {
		targets = wol.Broadcasts()
	}
	// The same tag on every packet of this wake: the app starts the server
	// once for it, however many it hears.
	tag := make([]byte, 6)
	cryptorand.Read(tag)
	send := func() {
		if err := b.send(mac, tag, targets); err != nil {
			b.log.Print(err)
		}
	}
	send()
	tick := time.NewTicker(b.wakeEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		elapsed := time.Since(now)
		if elapsed < b.wakeFor {
			send()
		}
		if info, ok := b.up(ctx); ok {
			b.edit(ctx, appID, token, fmt.Sprintf("✅ %s is online%s. Have fun!", name, playing(info)))
			return
		}
		if elapsed >= b.waitFor {
			b.mu.Lock()
			works := b.queryWorks
			b.mu.Unlock()
			msg := "⏰ The server computer was woken up and should start Valheim. If nobody can join in a few minutes, someone may need to look at it through the KVM."
			if works {
				msg = fmt.Sprintf("⚠️ The server computer was woken up, but Valheim didn’t answer within %d minutes. Someone may need to look at it through the KVM.", int(b.waitFor.Minutes()))
			}
			b.edit(ctx, appID, token, msg)
			return
		}
	}
}

// noMentions keeps replies from pinging anyone.
var noMentions = map[string]any{"parse": []string{}}

func (b *Bot) reply(ctx context.Context, id, token, text string) {
	b.answer(ctx, id, token, map[string]any{"content": text, "allowed_mentions": noMentions})
}

// replyOnly answers so only the person who asked sees it.
func (b *Bot) replyOnly(ctx context.Context, id, token, text string) {
	b.answer(ctx, id, token, map[string]any{"content": text, "allowed_mentions": noMentions, "flags": 64})
}

func (b *Bot) answer(ctx context.Context, id, token string, data map[string]any) {
	err := b.rest(ctx, http.MethodPost, "/interactions/"+id+"/"+token+"/callback", false,
		map[string]any{"type": 4, "data": data}, nil)
	if err != nil {
		b.log.Printf("answering /valheim: %v", err)
	}
}

func (b *Bot) edit(ctx context.Context, appID, token, text string) {
	if appID == "" {
		b.mu.Lock()
		appID = b.appID
		b.mu.Unlock()
	}
	err := b.rest(ctx, http.MethodPatch, "/webhooks/"+appID+"/"+token+"/messages/@original", false,
		map[string]any{"content": text, "allowed_mentions": noMentions}, nil)
	if err != nil {
		b.log.Printf("updating the /valheim answer: %v", err)
	}
}

// setStatus changes the status and saves it for the app.
func (b *Bot) setStatus(change func(*Status)) {
	b.mu.Lock()
	change(&b.status)
	b.status.Version, b.status.Updated = b.Version, time.Now().UTC()
	data, err := json.Marshal(b.status)
	b.mu.Unlock()
	if err != nil || b.StatusFile == "" {
		return
	}
	tmp := b.StatusFile + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, filepath.Clean(b.StatusFile))
	}
}
