package records

import (
	"reflect"
	"testing"
	"time"
)

var zone = time.FixedZone("CET", 3600)

// at is a time on 20 October 2026 (day 0) or the days after.
func at(day, hour, min int) time.Time { return time.Date(2026, 10, 20+day, hour, min, 0, 0, zone) }

func ev(t time.Time, kind, name string) Event { return Event{T: t.Unix(), Kind: kind, Name: name} }

// Two evenings on the server.
var evening = []Event{
	ev(at(0, 18, 0), Join, "Astrid"),
	ev(at(0, 18, 30), Join, "Bjorn"),
	ev(at(0, 18, 31), Death, "Bjorn"), // a minute after arriving
	ev(at(0, 19, 0), Death, "Bjorn"),
	ev(at(0, 21, 0), Leave, "Astrid"),
	ev(at(0, 23, 30), Leave, "Bjorn"),
	ev(at(1, 6, 30), Join, "Sigrid"),
	ev(at(1, 7, 0), Leave, "Sigrid"),
	ev(at(1, 20, 0), Join, "Astrid"), // still on
}

var now = at(1, 23, 45)

func record(t *testing.T, s Summary, id string) Record {
	t.Helper()
	for _, r := range s.Records {
		if r.ID == id {
			return r
		}
	}
	return Record{}
}

func TestAllTime(t *testing.T) {
	from, to, _ := Period("all", now)
	s := Compute(evening, from, to, now)
	want := map[string]Record{
		"deaths":         {Names: []string{"Bjorn"}, Value: 2},
		"dangerous":      {Names: []string{"Bjorn"}, Value: 9000},   // 5 h, 2 deaths
		"longestLife":    {Names: []string{"Astrid"}, Value: 24300}, // 3 h, then 3 h 45 min on her next visit
		"quickestDeath":  {Names: []string{"Bjorn"}, Value: 60},
		"playTime":       {Names: []string{"Astrid"}, Value: 24300},
		"longestSession": {Names: []string{"Bjorn"}, Value: 18000},
		"days":           {Names: []string{"Astrid"}, Value: 2},
	}
	for id, w := range want {
		if r := record(t, s, id); !reflect.DeepEqual(r.Names, w.Names) || r.Value != w.Value {
			t.Errorf("%s = %v %v, want %v %v", id, r.Names, r.Value, w.Names, w.Value)
		}
	}
	if r := record(t, s, "firstIn"); r.ID != "" {
		t.Errorf("first on one day each is a record: %+v", r)
	}
	if r := record(t, s, "nightOwl"); len(r.Names) != 1 || r.Names[0] != "Astrid" || !r.At.Equal(now) {
		t.Errorf("night owl = %+v", r)
	}
	if r := record(t, s, "earlyBird"); len(r.Names) != 1 || r.Names[0] != "Sigrid" || !r.At.Equal(at(1, 6, 30)) {
		t.Errorf("early bird = %+v", r)
	}
	var names []string
	for _, p := range s.Players {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"Bjorn", "Astrid", "Sigrid"}) {
		t.Errorf("players = %v", names)
	}
}

func TestToday(t *testing.T) {
	from, to, _ := Period("today", now)
	s := Compute(evening, from, to, now)
	if r := record(t, s, "deaths"); r.ID != "" {
		t.Errorf("deaths today: %+v", r)
	}
	// Only today's part of Astrid's life counts.
	if r := record(t, s, "longestLife"); !reflect.DeepEqual(r.Names, []string{"Astrid"}) || r.Value != 13500 {
		t.Errorf("longest life today = %+v", r)
	}
	if r := record(t, s, "firstIn"); !reflect.DeepEqual(r.Names, []string{"Sigrid"}) {
		t.Errorf("first in today = %+v", r)
	}
	if len(s.Players) != 2 {
		t.Errorf("players today = %+v", s.Players)
	}
}

func TestYesterday(t *testing.T) {
	s := Compute(evening, at(0, 0, 0), at(1, 0, 0), now)
	if r := record(t, s, "deaths"); !reflect.DeepEqual(r.Names, []string{"Bjorn"}) || r.Value != 2 {
		t.Errorf("deaths = %+v", r)
	}
	if r := record(t, s, "nightOwl"); !reflect.DeepEqual(r.Names, []string{"Bjorn"}) || !r.At.Equal(at(0, 23, 30)) {
		t.Errorf("night owl = %+v", r)
	}
	if r := record(t, s, "playTime"); !reflect.DeepEqual(r.Names, []string{"Bjorn"}) || r.Value != 18000 {
		t.Errorf("play time = %+v", r)
	}
}

func TestOddLogs(t *testing.T) {
	from, to, _ := Period("all", now)
	// A death with no join line before it (the log started mid-game), and a
	// join with no leave before the next join.
	s := Compute([]Event{
		ev(at(0, 10, 0), Death, "Bjorn"),
		ev(at(0, 10, 0), Join, "Astrid"),
		ev(at(0, 11, 0), Join, "Astrid"),
		ev(at(0, 12, 0), Leave, "Astrid"),
		ev(at(0, 12, 0), Leave, "Bjorn"),
	}, from, to, now)
	if r := record(t, s, "quickestDeath"); r.ID != "" {
		t.Errorf("a death with no known arrival counted as quick: %+v", r)
	}
	for _, p := range s.Players {
		if p.Name == "Astrid" && (p.PlaySeconds != 7200 || p.LongestSession != 3600 || p.Visits != 2) {
			t.Errorf("Astrid = %+v", p)
		}
		if p.Name == "Bjorn" && (p.Deaths != 1 || p.PlaySeconds != 7200) {
			t.Errorf("Bjorn = %+v", p)
		}
	}
}

func TestNobodyPlayed(t *testing.T) {
	from, to, _ := Period("today", now)
	s := Compute(nil, from, to, now)
	if len(s.Players) != 0 || s.Records == nil || len(s.Records) != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestPeriod(t *testing.T) {
	from, to, ok := Period("week", now)
	if !ok || !from.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, zone)) || !to.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, zone)) {
		t.Errorf("week = %v to %v", from, to)
	}
	if _, _, ok := Period("year", now); ok {
		t.Error("an unknown period")
	}
}
