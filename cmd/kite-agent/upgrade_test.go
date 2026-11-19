package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzzbug01/kite/internal/agentsvc"
)

func TestGuardTokenDowngrade(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "agent.env")
	body := "AGENT_TOKEN=kite_agent_file\nRELAY_ADDR=wss://relay.example/agent\nAGENT_NAME=box\n"
	if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITE_CONFIG", envPath)
	t.Setenv("REGISTRATION_KEY", "from-the-shell")
	t.Setenv("AGENT_TOKEN", "")
	err := guardTokenDowngrade(agentsvc.UpgradeOptions{}, "v0.1.0-rc.3")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("env file rc.3: %v", err)
	}
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{Force: true}, "v0.1.0-rc.3"); err != nil {
		t.Fatal(err)
	}
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{}, "v0.1.0-rc.4"); err != nil {
		t.Fatal(err)
	}

	yamlPath := filepath.Join(dir, "config.yaml")
	yamlBody := "agent:\n  agent_token: kite_agent_file\n  relay_addr: wss://relay.example/agent\n  name: box\n"
	if err := os.WriteFile(yamlPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITE_CONFIG", yamlPath)
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{}, "v0.1.0-rc.3"); err == nil {
		t.Fatal("yaml token install was allowed to downgrade")
	}
	both := filepath.Join(dir, "both.env")
	if err := os.WriteFile(both, []byte("AGENT_TOKEN=kite_agent_file\nREGISTRATION_KEY=real-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITE_CONFIG", both)
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{}, "v0.1.0-rc.3"); err != nil {
		t.Fatal(err)
	}
}
