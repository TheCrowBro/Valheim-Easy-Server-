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
