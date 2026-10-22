package manager

import (
	"sync"
	"time"
)

// LogLine is one line of server output shown on the Log page.
type LogLine struct {
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// Activity is one entry in the activity feed.
type Activity struct {
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Name string    `json:"name,omitempty"`
	Text string    `json:"text"`
}

// ring is a fixed-size buffer that hands out increasing sequence numbers,
// so clients can ask for "everything after N".
type ring[T any] struct {
	mu    sync.Mutex
	items []T
	seqs  []int64
	next  int64
	max   int
}

func newRing[T any](max int) *ring[T] { return &ring[T]{max: max, next: 1} }

func (r *ring[T]) add(make func(seq int64) T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, make(r.next))
	r.seqs = append(r.seqs, r.next)
	r.next++
	if len(r.items) > r.max {
		drop := len(r.items) - r.max
		r.items = append(r.items[:0:0], r.items[drop:]...)
		r.seqs = append(r.seqs[:0:0], r.seqs[drop:]...)
	}
}

// after returns up to limit items with a sequence number greater than seq
// (the newest ones if there are more), plus the latest sequence number.
func (r *ring[T]) after(seq int64, limit int) ([]T, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := len(r.items)
	for i, s := range r.seqs {
		if s > seq {
			start = i
			break
		}
	}
	items := r.items[start:]
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return append([]T(nil), items...), r.next - 1
}

// last returns the newest n items, newest first.
func (r *ring[T]) last(n int) []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]T, 0, n)
	for i := len(r.items) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, r.items[i])
	}
	return out
}
