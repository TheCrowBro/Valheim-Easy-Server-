package stats

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestHistoryRingAndSince(t *testing.T) {
	h := NewHistory(3)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.Add(Sample{T: base.Add(time.Duration(i) * time.Second).UnixMilli(), Players: i})
	}
	got := h.Since(time.Time{})
	if len(got) != 3 || got[0].Players != 2 || got[2].Players != 4 {
		t.Fatalf("ring contents = %+v", got)
	}
	if got := h.Since(base.Add(3 * time.Second)); len(got) != 1 || got[0].Players != 4 {
		t.Fatalf("Since = %+v", got)
	}
	if got := h.Last(2); len(got) != 2 || got[0].Players != 3 {
		t.Fatalf("Last = %+v", got)
	}
}

func TestDownsampleKeepsPeakPlayers(t *testing.T) {
	var in []Sample
	for i := 0; i < 100; i++ {
		p := 0
		if i == 42 {
			p = 3
		}
		in = append(in, Sample{T: int64(i), CPU: 10, Players: p, Running: true})
	}
	out := Downsample(in, 10)
	if len(out) != 10 {
		t.Fatalf("len = %d", len(out))
	}
	peak := 0
	for _, s := range out {
		if math.Abs(s.CPU-10) > 1e-9 {
			t.Fatalf("average CPU = %v", s.CPU)
		}
		if s.Players > peak {
			peak = s.Players
		}
	}
	if peak != 3 {
		t.Fatalf("a short visit was lost: peak = %d", peak)
	}
}

func TestByPlayersEstimate(t *testing.T) {
	b := NewByPlayers()
	// 0 players: 2% CPU, 2 GB; each player adds 3% and 200 MB.
	for p := 0; p <= 3; p++ {
		for i := 0; i < 100; i++ {
			b.Add(Sample{Running: true, Players: p, CPU: 2 + 3*float64(p), Mem: uint64(2e9 + 2e8*float64(p))}, 2)
		}
	}
	b.Add(Sample{Running: false, Players: 9, CPU: 99}, 2) // ignored: server not running
	s := b.Summary()
	if len(s.Buckets) != 4 {
		t.Fatalf("buckets = %+v", s.Buckets)
	}
	if s.Estimate == nil {
		t.Fatal("no estimate")
	}
	if math.Abs(s.Estimate.CPUPerPlayer-3) > 1e-6 || math.Abs(s.Estimate.MemPerPlayer-2e8) > 1 {
		t.Fatalf("estimate = %+v", s.Estimate)
	}
	if math.Abs(s.Estimate.CPUAt10-32) > 1e-6 {
		t.Fatalf("CPU at 10 = %v", s.Estimate.CPUAt10)
	}

	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var back ByPlayers
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Buckets) != 4 || back.Buckets[2].Seconds != 200 {
		t.Fatalf("round trip = %+v", back.Buckets)
	}
}

func TestNoEstimateWithOnePlayerCount(t *testing.T) {
	b := NewByPlayers()
	for i := 0; i < 200; i++ {
		b.Add(Sample{Running: true, Players: 1, CPU: 5}, 2)
	}
	b.Add(Sample{Running: true, Players: 2, CPU: 9}, 2) // too short to count
	if s := b.Summary(); s.Estimate != nil {
		t.Fatalf("estimate from too little data: %+v", s.Estimate)
	}
}
