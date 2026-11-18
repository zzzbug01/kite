package pairing

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTokenStoreIssueAuthRevoke(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	raw, rec, err := s.Issue("my-pc", "lap top", "me", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "kite_") || rec.ID == "" || rec.Label != "laptop" {
		t.Fatalf("raw %q rec %+v", raw, rec)
	}
	body, err := os.ReadFile(filepath.Join(s.Dir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), raw) {
		t.Fatal("raw token was written to disk")
	}
	if !strings.Contains(string(body), rec.TokenHash) {
		t.Fatal("hash missing")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(s.Dir(), "clients.json"))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}

	if _, ok := s.Authenticate(raw, "my-pc"); !ok {
		t.Fatal("expected auth")
	}
	if _, ok := s.Authenticate(raw, "other"); ok {
		t.Fatal("token bound to another agent was accepted")
	}
	if _, ok := s.Authenticate(raw+"x", "my-pc"); ok {
		t.Fatal("mutated token accepted")
	}

	raw2, rec2, err := s.Issue("my-pc", "phone", "me", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != rec.ID || listed[1].ID != rec2.ID {
		t.Fatalf("%+v", listed)
	}
	for _, row := range listed {
		if row.TokenHash == "" {
			t.Fatal("list stripped the hash from the record the store returns")
		}
	}

	removed, err := s.Revoke("phone")
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != rec2.ID || removed.TokenHash != "" {
		t.Fatalf("%+v", removed)
	}
	if _, ok := s.Authenticate(raw2, "my-pc"); ok {
		t.Fatal("revoked token still works")
	}
	if _, ok := s.Authenticate(raw, "my-pc"); !ok {
		t.Fatal("other token was revoked")
	}
	if _, err := s.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(rec.ID); err == nil {
		t.Fatal("expected missing token")
	}
}

func TestRevokeAmbiguousLabel(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	if _, _, err := s.Issue("a", "laptop", "me", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Issue("b", "laptop", "me", now); err != nil {
		t.Fatal(err)
	}
	_, err := s.Revoke("laptop")
	if err == nil || !strings.Contains(err.Error(), "revoke by id") {
		t.Fatal(err)
	}
}

func TestClientURLAndHTTPBase(t *testing.T) {
	base, err := HTTPBase("wss://tunnel.example.com/agent")
	if err != nil || base != "https://tunnel.example.com" {
		t.Fatalf("%s %v", base, err)
	}
	got, err := ClientURL("wss://tunnel.example.com/agent")
	if err != nil || got != "wss://tunnel.example.com/client" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := HTTPBase("https://tunnel.example.com/agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientURL("http://tunnel.example.com"); err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	raw, _, err := s.Issue("my-pc", "laptop", "me", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s2 := Open(dir)
	if _, ok := s2.Authenticate(raw, "my-pc"); !ok {
		t.Fatal("reopen did not see the token")
	}
	if _, err := os.Stat(filepath.Join(dir, ".clients.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock directory left behind")
	}
}
