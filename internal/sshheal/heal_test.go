package sshheal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeSys struct {
	running  bool
	started  int
	detected string
	startErr error
}

func (f *fakeSys) Running(context.Context) (bool, error) { return f.running, nil }
func (f *fakeSys) Start(context.Context) error {
	f.started++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}
func (f *fakeSys) Detect(context.Context) (string, error) { return f.detected, nil }

func TestRecoverStartsAndThrottles(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{}
	dir := t.TempDir()
	h := New(sys, "127.0.0.1:22", false, filepath.Join(dir, "ssh-health.json"), "", nil, func() time.Time { return now })
	if !h.Recover(context.Background(), errors.New("connection refused")) {
		t.Fatal("expected a retry after start")
	}
	if sys.started != 1 {
		t.Fatal(sys.started)
	}
	if got := Read(h.healthPath).Line(); got != "Local SSH: started by kite (127.0.0.1:22)" {
		t.Fatal(got)
	}
	now = now.Add(10 * time.Second)
	if h.Recover(context.Background(), errors.New("connection refused")) {
		t.Fatal("throttled call should not retry")
	}
	if sys.started != 1 {
		t.Fatal("start ran inside the throttle window")
	}
	if got := Read(h.healthPath).Line(); got != "Local SSH: failed: connection refused" {
		t.Fatal(got)
	}
	now = now.Add(30 * time.Second)
	sys.running = true
	if h.Recover(context.Background(), errors.New("still down")) {
		t.Fatal("running sshd with an explicit address should not retry a bad dial")
	}
	if sys.started != 1 {
		t.Fatal(sys.started)
	}
}

func TestRecoverDetectsMovedPortBeforeStart(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{running: false, detected: "127.0.0.1:2222", startErr: errors.New("not root; cannot start sshd")}
	h := New(sys, "127.0.0.1:22", true, filepath.Join(t.TempDir(), "h.json"), "", nil, func() time.Time { return now })
	if !h.Recover(context.Background(), errors.New("refused")) {
		t.Fatal("expected retry on the new port")
	}
	if sys.started != 0 {
		t.Fatal("start ran before the port was re-detected")
	}
	if h.Addr() != "127.0.0.1:2222" {
		t.Fatal(h.Addr())
	}
	if got := Read(h.healthPath).Line(); got != "Local SSH: port changed from 127.0.0.1:22 to 127.0.0.1:2222" {
		t.Fatal(got)
	}
}

func TestRecoverChangesAutoPort(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{running: true, detected: "127.0.0.1:2222"}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := osWrite(cfg, "agent:\n  local_ssh_addr: 127.0.0.1:22\n  local_ssh_addr_auto: true\n"); err != nil {
		t.Fatal(err)
	}
	h := New(sys, "127.0.0.1:22", true, filepath.Join(dir, "ssh-health.json"), cfg, nil, func() time.Time { return now })
	if !h.Recover(context.Background(), errors.New("refused")) {
		t.Fatal("expected retry")
	}
	if h.Addr() != "127.0.0.1:2222" {
		t.Fatal(h.Addr())
	}
	if sys.started != 0 {
		t.Fatal("should not start a running sshd")
	}
	line := Read(h.healthPath).Line()
	if line != "Local SSH: port changed from 127.0.0.1:22 to 127.0.0.1:2222" {
		t.Fatal(line)
	}
	body, err := osRead(cfg)
	if err != nil || !strContains(body, "127.0.0.1:2222") {
		t.Fatalf("%s %v", body, err)
	}
}

func TestRecoverUpdatesEnvFile(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{running: true, detected: "127.0.0.1:2222"}
	env := filepath.Join(t.TempDir(), "agent.env")
	if err := osWrite(env, "LOCAL_SSH_ADDR=127.0.0.1:53257\nLOCAL_SSH_ADDR_AUTO=1\n"); err != nil {
		t.Fatal(err)
	}
	h := New(sys, "127.0.0.1:53257", true, filepath.Join(t.TempDir(), "h.json"), "", nil, func() time.Time { return now })
	h.WithEnvFile(env)
	if !h.Recover(context.Background(), errors.New("refused")) {
		t.Fatal("retry")
	}
	body, err := osRead(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strContains(body, "LOCAL_SSH_ADDR=127.0.0.1:2222") || !strContains(body, "LOCAL_SSH_ADDR_AUTO=1") {
		t.Fatal(body)
	}
}

func TestExplicitAddressIsNotRewritten(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{running: true, detected: "127.0.0.1:2222"}
	h := New(sys, "127.0.0.1:22", false, "", "", nil, func() time.Time { return now })
	if h.Recover(context.Background(), errors.New("refused")) {
		t.Fatal("explicit address should not switch")
	}
	if h.Addr() != "127.0.0.1:22" {
		t.Fatal(h.Addr())
	}
}

func TestHistorySurvivesOK(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	sys := &fakeSys{running: true, detected: "127.0.0.1:2222"}
	path := filepath.Join(t.TempDir(), "h.json")
	h := New(sys, "127.0.0.1:22", true, path, "", nil, func() time.Time { return now })
	if !h.Recover(context.Background(), errors.New("refused")) {
		t.Fatal("retry")
	}
	h.MarkOK("127.0.0.1:2222")
	text := Read(path).Text()
	if !strContains(text, "ok (127.0.0.1:2222)") || !strContains(text, "port changed from") {
		t.Fatal(text)
	}
}

func TestMarkOK(t *testing.T) {
	h := New(&fakeSys{}, "127.0.0.1:22", true, filepath.Join(t.TempDir(), "h.json"), "", nil, nil)
	h.MarkOK("127.0.0.1:22")
	h.MarkOK("127.0.0.1:22")
	if got := Read(h.healthPath).Line(); got != "Local SSH: ok (127.0.0.1:22)" {
		t.Fatal(got)
	}
}

func strContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
