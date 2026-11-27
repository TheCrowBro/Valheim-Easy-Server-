package manager

// The "still playing?" rule (spec 4.6, stream D), for a game whose
// presence is "none" (Windrose until a real log proves its lines, any game
// whose signal turns out unusable) or not yet proven on this install (2.6).
// Such a game can't tell the app that nobody is on, so auto-stop can't
// decide for it, and it would otherwise run until someone notices:
//
//   - 4 hours after it is ready the app asks in its channel whether it
//     should stay on ("Send any message in this channel…" when the bot can
//     read the channel, "Ask the owner to press Keep it on…" when it can't).
//     With no channel at all the question shows only in the control panel
//     (10.2 Q2).
//   - While it runs, the channel is read every 2 minutes with the waker's
//     bot token. Any message there that isn't from a bot or a webhook, a
//     join, or Keep it on in the panel restarts the 4-hour clock.
//   - 2 hours after the question with no answer, the app posts "Turning …
//     off" and stops it (stopAsked, through A's autoStopGame). That line is
//     its only stopped post.
//   - The stop chain never forces a server off, so a stop can end with the
//     server still on (it didn't exit in time). Then the app tries again
//     every 10 minutes, without posting again, and until it does, Keep it
//     on or a join keeps the game on after all.
//
// askObserver is D's observer (hooks.go): the one place this rule hears
// about readiness, joins, exits and the ticks that move its clock.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
)

const (
	askAfter = 4 * time.Hour   // how long a game runs before the question
	askWait  = 2 * time.Hour   // how long the question waits for an answer
	askEvery = 2 * time.Minute // how often the channel is read
	askRetry = time.Minute     // how soon a stop that couldn't start is tried again
	// askAgain is how soon a stop that ran but left the server on is tried
	// again. The stop chain waits minutes for a server to save and exit, so
	// trying at once would keep the game's job slot busy all the time.
	askAgain = 10 * time.Minute
)

// askState is one instance's "still playing?" clock (instance.ask), guarded
// by m.mu. Its times are by the rule's clock (askNowLocked).
type askState struct {
	run   *serverRun // the run the clock counts for; nil while the rule doesn't apply
	since time.Time  // when the 4-hour clock last started
	asked time.Time  // when the question went out; zero before
	askID string     // the question's message id in Discord, once posted

	off       time.Time // when the app decided to turn the game off; zero before
	stopTried time.Time // when it last asked for the stop
	// stopping: a stop is under way, from when it's asked for until it's
	// seen to have ended. While it is, nothing can keep the game on.
	stopping bool
	// stopStarted: the stop was accepted, so a job in the game's slot is
	// running it (or has, if that job is no longer running).
	stopStarted bool
	stopFails   int // stops that ran but left the server on

	// The channel the question goes to: the game's own, else the main one,
	// "" when it has none or its Discord posts are off.
	link    string
	seen    string    // the newest message id read so far: reads start after it
	readAt  time.Time // when the last read started
	readEnd time.Time // when the last read that has finished had started
	reading bool      // a read is under way
	denied  string    // why the bot can't read the channel (Discord said so); "" when it can
	name    string    // the channel's name, once the bot has read it
}

// AskStatus is a game's "ask" in Status (4.9): the clock and the question
// on its Home and the Overview, with Keep it on.
type AskStatus struct {
	Since   time.Time  `json:"since"`             // when the 4-hour clock last started
	AskAt   time.Time  `json:"askAt"`             // when the question goes out
	Asked   *time.Time `json:"asked,omitempty"`   // when it went out, once it has
	StopAt  *time.Time `json:"stopAt,omitempty"`  // when the game turns off without an answer
	CanRead bool       `json:"canRead"`           // the bot can read the channel, so any message there counts
	Channel string     `json:"channel,omitempty"` // the channel's name ("windrose" for #windrose), once known
	// Problem says, as a sentence ready to show, why only Keep it on counts
	// ("The bot can't read #windrose, so only Keep it on in the control
	// panel counts. Give the bot View Channel and Read Message History
	// there."); "" when CanRead.
	Problem string `json:"problem,omitempty"`
}

// askNowLocked is the rule's clock: the real one, or a test's.
func (m *Manager) askNowLocked() time.Time {
	if m.disc.now != nil {
		return m.disc.now()
	}
	return time.Now()
}

// askAppliesLocked says whether the rule runs for the instance now: its
// server is ready (and caught up after a takeover) and nothing it says can
// be trusted to show that nobody is on. The legacy game's presence is
// always proven, so the rule never applies to it.
func (m *Manager) askAppliesLocked(inst *instance) bool {
	run := inst.run
	return inst.srv != nil && run != nil && run.ready && !run.replaying &&
		(inst.info.Presence == game.Unknown || !m.presenceProvenLocked(inst))
}

// askStartLocked starts the clock for the instance's run, from scratch.
func (m *Manager) askStartLocked(inst *instance, now time.Time) {
	// Messages posted from now on count. Discord's ids carry the time they
	// were made, so this needs no read of the channel first.
	inst.ask = askState{run: inst.run, since: now, seen: discord.SnowflakeAt(now), link: m.askLinkLocked(inst)}
}

// askLinkLocked is the channel the instance's question goes to: where its
// posts go, "" when it has none or its Discord posts are off.
func (m *Manager) askLinkLocked(inst *instance) string {
	if r := m.discordRulesLocked(inst); r.on {
		return r.link
	}
	return ""
}

// askRestartLocked starts the 4-hour clock again: someone answered, joined
// or pressed Keep it on. A question that was waiting is answered.
func (m *Manager) askRestartLocked(inst *instance, now time.Time) {
	a := &inst.ask
	a.since, a.asked, a.askID = now, time.Time{}, ""
	// Only messages after this count from now on: one sent earlier and read
	// later (the bot was let in to the channel since, say) mustn't start
	// the clock again.
	if from := discord.SnowflakeAt(now); discord.Later(from, a.seen) {
		a.seen = from
	}
}

// askTickLocked moves the instance's clock on: reads the channel when it's
// time, asks at 4 hours, and turns the game off 2 hours after that.
func (m *Manager) askTickLocked(inst *instance, now time.Time) {
	a := &inst.ask
	if !m.askAppliesLocked(inst) {
		if a.run != nil {
			*a = askState{} // it ended, or its players can now be counted
		}
		return
	}
	run := inst.run
	if a.run != run {
		m.askStartLocked(inst, now)
	}
	r := m.discordRulesLocked(inst)
	link := m.askLinkLocked(inst)
	if link != a.link { // a new channel: find out afresh whether the bot can read it
		a.link, a.denied, a.name, a.readAt, a.reading = link, "", "", time.Time{}, false
	}
	if run.stopping && a.off.IsZero() {
		return // someone is stopping it already
	}
	if !a.off.IsZero() {
		m.askOffTickLocked(inst, now)
		return
	}
	token := m.settings.DiscordBotToken
	if link != "" && token != "" && !a.reading && (a.readAt.IsZero() || now.Sub(a.readAt) >= askEvery) {
		m.askReadLocked(inst, now, link, token)
	}
	switch {
	case a.asked.IsZero():
		if now.Sub(a.since) >= askAfter {
			m.askQuestionLocked(inst, now, r)
		}
	case now.Sub(a.asked) >= askWait:
		// Read the channel once more before turning it off, so an answer in
		// the last minutes still counts (waiting at most one more read).
		deadline := a.asked.Add(askWait)
		if m.askCanReadLocked(inst) && a.readEnd.Before(deadline) && now.Sub(deadline) < askEvery {
			if !a.reading {
				m.askReadLocked(inst, now, link, token)
			}
			return
		}
		m.askTurnOffLocked(inst, now, r)
	}
}

// askOffTickLocked follows a game the rule is turning off. A stop that
// couldn't start (another job was running, say) is tried again a minute
// later. One that started is the job's until the job ends; if the server is
// still on then, not stopping and not stopped, the stop chain gave up on it
// (it never forces a server off), so the app tries again 10 minutes later.
func (m *Manager) askOffTickLocked(inst *instance, now time.Time) {
	a, run := &inst.ask, inst.run
	if a.stopping && a.stopStarted && !run.stopping && !running(inst.job) {
		a.stopping, a.stopStarted, a.stopTried = false, false, now
		a.stopFails++
		m.log.Printf("still playing? %s: the stop ended with the server still on (%d)", inst.id, a.stopFails)
		if a.stopFails == 1 {
			m.addGameActivity(inst, "warning", "", fmt.Sprintf("%s didn't stop when the app tried to turn it off. The app tries again every 10 minutes. To keep it on, press Keep it on.", inst.short()))
		}
		go m.notify()
	}
	if !a.stopping && !run.stopping && now.Sub(a.stopTried) >= m.askRetryLocked(inst) {
		m.askStopLocked(inst, now)
	}
}

// askRetryLocked is how long after the last try the stop is tried again.
func (m *Manager) askRetryLocked(inst *instance) time.Duration {
	if inst.ask.stopFails > 0 {
		return askAgain
	}
	return askRetry
}

// askKeepLocked keeps a game on that the rule decided to turn off, when
// someone shows they want it after all (Keep it on, a join) before the
// stop is under way: the clock starts again. The run is no longer being
// stopped by the rule, so a later stop is an ordinary one, with its own
// stopped post.
func (m *Manager) askKeepLocked(inst *instance, now time.Time) {
	a := &inst.ask
	a.off, a.stopTried, a.stopping, a.stopStarted, a.stopFails = time.Time{}, time.Time{}, false, false, 0
	if run := a.run; run != nil && run.autoStopped && run.autoReason == stopAsked {
		run.autoStopped, run.autoReason = false, stopStopped
	}
	m.askRestartLocked(inst, now)
}

// askStopUnderwayLocked says whether the rule's stop is under way, so
// nothing can keep the game on now.
func (m *Manager) askStopUnderwayLocked(inst *instance) bool {
	a := &inst.ask
	return !a.off.IsZero() && (a.stopping || inst.run != nil && inst.run.stopping)
}

// askCanReadLocked says whether a message in the channel would count: the
// game has a channel, there's a bot token, and Discord hasn't said the bot
// can't read it.
func (m *Manager) askCanReadLocked(inst *instance) bool {
	a := &inst.ask
	return a.link != "" && m.settings.DiscordBotToken != "" && a.denied == ""
}

// askProblemLocked is AskStatus.Problem.
func (m *Manager) askProblemLocked(inst *instance) string {
	a := &inst.ask
	const only = "so only Keep it on in the control panel counts."
	channel := "the game's Discord channel"
	if a.name != "" {
		channel = "#" + a.name
	}
	switch {
	case m.askCanReadLocked(inst):
		return ""
	case a.link == "" && !m.discordRulesLocked(inst).on:
		return "This game's Discord posts are off, " + only
	case a.link == "":
		return "This game has no Discord channel, " + only
	case m.settings.DiscordBotToken == "":
		return "There's no Discord bot that can read " + channel + ", " + only
	}
	return "The bot can't read " + channel + ", " + only + " " + a.denied
}

// askReadLocked starts a read of the instance's channel in the background.
func (m *Manager) askReadLocked(inst *instance, now time.Time, link, token string) {
	a := &inst.ask
	a.reading, a.readAt = true, now
	run, after := a.run, a.seen
	m.go_(func() { m.readAskChannel(inst, run, link, token, after, now) })
}

// readAskChannel reads the messages posted in the channel after the id
// after, and restarts the clock if someone sent one. started is when the
// read began, by the rule's clock.
func (m *Manager) readAskChannel(inst *instance, run *serverRun, link, token, after string, started time.Time) {
	ctx := m.ctx
	h := m.hookFor(ctx, link)
	name := h.name
	var msgs []discord.Message
	var err error
	if h.channel == "" {
		if h.gone {
			err = &discord.Error{Msg: h.err}
		} else {
			err = &discord.Error{Msg: "Discord didn't say which channel the webhook posts in.", Temporary: true}
		}
	} else {
		bot := discord.Bot{Token: token, API: m.opt.DiscordAPI}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		msgs, err = bot.Messages(rctx, h.channel, after, 20)
		if name == "" {
			// Its name needs only View Channel, so it may come even when the
			// messages don't, and the panel can say which channel it is.
			if n, e := bot.ChannelName(rctx, h.channel); e == nil {
				name = n
			}
		}
		cancel()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if hi := m.disc.hooks[link]; hi != nil && name != "" && hi.channel == h.channel {
		hi.name = name
	}
	a := &inst.ask
	if inst.run != run || a.run != run || a.link != link {
		return // the run ended or the channel changed meanwhile: this read counts for nothing
	}
	a.reading = false
	if started.After(a.readEnd) {
		a.readEnd = started
	}
	if name != "" {
		a.name = name
	}
	var de *discord.Error
	switch {
	case err == nil:
		a.denied = ""
		// Only what's newer than the clock's last start counts: Keep it on
		// may have been pressed while this read was under way.
		from, person := a.seen, false
		for _, msg := range msgs {
			if !discord.Later(msg.ID, from) {
				continue
			}
			if discord.Later(msg.ID, a.seen) {
				a.seen = msg.ID
			}
			person = person || msg.FromPerson()
		}
		if person && a.off.IsZero() {
			answered := !a.asked.IsZero()
			m.askRestartLocked(inst, m.askNowLocked())
			if answered {
				m.addGameActivity(inst, "info", "", fmt.Sprintf("Someone answered in Discord, so %s stays on. The app asks again in 4 hours.", inst.short()))
			}
			go m.notify()
		}
	case errors.As(err, &de) && !de.Temporary:
		was := a.denied
		a.denied = de.Msg
		if de.Status == 403 {
			a.denied = "Give the bot View Channel and Read Message History there."
		}
		if was != a.denied {
			m.log.Printf("still playing? %s: can't read the channel: %v", inst.id, err)
			go m.notify()
		}
	default:
		// Discord can't be reached for now: the next read tries again, and
		// whether the bot may read the channel stays as it was.
	}
}

// askQuestionLocked asks whether the game should stay on: in its channel
// when it has one, and in the control panel either way.
func (m *Manager) askQuestionLocked(inst *instance, now time.Time, r discordRules) {
	a := &inst.ask
	a.asked = now
	go m.notify()
	run, short := a.run, inst.short()
	if a.link == "" {
		m.addGameActivity(inst, "warning", "", fmt.Sprintf("%s has been on for 4 hours. Press Keep it on in the control panel within 2 hours to keep it on; otherwise it will be turned off.", short))
		return
	}
	text := r.words(inst, askQuestionText(postServerNameLocked(inst, run), m.askCanReadLocked(inst)))
	m.queueDiscord(discordPost{
		link: a.link,
		text: func() string { return text },
		posted: func(id string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			a := &inst.ask
			if a.run != run || a.asked.IsZero() || id == "" {
				return
			}
			a.askID = id
			// Every answer comes after the question, so the reads start there
			// at the latest, even if this computer's clock is ahead of
			// Discord's.
			if discord.Later(a.seen, id) {
				a.seen = id
			}
		},
		done: func(err error) {
			if err != nil {
				m.addGameActivity(inst, "warning", "", fmt.Sprintf("Couldn't ask in Discord whether %s should stay on. %v", short, err))
				return
			}
			m.addGameActivity(inst, "discord", "", fmt.Sprintf("Asked in Discord whether %s should stay on.", short))
		},
	})
}

// askQuestionText is the question (spec 4.6), worded for whether a message
// in the channel counts as an answer.
func askQuestionText(server string, canRead bool) string {
	name := "**" + discord.Escape(server) + "**"
	if canRead {
		return "⏳ " + name + " has been on for 4 hours. Should it stay on? Send any message in this channel in the next 2 hours to keep it on. Otherwise it will be turned off."
	}
	return "⏳ " + name + " has been on for 4 hours. Should it stay on? Ask the owner to press Keep it on in the control panel within 2 hours. Otherwise it will be turned off."
}

// askOffText is the line posted when nobody answered: the game's only
// stopped post for that stop.
func askOffText(server string) string {
	return "🔴 Turning **" + discord.Escape(server) + "** off: nobody answered for 2 hours."
}

// askTurnOffLocked posts that the game is being turned off, once, and
// stops it.
func (m *Manager) askTurnOffLocked(inst *instance, now time.Time, r discordRules) {
	a := &inst.ask
	a.off = now
	go m.notify()
	if a.link != "" {
		short := inst.short()
		text := r.words(inst, askOffText(postServerNameLocked(inst, a.run)))
		m.queueDiscord(discordPost{
			link: a.link,
			text: func() string { return text },
			done: func(err error) {
				if err != nil {
					m.addGameActivity(inst, "warning", "", fmt.Sprintf("Couldn't post in Discord that %s is being turned off. %v", short, err))
				}
			},
		})
	}
	m.askStopLocked(inst, now)
}

// askStopLocked asks for the stop (stopAsked), in the background: the stop
// chain runs as a quiet job, as auto-stop's does.
func (m *Manager) askStopLocked(inst *instance, now time.Time) {
	a := &inst.ask
	first := a.stopTried.IsZero()
	a.stopTried, a.stopping = now, true
	run, off := a.run, a.off
	stop := m.disc.stop
	if stop == nil {
		stop = m.autoStopGame
	}
	m.go_(func() {
		err := stop(inst, stopAsked)
		m.mu.Lock()
		// Still the same decision (Keep it on can't undo one under way, but
		// the run may have ended).
		if a := &inst.ask; a.run == run && a.off.Equal(off) {
			if err == nil {
				a.stopStarted = true
			} else {
				a.stopping = false // tried again in a minute
			}
		}
		m.mu.Unlock()
		if err != nil {
			m.log.Printf("still playing? %s: couldn't stop it: %v", inst.id, err)
			if first {
				m.addGameActivity(inst, "warning", "", fmt.Sprintf("Couldn't turn %s off yet: %v The app tries again every minute.", inst.short(), err))
			}
		}
	})
}

// askStatusLocked is the instance's "still playing?" state for Status, or
// nil when the rule doesn't apply to it now.
func (m *Manager) askStatusLocked(inst *instance) *AskStatus {
	a := &inst.ask
	if a.run == nil || a.run != inst.run || !m.askAppliesLocked(inst) {
		return nil
	}
	st := &AskStatus{Since: a.since, AskAt: a.since.Add(askAfter), CanRead: m.askCanReadLocked(inst),
		Channel: a.name, Problem: m.askProblemLocked(inst)}
	if !a.asked.IsZero() {
		asked, stop := a.asked, a.asked.Add(askWait)
		if !a.off.IsZero() && !m.askStopUnderwayLocked(inst) {
			stop = a.stopTried.Add(m.askRetryLocked(inst)) // the next try
		}
		st.Asked, st.StopAt = &asked, &stop
	}
	return st
}

// KeepOn restarts the "still playing?" clock of a running game, as a
// message in its channel would (POST /api/games/{id}/keep-on).
func (m *Manager) KeepOn(id string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := inst.run
	switch {
	case run == nil:
		return fmt.Errorf("%s isn't running.", inst.short())
	case inst.ask.run == run && m.askStopUnderwayLocked(inst):
		return fmt.Errorf("%s is already being turned off.", inst.short())
	case run.stopping:
		return fmt.Errorf("%s is stopping.", inst.short())
	}
	if inst.ask.run != run || !m.askAppliesLocked(inst) {
		return nil // the rule isn't running for it: there is no clock to restart
	}
	// Also when the app decided to turn it off but couldn't yet: the owner
	// wants it on after all.
	m.askKeepLocked(inst, m.askNowLocked())
	m.addGameActivity(inst, "info", "", fmt.Sprintf("%s stays on: Keep it on was pressed. The app asks again in 4 hours.", inst.short()))
	go m.notify()
	return nil
}

// askObserver is stream D's observer (hooks.go): the "still playing?"
// clock. Every method runs with m.mu held and must not block.
type askObserver struct{ m *Manager }

// onReady starts the clock when a run the app follows live is ready.
func (o askObserver) onReady(inst *instance) {
	if o.m.askAppliesLocked(inst) {
		o.m.askStartLocked(inst, o.m.askNowLocked())
	}
}

// onEvent: a join, or a count above zero, shows someone is playing. It
// keeps on a game the app decided to turn off but whose stop isn't under
// way (a stop that left it on is tried again later): turning it off now
// would put that player out.
func (o askObserver) onEvent(inst *instance, ev game.Event) {
	a := &inst.ask
	if a.run == nil || a.run != inst.run || o.m.askStopUnderwayLocked(inst) {
		return
	}
	if ev.Kind == game.KindJoin || ev.Kind == game.KindCount && ev.Count > 0 {
		if !a.off.IsZero() {
			o.m.askKeepLocked(inst, o.m.askNowLocked())
			o.m.addGameActivity(inst, "info", "", fmt.Sprintf("%s stays on: someone joined. The app asks again in 4 hours.", inst.short()))
			return
		}
		o.m.askRestartLocked(inst, o.m.askNowLocked())
	}
}

// onExit forgets the run's clock.
func (o askObserver) onExit(inst *instance, run *serverRun, exit *ExitInfo) {
	if inst.ask.run == run {
		inst.ask = askState{}
	}
}

// onTick moves every game's clock on, by the rule's own clock (so tests can
// make hours pass at once).
func (o askObserver) onTick(now time.Time) {
	at := o.m.askNowLocked()
	o.m.eachInstance(func(inst *instance) { o.m.askTickLocked(inst, at) })
}

func (o askObserver) onJob(inst *instance, job *Job) {}

// onTakeover starts the clock for a server the app took over, once it has
// caught up with its log: the app can't know how long it has been on
// unwatched, so it counts from now, which only ever keeps it on longer.
func (o askObserver) onTakeover(inst *instance) {
	if o.m.askAppliesLocked(inst) {
		o.m.askStartLocked(inst, o.m.askNowLocked())
	}
}

func (o askObserver) onWake(cause string) {}
