package pairing

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testBroker(now *time.Time) *Broker {
	return NewBroker(Config{
		Now:              func() time.Time { return *now },
		TTL:              10 * time.Minute,
		CreateIPLimit:    5,
		CreateAgentLimit: 5,
		RedeemIPLimit:    8,
		LimitWindow:      time.Minute,
		MaxCodesPerAgent: 3,
		MaxCodes:         10,
	})
}

func TestCodeFormatAndNormalize(t *testing.T) {
	norm, err := newCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(norm) != 8 {
		t.Fatalf("len %d", len(norm))
	}
	for _, r := range norm {
		if !strings.ContainsRune(Alphabet, r) {
			t.Fatalf("ambiguous or out of alphabet: %q in %q", r, norm)
		}
	}
	display := FormatCode(norm)
	if display[4] != '-' {
		t.Fatal(display)
	}
	got, err := NormalizeCode("  " + strings.ToLower(display) + " ")
	if err != nil || got != norm {
		t.Fatalf("normalize %q -> %q %v", display, got, err)
	}
	if _, err := NormalizeCode("ABCD-EFG"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatal(err)
	}
	if _, err := NormalizeCode("ABCD-EFG0"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatal(err)
	}
}

func TestCreateDoesNotStoreRawCodeOrKey(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	code, exp, err := b.Create("203.0.113.4", "my-pc", "me", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(10 * time.Minute)) {
		t.Fatal(exp)
	}
	norm, err := NormalizeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.codes) != 1 {
		t.Fatalf("stored %d", len(b.codes))
	}
	blob := string(b.codes[0].hash) + b.codes[0].agent + b.codes[0].sshUser + b.codes[0].clientURL
	if strings.Contains(blob, norm) || strings.Contains(blob, code) {
		t.Fatal("raw pairing code was stored")
	}
	if strings.Contains(blob, "registration") {
		t.Fatal(blob)
	}
}

func TestRedeemExpiryAndSingleUse(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	code, _, err := b.Create("203.0.113.5", "my-pc", "me", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := b.Create("203.0.113.5", "other", "you", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.Redeem("203.0.113.9", "ZZZZ-ZZZZ"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	issued, err := b.Redeem("198.51.100.2", code)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Agent != "my-pc" || issued.SSHUser != "me" || issued.ClientURL != "wss://tunnel.example.com/client" {
		t.Fatalf("%+v", issued)
	}
	if _, err := b.Redeem("198.51.100.3", code); !errors.Is(err, ErrCodeUsed) {
		t.Fatalf("second redeem: %v", err)
	}
	// The other code is still outstanding.
	if _, err := b.Redeem("198.51.100.4", other); err != nil {
		t.Fatal(err)
	}

	fresh, _, err := b.Create("203.0.113.8", "my-pc", "me", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10*time.Minute + time.Second)
	if _, err := b.Redeem("198.51.100.5", fresh); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestRedeemSingleUseRace(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	b.redeemIP = 100
	code, _, err := b.Create("203.0.113.10", "my-pc", "me", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Redeem("198.51.100.20", code); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("successful redeems: %d", ok.Load())
	}
}

func TestRateLimits(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	for i := 0; i < 3; i++ {
		if _, _, err := b.Create("203.0.113.20", "pc-a", "me", "wss://tunnel.example.com/client", 0); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, _, err := b.Create("203.0.113.21", "pc-a", "me", "wss://tunnel.example.com/client", 0); !errors.Is(err, ErrTooManyCodes) {
		t.Fatalf("over cap: %v", err)
	}

	b2 := testBroker(&now)
	b2.maxAgent = 100
	b2.maxAll = 100
	for i := 0; i < 5; i++ {
		if _, _, err := b2.Create("203.0.113.30", "pc-b", "me", "wss://tunnel.example.com/client", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b2.Create("203.0.113.30", "pc-c", "me", "wss://tunnel.example.com/client", 0); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("ip limit: %v", err)
	}
	if _, _, err := b2.Create("203.0.113.31", "pc-fresh", "me", "wss://tunnel.example.com/client", 0); err != nil {
		t.Fatalf("other ip should not inherit the first ip's limit: %v", err)
	}
	if _, _, err := b2.Create("203.0.113.32", "pc-b", "me", "wss://tunnel.example.com/client", 0); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("agent limit: %v", err)
	}

	for i := 0; i < 8; i++ {
		_, err := b2.Redeem("198.51.100.40", "ABCD-EFGH")
		if !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("redeem %d: %v", i, err)
		}
	}
	if _, err := b2.Redeem("198.51.100.40", "ABCD-EFGH"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("redeem limit: %v", err)
	}
	// A different IP is not blocked by the first IP's failures.
	if _, err := b2.Redeem("198.51.100.41", "ABCD-EFGH"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("other ip: %v", err)
	}
}

func TestRateLimitWindowSlides(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	b.maxAgent = 100
	b.maxAll = 100
	for i := 0; i < 5; i++ {
		if _, _, err := b.Create("203.0.113.50", "pc", "me", "wss://tunnel.example.com/client", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.Create("203.0.113.50", "pc", "me", "wss://tunnel.example.com/client", 0); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Second)
	if _, _, err := b.Create("203.0.113.50", "pc", "me", "wss://tunnel.example.com/client", 0); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestBeginFailureDoesNotConsume(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	b := testBroker(&now)
	code, _, err := b.Create("203.0.113.8", "my-pc", "me", "wss://tunnel.example.com/client", 0)
	if err != nil {
		t.Fatal(err)
	}
	issued, done, err := b.Begin("198.51.100.8", code)
	if err != nil || issued.Agent != "my-pc" {
		t.Fatal(err, issued)
	}
	if _, _, err := b.Begin("198.51.100.9", code); !errors.Is(err, ErrCodeBusy) {
		t.Fatalf("second begin: %v", err)
	}
	done(false)
	if _, err := b.Redeem("198.51.100.10", code); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeLabel(t *testing.T) {
	if got := SanitizeLabel("my laptop"); got != "mylaptop" {
		t.Fatal(got)
	}
	if got := SanitizeLabel("ok.box_1"); got != "ok.box_1" {
		t.Fatal(got)
	}
	if got := SanitizeLabel("  \n"); got != "client" {
		t.Fatal(got)
	}
	long := strings.Repeat("a", 40)
	if got := SanitizeLabel(long); len(got) != 32 {
		t.Fatal(len(got))
	}
	c := PairComment("lap top", time.Date(2026, 11, 18, 15, 0, 0, 0, time.FixedZone("CST", 8*3600)))
	if c != "kite-pair laptop 2026-11-18" {
		t.Fatal(c)
	}
}
