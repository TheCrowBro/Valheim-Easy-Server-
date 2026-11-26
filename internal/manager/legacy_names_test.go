package manager

// The manager's tests were written while Valheim's code was still part of
// the manager, and call it by the names it had here. It now lives in
// Valheim's game adapter (internal/games/valheim), which the manager only
// reaches through the game contract. These keep the old names for the
// tests, each one now the adapter's code (or the manager's generic code
// with Valheim's words), so the existing tests and the R1 goldens check the
// moved code without being changed (spec 4.1 and 7.2).

import (
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/games/valheim" // also registers Valheim, which New needs
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/worlds"
)

// argsKey is runtime.json's fingerprint of a start with s.
func argsKey(s config.Settings, saveDir string) string { return valheim.ArgsKey(s, saveDir) }

// onlineMessage is Valheim's online post.
type onlineMessage valheim.OnlineMessage

func (o onlineMessage) text() string { return valheim.OnlineMessage(o).Text() }

// exitHint is Valheim's hint after its server stopped, from the legacy
// log ring's newest 80 lines (the manager hands the adapter the same).
func (m *Manager) exitHint() string {
	lines, _ := m.logs.after(0, 80)
	tail := make([]string, len(lines))
	for i, l := range lines {
		tail[i] = l.Text
	}
	return valheim.ExitHint(tail)
}

// eventsFromLog reads the records from a finished Valheim log, as the
// records' backfill does.
func eventsFromLog(path string, accounts map[string]string) ([]records.Event, error) {
	return readLogEvents(func() game.Monitor {
		return valheim.NewMonitor(game.Record{}, func(name string) string { return accounts[name] })
	}, path)
}

// remind is the reminders for Valheim's players.
func remind(last *played, now played) []string {
	return remindFor(valheim.Game{}.Info().Short, last, now)
}

// dailyText is the daily post with Valheim's words ("vikings").
func dailyText(sum records.Summary, when string) string {
	return dailyTextFor(valheim.Texts(), sum, when)
}

// library is the legacy game's world library.
func (m *Manager) library() *worlds.Library {
	return m.legacy().srv.Worlds().(valheim.Worlds).Library()
}

// savedCode is a mods code in modscodes.json, as Valheim's adapter keeps it.
type savedCode struct {
	Code string    `json:"code"`
	Made time.Time `json:"made"`
}
