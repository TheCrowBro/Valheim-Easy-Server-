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
	"valheim-easy-server/internal/mods"
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
}

// Why the server stopped, for the Discord message.
type stopReason int

const (
	stopStopped stopReason = iota
	stopRestart
	stopRest
	stopCrash
	stopCrashRestart
)

// announceLocked returns the message with the join code once the running
// server has one, and again if the code changes; ok is false when there's
// nothing new to post. The caller queues it and saves run.info.
func (m *Manager) announceLocked(run *serverRun) (p discordPost, ok bool) {
	s := m.settings
	if !s.Discord || m.run != run || !run.ready || run.stopping || run.replaying {
		return p, false
	}
	code := run.tracker.JoinCode
	key := code
	if key == "" {
		if s.Crossplay {
			return p, false // the code comes a few seconds after the server is ready
		}
		key = "online"
	}
	if run.info.Announced == key {
		return p, false
	}
	run.info.Announced = key
	run.discordAt, run.discordErr = time.Time{}, ""

	msg := onlineMessage{Server: s.ServerName, World: run.info.World, Code: code}
	if s.DiscordPassword {
		msg.Password = s.Password
	}
	for _, id := range s.WorldOptions[run.info.World].Mods {
		if md, ok := mods.Find(id); ok {
			msg.Mods = append(msg.Mods, ModInfo{Mod: md, Version: m.modVersions[md.Package]})
		}
	}
	what := "the join code"
	if code == "" {
		what = "that the server is online"
	}
	port := s.Port
	return discordPost{
		link: s.DiscordWebhook,
		text: func() string {
			if msg.Code == "" {
				msg.Address = m.publicAddress(port)
			}
			return msg.text()
		},
		done: func(err error) {
			m.mu.Lock()
			if run.info.Announced == key {
				if err == nil {
					run.discordAt = time.Now()
				} else {
					run.discordErr = err.Error()
				}
			}
			m.mu.Unlock()
			if err != nil {
				m.addActivity("warning", "", fmt.Sprintf("Couldn't post %s in Discord. %v", what, err))
			} else {
				m.addActivity("discord", "", fmt.Sprintf("Posted %s in Discord.", what))
			}
		},
	}, true
}

// stoppedPostLocked returns the message saying the server stopped, if the
// channel was told it was online.
func (m *Manager) stoppedPostLocked(run *serverRun, why stopReason) (p discordPost, ok bool) {
	s := m.settings
	if !s.Discord || !s.DiscordStopped || run.info.Announced == "" {
		return p, false
	}
	text := stoppedText(s.ServerName, why, run.info.Announced != "online")
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

// onlineMessage is what the channel sees when the server is ready.
type onlineMessage struct {
	Server, World string
	Code          string // crossplay join code
	Address       string // internet address, when there's no join code
	Password      string // empty to leave it out
	Mods          []ModInfo
}

func (o onlineMessage) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🟢 **%s** is online\n", discord.Escape(o.Server))
	fmt.Fprintf(&b, "World: %s\n", discord.Escape(o.World))
	switch {
	case o.Code != "":
		fmt.Fprintf(&b, "Join code: **%s**\n", o.Code)
	case o.Address != "":
		fmt.Fprintf(&b, "Address: **%s**\n", o.Address)
	}
	if o.Password != "" {
		fmt.Fprintf(&b, "Password: ||%s||\n", discord.Escape(o.Password))
	}
	hint := "In Valheim, go to Join Game and join by code."
	if o.Code == "" {
		hint = "In Valheim, go to Join Game and add the server by its address."
	}
	if len(o.Mods) > 0 {
		list := make([]string, 0, len(o.Mods))
		for _, md := range o.Mods {
			item := fmt.Sprintf("[%s](%s)", md.Name, md.URL)
			if md.Version != "" {
				item += " " + md.Version
			}
			list = append(list, item)
		}
		fmt.Fprintf(&b, "Mods: %s\n", strings.Join(list, ", "))
		if len(o.Mods) > 1 {
			hint += " Install the mods first with the r2modman or Gale mod manager."
		} else {
			hint += " Install the mod first with the r2modman or Gale mod manager."
		}
	}
	b.WriteString("-# " + hint)
	return b.String()
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

// publicAddress is the server's internet address, looked up if needed.
func (m *Manager) publicAddress(port int) string {
	m.mu.Lock()
	ip := m.net.public
	m.mu.Unlock()
	if ip == "" && !m.opt.NoPublicIP {
		if ip = fetchPublicIP(); ip != "" {
			m.mu.Lock()
			m.net.public, m.net.publicAt = ip, time.Now()
			m.mu.Unlock()
		}
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

// saveRunInfo writes what's known about the running server to
// runtime.json, so a restarted app knows what was already posted.
func (m *Manager) saveRunInfo(run *serverRun) {
	m.mu.Lock()
	info, current := run.info, m.run == run
	path := m.dataFile("runtime.json")
	m.mu.Unlock()
	if current {
		saveRuntime(path, info)
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
