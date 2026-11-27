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

// loadRecords reads the instance's records file at once. New does this for
// the legacy game only, exactly as it always has; every other game's file
// is read the first time it's needed (ensureRecords), because New must not
// read other games' files (invariant I5).
func (m *Manager) loadRecords(inst *instance) {
	inst.recs, _ = m.readRecords(inst)
	m.markRecordsRead(inst)
}

// markRecordsRead notes that the instance's records came from its file,
// so they may be saved over it. m.mu must be held, or the app not started.
func (m *Manager) markRecordsRead(inst *instance) {
	if m.disc.recsRead == nil {
		m.disc.recsRead = map[*instance]bool{}
	}
	m.disc.recsRead[inst] = true
}

// readRecords reads the instance's records file. ok is false when the file
// is there but couldn't be read (another program has it open, say): then
// nothing may be saved over it yet.
func (m *Manager) readRecords(inst *instance) (d recordsData, ok bool) {
	d = recordsData{Runs: map[string][]records.Event{}}
	path := m.instFile(inst, recordsFile)
	b, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(b, &d) == nil {
			if d.Runs == nil {
				d.Runs = map[string][]records.Event{}
			}
			return d, true
		}
		// Damaged: keep it for a look and start again from the logs.
		d = recordsData{Runs: map[string][]records.Event{}}
		broken := path + ".damaged-" + time.Now().Format("20060102-150405")
		os.Rename(path, broken)
		m.log.Printf("records were damaged; saved as %s", broken)
	}
	ok = err == nil || errors.Is(err, os.ErrNotExist)
	// The first time: the daily Discord post starts tonight, not with
	// yesterday's.
	d.Posted = records.Midnight(time.Now()).AddDate(0, 0, -1).Format("2006-01-02")
	return d, ok
}

// recordsReadLocked says whether the instance's records came from its file
// (always so for the legacy game, which New reads).
func (m *Manager) recordsReadLocked(inst *instance) bool {
	return inst.legacy || m.disc.recsRead[inst]
}

// ensureRecords reads a game's records file the first time its records are
// needed: the first daily check, the first look at its records, the first
// save. It reports whether they have been read, so they may be saved.
//
// Whatever was kept before the file was read (a run that ended first) is
// newer than the file, so it's kept with what the file has, and of the two
// days posted the later one counts: the file is added to, never replaced.
// It runs without m.mu.
func (m *Manager) ensureRecords(inst *instance) bool {
	m.mu.Lock()
	done := m.recordsReadLocked(inst)
	m.mu.Unlock()
	if done {
		return true
	}
	m.disc.recsLoad.Lock()
	defer m.disc.recsLoad.Unlock()
	m.mu.Lock()
	done = m.recordsReadLocked(inst) // read by another caller meanwhile
	m.mu.Unlock()
	if done {
		return true
	}
	d, ok := m.readRecords(inst)
	if !ok {
		return false // tried again next time
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, evs := range inst.recs.Runs {
		d.Runs[name] = evs
	}
	if inst.recs.Posted > d.Posted {
		d.Posted = inst.recs.Posted
	}
	inst.recs = d
	m.markRecordsRead(inst)
	return true
}

// saveRecords saves the legacy game's records.
func (m *Manager) saveRecords() { m.saveRecordsFor(m.legacy()) }

// saveRecordsFor saves the instance's records, once its file has been read:
// saving records that never came from the file would wipe the runs and the
// day posted that it holds.
func (m *Manager) saveRecordsFor(inst *instance) {
	if !m.ensureRecords(inst) {
		m.log.Printf("records: not saving %s's yet: its records file couldn't be read", inst.id)
		return
	}
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
	// A game whose records file hasn't been read yet keeps the run here
	// meanwhile: saveRecordsFor reads the file first and keeps both.
	if run.inst.recs.Runs == nil {
		run.inst.recs.Runs = map[string][]records.Event{}
	}
	run.inst.recs.Runs[name] = run.events
	return true
}

// backfillRecords reads the logs of runs that aren't in records.json yet:
// those from before records existed, or that ended while the app was closed.
// It runs in the background once the app has started, so it is also where
// each game's own records and what its players last played with are first
// read (New reads only the legacy game's files, invariant I5).
func (m *Manager) backfillRecords() { m.eachInstance(m.backfillRecordsFor) }

func (m *Manager) backfillRecordsFor(inst *instance) {
	if inst.srv == nil {
		return // a game this version doesn't run: its records are left as they are
	}
	m.ensurePlayed(inst)
	if !m.ensureRecords(inst) {
		return // its file can't be read now: the next start of the app tries again
	}
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

// RecordsGame works out a game's records for "today", "week" or "all"
// (GET /api/games/{id}/records), from that game's own records (records.json
// for the legacy game, games/<id>/records.json for another).
func (m *Manager) RecordsGame(id, period string) (records.Summary, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return records.Summary{}, err
	}
	now := time.Now()
	from, to, ok := records.Period(period, now)
	if !ok {
		return records.Summary{}, fmt.Errorf("unknown period %q", period)
	}
	m.ensureRecords(inst) // if its file can't be read now, what's known so far
	return m.recordsBetween(inst, from, to, now), nil
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

// dailyLoop posts each game's day in Discord: who died the most, or for a
// game without deaths, who played.
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

// checkDaily posts each game's day at 23:59, if anyone played, in that
// game's channel. If the computer was off or asleep then, yesterday's goes
// out once it's back (an older day is left out). Each game keeps the last
// day it posted in its own records file, so one game's post never stands
// for another's.
func (m *Manager) checkDaily(now time.Time) {
	day, when := records.Midnight(now).AddDate(0, 0, -1), "yesterday"
	if now.Hour() == 23 && now.Minute() == 59 {
		day, when = records.Midnight(now), "today"
	}
	m.eachInstance(func(inst *instance) { m.checkDailyFor(inst, day, when, now) })
}

func (m *Manager) checkDailyFor(inst *instance, day time.Time, when string, now time.Time) {
	if inst.srv == nil {
		return // a game this version doesn't run
	}
	if !m.ensureRecords(inst) {
		return // which day it last posted is in its file: the next check tries again
	}
	key := day.Format("2006-01-02")
	m.mu.Lock()
	if inst.recs.Posted >= key {
		m.mu.Unlock()
		return
	}
	inst.recs.Posted = key
	r := m.discordRulesLocked(inst)
	m.mu.Unlock()
	m.saveRecordsFor(inst)
	if !r.posts() || !r.daily {
		return
	}
	sum := m.recordsBetween(inst, day, day.AddDate(0, 0, 1), now)
	if len(sum.Players) == 0 {
		return // nobody played
	}
	// The legacy game's post and its activity entries are exactly as they
	// always were.
	text := dailyTextFor(inst.srv.Texts(), sum, when)
	what := "who died the most " + when
	if !inst.legacy {
		what = "who died the most in " + inst.short() + " " + when
		if !inst.info.Deaths {
			text = playedTextFor(inst.short(), inst.srv.Texts(), sum, when)
			what = "who played " + inst.short() + " " + when
		}
	}
	text = r.words(inst, text)
	m.queueDiscord(discordPost{
		link: r.link,
		text: func() string { return text },
		done: func(err error) {
			if err != nil {
				m.discordNote(inst, "warning", "Couldn't post in Discord "+what+". "+err.Error())
				return
			}
			m.discordNote(inst, "discord", "Posted in Discord "+what+".")
		},
	})
}

// playedTextFor is the daily post of a game without deaths (spec 6.3): how
// many played, and who played the longest in all ("Most time"), then, as
// the note, the longest single session when that tells something more.
// "🎮 **3 players** played Terraria today. Most time: **Alex** (2 h 10 min)"
func playedTextFor(short string, tx game.Texts, sum records.Summary, when string) string {
	n := len(sum.Players)
	noun := tx.Players
	if n == 1 {
		noun = tx.Player
	}
	if noun == "" {
		noun = "players"
		if n == 1 {
			noun = "player"
		}
	}
	var most, longest records.Record
	for _, r := range sum.Records {
		switch r.ID {
		case "playTime":
			most = r
		case "longestSession":
			longest = r
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎮 **%d %s** played %s %s.", n, noun, discord.Escape(short), when)
	if most.ID != "" {
		fmt.Fprintf(&b, " Most time: %s (%s)", boldNames(most.Names), humanSeconds(most.Value))
	}
	// One session each, or the same one: the note would say it again.
	if longest.ID != "" && !(most.ID != "" && slices.Equal(longest.Names, most.Names) && longest.Value == most.Value) {
		fmt.Fprintf(&b, "\n-# Longest session: %s, %s", plainNames(longest.Names), humanSeconds(longest.Value))
	}
	return b.String()
}

// boldNames lists names in bold: "**A**", "**A** and **B**", "**A**, **B**
// and **C**".
func boldNames(names []string) string {
	var out []string
	for _, n := range names {
		out = append(out, "**"+discord.Escape(n)+"**")
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// plainNames lists names as the daily post's note does: "A and B".
func plainNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = discord.Escape(n)
	}
	return strings.Join(out, " and ")
}

// humanSeconds says a number of seconds as people read it: "2 h 10 min".
func humanSeconds(s float64) string {
	return logparse.HumanDuration(time.Duration(s) * time.Second)
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
