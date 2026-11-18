package pairing

import (
	"sync"
	"time"
)

// Rule is one sliding-window counter.
type Rule struct {
	Key    string
	Limit  int
	Window time.Duration
}

// Limiter counts events per key inside a sliding window.
// A denied call does not record a new event, so the window is not extended
// by the retries that the limit is already rejecting.
type Limiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
}

func newLimiter() *Limiter {
	return &Limiter{events: make(map[string][]time.Time)}
}

// AllowAll reports whether every rule still has room. On success it records
// one event for each rule. On failure it records nothing.
func (l *Limiter) AllowAll(now time.Time, rules ...Rule) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(rules) == 0 {
		return true
	}
	for _, r := range rules {
		if r.Limit <= 0 || r.Window <= 0 {
			return false
		}
		if countRecent(l.events[r.Key], now, r.Window) >= r.Limit {
			return false
		}
	}
	for _, r := range rules {
		l.events[r.Key] = append(trimOlder(l.events[r.Key], now, r.Window), now)
	}
	if len(l.events) > 4096 {
		l.events = make(map[string][]time.Time)
	}
	return true
}

func countRecent(times []time.Time, now time.Time, window time.Duration) int {
	n := 0
	cut := now.Add(-window)
	for _, t := range times {
		if t.After(cut) {
			n++
		}
	}
	return n
}

func trimOlder(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cut := now.Add(-window)
	kept := times[:0]
	for _, t := range times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	return kept
}
