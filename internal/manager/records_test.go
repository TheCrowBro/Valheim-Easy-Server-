package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/records"
)

func findRecord(s records.Summary, id string) records.Record {
	for _, r := range s.Records {
		if r.ID == id {
			return r
		}
	}
	return records.Record{}
}

// deaths is how many times each player died in a period's records.
func deaths(t *testing.T, m *Manager, period string) map[string]int {
	t.Helper()
	sum, err := m.Records(period)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, p := range sum.Players {
		out[p.Name] = p.Deaths
	}
	return out
}

func readRecords(t *testing.T, data string) recordsData {
	t.Helper()
	var r recordsData
	b, err := os.ReadFile(filepath.Join(data, recordsFile))
	if err != nil || json.Unmarshal(b, &r) != nil {
		t.Fatalf("records.json: %v %s", err, b)
	}
	return r
}

func TestRecordsFromServerRuns(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Astrid@0.3,join:Bjorn@0.6,die:Bjorn@0.9,die:Bjorn@1.6,leave:Astrid@2.2")
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	// Counted while the server runs.
	waitFor(t, "Bjorn's deaths", 20*time.Second, func() bool { return deaths(t, m, "today")["Bjorn"] == 2 })
	waitFor(t, "Astrid to leave", 10*time.Second, func() bool { return hasActivity(m, "leave", "Astrid") })
	sum, _ := m.Records("today")
	if r := findRecord(sum, "deaths"); len(r.Names) != 1 || r.Names[0] != "Bjorn" || r.Value != 2 {
		t.Fatalf("most deaths = %+v", r)
	}
	if _, err := m.Records("year"); err == nil {
		t.Fatal("an unknown period")
	}
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m, currentJobID(m)); job.Error != "" {
		t.Fatal(job.Error)
	}

	// The run is kept, with Bjorn leaving when the server stopped.
	saved := readRecords(t, env.data)
	if len(saved.Runs) != 1 {
		t.Fatalf("runs = %+v", saved.Runs)
	}
	for name, evs := range saved.Runs {
		last := evs[len(evs)-1]
		if !strings.HasPrefix(name, "server-") || last.Kind != records.Leave || last.Name != "Bjorn" {
			t.Fatalf("run %s = %+v", name, evs)
		}
	}
	if d := deaths(t, m, "all"); d["Bjorn"] != 2 || d["Astrid"] != 0 || len(d) != 2 {
		t.Fatalf("deaths after the stop = %v", d)
	}

	// And after the app restarts.
	m.Close()
	m2 := newTestManager(t, env.data, env)
	m2.Start()
	env.m = m2
	if d := deaths(t, m2, "week"); d["Bjorn"] != 2 {
		t.Fatalf("deaths after a restart = %v", d)
	}
}

// A server that kept running while the app was closed: its log is read
// again from the start, and nothing counts twice.
func TestRecordsAfterTakeover(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SCRIPT", "join:Gunnar@0.3,die:Gunnar@0.6,die:Gunnar@5")
	env := setup(t)
	m := env.m
	install(t, m)
	if err := m.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Gunnar's first death", 20*time.Second, func() bool { return deaths(t, m, "today")["Gunnar"] >= 1 })
	m.Close()                   // the app closes; the server keeps running
	time.Sleep(5 * time.Second) // Gunnar dies again meanwhile

	m2 := newTestManager(t, env.data, env)
	m2.Start()
	env.m = m2
	waitFor(t, "both deaths", 20*time.Second, func() bool { return deaths(t, m2, "today")["Gunnar"] == 2 })
	if err := m2.StopServer(); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t, m2, currentJobID(m2)); job.Error != "" {
		t.Fatal(job.Error)
	}
	if d := deaths(t, m2, "today"); d["Gunnar"] != 2 {
		t.Fatalf("deaths after the stop = %v", d)
	}
	if runs := readRecords(t, env.data).Runs; len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
}

// Logs from before records were kept are read once.
func TestRecordsFromOldLogs(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	day := time.Now().AddDate(0, 0, -2)
	at := func(h, min int) string {
		return time.Date(day.Year(), day.Month(), day.Day(), h, min, 0, 0, time.Local).Format("01/02/2006 15:04:05") + ": "
	}
	log := strings.Join([]string{
		at(20, 0) + "Got connection SteamID 76561198000012345",
		at(20, 0) + "Got handshake from client 76561198000012345",
		at(20, 0) + "Server: New peer connected,sending global keys",
		at(20, 0) + "Got character ZDOID from Sigrid : -123:1",
		at(20, 30) + "Got character ZDOID from Sigrid : 0:0",
		at(20, 30) + "Got character ZDOID from Sigrid : -456:9",
		at(21, 0) + "Got character ZDOID from Sigrid : 0:0",
		at(21, 0) + "Got character ZDOID from Sigrid : -789:9",
		at(21, 15) + "World saved ( 10.1ms )",
	}, "\n") + "\n"
	logs := m.Paths().Logs
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "server-" + day.Format("2006-01-02") + "_20-00-00.log"
	os.WriteFile(filepath.Join(logs, name), []byte(log), 0o644)
	m.Start()
	waitFor(t, "the old log to be read", 10*time.Second, func() bool { return deaths(t, m, "all")["Sigrid"] == 2 })
	sum, _ := m.Records("week")
	if p := sum.Players[0]; p.PlaySeconds != 75*60 || p.LongestLife != 30*60 || p.Visits != 1 {
		t.Fatalf("Sigrid = %+v", p)
	}
	waitFor(t, "records.json", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(env.data, recordsFile))
		return err == nil
	})
	if runs := readRecords(t, env.data).Runs; len(runs[name]) != 4 {
		t.Fatalf("runs = %+v", runs)
	}
	m.Close()

	// Not read again: the records stay as they were even once the log is gone.
	os.Remove(filepath.Join(logs, name))
	m = newTestManager(t, env.data, env)
	m.Start()
	t.Cleanup(m.Close)
	if d := deaths(t, m, "all"); d["Sigrid"] != 2 {
		t.Fatalf("deaths = %v", d)
	}
}

func TestDailyDiscordPost(t *testing.T) {
	fake := newFakeDiscord(t)
	env := setup(t)
	m := env.m
	m.opt.DiscordURL = fake.URL
	turnOnDiscord(t, m)
	s := m.Settings()
	s.DiscordDaily = true
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}

	// Days far ahead, so the app's own check on the real clock stays out of it.
	day := func(d, h, min int) time.Time { return time.Date(2099, 3, 10+d, h, min, 0, 0, time.Local) }
	ev := func(t time.Time, kind, name string) records.Event {
		return records.Event{T: t.Unix(), Kind: kind, Name: name}
	}
	m.mu.Lock()
	m.recs.Runs["server-2099-03-10_17-55-00.log"] = []records.Event{
		ev(day(0, 18, 0), records.Join, "Astrid"),
		ev(day(0, 18, 0), records.Join, "Bjorn"),
		ev(day(0, 18, 5), records.Death, "Bjorn"),
		ev(day(0, 19, 0), records.Death, "Astrid"),
		ev(day(0, 20, 0), records.Death, "Bjorn"),
		ev(day(0, 22, 0), records.Leave, "Astrid"),
		ev(day(0, 22, 0), records.Leave, "Bjorn"),
		// The next day, nobody died.
		ev(day(1, 12, 0), records.Join, "Sigrid"),
		ev(day(1, 13, 0), records.Leave, "Sigrid"),
		// And the day after that, nobody played. Then someone did, but the
		// daily post was off.
		ev(day(3, 12, 0), records.Join, "Sigrid"),
		ev(day(3, 12, 1), records.Death, "Sigrid"),
	}
	m.recs.Posted = day(-1, 0, 0).Format("2006-01-02")
	m.mu.Unlock()
	posted := func() []string {
		t.Helper()
		waitFor(t, "Discord posts to go out", 20*time.Second, func() bool { return m.discordPending.Load() == 0 })
		var out []string
		for _, p := range fake.all() {
			if strings.Contains(p, "died") {
				out = append(out, p)
			}
		}
		return out
	}

	// The last message of the day.
	m.checkDaily(day(0, 23, 59))
	m.checkDaily(day(0, 23, 59).Add(30 * time.Second)) // the next check that minute
	m.checkDaily(day(1, 9, 0))                         // and the next morning
	want := []string{"💀 **Bjorn** died the most today: twice\n-# Also died: Astrid 1 · Longest life: Astrid, 3 h"}
	if got := posted(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("posts = %q", got)
	}
	if !hasText(m, "discord", "Posted in Discord who died the most today.") {
		t.Fatal("no activity about the post")
	}

	// The computer was off at the end of the next day.
	m.checkDaily(day(2, 9, 0))
	want = append(want, "🛡️ **Nobody died yesterday!** 1 viking played.\n-# Longest life: Sigrid, 1 h")
	if got := posted(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("posts = %q", got)
	}

	// Nobody played, then the post was switched off.
	m.checkDaily(day(2, 23, 59))
	s.DiscordDaily = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	m.checkDaily(day(3, 23, 59))
	if got := posted(); len(got) != len(want) {
		t.Fatalf("posts = %q", got)
	}
	if p := readRecords(t, env.data).Posted; p != "2099-03-13" {
		t.Fatalf("posted = %s", p)
	}
}

func TestDailyText(t *testing.T) {
	for _, c := range []struct {
		sum  records.Summary
		when string
		want string
	}{
		{records.Summary{
			Players: []records.Player{{Name: "Bjorn", Deaths: 3}, {Name: "_Astrid_", Deaths: 3}, {Name: "Ulf", Deaths: 3}, {Name: "Sigrid", Deaths: 1}, {Name: "Gunnar"}},
			Records: []records.Record{{ID: "deaths", Names: []string{"Bjorn", "Ulf", "_Astrid_"}, Value: 3}},
		}, "today", "💀 **Bjorn**, **Ulf** and **\\_Astrid\\_** died the most today: 3 times each\n-# Also died: Sigrid 1"},
		{records.Summary{
			Players: []records.Player{{Name: "Bjorn", Deaths: 1}, {Name: "Astrid", Deaths: 1}},
			Records: []records.Record{{ID: "deaths", Names: []string{"Astrid", "Bjorn"}, Value: 1},
				{ID: "longestLife", Names: []string{"Astrid", "Bjorn"}, Value: 125 * 60}},
		}, "yesterday", "💀 **Astrid** and **Bjorn** died the most yesterday: once each\n-# Longest life: Astrid and Bjorn, 2 h 5 min"},
		{records.Summary{
			Players: []records.Player{{Name: "Bjorn"}, {Name: "Astrid"}},
		}, "today", "🛡️ **Nobody died today!** 2 vikings played."},
	} {
		if got := dailyText(c.sum, c.when); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestDamagedRecords(t *testing.T) {
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, recordsFile), []byte(`{"runs": {"server-1.log": [{"t": 1`), 0o644)
	env := &testEnv{data: data}
	m := newTestManager(t, data, env)
	if len(m.recs.Runs) != 0 || m.recs.Posted == "" {
		t.Fatalf("records = %+v", m.recs)
	}
	if kept, _ := filepath.Glob(filepath.Join(data, recordsFile+".damaged-*")); len(kept) != 1 {
		t.Fatalf("the damaged file wasn't kept: %v", kept)
	}
}
