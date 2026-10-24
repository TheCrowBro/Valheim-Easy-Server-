// Package records turns who joined, left and died, as the Valheim server's
// log tells it, into records such as who died the most and who lasted the
// longest without dying. That's all the server sees of players: crafting,
// damage and the like stay in each player's own game.
package records

import (
	"math"
	"sort"
	"time"
)

// What happened.
const (
	Join  = "j"
	Leave = "l"
	Death = "d"
)

// Event is one thing a player did, from the server's log.
type Event struct {
	T    int64  `json:"t"` // when, in Unix seconds
	Kind string `json:"k"` // Join, Leave or Death
	Name string `json:"n"` // the character
}

// Player is one player's numbers for a stretch of time.
type Player struct {
	Name           string     `json:"name"`
	Deaths         int        `json:"deaths"`
	PlaySeconds    float64    `json:"playSeconds"`
	Visits         int        `json:"visits"`
	Days           int        `json:"days"`                    // days they played on
	LongestLife    float64    `json:"longestLife"`             // seconds in the game without dying
	QuickestDeath  *float64   `json:"quickestDeath,omitempty"` // seconds after arriving or respawning
	LongestSession float64    `json:"longestSession"`
	FirstIn        int        `json:"firstIn"`            // days they were the first to arrive
	Latest         *time.Time `json:"latest,omitempty"`   // the latest at night they were on
	Earliest       *time.Time `json:"earliest,omitempty"` // the earliest in the morning they arrived

	night, morning int // minutes after noon of Latest, after 4:00 of Earliest
	days           map[string]bool
}

// Record is a title one player holds, or several sharing it.
type Record struct {
	ID    string     `json:"id"`
	Names []string   `json:"names"`
	Value float64    `json:"value"`        // a count, or seconds
	At    *time.Time `json:"at,omitempty"` // when, for the night owl and the early bird
}

// Summary is the records for a stretch of time.
type Summary struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Players []Player  `json:"players"` // most deaths first
	Records []Record  `json:"records"`
}

// Period gives the stretch of time for "today", "week" (today and the six
// days before) or "all", in this computer's time zone.
func Period(name string, now time.Time) (from, to time.Time, ok bool) {
	today := Midnight(now)
	to = today.AddDate(0, 0, 1)
	switch name {
	case "today":
		return today, to, true
	case "week":
		return today.AddDate(0, 0, -6), to, true
	case "all":
		return time.Unix(0, 0).In(now.Location()), to, true
	}
	return time.Time{}, time.Time{}, false
}

// Midnight is the start of t's day.
func Midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// state is where a player is in the events read so far.
type state struct {
	on    bool      // in the game
	start time.Time // when this visit began
	alive time.Time // since when alive in the game: the visit's start or the last death
	sure  bool      // alive is a real arrival or respawn (not a death without a join)
	life  float64   // seconds of this life inside the stretch so far
}

// Compute works out the records for [from, to) from events (in any order).
// Visits still going are counted up to now.
func Compute(events []Event, from, to, now time.Time) Summary {
	evs := append([]Event(nil), events...)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].T < evs[j].T })
	end := to
	if now.Before(end) {
		end = now
	}
	in := func(t time.Time) bool { return !t.Before(from) && t.Before(end) }
	overlap := func(a, b time.Time) float64 { // seconds of [a, b) inside [from, end)
		if a.Before(from) {
			a = from
		}
		if b.After(end) {
			b = end
		}
		if !b.After(a) {
			return 0
		}
		return b.Sub(a).Seconds()
	}

	players := map[string]*Player{}
	get := func(name string) *Player {
		p := players[name]
		if p == nil {
			p = &Player{Name: name, days: map[string]bool{}, morning: math.MaxInt}
			players[name] = p
		}
		return p
	}
	states := map[string]*state{}
	firstIn := map[string]string{} // day -> who arrived first

	// stretch counts the time alive in the game up to t.
	stretch := func(s *state, t time.Time) {
		s.life += overlap(s.alive, t)
		s.alive = t
	}
	endLife := func(name string, s *state) {
		if s.life > 0 {
			if p := get(name); s.life > p.LongestLife {
				p.LongestLife = s.life
			}
		}
		s.life = 0
	}
	endVisit := func(name string, s *state, t time.Time) {
		stretch(s, t)
		s.on = false
		played := overlap(s.start, t)
		if played <= 0 {
			return
		}
		p := get(name)
		p.PlaySeconds += played
		p.LongestSession = math.Max(p.LongestSession, played)
		if s.life > p.LongestLife {
			p.LongestLife = s.life // so far: it goes on next visit
		}
		a, b := maxTime(s.start, from), minTime(t, end)
		for d := Midnight(a); d.Before(b); d = d.AddDate(0, 0, 1) {
			p.days[d.Format("2006-01-02")] = true
		}
		if m, ok := nightMinutes(b); ok && m > p.night {
			p.night, p.Latest = m, &b
		}
	}

	for _, e := range evs {
		t := time.Unix(e.T, 0).In(from.Location())
		s := states[e.Name]
		if s == nil {
			s = &state{}
			states[e.Name] = s
		}
		switch e.Kind {
		case Join:
			if s.on {
				endVisit(e.Name, s, t) // the leave wasn't in the log
			}
			s.on, s.start, s.alive, s.sure = true, t, t, true
			if in(t) {
				p := get(e.Name)
				p.Visits++
				day := t.Format("2006-01-02")
				if firstIn[day] == "" {
					firstIn[day] = e.Name
				}
				if m, ok := morningMinutes(t); ok && m < p.morning {
					p.morning, p.Earliest = m, &t
				}
			}
		case Death:
			if !s.on { // in the game without a join line: from now on, then
				s.on, s.start, s.alive, s.sure = true, t, t, false
			}
			lived := t.Sub(s.alive).Seconds()
			sure := s.sure
			stretch(s, t)
			if in(t) {
				p := get(e.Name)
				p.Deaths++
				if sure && (p.QuickestDeath == nil || lived < *p.QuickestDeath) {
					p.QuickestDeath = &lived
				}
			}
			endLife(e.Name, s)
			s.sure = true // the next life starts with the respawn
		case Leave:
			if s.on {
				endVisit(e.Name, s, t)
			}
		}
	}
	for name, s := range states {
		if s.on {
			endVisit(name, s, end)
		}
		if s.life > 0 {
			endLife(name, s)
		}
	}
	for _, name := range firstIn {
		get(name).FirstIn++
	}

	sum := Summary{From: from, To: to, Players: []Player{}}
	for _, p := range players {
		p.Days = len(p.days)
		if p.PlaySeconds > 0 || p.Deaths > 0 || p.Visits > 0 {
			sum.Players = append(sum.Players, *p)
		}
	}
	sort.Slice(sum.Players, func(i, j int) bool {
		a, b := sum.Players[i], sum.Players[j]
		if a.Deaths != b.Deaths {
			return a.Deaths > b.Deaths
		}
		if a.PlaySeconds != b.PlaySeconds {
			return a.PlaySeconds > b.PlaySeconds
		}
		return a.Name < b.Name
	})
	sum.Records = recordsOf(sum.Players)
	return sum
}

// recordsOf picks the record holders.
func recordsOf(ps []Player) []Record {
	out := []Record{}
	// best adds a record for the highest (or, with low, lowest) value of
	// those players that have one, rounded to whole units.
	best := func(id string, low bool, value func(p Player) (float64, bool)) {
		var r Record
		found := false
		for _, p := range ps {
			v, ok := value(p)
			if !ok {
				continue
			}
			v = math.Round(v)
			switch {
			case !found || (low && v < r.Value) || (!low && v > r.Value):
				r, found = Record{ID: id, Names: []string{p.Name}, Value: v}, true
			case v == r.Value:
				r.Names = append(r.Names, p.Name)
			}
		}
		if found {
			sort.Strings(r.Names)
			out = append(out, r)
		}
	}
	best("deaths", false, func(p Player) (float64, bool) { return float64(p.Deaths), p.Deaths > 0 })
	best("dangerous", true, func(p Player) (float64, bool) { // seconds played per death
		return p.PlaySeconds / float64(p.Deaths), p.Deaths >= 2 && p.PlaySeconds >= 30*60
	})
	best("longestLife", false, func(p Player) (float64, bool) { return p.LongestLife, p.LongestLife >= 60 })
	best("quickestDeath", true, func(p Player) (float64, bool) {
		if p.QuickestDeath == nil {
			return 0, false
		}
		return *p.QuickestDeath, true
	})
	best("playTime", false, func(p Player) (float64, bool) { return p.PlaySeconds, p.PlaySeconds >= 60 })
	best("longestSession", false, func(p Player) (float64, bool) { return p.LongestSession, p.LongestSession >= 60 })
	best("days", false, func(p Player) (float64, bool) { return float64(p.Days), p.Days >= 2 })
	best("firstIn", false, func(p Player) (float64, bool) { return float64(p.FirstIn), p.FirstIn > 0 })
	// Being first on one day each is no record.
	if n := len(out); n > 0 && out[n-1].ID == "firstIn" && out[n-1].Value < 2 && len(out[n-1].Names) > 1 {
		out = out[:n-1]
	}
	// The night owl and the early bird: one each, at the time it happened.
	var owl, bird *Player
	for i := range ps {
		p := &ps[i]
		if p.Latest != nil && (owl == nil || p.night > owl.night) {
			owl = p
		}
		if p.Earliest != nil && (bird == nil || p.morning < bird.morning) {
			bird = p
		}
	}
	if owl != nil {
		out = append(out, Record{ID: "nightOwl", Names: []string{owl.Name}, Value: float64(owl.night), At: owl.Latest})
	}
	if bird != nil {
		out = append(out, Record{ID: "earlyBird", Names: []string{bird.Name}, Value: float64(bird.morning), At: bird.Earliest})
	}
	return out
}

// nightMinutes says how late at night t is, as minutes after noon, for
// times from 22:00 to 5:00.
func nightMinutes(t time.Time) (int, bool) {
	m := (t.Hour()*60 + t.Minute() + 12*60) % (24 * 60)
	return m, m >= 10*60 && m < 17*60
}

// morningMinutes says how early in the morning t is, as minutes after 4:00,
// for times from 4:00 to 10:00.
func morningMinutes(t time.Time) (int, bool) {
	m := t.Hour()*60 + t.Minute() - 4*60
	return m, m >= 0 && m < 6*60
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
