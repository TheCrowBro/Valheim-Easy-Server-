// Package stats keeps a history of CPU, GPU, memory and network usage and
// summarises it by how many players were online at the time.
package stats

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// Sample is one reading, taken every few seconds.
type Sample struct {
	T       int64   `json:"t"`       // unix milliseconds
	CPU     float64 `json:"cpu"`     // Valheim server, % of the whole computer
	CPUSys  float64 `json:"cpuSys"`  // whole computer %
	Mem     uint64  `json:"mem"`     // Valheim server bytes
	MemSys  uint64  `json:"memSys"`  // whole computer bytes in use
	GPU     float64 `json:"gpu"`     // busiest graphics card %
	NetUp   float64 `json:"netUp"`   // bytes/s
	NetDown float64 `json:"netDown"` // bytes/s
	Players int     `json:"players"`
	Running bool    `json:"running"`
}

// History is a fixed-size ring of samples.
type History struct {
	mu   sync.Mutex
	buf  []Sample
	next int
	full bool
}

// NewHistory keeps up to capacity samples.
func NewHistory(capacity int) *History {
	return &History{buf: make([]Sample, capacity)}
}

// Add appends a sample, dropping the oldest when full.
func (h *History) Add(s Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf[h.next] = s
	h.next = (h.next + 1) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
}

// Restore adds saved samples (oldest first), e.g. after the app restarts.
func (h *History) Restore(samples []Sample) {
	for _, s := range samples {
		h.Add(s)
	}
}

// Last returns up to n most recent samples, oldest first.
func (h *History) Last(n int) []Sample {
	all := h.Since(time.Time{})
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Since returns the samples taken after t, oldest first.
func (h *History) Since(t time.Time) []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := t.UnixMilli()
	var out []Sample
	add := func(part []Sample) {
		for _, s := range part {
			if s.T > cutoff {
				out = append(out, s)
			}
		}
	}
	if h.full {
		add(h.buf[h.next:])
	}
	add(h.buf[:h.next])
	return out
}

// Downsample averages samples into at most max points so long ranges stay
// light to send and draw. Player counts keep the bucket's maximum so short
// visits still show up.
func Downsample(in []Sample, max int) []Sample {
	if max <= 0 || len(in) <= max {
		return in
	}
	per := int(math.Ceil(float64(len(in)) / float64(max)))
	out := make([]Sample, 0, max)
	for i := 0; i < len(in); i += per {
		end := i + per
		if end > len(in) {
			end = len(in)
		}
		part := in[i:end]
		var a Sample
		n := float64(len(part))
		var mem, memSys float64
		for _, s := range part {
			a.CPU += s.CPU / n
			a.CPUSys += s.CPUSys / n
			mem += float64(s.Mem) / n
			memSys += float64(s.MemSys) / n
			a.GPU += s.GPU / n
			a.NetUp += s.NetUp / n
			a.NetDown += s.NetDown / n
			if s.Players > a.Players {
				a.Players = s.Players
			}
			a.Running = a.Running || s.Running
		}
		a.Mem, a.MemSys = uint64(mem), uint64(memSys)
		a.T = part[len(part)-1].T
		out = append(out, a)
	}
	return out
}

// Bucket is the usage seen while a given number of players were online.
type Bucket struct {
	Players int     `json:"players"`
	Seconds float64 `json:"seconds"` // how long this was observed
	CPU     float64 `json:"cpu"`     // average server CPU %
	CPUMax  float64 `json:"cpuMax"`
	CPUSys  float64 `json:"cpuSys"` // average whole-computer CPU %
	Mem     float64 `json:"mem"`    // average server memory, bytes
	MemMax  float64 `json:"memMax"`
	GPU     float64 `json:"gpu"`
	Net     float64 `json:"net"` // average up+down bytes/s
}

// ByPlayers accumulates usage per player count while the server runs.
type ByPlayers struct {
	mu      sync.Mutex
	Since   time.Time       `json:"since"`
	Buckets map[int]*Bucket `json:"buckets"`
}

// NewByPlayers returns an empty summary.
func NewByPlayers() *ByPlayers {
	return &ByPlayers{Since: time.Now(), Buckets: map[int]*Bucket{}}
}

// Add records a sample that lasted dt seconds.
func (b *ByPlayers) Add(s Sample, dt float64) {
	if !s.Running || dt <= 0 || dt > 60 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	k := b.Buckets[s.Players]
	if k == nil {
		k = &Bucket{Players: s.Players}
		b.Buckets[s.Players] = k
	}
	// Running averages weighted by time.
	total := k.Seconds + dt
	w := dt / total
	k.CPU += (s.CPU - k.CPU) * w
	k.CPUSys += (s.CPUSys - k.CPUSys) * w
	k.Mem += (float64(s.Mem) - k.Mem) * w
	k.GPU += (s.GPU - k.GPU) * w
	k.Net += (s.NetUp + s.NetDown - k.Net) * w
	k.CPUMax = math.Max(k.CPUMax, s.CPU)
	k.MemMax = math.Max(k.MemMax, float64(s.Mem))
	k.Seconds = total
}

// Summary is what the stats page shows.
type Summary struct {
	Since    time.Time `json:"since"`
	Buckets  []Bucket  `json:"buckets"`
	Estimate *Estimate `json:"estimate,omitempty"`
}

// Estimate is the approximate cost of each extra player.
type Estimate struct {
	CPUPerPlayer float64 `json:"cpuPerPlayer"` // percentage points
	MemPerPlayer float64 `json:"memPerPlayer"` // bytes
	CPUAt10      float64 `json:"cpuAt10"`      // predicted server CPU % with 10 players
	MemAt10      float64 `json:"memAt10"`      // predicted server memory with 10 players
}

// minSeconds is how long a player count must be seen to count towards
// the per-player estimate.
const minSeconds = 120

// Summary returns the buckets in player order plus an estimate when there
// is enough data (at least two player counts seen for a couple of minutes).
func (b *ByPlayers) Summary() Summary {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := Summary{Since: b.Since, Buckets: []Bucket{}}
	for _, k := range b.Buckets {
		out.Buckets = append(out.Buckets, *k)
	}
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Players < out.Buckets[j].Players })

	var xs, cpu, mem, ws []float64
	for _, k := range out.Buckets {
		if k.Seconds >= minSeconds {
			xs = append(xs, float64(k.Players))
			cpu = append(cpu, k.CPU)
			mem = append(mem, k.Mem)
			ws = append(ws, math.Sqrt(k.Seconds))
		}
	}
	if len(xs) >= 2 {
		cs, ci := fitLine(xs, cpu, ws)
		ms, mi := fitLine(xs, mem, ws)
		out.Estimate = &Estimate{
			CPUPerPlayer: math.Max(cs, 0),
			MemPerPlayer: math.Max(ms, 0),
			CPUAt10:      math.Max(ci+10*math.Max(cs, 0), 0),
			MemAt10:      math.Max(mi+10*math.Max(ms, 0), 0),
		}
	}
	return out
}

// fitLine is a weighted least-squares line fit; it returns slope, intercept.
func fitLine(x, y, w []float64) (slope, intercept float64) {
	var sw, sx, sy, sxx, sxy float64
	for i := range x {
		sw += w[i]
		sx += w[i] * x[i]
		sy += w[i] * y[i]
		sxx += w[i] * x[i] * x[i]
		sxy += w[i] * x[i] * y[i]
	}
	den := sw*sxx - sx*sx
	if den == 0 {
		return 0, sy / sw
	}
	slope = (sw*sxy - sx*sy) / den
	intercept = (sy - slope*sx) / sw
	return slope, intercept
}

// Reset clears the summary.
func (b *ByPlayers) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Since = time.Now()
	b.Buckets = map[int]*Bucket{}
}

// Load reads a saved summary; a missing file gives an empty one.
func Load(path string) (*ByPlayers, error) {
	b := NewByPlayers()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, b); err != nil {
		return NewByPlayers(), err
	}
	if b.Buckets == nil {
		b.Buckets = map[int]*Bucket{}
	}
	return b, nil
}

// MarshalJSON is safe to call while samples are being added.
func (b *ByPlayers) MarshalJSON() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	type plain struct {
		Since   time.Time       `json:"since"`
		Buckets map[int]*Bucket `json:"buckets"`
	}
	return json.Marshal(plain{b.Since, b.Buckets})
}

// UnmarshalJSON restores a saved summary.
func (b *ByPlayers) UnmarshalJSON(data []byte) error {
	var p struct {
		Since   time.Time       `json:"since"`
		Buckets map[int]*Bucket `json:"buckets"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	b.Since, b.Buckets = p.Since, p.Buckets
	return nil
}
