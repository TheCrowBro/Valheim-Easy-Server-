package manager

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/mods/modstest"
)

func TestRemind(t *testing.T) {
	last := &played{Game: "1.0.20", Mods: map[string]string{"advancedportals": "1.2.0", "riverheim": "1.2.1"}, Password: hashPassword("fjord-1234")}
	now := played{Game: "1.0.21", Mods: map[string]string{"advancedportals": "1.2.1", "seasonality": "3.8.5"}, Password: hashPassword("rune-5678")}
	want := "update Valheim to 1.0.21, turn off Riverheim, update Advanced Portals to 1.2.1, install Seasonality, use the new password"
	if got := strings.Join(remind(last, now), ", "); got != want {
		t.Errorf("remind:\n got %s\nwant %s", got, want)
	}
	if got := remind(nil, now); got != nil {
		t.Errorf("before anyone played: %q", got)
	}
	if got := remind(&now, now); got != nil {
		t.Errorf("nothing changed: %q", got)
	}
	unknown := now
	unknown.Game = ""
	if got := remind(&now, unknown); got != nil {
		t.Errorf("an unknown version isn't a change: %q", got)
	}
}

func TestRemindersAfterUpdates(t *testing.T) {
	fake := modstest.New()
	store := httptest.NewServer(fake)
	defer store.Close()
	chat := newFakeDiscord(t)
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.2")
	env := setup(t)
	m := env.m
	m.opt.ModsURL, m.opt.DiscordURL = store.URL, chat.URL
	install(t, m)
	turnOnDiscord(t, m)
	world := m.Settings().World
	if err := m.SetWorldMods(world, []string{"advancedportals"}); err != nil {
		t.Fatal(err)
	}

	// The first time, there's nothing to remind anyone of.
	code := startOnline(t, m)
	chat.wait(t, "Join code: **"+code+"**", 1)
	waitFor(t, "a player to join", 10*time.Second, func() bool { return hasActivity(m, "join", "Astrid") })
	waitFor(t, "what they played with to be noted", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(env.data, "lastplayed.json"))
		return err == nil
	})
	if chat.count("Before you join") != 0 || len(m.Status().Reminder) != 0 {
		t.Fatalf("a reminder with nothing changed: %q %q", chat.all(), m.Status().Reminder)
	}

	// Then Valheim and a mod are updated, the world gets another mod and
	// the server a new password.
	os.Unsetenv("FAKE_VALHEIM_SCRIPT")
	t.Setenv("FAKE_VALHEIM_VERSION", "1.0.21")
	fake.SetVersion(modstest.Portal, "1.2.1")
	s := m.Settings()
	s.UpdateOnStart, s.Password = true, "rune-5678"
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := m.SetWorldMods(world, []string{"advancedportals", "seasonality"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatalf("restart: %s", job.Error)
	}
	want := "update Valheim to 1.0.21, update Advanced Portals to 1.2.1, install Seasonality, use the new password"
	chat.wait(t, "📢 **Before you join:** "+want+".", 1)
	waitFor(t, "the reminder on the Home page", 10*time.Second, func() bool {
		return strings.Join(m.Status().Reminder, ", ") == want
	})
}
