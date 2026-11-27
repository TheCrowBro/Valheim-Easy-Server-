package manager

// The "still playing?" rule (spec 4.6) with a stand-in Discord (its
// webhooks and, for the bot, the messages endpoint) and a pretend clock, so
// hours pass at once. The game is a stand-in that can't tell who is on,
// like Windrose.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
)

// stopsSeen stands in for A's autoStopGame and records the stops the rule
// asks for. Like the real one, it marks the run auto-stopped and runs the
// stop as a quiet job in the game's slot, which marks the run stopping. A
// stop that works leaves it stopping until the test ends the run (as the
// server's exit would); with fail set, the stop gives up and the server
// stays on, as the stop chain does when a server doesn't exit in time (it
// never forces one off). With hold set, the job waits for it to be closed.
type stopsSeen struct {
	mu   sync.Mutex
	m    *Manager
	whys []stopReason
	fail bool
	hold chan struct{}
}

func (s *stopsSeen) stop(inst *instance, why stopReason) error {
	s.mu.Lock()
	s.whys = append(s.whys, why)
	fail, hold := s.fail, s.hold
	s.mu.Unlock()
	m := s.m
	m.mu.Lock()
	run := inst.run
	if run != nil {
		run.autoStopped, run.autoReason = true, why
	}
	m.mu.Unlock()
	return m.runQuietJob(inst, "stop", "Stopping "+inst.short(), func(ctx context.Context, j *jobCtl) error {
		m.mu.Lock()
		if run != nil {
			run.stopping = true
		}
		m.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
			}
		}
		if !fail {
			return nil
		}
		m.mu.Lock()
		if run != nil {
			run.stopping = false
		}
		m.mu.Unlock()
		return errors.New("The server is taking unusually long to stop.")
	})
}

func (s *stopsSeen) set(fail bool, hold chan struct{}) {
	s.mu.Lock()
	s.fail, s.hold = fail, hold
	s.mu.Unlock()
}

func (s *stopsSeen) all() []stopReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stopReason(nil), s.whys...)
}

// askGame makes a manager with the stand-in game in its own channel
// (webhook 2) and the main channel (webhook 1); token is the bot's.
func askGame(t *testing.T, api *standIn, clock *fakeClock, token string, d *servers.Discord) (*Manager, *instance, *stopsSeen) {
	t.Helper()
	stops := &stopsSeen{}
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink("1")
		m.settings.DiscordBotToken = token
		m.disc.now, m.disc.stop, stops.m = clock.Now, stops.stop, m
		inst = addTestGame(t, m, testGame{seafarer}, d)
	})
	return m, inst, stops
}

func askStatus(t *testing.T, m *Manager, inst *instance) *AskStatus {
	t.Helper()
	return gameStatus(t, m, inst.id).Ask
}

func waitAsk(t *testing.T, m *Manager, inst *instance, what string, ok func(a *AskStatus) bool) *AskStatus {
	t.Helper()
	var a *AskStatus
	waitFor(t, what, 20*time.Second, func() bool {
		a = askStatus(t, m, inst)
		return a != nil && ok(a)
	})
	return a
}

// The whole rule, with a bot that can read the channel: the question at 4
// hours, a message in the channel answering it (the clock starts again),
// and with no answer the next time, one "Turning … off" post and one stop.
func TestStillPlayingRule(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	m, inst, stops := askGame(t, api, clock, "tok",
		&servers.Discord{On: true, Webhook: hookLink("2"), Stopped: true, Daily: true})
	start := clock.Now()
	run := fakeRun(m, inst, "Sea Crew")
	if posts := waitPosts(t, api, otherHook, 1); !strings.HasPrefix(posts[0], "🟢 **Sea Crew** is online") {
		t.Fatalf("online post: %q", posts)
	}

	// The clock runs from when it was ready, and the bot reads the channel.
	a := waitAsk(t, m, inst, "the channel to be read", func(a *AskStatus) bool { return a.Channel != "" })
	if !a.Since.Equal(start) || !a.AskAt.Equal(start.Add(4*time.Hour)) || a.Asked != nil || !a.CanRead ||
		a.Channel != "chat-"+channelOf(otherHook) || a.Problem != "" {
		t.Fatalf("at the start: %+v", a)
	}
	api.botSays(otherHook) // a bot's message (the waker's reply) is no answer
	clock.Add(3*time.Hour + 59*time.Minute)
	reads := api.readCount()
	waitFor(t, "another read", 10*time.Second, func() bool { return api.readCount() > reads })
	time.Sleep(500 * time.Millisecond)
	if n := len(api.postsIn(otherHook)); n != 1 {
		t.Fatalf("asked before 4 hours: %q", api.postsIn(otherHook))
	}
	if a := askStatus(t, m, inst); a == nil || !a.Since.Equal(start) {
		t.Fatalf("a bot's message restarted the clock: %+v", a)
	}

	// 4 hours: the question, worded for a channel the bot can read.
	clock.Add(time.Minute)
	posts := waitPosts(t, api, otherHook, 2)
	want := "⏳ **Sea Crew** has been on for 4 hours. Should it stay on? Send any message in this channel in the next 2 hours to keep it on. Otherwise it will be turned off."
	if posts[1] != want {
		t.Fatalf("the question:\n%s\nwant:\n%s", posts[1], want)
	}
	asked := clock.Now()
	a = waitAsk(t, m, inst, "the question in the status", func(a *AskStatus) bool { return a.Asked != nil })
	if !a.Asked.Equal(asked) || !a.StopAt.Equal(asked.Add(2*time.Hour)) {
		t.Fatalf("after asking: %+v", a)
	}
	waitFor(t, "the activity entry", 5*time.Second, func() bool {
		return hasText(m, "discord", "Asked in Discord whether Seafarer should stay on.")
	})

	// Someone answers: the next read restarts the clock.
	clock.Add(30 * time.Minute)
	api.say(otherHook)
	clock.Add(2 * time.Minute)
	a = waitAsk(t, m, inst, "the answer to count", func(a *AskStatus) bool { return a.Asked == nil })
	if !a.Since.After(asked) {
		t.Fatalf("after the answer: %+v", a)
	}
	restarted := a.Since
	if !hasText(m, "info", "Someone answered in Discord, so Seafarer stays on.") {
		t.Fatal("no activity about the answer")
	}

	// 4 more hours: asked again. Nobody answers for 2 hours: turned off,
	// once, after one more read of the channel.
	clock.Add(4 * time.Hour)
	waitPosts(t, api, otherHook, 3)
	clock.Add(2 * time.Hour)
	posts = waitPosts(t, api, otherHook, 4)
	if want := "🔴 Turning **Sea Crew** off: nobody answered for 2 hours."; posts[3] != want {
		t.Fatalf("the last post:\n%s\nwant:\n%s", posts[3], want)
	}
	waitFor(t, "the stop", 10*time.Second, func() bool { return len(stops.all()) > 0 })
	for i := 0; i < 5; i++ { // later ticks change nothing
		clock.Add(5 * time.Minute)
		time.Sleep(250 * time.Millisecond)
	}
	if got := stops.all(); len(got) != 1 || got[0] != stopAsked {
		t.Fatalf("stops = %v, want one stopAsked", got)
	}
	if n := api.count("Turning **Sea Crew** off"); n != 1 {
		t.Fatalf("%d Turning off posts: %q", n, api.postsIn(otherHook))
	}
	if !restarted.Add(4 * time.Hour).Before(clock.Now()) {
		t.Fatal("the clock didn't move as the test thinks")
	}

	// Its exit (with the reason the rule gave) makes no second post, and the
	// rule forgets the run.
	m.mu.Lock()
	run.autoStopped, run.autoReason = true, stopAsked
	_, stoppedPost := m.stoppedPostLocked(run, stopAsked)
	m.mu.Unlock()
	if stoppedPost {
		t.Fatal("a stopped post after Turning off")
	}
	endRun(m, inst, run)
	if a := askStatus(t, m, inst); a != nil {
		t.Fatalf("after the exit: %+v", a)
	}
	if err := m.KeepOn(inst.id); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Fatalf("Keep it on with the game stopped: %v", err)
	}
}

// A bot that isn't allowed to read the channel (403): the question asks
// for Keep it on in the control panel, which then restarts the clock, and
// the panel says why only that counts.
func TestStillPlayingCantRead(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	api.setDeny(true)
	m, inst, stops := askGame(t, api, clock, "tok", nil) // the main channel
	fakeRun(m, inst, "Sea Crew")
	a := waitAsk(t, m, inst, "the bot to be refused", func(a *AskStatus) bool { return !a.CanRead })
	want := "The bot can't read #chat-" + channelOf(mainHook) + ", so only Keep it on in the control panel counts. Give the bot View Channel and Read Message History there."
	if a.Problem != want || a.Channel != "chat-"+channelOf(mainHook) {
		t.Fatalf("problem %q (channel %q)\nwant %q", a.Problem, a.Channel, want)
	}

	clock.Add(4 * time.Hour)
	posts := waitPosts(t, api, mainHook, 2)
	if want := "Seafarer · ⏳ **Sea Crew** has been on for 4 hours. Should it stay on? Ask the owner to press Keep it on in the control panel within 2 hours. Otherwise it will be turned off."; posts[1] != want {
		t.Fatalf("the question:\n%s\nwant:\n%s", posts[1], want)
	}
	waitAsk(t, m, inst, "the question in the status", func(a *AskStatus) bool { return a.Asked != nil })
	api.say(mainHook) // can't be read, so it can't count
	clock.Add(90 * time.Minute)
	if err := m.KeepOn(inst.id); err != nil {
		t.Fatal(err)
	}
	a = askStatus(t, m, inst)
	if a == nil || a.Asked != nil || !a.Since.Equal(clock.Now()) {
		t.Fatalf("after Keep it on: %+v", a)
	}
	if !hasText(m, "info", "Seafarer stays on: Keep it on was pressed.") {
		t.Fatal("no activity about Keep it on")
	}
	clock.Add(2*time.Hour + time.Minute) // past the old deadline: nothing happens
	time.Sleep(500 * time.Millisecond)
	if len(stops.all()) != 0 || api.count("Turning") != 0 {
		t.Fatalf("turned off after Keep it on: %v %q", stops.all(), api.postsIn(mainHook))
	}

	// The bot is given access before the next question (due 4 hours after
	// Keep it on): it's worded for the channel again.
	api.setDeny(false)
	clock.Add(time.Hour) // 3 h 1 min after Keep it on
	waitAsk(t, m, inst, "the bot to read again", func(a *AskStatus) bool { return a.CanRead && a.Problem == "" })
	if n := len(api.postsIn(mainHook)); n != 2 {
		t.Fatalf("asked again too soon: %q", api.postsIn(mainHook))
	}
	clock.Add(time.Hour)
	posts = waitPosts(t, api, mainHook, 3)
	if !strings.Contains(posts[2], "Send any message in this channel") {
		t.Fatalf("the second question: %q", posts[2])
	}
	clock.Add(2 * time.Hour)
	posts = waitPosts(t, api, mainHook, 4)
	if posts[3] != "Seafarer · 🔴 Turning **Sea Crew** off: nobody answered for 2 hours." {
		t.Fatalf("the last post: %q", posts[3])
	}
	waitFor(t, "the stop", 10*time.Second, func() bool { return len(stops.all()) == 1 })
}

// No bot token: the question still goes out, worded for Keep it on.
func TestStillPlayingNoBot(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	m, inst, _ := askGame(t, api, clock, "", &servers.Discord{On: true, Webhook: hookLink("2")})
	fakeRun(m, inst, "Sea Crew")
	a := waitAsk(t, m, inst, "the clock", func(a *AskStatus) bool { return true })
	if a.CanRead || !strings.HasPrefix(a.Problem, "There's no Discord bot that can read") {
		t.Fatalf("no bot: %+v", a)
	}
	clock.Add(4 * time.Hour)
	posts := waitPosts(t, api, otherHook, 2)
	if !strings.Contains(posts[1], "Ask the owner to press Keep it on in the control panel within 2 hours.") {
		t.Fatalf("the question: %q", posts[1])
	}
	if n := api.readCount(); n != 0 {
		t.Fatalf("read the channel %d times without a bot", n)
	}
}

// No Discord channel at all: the same clock, with the question only in
// the control panel, and 2 hours later the game is turned off, with
// nothing posted anywhere (10.2 Q2).
func TestStillPlayingNoChannel(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	stops := &stopsSeen{}
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordBotToken = "tok"
		m.disc.now, m.disc.stop, stops.m = clock.Now, stops.stop, m
		inst = addTestGame(t, m, testGame{seafarer}, nil)
	})
	fakeRun(m, inst, "Sea Crew")
	a := waitAsk(t, m, inst, "the clock", func(a *AskStatus) bool { return true })
	if a.CanRead || a.Problem != "This game has no Discord channel, so only Keep it on in the control panel counts." {
		t.Fatalf("no channel: %+v", a)
	}
	clock.Add(4 * time.Hour)
	waitAsk(t, m, inst, "the question in the panel", func(a *AskStatus) bool { return a.Asked != nil })
	if !hasText(m, "warning", "Seafarer has been on for 4 hours. Press Keep it on") {
		t.Fatal("no activity with the question")
	}
	clock.Add(2 * time.Hour)
	waitFor(t, "the stop", 10*time.Second, func() bool { return len(stops.all()) == 1 })
	if posts := api.all(); len(posts) != 0 || api.readCount() != 0 {
		t.Fatalf("posted %+v, read %d times", posts, api.readCount())
	}
}

// Valheim's players are always counted, so the rule never applies to it,
// and a game whose posts are off is asked only in the panel.
func TestStillPlayingOnlyWhenNeeded(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	m, inst, stops := askGame(t, api, clock, "tok", &servers.Discord{On: false, Webhook: hookLink("2")})
	m.mu.Lock()
	legacy := m.legacy()
	legacy.run = &serverRun{inst: legacy, ready: true}
	applies := m.askAppliesLocked(legacy)
	legacy.run = nil
	m.mu.Unlock()
	if applies {
		t.Fatal("the rule applies to Valheim")
	}

	fakeRun(m, inst, "Sea Crew")
	a := waitAsk(t, m, inst, "the clock", func(a *AskStatus) bool { return true })
	if a.CanRead || a.Problem != "This game's Discord posts are off, so only Keep it on in the control panel counts." {
		t.Fatalf("posts off: %+v", a)
	}
	clock.Add(6 * time.Hour)
	waitAsk(t, m, inst, "the question", func(a *AskStatus) bool { return a.Asked != nil })
	clock.Add(2 * time.Hour)
	waitFor(t, "the stop", 10*time.Second, func() bool { return len(stops.all()) == 1 })
	if posts := api.all(); len(posts) != 0 {
		t.Fatalf("a game with its posts off posted: %+v", posts)
	}
}

// A join (a game whose join lines aren't proven yet still has them now
// and then) restarts the clock like a message would.
func TestStillPlayingJoinRestarts(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	m, inst, _ := askGame(t, api, clock, "", nil)
	run := fakeRun(m, inst, "Sea Crew")
	waitAsk(t, m, inst, "the clock", func(a *AskStatus) bool { return true })
	clock.Add(4 * time.Hour)
	waitAsk(t, m, inst, "the question", func(a *AskStatus) bool { return a.Asked != nil })
	clock.Add(time.Hour)
	m.handleEvents(run, []game.Event{{Kind: game.KindJoin, Name: "Alex", Time: time.Now()}}, nil)
	a := askStatus(t, m, inst)
	if a == nil || a.Asked != nil || !a.Since.Equal(clock.Now()) {
		t.Fatalf("after a join: %+v", a)
	}
}

// A stop that runs but leaves the server on (it didn't exit in time, and
// the stop chain never forces a server off) doesn't leave the rule stuck:
// while the stop is under way nothing keeps the game on, then the app
// tries again every 10 minutes without posting again, and until then Keep
// it on or a join keeps it on after all, and the clock starts again.
func TestStillPlayingStopLeftOn(t *testing.T) {
	clock := newFakeClock()
	api := newStandIn(t, clock.Now)
	m, inst, stops := askGame(t, api, clock, "", &servers.Discord{On: true, Webhook: hookLink("2")})
	hold := make(chan struct{})
	stops.set(true, hold)
	run := fakeRun(m, inst, "Sea Crew")
	waitAsk(t, m, inst, "the clock", func(a *AskStatus) bool { return true })
	clock.Add(4 * time.Hour)
	waitAsk(t, m, inst, "the question", func(a *AskStatus) bool { return a.Asked != nil })
	clock.Add(2 * time.Hour)
	waitFor(t, "the stop", 10*time.Second, func() bool { return len(stops.all()) == 1 })
	off := clock.Now()

	// While the stop is under way, Keep it on can't keep it on.
	if err := m.KeepOn(inst.id); err == nil || !strings.Contains(err.Error(), "is already being turned off") {
		t.Fatalf("Keep it on during the stop: %v", err)
	}
	close(hold)
	waitFor(t, "the failed stop to be noticed", 10*time.Second, func() bool {
		return hasText(m, "warning", "Seafarer didn't stop when the app tried to turn it off.")
	})
	a := askStatus(t, m, inst)
	if a == nil || a.StopAt == nil || !a.StopAt.Equal(off.Add(10*time.Minute)) {
		t.Fatalf("waiting to try again: %+v (turned off at %v)", a, off)
	}

	// 10 minutes later: tried again, with no second post; it fails again,
	// with no second warning.
	stops.set(true, nil)
	clock.Add(9 * time.Minute)
	time.Sleep(300 * time.Millisecond)
	if n := len(stops.all()); n != 1 {
		t.Fatalf("tried again after 9 minutes: %d stops", n)
	}
	clock.Add(time.Minute)
	waitFor(t, "the second stop", 10*time.Second, func() bool { return len(stops.all()) == 2 })
	waitAsk(t, m, inst, "the second stop to fail", func(a *AskStatus) bool {
		return a.StopAt != nil && a.StopAt.After(clock.Now())
	})
	if n := api.count("Turning **Sea Crew** off"); n != 1 {
		t.Fatalf("%d Turning off posts", n)
	}
	if n := countText(m, "didn't stop when the app tried to turn it off"); n != 1 {
		t.Fatalf("%d warnings about the stop", n)
	}

	// Keep it on now works: the clock starts again, and the run is no
	// longer being stopped by the rule.
	if err := m.KeepOn(inst.id); err != nil {
		t.Fatal(err)
	}
	a = askStatus(t, m, inst)
	if a == nil || a.Asked != nil || a.StopAt != nil || !a.Since.Equal(clock.Now()) {
		t.Fatalf("after Keep it on: %+v", a)
	}
	m.mu.Lock()
	auto := run.autoStopped
	m.mu.Unlock()
	if auto {
		t.Fatal("the run is still marked as stopped by the rule")
	}
	clock.Add(30 * time.Minute)
	time.Sleep(300 * time.Millisecond)
	if n := len(stops.all()); n != 2 {
		t.Fatalf("stopped after Keep it on: %d stops", n)
	}

	// 4 hours on it asks again; 2 hours after that it's turned off again,
	// that stop leaves it on too, and a join keeps it on.
	clock.Add(3*time.Hour + 30*time.Minute)
	waitPosts(t, api, otherHook, 4) // online, question, Turning off, question
	clock.Add(2 * time.Hour)
	waitFor(t, "the third stop", 10*time.Second, func() bool { return len(stops.all()) == 3 })
	waitAsk(t, m, inst, "the third stop to fail", func(a *AskStatus) bool {
		return a.StopAt != nil && a.StopAt.After(clock.Now())
	})
	m.handleEvents(run, []game.Event{{Kind: game.KindJoin, Name: "Alex", Time: time.Now()}}, nil)
	a = askStatus(t, m, inst)
	if a == nil || a.Asked != nil || !a.Since.Equal(clock.Now()) {
		t.Fatalf("after a join: %+v", a)
	}
	if !hasText(m, "info", "Seafarer stays on: someone joined.") {
		t.Fatal("no activity about the join")
	}
	clock.Add(20 * time.Minute)
	time.Sleep(300 * time.Millisecond)
	if n := len(stops.all()); n != 3 {
		t.Fatalf("stopped after a join: %d stops", n)
	}
}

// countText counts the activity entries that say text.
func countText(m *Manager, text string) int {
	acts, _ := m.Activity(0, 0)
	n := 0
	for _, a := range acts {
		if strings.Contains(a.Text, text) {
			n++
		}
	}
	return n
}
