package agentctl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/authkeys"
)

type fakeHandler struct{}

func (fakeHandler) Pair(context.Context, time.Duration) (string, string, time.Time, error) {
	return "ABCD-EFGH", "my-pc", time.Date(2026, 11, 18, 9, 10, 0, 0, time.UTC), nil
}

func (fakeHandler) List(context.Context) ([]authkeys.Record, error) {
	return []authkeys.Record{{Label: "laptop", Date: "2026-11-18"}}, nil
}

func (fakeHandler) Revoke(context.Context, string) (int, error) {
	return 1, nil
}

func TestControlRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-control.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Start(ctx, path, fakeHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
	resp, err := Call(ctx, path, Request{Op: "pair", TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Code != "ABCD-EFGH" || resp.Agent != "my-pc" {
		t.Fatalf("%+v", resp)
	}
	listed, err := Call(ctx, path, Request{Op: "list"})
	if err != nil || len(listed.Keys) != 1 || listed.Keys[0].Label != "laptop" {
		t.Fatalf("%v %+v", err, listed)
	}
	rev, err := Call(ctx, path, Request{Op: "revoke", Label: "laptop"})
	if err != nil || rev.Removed != 1 {
		t.Fatalf("%v %+v", err, rev)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ep Endpoint
	if err := json.Unmarshal(body, &ep); err != nil {
		t.Fatal(err)
	}
	ep.Token = "nope"
	raw, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Call(ctx, bad, Request{Op: "pair"})
	if err == nil || err.Error() != "unauthorized" {
		t.Fatalf("bad token: %v", err)
	}
}

func TestRejectsNonLoopback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-control.json")
	raw, err := json.Marshal(Endpoint{Addr: "203.0.113.10:9", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Call(context.Background(), path, Request{Op: "pair"})
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatal(err)
	}
}
