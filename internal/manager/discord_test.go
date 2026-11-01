package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/mods"
)

// fakeDiscord records the messages posted to its webhooks.
type fakeDiscord struct {
	URL   string
	mu    sync.Mutex
	posts []string
	gone  atomic.Bool // answer like a deleted webhook
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.gone.Load() {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "Unknown Webhook", "code": 10015}`))
			return
		}
		if r.Method == http.MethodGet { // which Discord server the webhook is in
			w.Write([]byte(`{"id": "123", "type": 1, "guild_id": "g1", "channel_id": "2"}`))
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.posts = append(f.posts, body.Content)
		f.mu.Unlock()
		w.Write([]byte(`{"id": "1"}`))
	}))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *fakeDiscord) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

// count is how many posts contain text.
func (f *fakeDiscord) count(text string) int {
	n := 0
	for _, p := range f.all() {
		if strings.Contains(p, text) {
			n++
		}
	}
	return n
}

func (f *fakeDiscord) wait(t *testing.T, text string, n int) {
	t.Helper()
	waitFor(t, "a Discord post with "+text, 20*time.Second, func() bool { return f.count(text) >= n })
}

func turnOnDiscord(t *testing.T, m *Manager) {
	t.Helper()
	s := m.Settings()
	s.Discord = true
	s.DiscordWebhook = "https://discord.com/channels/1/2"
	if err := m.UpdateSettings(s); err == nil || !strings.Contains(err.Error(), "link to the channel") {
		t.Fatalf("a channel link was accepted: %v", err)
	}
	s.DiscordWebhook = " https://discord.com/api/webhooks/123/abc_DEF\n"
	s.DiscordPassword = true
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
}

func startOnline(t *testing.T, m *Manager) string {
	t.Helper()
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	var code string
	waitFor(t, "the join code", 20*time.Second, func() bool {
		code = m.Status().Join.Code
		return code != ""
	})
	return code
}

func TestDiscordPostsJoinCode(t *testing.T) {
	fake := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.DiscordURL = fake.URL
	install(t, m)
	turnOnDiscord(t, m)

	if err := m.TestDiscord("https://discord.com/api/webhooks/123/abc"); err != nil {
		t.Fatal(err)
	}
	if fake.count("can post here") != 1 {
		t.Fatalf("test message: %q", fake.all())
	}
	if err := m.TestDiscord("https://discord.gg/abc"); err == nil {
		t.Fatal("tested an invite link")
	}

	code := startOnline(t, m)
	fake.wait(t, "Join code: **"+code+"**", 1)
	posts := fake.all()
	online := posts[len(posts)-1]
	for _, want := range []string{"is online", "World: Midgard", "||" + m.Settings().Password + "||", "join by code"} {
		if !strings.Contains(online, want) {
			t.Errorf("the online message has no %q:\n%s", want, online)
		}
	}
	waitFor(t, "the panel to show it was posted", 5*time.Second, func() bool {
		d := m.Status().Discord
		return d != nil && d.Posted != nil && d.Error == ""
	})
	if !hasActivity(m, "discord", "") {
		t.Error("no activity about posting")
	}

	// A restart says so, then posts the new code.
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	fake.wait(t, "is restarting", 1)
	var code2 string
	waitFor(t, "a new join code", 30*time.Second, func() bool {
		code2 = m.Status().Join.Code
		return code2 != "" && code2 != code
	})
	fake.wait(t, "Join code: **"+code2+"**", 1)

	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	fake.wait(t, "has stopped", 1)
	waitJob(t, m, currentJobID(m)) // it finishes after the post goes out
	if n := fake.count("is online"); n != 2 {
		t.Fatalf("posted online %d times: %q", n, fake.all())
	}

	// A deleted webhook shows on the panel and in the activity feed.
	fake.gone.Store(true)
	startOnline(t, m)
	waitFor(t, "the error on the panel", 20*time.Second, func() bool {
		d := m.Status().Discord
		return d != nil && strings.Contains(d.Error, "webhook link doesn't work")
	})
	if !hasActivity(m, "warning", "") {
		t.Error("no warning activity")
	}
}

// Turning on "Include the password" while the server is online posts again,
// so friends get the password; turning it off doesn't.
func TestDiscordPasswordTurnedOnLater(t *testing.T) {
	fake := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.DiscordURL = fake.URL
	install(t, m)
	turnOnDiscord(t, m)
	s := m.Settings()
	s.DiscordPassword = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	code := startOnline(t, m)
	fake.wait(t, "Join code: **"+code+"**", 1)
	if fake.count("Password:") != 0 {
		t.Fatalf("the password was posted: %q", fake.all())
	}
	s.DiscordPassword = true
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	fake.wait(t, "Password: ||", 1)
	s.DiscordPassword = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Discord posts to go out", 20*time.Second, func() bool { return m.discordPending.Load() == 0 })
	if n := fake.count("is online"); n != 2 {
		t.Fatalf("posted online %d times: %q", n, fake.all())
	}
}

// When crossplay can't start, the panel says so instead of waiting for a
// join code, and nothing is posted in Discord.
func TestCrossplayCantStart(t *testing.T) {
	fake := newFakeDiscord(t)
	t.Setenv("FAKE_VALHEIM_PARTY", "missing")
	env := setup(t)
	m := env.m
	m.opt.DiscordURL = fake.URL
	install(t, m)
	turnOnDiscord(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reason there's no join code", 20*time.Second, func() bool { return m.Status().Join.Problem == "crossplay" })
	if st := m.Status(); st.State != StateOnline || st.Join.Code != "" {
		t.Fatalf("state %s, code %q", st.State, st.Join.Code)
	}
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, currentJobID(m))
	if n := fake.count("is online"); n != 0 {
		t.Fatalf("posted online %d times: %q", n, fake.all())
	}
}

// A join code that doesn't come, for no reason the log gives, is late after
// a while.
func TestJoinCodeLate(t *testing.T) {
	old := codeWait
	codeWait = time.Second
	t.Cleanup(func() { codeWait = old })
	t.Setenv("FAKE_VALHEIM_PARTY", "silent")
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the server to be ready", 20*time.Second, func() bool { return m.Status().State == StateOnline })
	if p := m.Status().Join.Problem; p != "" {
		t.Fatalf("problem %q straight away", p)
	}
	waitFor(t, "the join code to be late", 10*time.Second, func() bool { return m.Status().Join.Problem == "late" })
}

func TestDiscordAfterTakeoverAndRest(t *testing.T) {
	fake := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.DiscordURL = fake.URL
	install(t, m)
	turnOnDiscord(t, m)
	code := startOnline(t, m)
	fake.wait(t, "Join code: **"+code+"**", 1)

	// The app closes and opens again: the code isn't posted twice.
	m.Close()
	m2 := newTestManager(t, env.data, env)
	m2.opt.DiscordURL = fake.URL
	var atRest []string
	m2.opt.Hibernate = func() error {
		atRest = fake.all()
		return nil
	}
	m2.opt.Sleep = m2.opt.Hibernate // what a Mac does
	m2.Start()
	env.m = m2
	waitFor(t, "takeover", 10*time.Second, func() bool {
		m2.mu.Lock()
		defer m2.mu.Unlock()
		return m2.run != nil && m2.run.ready && !m2.run.replaying
	})
	time.Sleep(time.Second)
	if n := fake.count("is online"); n != 1 {
		t.Fatalf("posted online %d times: %q", n, fake.all())
	}

	// Resting: the message goes out before the computer rests.
	if err := m2.Hibernate(""); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m2, currentJobID(m2)); job.Error != "" {
		t.Fatalf("hibernate: %s", job.Error)
	}
	if len(atRest) == 0 || !strings.Contains(atRest[len(atRest)-1], "going to rest") {
		t.Fatalf("posts when the computer went to rest: %q", atRest)
	}
}

func TestDiscordMessages(t *testing.T) {
	portals, _ := mods.Find("advancedportals")
	seasons, _ := mods.Find("seasonality")
	o := onlineMessage{Server: "Vikings_of *Odin*", World: "My_World", Code: "123456", Password: "a|b",
		Mods: []ModInfo{{Mod: portals, Version: "1.2.0"}, {Mod: seasons}}}
	want := "🟢 **Vikings\\_of \\*Odin\\*** is online\n" +
		"World: My\\_World\n" +
		"Join code: **123456**\n" +
		"Password: ||a\\|b||\n" +
		"Mods: [Advanced Portals](" + portals.URL + ") 1.2.0, [Seasonality](" + seasons.URL + ")\n" +
		"-# In Valheim, go to Join Game and join by code. Install the mods first with the Gale or r2modman mod manager."
	if got := o.text(); got != want {
		t.Errorf("online message:\n%s\nwant:\n%s", got, want)
	}
	// A mod just for players' own games is offered, not asked for.
	far, _ := mods.Find("farlands")
	o = onlineMessage{Server: "S", World: "W", Code: "123456", Optional: []ModInfo{{Mod: far}}}
	if got := o.text(); !strings.Contains(got, "Optional, just for your own game: [FarLands]("+far.URL+")\n") ||
		strings.Contains(got, "Install") || strings.Contains(got, "Mods:") {
		t.Errorf("message with an optional mod:\n%s", got)
	}
	o = onlineMessage{Server: "S", World: "W", Address: "203.0.113.5:2456"}
	if got := o.text(); !strings.Contains(got, "Address: **203.0.113.5:2456**") || strings.Contains(got, "Password") ||
		!strings.Contains(got, "add the server by its address") {
		t.Errorf("message without crossplay:\n%s", got)
	}
	for why, want := range map[stopReason]string{
		stopStopped:      "has stopped. The new join code",
		stopRestart:      "is restarting",
		stopRest:         "the computer is going to rest",
		stopCrash:        "Someone needs to start it again",
		stopCrashRestart: "is starting again",
	} {
		if got := stoppedText("S", why, true); !strings.Contains(got, want) {
			t.Errorf("stopped (%d): %s", why, got)
		}
	}
}
