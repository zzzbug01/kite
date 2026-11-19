package invite

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIssueRedeemSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	now := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
	code, exp, err := s.Issue("", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 9 || code[4] != '-' {
		t.Fatalf("code %q", code)
	}
	if !exp.Equal(now.Add(time.Hour)) {
		t.Fatal(exp)
	}
	body, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), code) || strings.Contains(strings.ToLower(string(body)), strings.ToLower(strings.ReplaceAll(code, "-", ""))) {
		t.Fatalf("raw code written to disk:\n%s", body)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}

	reopened := Open(dir)
	token, agent, err := reopened.Redeem(code, "my-pc", false, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if agent != "my-pc" || !strings.HasPrefix(token, "kite_agent_") {
		t.Fatalf("token %q agent %q", token, agent)
	}
	body, err = os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), token) {
		t.Fatal("raw token was written to disk")
	}
	if !reopened.Authenticate(token, "my-pc") {
		t.Fatal("token did not authenticate")
	}
	if reopened.Authenticate(token, "other") {
		t.Fatal("token accepted for a different agent")
	}
	if _, _, err := reopened.Redeem(strings.ToLower(code), "my-pc", false, now.Add(time.Minute)); !errors.Is(err, ErrUsed) {
		t.Fatalf("second redeem: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".install.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock directory left behind")
	}
}

func TestExpiredAndWrongNameDoNotIssue(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	now := time.Now().UTC()
	code, _, err := s.Issue("desktop", time.Hour, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(code, "desktop", true, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	listed, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("expired code issued a token: %+v", listed)
	}

	fresh, _, err := s.Issue("desktop", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(fresh, "other", true, now); !errors.Is(err, ErrName) {
		t.Fatalf("name: %v", err)
	}
	adopted, adoptedName, err := s.Redeem(fresh, "hostname", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if adoptedName != "desktop" || adopted == "" {
		t.Fatalf("adopt %s %s", adoptedName, adopted)
	}
	if _, err := s.Revoke("desktop"); err != nil {
		t.Fatal(err)
	}
	fresh, _, err = s.Issue("desktop", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	token, agent, err := s.Redeem(fresh, "desktop", true, now)
	if err != nil {
		t.Fatal(err)
	}
	if agent != "desktop" || token == "" {
		t.Fatalf("%s %s", agent, token)
	}
	if _, _, err := s.Redeem("not-a-code", "desktop", false, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
}

func TestRevokeDropsToken(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Now().UTC()
	code, _, err := s.Issue("", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Redeem(code, "my-pc", false, now)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Revoke("my-pc")
	if err != nil || info.Name != "my-pc" || info.ID == "" {
		t.Fatalf("%+v %v", info, err)
	}
	if s.Authenticate(token, "my-pc") {
		t.Fatal("revoked token still works")
	}
	if _, err := s.Revoke("my-pc"); err == nil {
		t.Fatal("expected missing token")
	}
}

func TestRevokeByListID(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Now().UTC()
	code, _, err := s.Issue("desktop", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Redeem(code, "desktop", true, now); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID == "" {
		t.Fatalf("%+v %v", list, err)
	}
	info, err := s.Revoke(list[0].ID)
	if err != nil || info.Name != "desktop" || info.ID != list[0].ID {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := s.Revoke(list[0].ID); err == nil {
		t.Fatal("id still revoked")
	}
}

func TestRejectsConnectAndBadTTL(t *testing.T) {
	s := Open(t.TempDir())
	if _, _, err := s.Issue("connect", time.Hour, time.Now()); err == nil {
		t.Fatal("connect should be reserved")
	}
	if _, _, err := s.Issue("", 24*time.Hour+time.Second, time.Now()); err == nil {
		t.Fatal("ttl over 24h should fail")
	}
}
