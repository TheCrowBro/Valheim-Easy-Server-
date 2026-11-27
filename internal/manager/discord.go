package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
)

// Posting the join code in a Discord channel. Messages wait in a queue and
// go out one at a time, so "stopped" never overtakes the "online" before it.

// discordRetries are the waits before trying a message again while Discord
// can't be reached.
var discordRetries = []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}

// discordPost is a message waiting to be posted.
type discordPost struct {
	link string
	text func() string   // built when it's sent: the address may need looking up
	done func(err error) // gets the outcome; err is nil when it was posted
}

// DiscordInfo says whether the join code made it to Discord.
type DiscordInfo struct {
	Posted *time.Time `json:"posted,omitempty"`
	Error  string     `json:"error,omitempty"`
	Daily  bool       `json:"daily,omitempty"` // who died the most is posted each night
}

// Why the server stopped, for the Discord message.
type stopReason int

const (
	stopStopped stopReason = iota
	stopRestart
	stopRest
	stopCrash
	stopCrashRestart
	// R3 (spec 6.3); onExit picks them from serverRun.autoReason.
	stopIdle  // auto-stop: "nobody played for an hour" (A sets it, autoStopText)
	stopAsked // the "still playing?" rule: its "Turning … off" line is the only stopped post (D)
)

// announceLocked returns the online message once the running server is
// ready (for a game with a join code, once it has one, and again if the
// code changes); ok is false when there's nothing new to post. The caller
// queues it and saves the run record.
func (m *Manager) announceLocked(run *serverRun) (p discordPost, ok bool) {
	inst := run.inst
	s := m.settings
	if !s.Discord || inst.run != run || !run.ready || run.stopping || run.replaying || run.afterPending {
		return p, false // the game's work after the start (the mods code) comes within seconds
	}
	saved := inst.saved()
	st := run.state()
	// What friends join with: what the running server started with.
	j := inst.srv.Join(game.JoinInput{Rec: &run.rec, State: &st, Saved: saved})
	code := j.Code
	key := code
	if key == "" {
		if j.How == "code" {
			return p, false // the code comes a few seconds after the server is ready
		}
		key = "online"
	}
	if run.rec.Announced == key {
		return p, false
	}
	run.rec.Announced = key
	run.rec.Posted, run.rec.PostError = time.Time{}, ""
	run.discordAt, run.discordErr = time.Time{}, ""

	server := run.rec.Server
	if server == "" { // started by an older version of the app
		server = saved.Server["serverName"]
	}
	in := game.OnlineInput{Server: server, World: run.rec.World, Join: j, Reminder: m.reminderLocked(inst, run)}
	if s.DiscordPassword {
		in.Password = j.Password
	}
	rec := run.rec
	in.Rec = &rec
	what := "the join code"
	if code == "" {
		what = "that the server is online"
	}
	texter, _ := inst.srv.(game.OnlineTexter)
	return discordPost{
		link: s.DiscordWebhook,
		text: func() string {
			if in.Join.Code == "" {
				in.Join.Address = m.publicAddress(in.Join.Port)
			}
			if texter != nil {
				return texter.Online(in)
			}
			return onlineText(in)
		},
		done: func(err error) {
			m.mu.Lock()
			current := run.rec.Announced == key
			if current {
				if err == nil {
					run.discordAt = time.Now()
				} else {
					run.discordErr = err.Error()
				}
				run.rec.Posted, run.rec.PostError = run.discordAt, run.discordErr
			}
			m.mu.Unlock()
			if current {
				m.saveRunInfo(run) // so an app that takes over can say how it went
			}
			if err != nil {
				m.addActivity("warning", "", fmt.Sprintf("Couldn't post %s in Discord. %v", what, err))
			} else {
				m.addActivity("discord", "", fmt.Sprintf("Posted %s in Discord.", what))
			}
		},
	}, true
}

// onlineText is the online post of a game without its own wording.
func onlineText(in game.OnlineInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🟢 **%s** is online\n", discord.Escape(in.Server))
	fmt.Fprintf(&b, "World: %s\n", discord.Escape(in.World))
	switch {
	case in.Join.Code != "":
		label := in.Join.CodeLabel
		if label == "" {
			label = "Join code"
		}
		fmt.Fprintf(&b, "%s: **%s**\n", label, in.Join.Code)
	case in.Join.Address != "":
		fmt.Fprintf(&b, "Address: **%s**\n", in.Join.Address)
	}
	if in.Password != "" {
		fmt.Fprintf(&b, "Password: ||%s||\n", discord.Escape(in.Password))
	}
	if len(in.Reminder) > 0 {
		fmt.Fprintf(&b, "📢 **Before you join:** %s.\n", strings.Join(in.Reminder, ", "))
	}
	if in.Join.Hint != "" {
		b.WriteString("-# " + in.Join.Hint)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// stoppedPostLocked returns the message saying the server stopped, if the
// channel was told it was online.
func (m *Manager) stoppedPostLocked(run *serverRun, why stopReason) (p discordPost, ok bool) {
	s := m.settings
	if !s.Discord || !s.DiscordStopped || run.rec.Announced == "" {
		return p, false
	}
	text := stoppedText(run.inst.saved().Server["serverName"], why, run.rec.Announced != "online")
	return discordPost{
		link: s.DiscordWebhook,
		text: func() string { return text },
		done: func(err error) {
			if err != nil {
				m.addActivity("warning", "", "Couldn't post in Discord that the server stopped. "+err.Error())
			}
		},
	}, true
}

func stoppedText(server string, why stopReason, code bool) string {
	name := "**" + discord.Escape(server) + "**"
	back := "A message will be posted here when it's back online."
	if code {
		back = "The new join code will be posted here when it's back online."
	}
	switch why {
	case stopRestart:
		if code {
			return "🔄 " + name + " is restarting. The new join code will be posted here in a minute or two."
		}
		return "🔄 " + name + " is restarting. It will be back in a minute or two."
	case stopRest:
		return "🌙 " + name + " has stopped and the computer is going to rest. " + back
	case stopCrashRestart:
		return "⚠️ " + name + " stopped unexpectedly and is starting again. " + back
	case stopCrash:
		return "⚠️ " + name + " stopped unexpectedly. Someone needs to start it again from the control panel."
	}
	return "🔴 " + name + " has stopped. " + back
}

// publicAddress is the server's internet address, looked up again unless
// it was looked up lately: it may have changed, for example while the
// computer slept, and nobody may have opened the control panel since.
func (m *Manager) publicAddress(port int) string {
	m.mu.Lock()
	ip, at := m.net.public, m.net.publicAt
	m.mu.Unlock()
	if (ip == "" || at.IsZero() || time.Since(at) > publicMaxAge) && !m.opt.NoPublicIP {
		if fresh := fetchPublicIP(); fresh != "" {
			ip = fresh
			m.mu.Lock()
			m.net.public, m.net.publicAt = ip, time.Now()
			m.mu.Unlock()
		} // otherwise the last address known is the best guess
	}
	if ip == "" {
		return ""
	}
	return net.JoinHostPort(ip, strconv.Itoa(port))
}

// queueDiscord adds a message to the queue.
func (m *Manager) queueDiscord(p discordPost) {
	m.discordPending.Add(1)
	select {
	case m.discordQ <- p:
	default:
		m.discordPending.Add(-1)
		m.log.Printf("discord: too many messages waiting; one was dropped")
	}
}

// discordLoop posts queued messages one at a time.
func (m *Manager) discordLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case p := <-m.discordQ:
			err := m.postDiscord(p)
			if m.ctx.Err() == nil && p.done != nil {
				p.done(err)
			}
			m.discordPending.Add(-1)
		}
	}
}

// postDiscord sends one message, trying again a few times while Discord
// can't be reached.
func (m *Manager) postDiscord(p discordPost) error {
	text := p.text()
	err := discord.Post(m.ctx, nil, m.webhookURL(p.link), text)
	for _, wait := range discordRetries {
		var de *discord.Error
		if err == nil || !errors.As(err, &de) || !de.Temporary {
			break
		}
		m.log.Printf("discord: %v Trying again in %s.", err, wait)
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-time.After(wait):
		}
		err = discord.Post(m.ctx, nil, m.webhookURL(p.link), text)
	}
	return err
}

// flushDiscord waits up to timeout for queued messages to be posted, for
// example before the computer goes to rest.
func (m *Manager) flushDiscord(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for m.discordPending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}

// webhookURL sends webhook links to Options.DiscordURL in tests.
func (m *Manager) webhookURL(link string) string {
	if m.opt.DiscordURL == "" {
		return link
	}
	u, err := url.Parse(link)
	base, err2 := url.Parse(m.opt.DiscordURL)
	if err != nil || err2 != nil {
		return link
	}
	u.Scheme, u.Host = base.Scheme, base.Host
	return u.String()
}

// saveRunInfo writes what's known about the running server to its run
// record (runtime.json for the legacy game), so a restarted app knows what
// was already posted.
func (m *Manager) saveRunInfo(run *serverRun) {
	// Saves that overlap are written in the order they read run.rec, so
	// the file ends up with the newest.
	m.runFileMu.Lock()
	defer m.runFileMu.Unlock()
	m.mu.Lock()
	rec, current := run.rec, run.inst.run == run
	path := m.runFile(run.inst)
	m.mu.Unlock()
	if current {
		saveRuntime(path, rec)
	}
}

// TestDiscord posts a test message through the webhook at link, which
// doesn't have to be saved yet.
func (m *Manager) TestDiscord(link string) error {
	link = strings.TrimSpace(link)
	if msg := discord.Problem(link); msg != "" {
		return config.FieldErrors{"discordWebhook": msg}
	}
	m.mu.Lock()
	s := m.settings
	m.mu.Unlock()
	what := "its join code"
	if !s.Crossplay {
		what = "how to join"
	}
	text := fmt.Sprintf("✅ **%s** can post here. Each time the server starts, %s will appear in this channel.", discord.Escape(s.ServerName), what)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return discord.Post(ctx, nil, m.webhookURL(link), text)
}

// --- R3 (stream D) ------------------------------------------------------------
//
// Phase 0' made what follows with its signatures and empty bodies; stream
// D fills it in (spec 6.3): a channel per game, one queue per webhook link,
// daily posts per game.

// discordState is stream D's own app-wide state (Manager.disc), guarded by
// m.mu: the queues by webhook link (each with its own goroutine, 32 posts,
// order kept per link) and whatever else D needs. D adds its fields here.
type discordState struct{}

// GameDiscordStatus is a game's "discord" in Status (4.9): whether its
// online post made it.
type GameDiscordStatus struct {
	Posted *time.Time `json:"posted,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// discordStatusLocked is the instance's Discord status, or nil when it
// doesn't post.
func (m *Manager) discordStatusLocked(inst *instance) *GameDiscordStatus {
	return nil
}

// gameLinkLocked is the webhook link a game posts in: its own channel's,
// else the main channel's (settings.json discordWebhook); "" when it has
// neither. Every post of a game goes there, and its slash command works
// only in that channel (6.4).
func (m *Manager) gameLinkLocked(inst *instance) string {
	return ""
}

// GameDiscordSettings is a game's Discord settings on its Settings tab and
// the Discord page. For the legacy game, On, Stopped, Daily and Password
// are settings.json's discord, discordStopped, discordDaily and
// discordPassword (same meanings); its own channel is servers.json's.
type GameDiscordSettings struct {
	On       bool   `json:"on"`
	Webhook  string `json:"webhook"` // "" = the main channel
	Stopped  bool   `json:"stopped"`
	Daily    bool   `json:"daily"`
	Password bool   `json:"password"`
	GuildOK  bool   `json:"guildOK"` // the channel is in the main channel's Discord server, so its command works there
}

// GameDiscordChange is what a settings save changes in a game's Discord
// settings: only the parts that are set.
type GameDiscordChange struct {
	On       *bool   `json:"on,omitempty"`
	Webhook  *string `json:"webhook,omitempty"`
	Stopped  *bool   `json:"stopped,omitempty"`
	Daily    *bool   `json:"daily,omitempty"`
	Password *bool   `json:"password,omitempty"`
}

// gameDiscordLocked is the instance's Discord settings (M3's GameSettings
// shows them).
func (m *Manager) gameDiscordLocked(inst *instance) GameDiscordSettings {
	return GameDiscordSettings{}
}

// setGameDiscord saves a change to the instance's Discord settings (M3's
// UpdateGameSettings calls it, without m.mu).
func (m *Manager) setGameDiscord(inst *instance, ch GameDiscordChange) error {
	return errNotYet
}

// SetMainDiscord saves the main channel (settings.json discordWebhook,
// checked with discord.Problem; POST /api/discord/main).
func (m *Manager) SetMainDiscord(link string) error {
	return errNotYet
}

// TestGameDiscord posts a test message through link, which doesn't have
// to be saved yet, worded for the game id ("" for the main channel; POST
// /api/discord/test with {webhook, game}). The legacy body {webhook} keeps
// TestDiscord's message.
func (m *Manager) TestGameDiscord(link, id string) error {
	if id == "" {
		return m.TestDiscord(link)
	}
	return m.legacyOnly(id, func() error { return m.TestDiscord(link) })
}
