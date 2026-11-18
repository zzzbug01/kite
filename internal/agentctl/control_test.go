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

func (fakeHandler) Pair(context.Context, time.Duration, string) (string, string, time.Time, error) {
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

type relayHandler struct{ fakeHandler }

func (relayHandler) ClientURL() string { return "wss://instance.example/client" }

func TestPairResponseIncludesRelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-control.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := Start(ctx, path, relayHandler{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := Call(ctx, path, Request{Op: "pair"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Relay != "wss://instance.example/client" {
		t.Fatal(resp.Relay)
	}
}

func TestUnixInstancePaths(t *testing.T) {
	if UnixBinary("") != "/usr/local/bin/kite-agent" || UnixBinary("kite-agent") != "/usr/local/bin/kite-agent" {
		t.Fatal(UnixBinary("kite-agent"))
	}
	if UnixBinary("kite-agent-test2") != "/usr/local/lib/kite/kite-agent-test2/kite-agent" {
		t.Fatal(UnixBinary("kite-agent-test2"))
	}
	if UnixHealthPath("kite-agent") != "/var/lib/kite-agent/ssh-health.json" {
		t.Fatal(UnixHealthPath("kite-agent"))
	}
	if UnixHealthPath("kite-agent-test2") != "/var/lib/kite-agent/kite-agent-test2/ssh-health.json" {
		t.Fatal(UnixHealthPath("kite-agent-test2"))
	}
	if UnixEnvFile("kite-agent-test2") != "/etc/kite/kite-agent-test2/agent.env" {
		t.Fatal(UnixEnvFile("kite-agent-test2"))
	}
	name, ok := InstanceOfEnv("/etc/kite/kite-agent-test2/agent.env")
	if !ok || name != "kite-agent-test2" {
		t.Fatal(name, ok)
	}
	name, ok = InstanceOfEnv("/etc/kite/agent.env")
	if !ok || name != "kite-agent" {
		t.Fatal(name, ok)
	}
	if _, ok := InstanceOfEnv("/tmp/config.yaml"); ok {
		t.Fatal("custom config is not an instance env")
	}
}

func TestConfigSelectsInstanceControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	paths := CandidatePaths("/etc/kite/kite-agent-test2/agent.env", "kite-agent")
	if len(paths) == 0 || paths[0] != "/var/lib/kite-agent/kite-agent-test2/agent-control.json" {
		t.Fatal(paths)
	}
}

func TestNamedInstanceControlPathComesFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	paths := CandidatePaths("", "kite-agent-test")
	if len(paths) == 0 || !strings.HasSuffix(paths[0], "/var/lib/kite-agent/kite-agent-test/agent-control.json") {
		t.Fatal(paths)
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
