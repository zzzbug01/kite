package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/invite"
	"github.com/zzzbug01/kite/internal/pairing"
)

func TestInviteRedeemAndAgentToken(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		RegistrationKey: "test-key",
		StateDir:        dir,
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln, "", "") }()
	t.Cleanup(func() { ln.Close() })

	store := invite.Open(dir)
	now := time.Now().UTC()
	code, _, err := store.Issue("my-pc", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := store.Issue("old-pc", time.Hour, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()

	res := postInvite(t, base, expired, "old-pc")
	if res.StatusCode != http.StatusGone || !strings.Contains(res.Body, "install code expired") {
		t.Fatalf("expired: %d %s", res.StatusCode, res.Body)
	}
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}

	badName := postInvite(t, base, code, "other")
	if badName.StatusCode != http.StatusConflict || !strings.Contains(badName.Body, "different agent") {
		t.Fatalf("name: %d %s", badName.StatusCode, badName.Body)
	}

	ok := postInvite(t, base, code, "my-pc")
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("redeem: %d %s", ok.StatusCode, ok.Body)
	}
	var issued struct {
		Agent string `json:"agent"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(ok.Body), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Agent != "my-pc" || !strings.HasPrefix(issued.Token, "kite_agent_") {
		t.Fatalf("%+v", issued)
	}
	again := postInvite(t, base, code, "my-pc")
	if again.StatusCode != http.StatusConflict || !strings.Contains(again.Body, "already used") {
		t.Fatalf("used: %d %s", again.StatusCode, again.Body)
	}

	go func() {
		_ = agent.Run(ctx, agent.Config{
			RelayURL:        "ws://" + ln.Addr().String() + "/agent",
			RegistrationKey: issued.Token,
			Auth:            "agent",
			Name:            "my-pc",
			LocalSSHAddr:    "127.0.0.1:1",
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !srv.HasAgent("my-pc") {
		time.Sleep(15 * time.Millisecond)
	}
	if !srv.HasAgent("my-pc") {
		t.Fatal("agent token was not accepted")
	}
	created, err := pairing.PostCreate(ctx, base, pairing.CreateRequest{
		Key:       issued.Token,
		Auth:      "agent",
		Agent:     "my-pc",
		SSHUser:   "me",
		ClientURL: "ws://" + ln.Addr().String() + "/client",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Code == "" || created.Agent != "my-pc" {
		t.Fatalf("%+v", created)
	}
	if _, err := pairing.PostCreate(ctx, base, pairing.CreateRequest{
		Key: "not-the-token", Auth: "agent", Agent: "my-pc", ClientURL: "ws://127.0.0.1/client",
	}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("bad token: %v", err)
	}

	// The shared registration key still registers an agent.
	go func() {
		_ = agent.Run(ctx, agent.Config{
			RelayURL:        "ws://" + ln.Addr().String() + "/agent",
			RegistrationKey: "test-key",
			Name:            "legacy",
			LocalSSHAddr:    "127.0.0.1:1",
			IdleTimeout:     time.Hour,
			PingInterval:    time.Hour,
			Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !srv.HasAgent("legacy") {
		time.Sleep(15 * time.Millisecond)
	}
	if !srv.HasAgent("legacy") {
		t.Fatal("registration key stopped working")
	}
}

func TestInviteRateLimit(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		RegistrationKey: "test-key",
		StateDir:        dir,
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln, "", "") }()
	t.Cleanup(func() { ln.Close() })
	base := "http://" + ln.Addr().String()
	var last apiResult
	for i := 0; i < 31; i++ {
		last = postInvite(t, base, "AAAAAAAA", "my-pc")
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d body %s", last.StatusCode, last.Body)
	}
}

type apiResult struct {
	StatusCode int
	Body       string
}

func postInvite(t *testing.T, base, code, name string) apiResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "name": name})
	resp, err := http.Post(base+"/invite/redeem", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return apiResult{StatusCode: resp.StatusCode, Body: string(b)}
}
