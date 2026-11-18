package pairing

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterEvictsExpiredThenOldest(t *testing.T) {
	l := newLimiter()
	old := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 4100; i++ {
		key := fmt.Sprintf("old-%d", i)
		if !l.AllowAll(old, Rule{Key: key, Limit: 5, Window: time.Minute}) {
			t.Fatalf("record %d", i)
		}
	}
	if len(l.events) > 4096 {
		t.Fatalf("cap not applied: %d", len(l.events))
	}
	now := old.Add(time.Hour)
	if !l.AllowAll(now, Rule{Key: "fresh", Limit: 1, Window: time.Minute}) {
		t.Fatal("fresh denied")
	}
	if _, ok := l.events["fresh"]; !ok {
		t.Fatal("fresh key was wiped with the map")
	}
	// Events from an hour ago are outside the one-minute window, so eviction
	// drops them instead of deleting the key just recorded.
	for key := range l.events {
		if key != "fresh" {
			t.Fatalf("expired key survived: %s (len %d)", key, len(l.events))
		}
	}
}

func TestLimiterKeepsLiveKeysWhenFull(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 4096; i++ {
		key := fmt.Sprintf("live-%d", i)
		at := now.Add(time.Duration(i) * time.Millisecond)
		if !l.AllowAll(at, Rule{Key: key, Limit: 5, Window: time.Hour}) {
			t.Fatal(i)
		}
	}
	later := now.Add(time.Second)
	if !l.AllowAll(later, Rule{Key: "newest", Limit: 5, Window: time.Hour}) {
		t.Fatal("newest denied")
	}
	if _, ok := l.events["newest"]; !ok {
		t.Fatal("newest evicted")
	}
	if _, ok := l.events["live-0"]; ok {
		t.Fatal("oldest live key should have been the one evicted")
	}
	if len(l.events) > 4096 {
		t.Fatalf("len %d", len(l.events))
	}
}
