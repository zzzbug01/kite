package pairing

import (
	"sort"
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
	events map[string]*series
}

// series is one key's timestamps. window is the rule that last wrote it, so
// a later eviction can drop expired keys without forgetting that window.
type series struct {
	window time.Duration
	times  []time.Time
}

func newLimiter() *Limiter {
	return &Limiter{events: make(map[string]*series)}
}

// NewLimiter is an empty sliding-window limiter.
func NewLimiter() *Limiter {
	return newLimiter()
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
		if countSeries(l.events[r.Key], now, r.Window) >= r.Limit {
			return false
		}
	}
	touched := make([]string, 0, len(rules))
	for _, r := range rules {
		s := l.events[r.Key]
		if s == nil {
			s = &series{window: r.Window}
		}
		s.window = r.Window
		s.times = append(trimOlder(s.times, now, r.Window), now)
		l.events[r.Key] = s
		touched = append(touched, r.Key)
	}
	if len(l.events) > 4096 {
		l.evict(now, touched)
	}
	return true
}

// evict drops expired keys, then the keys whose newest event is oldest,
// until the map is back to 4096 entries. Keys just recorded in this call
// are kept: wiping the whole map would forgive every active limit.
func (l *Limiter) evict(now time.Time, keep []string) {
	kept := make(map[string]struct{}, len(keep))
	for _, k := range keep {
		kept[k] = struct{}{}
	}
	for key, s := range l.events {
		if s == nil || s.window <= 0 {
			delete(l.events, key)
			continue
		}
		s.times = trimOlder(s.times, now, s.window)
		if len(s.times) == 0 {
			delete(l.events, key)
		}
	}
	if len(l.events) <= 4096 {
		return
	}
	type item struct {
		key    string
		latest time.Time
	}
	order := make([]item, 0, len(l.events))
	for key, s := range l.events {
		latest := s.times[len(s.times)-1]
		order = append(order, item{key: key, latest: latest})
	}
	sort.Slice(order, func(i, j int) bool {
		return order[i].latest.Before(order[j].latest)
	})
	for _, it := range order {
		if len(l.events) <= 4096 {
			return
		}
		if _, ok := kept[it.key]; ok {
			continue
		}
		delete(l.events, it.key)
	}
}

func countSeries(s *series, now time.Time, window time.Duration) int {
	if s == nil {
		return 0
	}
	return countRecent(s.times, now, window)
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
