package manager

import (
	"os"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/games/valheim"
)

// TestOverdueAfterTakeover: a server taken over while it is still loading,
// which never says it opened, counts as online once ReadyWait has passed
// since it logged in to Steam, straight after the app catches up with its
// log, as build-33 did. (It used to wait for the server's next log line,
// which can be minutes away.)
func TestOverdueAfterTakeover(t *testing.T) {
	old := valheim.ReadyWait
	valheim.ReadyWait = time.Hour
	t.Cleanup(func() { valheim.ReadyWait = old })
	t.Setenv("FAKE_VALHEIM_OPEN", "quiet")
	env := setup(t)
	m := env.m
	s := m.Settings()
	s.Crossplay = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the world to load", 15*time.Second, func() bool {
		b, _ := os.ReadFile(m.CurrentLogPath())
		return strings.Contains(string(b), "DungeonDB Start")
	})
	m.Close()
	valheim.ReadyWait = 3 * time.Second
	m2 := newTestManager(t, env.data, env)
	m2.Start()
	env.m = m2
	waitFor(t, "the takeover", 10*time.Second, func() bool {
		m2.mu.Lock()
		defer m2.mu.Unlock()
		r := m2.legacy().run
		return r != nil && !r.replaying
	})
	start := time.Now()
	// ReadyWait has passed by now; the fallback is checked every 400 ms.
	waitFor(t, "online after the takeover", 10*time.Second, func() bool { return m2.Status().State == StateOnline })
	t.Logf("online %s after catching up", time.Since(start))
}
