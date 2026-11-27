package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
)

// Posting in Discord (spec 6.3). Each game posts in its own channel, or in
// the main one when it has none. Messages wait in one queue per channel
// and go out one at a time, so "stopped" never overtakes the "online"
// before it, and a channel Discord can't take posts in for now (a deleted
// webhook, Discord having trouble) never holds up another channel's posts.

// discordRetries are the waits before trying a message again while Discord
// can't be reached.
var discordRetries = []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}

// discordQueueSize is how many posts can wait for one channel. When that
// many are waiting, Discord has been out of reach for a long while, so the
// next one is dropped (with a line in the log) rather than kept forever.
const discordQueueSize = 32

// discordPost is a message waiting to be posted.
type discordPost struct {
	link string
	text func() string   // built when it's sent: the address may need looking up
	done func(err error) // gets the outcome; err is nil when it was posted
	// posted, when set, gets the new message's id once it's posted (before
	// done): the "still playing?" question uses it.
	posted func(id string)
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
	stopIdle  // auto-stop: "nobody played for an hour" (A sets it; autoStopText(run.autoMinutes))
	stopAsked // the "still playing?" rule: its "Turning … off" line is the only stopped post (D)
)

// discordRules is how an instance posts in Discord.
type discordRules struct {
	on, stopped, daily, password bool
	// link is the webhook of the channel every post of the game goes to: its
	// own channel's, else the main one's; "" when there is neither.
	link string
	// main says link is the main channel, where several games may post, so
	// a game other than the legacy one names itself first.
	main bool
}

// posts says whether the instance posts at all: it's on and has a channel.
func (r discordRules) posts() bool { return r.on && r.link != "" }

// discordRulesLocked works out the instance's Discord rules. The legacy
// game's on, stopped, daily and password are settings.json's discord,
// discordStopped, discordDaily and discordPassword, as they always were;
// every other game's are servers.json's, where a game nobody changed has
// the defaults (on, the main channel, stopped and daily posts, no
// password), so a newly added game posts in the main channel. Only the
// own channel (servers.json discord.webhook) is new for the legacy game.
func (m *Manager) discordRulesLocked(inst *instance) discordRules {
	s := m.settings
	d := servers.DefaultDiscord()
	if in := m.servers.Instance(inst.id); in != nil {
		d = in.DiscordRules()
	}
	r := discordRules{on: d.On, stopped: d.Stopped, daily: d.Daily, password: d.Password}
	if inst.legacy {
		r.on, r.stopped, r.daily, r.password = s.Discord, s.DiscordStopped, s.DiscordDaily, s.DiscordPassword
	}
	main := strings.TrimSpace(s.DiscordWebhook)
	r.link = strings.TrimSpace(d.Webhook)
	if r.link == "" {
		r.link = main
	}
	r.main = r.link != "" && r.link == main
	return r
}

// words is a post as it goes to the instance's channel: in the main
// channel a game other than the legacy one starts with its name
// ("Minecraft · 🟢 …"), so people can tell the games apart. The legacy
// game's posts are always exactly as they were.
func (r discordRules) words(inst *instance, text string) string {
	if inst.legacy || !r.main {
		return text
	}
	return gamePrefix(inst) + text
}

func gamePrefix(inst *instance) string { return discord.Escape(inst.short()) + " · " }

// discordNote adds an activity entry about the instance's posts. The
// legacy game's entries stay as they were (no game named), so activity.json
// is what older builds wrote; every other game's carry the game.
func (m *Manager) discordNote(inst *instance, kind, text string) {
	if inst.legacy {
		m.addActivity(kind, "", text)
		return
	}
	m.addGameActivity(inst, kind, "", text)
}

// postServerNameLocked is the server name for a run's posts: what it
// started with, or for one started by an older version of the app, the
// name in its settings.
func postServerNameLocked(inst *instance, run *serverRun) string {
	if run != nil && run.rec.Server != "" {
		return run.rec.Server
	}
	if name := inst.saved().Server["serverName"]; name != "" {
		return name
	}
	return inst.short()
}

// announceLocked returns the online message once the running server is
// ready (for a game with a join code, once it has one, and again if the
// code changes); ok is false when there's nothing new to post. The caller
// queues it and saves the run record.
func (m *Manager) announceLocked(run *serverRun) (p discordPost, ok bool) {
	inst := run.inst
	r := m.discordRulesLocked(inst)
	if m.askedFromDiscordLocked(inst) {
		// Someone asked for it with its Discord command, in its channel:
		// the online post goes there whatever its own posts say (6.4).
		r.on = true
	}
	if !r.posts() || inst.run != run || !run.ready || run.stopping || run.replaying || run.afterPending {
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
	if r.password {
		in.Password = j.Password
	}
	rec := run.rec
	in.Rec = &rec
	// The activity entries: the legacy game's as they always were; another
	// game's name it, since the feed shows every game's.
	what := "the join code"
	if code == "" {
		what = "that the server is online"
	}
	posted, failed := fmt.Sprintf("Posted %s in Discord.", what), fmt.Sprintf("Couldn't post %s in Discord.", what)
	switch {
	case inst.legacy:
	case code == "":
		posted = fmt.Sprintf("Posted in Discord that %s is online.", inst.short())
		failed = fmt.Sprintf("Couldn't post in Discord that %s is online.", inst.short())
	default:
		label := j.CodeLabel
		if label == "" {
			label = "join code"
		}
		what = fmt.Sprintf("the %s %s", inst.short(), lowerFirst(label))
		posted, failed = fmt.Sprintf("Posted %s in Discord.", what), fmt.Sprintf("Couldn't post %s in Discord.", what)
	}
	texter, _ := inst.srv.(game.OnlineTexter)
	return discordPost{
		link: r.link,
		text: func() string {
			if in.Join.Code == "" {
				in.Join.Address = m.publicAddress(in.Join.Port)
			}
			if texter != nil {
				return r.words(inst, texter.Online(in))
			}
			return r.words(inst, onlineText(in))
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
				m.discordNote(inst, "warning", failed+" "+err.Error())
			} else {
				m.discordNote(inst, "discord", posted)
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
// channel was told it was online. The "still playing?" rule's stop has
// none: its "Turning … off" line, posted when it decided, is that message
// (spec 4.6).
func (m *Manager) stoppedPostLocked(run *serverRun, why stopReason) (p discordPost, ok bool) {
	inst := run.inst
	r := m.discordRulesLocked(inst)
	if !r.posts() || !r.stopped || run.rec.Announced == "" || why == stopAsked {
		return p, false
	}
	server := inst.saved().Server["serverName"]
	var text string
	if why == stopIdle {
		minutes := run.autoMinutes
		if minutes <= 0 { // A records the minutes it stopped with; this is only a fallback
			minutes = m.autoStopMinutesLocked(inst)
		}
		if minutes <= 0 {
			minutes = servers.DefaultAutoStopMinutes
		}
		text = idleStoppedText(server, minutes, m.commandLocked(inst))
	} else {
		text = stoppedText(server, why, run.rec.Announced != "online")
	}
	text = r.words(inst, text)
	failed := "Couldn't post in Discord that the server stopped. "
	if !inst.legacy {
		failed = fmt.Sprintf("Couldn't post in Discord that %s stopped. ", inst.short())
	}
	return discordPost{
		link: r.link,
		text: func() string { return text },
		done: func(err error) {
			if err != nil {
				m.discordNote(inst, "warning", failed+err.Error())
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

// idleStoppedText is the stopped post after auto-stop (spec 4.5), from the
// minutes it waited: "🔴 **X** has stopped: nobody played for an hour.
// Start it again with /minecraft or from the control panel." The command
// is named only when it works (command is "" otherwise).
func idleStoppedText(server string, minutes int, command string) string {
	again := "Start it again from the control panel."
	if command != "" {
		again = "Start it again with /" + command + " or from the control panel."
	}
	return "🔴 **" + discord.Escape(server) + "** has stopped: nobody played for " + autoStopText(minutes) + ". " + again
}

// commandLocked is the instance's slash command when friends can use it to
// start the game again: the game is starred, turned on and shown, and the
// waker is set up (spec 6.4); otherwise "".
func (m *Manager) commandLocked(inst *instance) string {
	if !m.settings.DiscordWake || !m.isStarredLocked(inst) || m.isDisabledLocked(inst) || m.isHiddenLocked(inst) {
		return ""
	}
	if inst.info.Command != "" {
		return inst.info.Command
	}
	return inst.info.ID
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

// --- the queues ---------------------------------------------------------------

// postQueue holds the posts waiting for one channel (one webhook link), and
// has its own goroutine that posts them in order.
type postQueue struct {
	link string
	ch   chan discordPost
}

// queueDiscord adds a message to its channel's queue. It never blocks or
// takes m.mu, so it may be called with m.mu held.
func (m *Manager) queueDiscord(p discordPost) {
	d := &m.disc
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		m.log.Printf("discord: the app is closing; a message wasn't posted")
		return
	}
	q := d.queues[p.link]
	if q == nil {
		if d.queues == nil {
			d.queues = map[string]*postQueue{}
		}
		q = &postQueue{link: p.link, ch: make(chan discordPost, discordQueueSize)}
		d.queues[p.link] = q
		if d.ctx != nil { // before Start, discordLoop starts it
			m.startQueueLocked(q)
		}
	}
	m.discordPending.Add(1)
	select {
	case q.ch <- p:
	default:
		m.discordPending.Add(-1)
		m.log.Printf("discord: too many messages waiting for one channel; one was dropped")
	}
}

// startQueueLocked starts the goroutine that posts q's messages. d.mu must
// be held, so it is never started after discordLoop began waiting for them.
func (m *Manager) startQueueLocked(q *postQueue) {
	d := &m.disc
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer m.recoverPanic()
		for {
			select {
			case <-d.ctx.Done():
				return
			case p := <-q.ch:
				m.sendPost(p)
			}
		}
	}()
}

// sendPost posts one queued message and reports how it went.
func (m *Manager) sendPost(p discordPost) {
	defer m.discordPending.Add(-1)
	id, err := m.postDiscord(p)
	if m.ctx.Err() != nil {
		return
	}
	if err == nil && p.posted != nil {
		p.posted(id)
	}
	if p.done != nil {
		p.done(err)
	}
}

// discordLoop runs the channels' queues for as long as the app runs: it
// starts the ones made before Start, and when the app closes it waits for
// every queue's goroutine to end, so Close's wait covers them all.
func (m *Manager) discordLoop() {
	d := &m.disc
	d.mu.Lock()
	d.ctx = m.ctx
	for _, q := range d.queues {
		m.startQueueLocked(q)
	}
	d.mu.Unlock()
	<-m.ctx.Done()
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.wg.Wait()
}

// postDiscord sends one message, trying again a few times while Discord
// can't be reached. Only this message's channel waits meanwhile.
func (m *Manager) postDiscord(p discordPost) (string, error) {
	text := p.text()
	id, err := discord.PostID(m.ctx, nil, m.webhookURL(p.link), text)
	for _, wait := range discordRetries {
		var de *discord.Error
		if err == nil || !errors.As(err, &de) || !de.Temporary {
			break
		}
		m.log.Printf("discord: %v Trying again in %s.", err, wait)
		select {
		case <-m.ctx.Done():
			return "", m.ctx.Err()
		case <-time.After(wait):
		}
		id, err = discord.PostID(m.ctx, nil, m.webhookURL(p.link), text)
	}
	return id, err
}

// flushDiscord waits up to timeout for every channel's queued messages to
// be posted, for example before the computer goes to rest.
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

// --- R3 (stream D): a channel per game ------------------------------------------

// discordState is stream D's app-wide state (Manager.disc). The queues
// have their own lock, d.mu, because posts are queued both with m.mu held
// and without it; everything after them is guarded by m.mu.
type discordState struct {
	mu     sync.Mutex
	queues map[string]*postQueue // by webhook link
	ctx    context.Context       // the app's, once discordLoop runs
	closed bool                  // the app is closing: nothing more is queued
	wg     sync.WaitGroup        // the queues' goroutines

	// Guarded by m.mu.
	hooks map[string]*hookInfo // what Discord said about each webhook link
	// now is the "still playing?" rule's clock, time.Now when nil; tests set
	// a pretend one, so four hours pass in a moment.
	now func() time.Time
	// stop turns a game off for the "still playing?" rule: A's autoStopGame
	// when nil. Tests set their own to see it called.
	stop func(inst *instance, why stopReason) error
	// saveServers saves a change to servers.json for a game's Discord
	// settings: M3's updateServers when nil. Tests set their own, to see
	// what happens when it works and when it's refused.
	saveServers func(change func(*servers.File) error) error
	// recsRead lists the games whose records file has been read (records.go:
	// the daily posts come from it). New reads only the legacy game's, so
	// every other game's is read the first time it's needed, and until then
	// nothing is saved over it.
	recsRead map[*instance]bool

	// recsLoad lets one caller at a time read a game's records file for the
	// first time (taken before m.mu, never while holding it).
	recsLoad sync.Mutex
}

// hookInfo is what Discord said about a webhook link: the Discord server
// and the channel it posts in (discord.Webhook), and the channel's name
// once the bot has read it.
type hookInfo struct {
	guild, channel, name string
	err                  string    // why the last look didn't work, "" when it did
	gone                 bool      // Discord said the webhook doesn't exist (err says so)
	next                 time.Time // when to look again
	busy                 bool      // a look is under way
}

// How often what Discord says about a webhook is looked up again: an hour
// after it answered (the owner can move a webhook to another channel), two
// minutes after it didn't.
const (
	hookFresh = time.Hour
	hookRetry = 2 * time.Minute
)

// hookLocked is what's known about link, and starts a look in the
// background when that is missing or old. It answers from memory, so
// Status and the settings pages can call it.
func (m *Manager) hookLocked(link string) hookInfo {
	h := m.disc.hooks[link]
	if h == nil {
		if m.disc.hooks == nil {
			m.disc.hooks = map[string]*hookInfo{}
		}
		h = &hookInfo{}
		m.disc.hooks[link] = h
	}
	if link != "" && !h.busy && time.Now().After(h.next) && m.ctx != nil && m.ctx.Err() == nil {
		h.busy = true
		m.go_(func() { m.lookHook(m.ctx, link) })
	}
	return *h
}

// hookFor is what Discord says about link, looked up now unless it was
// lately. It runs without m.mu.
func (m *Manager) hookFor(ctx context.Context, link string) hookInfo {
	m.mu.Lock()
	h := m.disc.hooks[link]
	fresh := h != nil && h.channel != "" && time.Now().Before(h.next)
	var known hookInfo
	if h != nil {
		known = *h
	}
	m.mu.Unlock()
	if fresh {
		return known
	}
	return m.lookHook(ctx, link)
}

// lookHook asks Discord which Discord server and channel link posts in.
func (m *Manager) lookHook(ctx context.Context, link string) hookInfo {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	guild, channel, err := discord.Webhook(ctx, nil, m.webhookURL(link))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disc.hooks == nil {
		m.disc.hooks = map[string]*hookInfo{}
	}
	h := m.disc.hooks[link]
	if h == nil {
		h = &hookInfo{}
		m.disc.hooks[link] = h
	}
	h.busy = false
	var de *discord.Error
	switch {
	case err == nil:
		if h.channel != channel {
			h.name = "" // moved to another channel
		}
		h.guild, h.channel, h.err, h.gone, h.next = guild, channel, "", false, time.Now().Add(hookFresh)
	case errors.As(err, &de) && !de.Temporary:
		// The webhook is gone: nothing is known about it any more.
		h.guild, h.channel, h.name, h.err, h.gone, h.next = "", "", "", err.Error(), true, time.Now().Add(hookRetry)
	default: // Discord can't be reached for now: keep what was known
		h.err, h.next = err.Error(), time.Now().Add(hookRetry)
	}
	return *h
}

// GameDiscordStatus is a game's "discord" in Status (4.9): whether its
// online post made it.
type GameDiscordStatus struct {
	Posted *time.Time `json:"posted,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// discordStatusLocked is the instance's Discord status, or nil when it
// doesn't post (its posts are off, or it has no channel).
func (m *Manager) discordStatusLocked(inst *instance) *GameDiscordStatus {
	if inst.srv == nil || !m.discordRulesLocked(inst).posts() {
		return nil
	}
	st := &GameDiscordStatus{}
	if run := inst.run; run != nil {
		st.Error = run.discordErr
		if !run.discordAt.IsZero() {
			at := run.discordAt
			st.Posted = &at
		}
	}
	return st
}

// gameLinkLocked is the webhook link a game posts in: its own channel's,
// else the main channel's (settings.json discordWebhook); "" when it has
// neither. Every post of a game goes there, and its slash command works
// only in that channel (6.4). It doesn't depend on whether the game's own
// posts are on: a Discord request is answered where it was asked.
func (m *Manager) gameLinkLocked(inst *instance) string {
	return m.discordRulesLocked(inst).link
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
	r := m.discordRulesLocked(inst)
	own := ""
	if in := m.servers.Instance(inst.id); in != nil {
		own = strings.TrimSpace(in.DiscordRules().Webhook)
	}
	return GameDiscordSettings{On: r.on, Webhook: own, Stopped: r.stopped, Daily: r.daily, Password: r.password,
		GuildOK: m.guildOKLocked(own)}
}

// guildOKLocked says whether a game's own channel is in the same Discord
// server as the main channel. The waker answers in that one Discord server
// only (6.3), so a channel elsewhere can't use the game's command. Until
// Discord has said where both are, it counts as fine: the page says
// nothing rather than something that may be wrong.
func (m *Manager) guildOKLocked(own string) bool {
	main := strings.TrimSpace(m.settings.DiscordWebhook)
	if own == "" || own == main || main == "" {
		return true
	}
	a, b := m.hookLocked(own), m.hookLocked(main)
	return a.guild == "" || b.guild == "" || a.guild == b.guild
}

// setGameDiscord saves a change to the instance's Discord settings (M3's
// UpdateGameSettings calls it, without m.mu). A game that is online posts
// its online message again when its posts were turned on, moved to another
// channel, or should now include the password, as the legacy game always
// has, so friends see it where they look.
//
// For the legacy game a change can touch two files: its own channel is in
// servers.json, the rest in settings.json. Everything is checked before
// either is written, and servers.json is written first, so that when the
// settings.json save posts the online message again (it does when Valheim's
// posts are turned on), the message goes to the channel just chosen, not
// the one before; and so that a refused servers.json (the app's list of
// games can't be changed now) leaves both files as they were.
func (m *Manager) setGameDiscord(inst *instance, ch GameDiscordChange) error {
	if ch.Webhook != nil {
		link := strings.TrimSpace(*ch.Webhook)
		if link != "" {
			if msg := discord.Problem(link); msg != "" {
				return game.FieldErrors{"discord.webhook": msg}
			}
		}
		ch.Webhook = &link
	}
	legacySettings := inst.legacy && (ch.On != nil || ch.Stopped != nil || ch.Daily != nil || ch.Password != nil)
	var next config.Settings
	if legacySettings {
		// settings.json, saved as the old Settings page saves it.
		next = m.Settings()
		setIf(&next.Discord, ch.On)
		setIf(&next.DiscordStopped, ch.Stopped)
		setIf(&next.DiscordDaily, ch.Daily)
		setIf(&next.DiscordPassword, ch.Password)
		check := next.Clone()
		check.Normalize()
		if errs := check.Validate(); errs != nil {
			return legacyDiscordError(errs)
		}
	}
	m.mu.Lock()
	before := m.discordRulesLocked(inst)
	oldOwn := ""
	if in := m.servers.Instance(inst.id); in != nil {
		oldOwn = strings.TrimSpace(in.DiscordRules().Webhook)
	}
	m.mu.Unlock()
	// Only the legacy game's own channel is in servers.json, so a change
	// that keeps it writes nothing there.
	writeServers := !inst.legacy || ch.Webhook != nil && *ch.Webhook != oldOwn
	if writeServers {
		err := m.saveDiscordServers(func(f *servers.File) error {
			in := f.Instance(inst.id)
			if in == nil {
				return errNoGame(inst.id)
			}
			if in.Discord == nil {
				d := servers.DefaultDiscord()
				in.Discord = &d
			}
			d := in.Discord
			if ch.Webhook != nil {
				d.Webhook = *ch.Webhook
			}
			if !inst.legacy { // the legacy game's are in settings.json (below)
				setIf(&d.On, ch.On)
				setIf(&d.Stopped, ch.Stopped)
				setIf(&d.Daily, ch.Daily)
				setIf(&d.Password, ch.Password)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if legacySettings {
		if err := m.UpdateSettings(next); err != nil {
			if writeServers { // put its channel back, so the failed save changes nothing
				if rerr := m.saveDiscordServers(func(f *servers.File) error {
					if in := f.Instance(inst.id); in != nil && in.Discord != nil {
						in.Discord.Webhook = oldOwn
					}
					return nil
				}); rerr != nil {
					m.log.Printf("discord: couldn't put %s's channel back: %v", inst.id, rerr)
				}
			}
			var fe config.FieldErrors
			if errors.As(err, &fe) {
				return legacyDiscordError(fe)
			}
			return err
		}
	}
	m.mu.Lock()
	after := m.discordRulesLocked(inst)
	again := after.posts() && (!before.posts() || before.link != after.link || after.password && !before.password)
	if legacySettings && after.on && (!before.on || after.password && !before.password) {
		// UpdateSettings has just posted it again, in after.link (servers.json
		// was already saved): once is enough.
		again = false
	}
	if again {
		m.reannounceLocked(inst)
	}
	if after.link != "" {
		m.hookLocked(after.link) // so the Settings tab can soon say which Discord server it's in
	}
	m.mu.Unlock()
	if before.link != after.link {
		// The waker's commands carry each game's channel, so a new channel
		// needs the same upkeep as a new star.
		m.starsChanged()
	}
	go m.notify()
	return nil
}

// saveDiscordServers saves a change to servers.json (saveServers in tests).
func (m *Manager) saveDiscordServers(change func(*servers.File) error) error {
	if save := m.disc.saveServers; save != nil {
		return save(change)
	}
	return m.updateServers(change)
}

// legacyDiscordError is a refused settings.json change as the game's
// Discord settings say it: Valheim's posts need the main channel, because
// settings.json can't say "on" without its link.
func legacyDiscordError(fe config.FieldErrors) error {
	if msg, ok := fe["discordWebhook"]; ok && len(fe) == 1 {
		return game.FieldErrors{"discord.on": "Add the main Discord channel first. " + msg}
	}
	return fe
}

func setIf(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}

// reannounceLocked posts the running instance's online message again, in
// the channel it posts in now.
func (m *Manager) reannounceLocked(inst *instance) {
	run := inst.run
	if run == nil {
		return
	}
	run.rec.Announced = ""
	if post, ok := m.announceLocked(run); ok {
		m.queueDiscord(post)
		go m.saveRunInfo(run)
	}
}

// SetMainDiscord saves the main channel (settings.json discordWebhook,
// checked with discord.Problem; POST /api/discord/main). Every game without
// a channel of its own posts there. An empty link removes it; Valheim's
// posts then go off too, because settings.json can't have them on without
// the link (and older versions of the app read it so).
func (m *Manager) SetMainDiscord(link string) error {
	link = strings.TrimSpace(link)
	if link != "" {
		if msg := discord.Problem(link); msg != "" {
			return config.FieldErrors{"discordWebhook": msg}
		}
	}
	s := m.Settings()
	if strings.TrimSpace(s.DiscordWebhook) == link {
		return nil
	}
	old := make(map[*instance]string)
	m.mu.Lock()
	m.eachInstance(func(inst *instance) {
		if !inst.legacy && inst.srv != nil {
			old[inst] = m.discordRulesLocked(inst).link
		}
	})
	m.mu.Unlock()
	s.DiscordWebhook = link
	if link == "" {
		s.Discord = false
	}
	// Saved as the old Settings page saves it, which also keeps the waker
	// and the legacy game's online post up to date.
	if err := m.UpdateSettings(s); err != nil {
		return err
	}
	m.mu.Lock()
	for inst, was := range old {
		if r := m.discordRulesLocked(inst); r.posts() && r.link != was {
			m.reannounceLocked(inst)
		}
	}
	if link != "" {
		m.hookLocked(link)
	}
	m.mu.Unlock()
	return nil
}

// TestGameDiscord posts a test message through link, which doesn't have
// to be saved yet, worded for the game id ("" for the main channel; POST
// /api/discord/test with {webhook, game}). The legacy body {webhook}, and
// the legacy game, keep TestDiscord's message.
func (m *Manager) TestGameDiscord(link, id string) error {
	if id == "" {
		return m.TestDiscord(link)
	}
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	if inst.legacy {
		return m.TestDiscord(link)
	}
	if inst.srv == nil {
		return fmt.Errorf("This version of the app can't run %s.", inst.short())
	}
	link = strings.TrimSpace(link)
	if msg := discord.Problem(link); msg != "" {
		return config.FieldErrors{"discordWebhook": msg}
	}
	m.mu.Lock()
	server := postServerNameLocked(inst, nil)
	r := discordRules{link: link, main: link == strings.TrimSpace(m.settings.DiscordWebhook)}
	m.mu.Unlock()
	text := r.words(inst, fmt.Sprintf("✅ **%s** can post here. Each time it starts, how to join will appear in this channel.",
		discord.Escape(server)))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return discord.Post(ctx, nil, m.webhookURL(link), text)
}
