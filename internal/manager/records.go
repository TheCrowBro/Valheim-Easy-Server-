package manager

// Player records: who died the most, who lasted the longest without dying,
// and so on, from who joins, leaves and dies in the server's logs. Each
// server run's events are kept in records.json under its log's name, so
// they outlast the logs (only the newest 30 are kept). Logs from before
// records existed are read once.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/logparse"
	"valheim-easy-server/internal/records"
)

const recordsFile = "records.json"

// recordsData is what records.json holds.
type recordsData struct {
	Runs   map[string][]records.Event `json:"runs"`   // by server log name
	Posted string                     `json:"posted"` // the last day summed up in Discord (2006-01-02)
}

func (m *Manager) loadRecords(inst *instance) {
	inst.recs = recordsData{Runs: map[string][]records.Event{}}
	path := m.instFile(inst, recordsFile)
	b, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(b, &inst.recs) == nil {
			if inst.recs.Runs == nil {
				inst.recs.Runs = map[string][]records.Event{}
			}
			return
		}
		// Damaged: keep it for a look and start again from the logs.
		inst.recs = recordsData{Runs: map[string][]records.Event{}}
		broken := path + ".damaged-" + time.Now().Format("20060102-150405")
		os.Rename(path, broken)
		m.log.Printf("records were damaged; saved as %s", broken)
	}
	// The first time: the daily Discord post starts tonight, not with
	// yesterday's.
	inst.recs.Posted = records.Midnight(time.Now()).AddDate(0, 0, -1).Format("2006-01-02")
}

// saveRecords saves the legacy game's records.
func (m *Manager) saveRecords() { m.saveRecordsFor(m.legacy()) }

func (m *Manager) saveRecordsFor(inst *instance) {
	m.mu.Lock()
	b, err := json.Marshal(inst.recs)
	m.mu.Unlock()
	if err == nil {
		config.WriteFileAtomic(m.instFile(inst, recordsFile), b, 0o644)
	}
}

// recordKind is the records event for a game event, or "".
func recordKind(activity string) string {
	switch activity {
	case "join":
		return records.Join
	case "leave":
		return records.Leave
	case "death":
		return records.Death
	}
	return ""
}

// noteEventsLocked keeps the joins, leaves and deaths of a run, including
// those read again after the app restarted (they're a fresh run's).
func noteEventsLocked(run *serverRun, evs []game.Event) {
	for _, a := range evs {
		if k := recordKind(a.Kind); k != "" && a.Name != "" && !a.Time.IsZero() {
			run.events = append(run.events, records.Event{T: a.Time.Unix(), Kind: k, Name: a.Name})
		}
	}
}

// archiveRunLocked keeps a finished run's events; it reports whether
// there's something to save.
func (m *Manager) archiveRunLocked(run *serverRun) bool {
	name := filepath.Base(run.rec.LogPath)
	if run.rec.LogPath == "" || len(run.events) == 0 {
		return false
	}
	run.inst.recs.Runs[name] = run.events
	return true
}

// backfillRecords reads the logs of runs that aren't in records.json yet:
// those from before records existed, or that ended while the app was closed.
func (m *Manager) backfillRecords() { m.eachInstance(m.backfillRecordsFor) }

func (m *Manager) backfillRecordsFor(inst *instance) {
	m.mu.Lock()
	dir := inst.dirs.Logs
	// The account each name last played from, to tell apart friends who
	// connected at the same time.
	accounts := make(map[string]string, len(inst.known))
	for name, p := range inst.known {
		accounts[name] = p.PlatformID
	}
	m.mu.Unlock()
	newMon := func() game.Monitor {
		return inst.srv.Watch(game.Record{}, func(name string) string { return accounts[name] })
	}
	logs, _ := filepath.Glob(filepath.Join(dir, logPrefix(inst)+"*.log"))
	changed := false
	for _, path := range logs {
		if m.ctx.Err() != nil {
			return
		}
		name := filepath.Base(path)
		m.mu.Lock()
		_, known := inst.recs.Runs[name]
		current := inst.run != nil && filepath.Base(inst.run.rec.LogPath) == name
		m.mu.Unlock()
		if known || current {
			continue
		}
		events, err := readLogEvents(newMon, path)
		if err != nil {
			m.log.Printf("records: reading %s: %v", name, err)
			continue
		}
		m.mu.Lock()
		if inst.run == nil || filepath.Base(inst.run.rec.LogPath) != name { // it may have started meanwhile
			inst.recs.Runs[name], changed = events, true
		}
		m.mu.Unlock()
	}
	if changed {
		m.saveRecordsFor(inst)
	}
}

// readLogEvents reads who joined, left and died from a finished run's
// log, with a fresh Monitor from newMon (which knows the account each name
// last played from). A Monitor that can read a log by its own clock
// (Valheim's) does so: each line counts at the newest time the log gave.
func readLogEvents(newMon func() game.Monitor, path string) ([]records.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	mon := newMon()
	if r, ok := mon.(fromLogReader); ok {
		r.FromLog()
	}
	events := []records.Event{}
	var last time.Time
	add := func(evs []game.Event) {
		for _, a := range evs {
			if k := recordKind(a.Kind); k != "" && a.Name != "" && !a.Time.IsZero() {
				events = append(events, records.Event{T: a.Time.Unix(), Kind: k, Name: a.Name})
			}
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := mon.Line(sc.Text(), time.Now())
		if !l.Time.IsZero() {
			last = l.Time
		}
		add(l.Events)
	}
	// Whoever is still listed left when the log ends.
	add(mon.End(last))
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return events, err
	}
	return events, nil
}

// Records works out the legacy game's records for "today", "week" or
// "all".
func (m *Manager) Records(period string) (records.Summary, error) {
	now := time.Now()
	from, to, ok := records.Period(period, now)
	if !ok {
		return records.Summary{}, fmt.Errorf("unknown period %q", period)
	}
	return m.recordsBetween(m.legacy(), from, to, now), nil
}

func (m *Manager) recordsBetween(inst *instance, from, to, now time.Time) records.Summary {
	m.mu.Lock()
	var events []records.Event
	current := ""
	if inst.run != nil {
		current = filepath.Base(inst.run.rec.LogPath)
		events = append(events, inst.run.events...)
	}
	for name, run := range inst.recs.Runs {
		if name != current { // the running server's own events are the ones above
			events = append(events, run...)
		}
	}
	m.mu.Unlock()
	return records.Compute(events, from, to, now)
}

// dailyLoop posts in Discord who died the most each day.
func (m *Manager) dailyLoop() {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
			m.checkDaily(time.Now())
		}
	}
}

// checkDaily posts who died the most today, at 23:59, if anyone played.
// If the computer was off or asleep then, yesterday's goes out once it's
// back (an older day is left out).
func (m *Manager) checkDaily(now time.Time) {
	day, when := records.Midnight(now).AddDate(0, 0, -1), "yesterday"
	if now.Hour() == 23 && now.Minute() == 59 {
		day, when = records.Midnight(now), "today"
	}
	key := day.Format("2006-01-02")
	inst := m.legacy()
	m.mu.Lock()
	if inst.recs.Posted >= key {
		m.mu.Unlock()
		return
	}
	inst.recs.Posted = key
	s := m.settings
	m.mu.Unlock()
	m.saveRecordsFor(inst)
	if !s.Discord || !s.DiscordDaily {
		return
	}
	sum := m.recordsBetween(inst, day, day.AddDate(0, 0, 1), now)
	if len(sum.Players) == 0 {
		return // nobody played
	}
	text := dailyTextFor(inst.srv.Texts(), sum, when)
	m.queueDiscord(discordPost{
		link: s.DiscordWebhook,
		text: func() string { return text },
		done: func(err error) {
			if err != nil {
				m.addActivity("warning", "", "Couldn't post in Discord who died the most "+when+". "+err.Error())
				return
			}
			m.addActivity("discord", "", "Posted in Discord who died the most "+when+".")
		},
	})
}

// dailyTextFor says who died the most on a day ("today" or "yesterday"),
// in the game's words for its players ("vikings").
func dailyTextFor(tx game.Texts, sum records.Summary, when string) string {
	var top records.Record
	var longest records.Record
	for _, r := range sum.Records {
		switch r.ID {
		case "deaths":
			top = r
		case "longestLife":
			longest = r
		}
	}
	bold := func(names []string) string {
		var out []string
		for _, n := range names {
			out = append(out, "**"+discord.Escape(n)+"**")
		}
		if len(out) == 1 {
			return out[0]
		}
		return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
	}
	var b strings.Builder
	if top.ID == "" {
		players := tx.Players
		if len(sum.Players) == 1 {
			players = tx.Player
		}
		fmt.Fprintf(&b, "🛡️ **Nobody died %s!** %d %s played.", when, len(sum.Players), players)
	} else {
		times := map[int]string{1: "once", 2: "twice"}[int(top.Value)]
		if times == "" {
			times = fmt.Sprintf("%d times", int(top.Value))
		}
		if len(top.Names) > 1 {
			times += " each"
		}
		fmt.Fprintf(&b, "💀 %s died the most %s: %s", bold(top.Names), when, times)
	}
	var notes []string
	var others []string
	for _, p := range sum.Players {
		if p.Deaths > 0 && !slices.Contains(top.Names, p.Name) {
			others = append(others, fmt.Sprintf("%s %d", discord.Escape(p.Name), p.Deaths))
		}
	}
	if len(others) > 0 {
		notes = append(notes, "Also died: "+strings.Join(others, ", "))
	}
	if longest.ID != "" {
		names := make([]string, len(longest.Names))
		for i, n := range longest.Names {
			names[i] = discord.Escape(n)
		}
		notes = append(notes, fmt.Sprintf("Longest life: %s, %s", strings.Join(names, " and "),
			logparse.HumanDuration(time.Duration(longest.Value)*time.Second)))
	}
	if len(notes) > 0 {
		b.WriteString("\n-# " + strings.Join(notes, " · "))
	}
	return b.String()
}
