package agentsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/config"
)

func TestAgentEnvFile(t *testing.T) {
	body, err := AgentEnvFile(config.Agent{
		RelayAddr:       "wss://tunnel.example.com/agent",
		RegistrationKey: "test-key",
		Name:            "my-pc",
		LocalSSHAddr:    "127.0.0.1:22",
		IdleTimeout:     90 * time.Second,
		SSHUser:         "me",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RELAY_ADDR=wss://tunnel.example.com/agent", "REGISTRATION_KEY=test-key", "AGENT_NAME=my-pc", "SSH_USER=me", "IDLE_TIMEOUT=90s"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s\n%s", want, body)
		}
	}
	if strings.Contains(body, "LOCAL_SSH_ADDR_AUTO") {
		t.Fatal("explicit address was marked auto")
	}
	if _, err := AgentEnvFile(config.Agent{RegistrationKey: "bad\nkey"}); err == nil {
		t.Fatal("newline should be rejected")
	}
}

func TestAgentEnvFileKeepsAuto(t *testing.T) {
	body, err := AgentEnvFile(config.Agent{
		RelayAddr:       "wss://tunnel.example.com/agent",
		RegistrationKey: "test-key",
		Name:            "my-pc",
		LocalSSHAddr:    "127.0.0.1:53257",
		LocalSSHAuto:    true,
		IdleTimeout:     90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "LOCAL_SSH_ADDR=127.0.0.1:53257") || !strings.Contains(body, "LOCAL_SSH_ADDR_AUTO=1") {
		t.Fatal(body)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(body, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		t.Setenv(key, val)
	}
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	cfg, err := config.LoadAgent("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalSSHAddr != "127.0.0.1:53257" || !cfg.LocalSSHAuto || cfg.LocalExplicit {
		t.Fatalf("%+v", cfg)
	}
}

func TestSystemdUnitMatchesDeployFile(t *testing.T) {
	unit := SystemdUnit()
	path := filepath.Join("..", "..", "deploy", "kite-agent.service")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(body), "\r\n", "\n") != unit {
		t.Fatalf("deploy/kite-agent.service drifted from the unit install writes:\n--- deploy\n%s\n--- install\n%s", body, unit)
	}
}

func TestLaunchDaemonHasNoSecret(t *testing.T) {
	text := LaunchDaemon()
	if strings.Contains(text, "REGISTRATION_KEY=") || strings.Contains(text, "CHANGE_ME") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "/etc/kite/agent.env") || !strings.Contains(text, "/usr/local/bin/kite-agent") {
		t.Fatal(text)
	}
}

func TestManualInstallMentionsSupervisors(t *testing.T) {
	if !strings.Contains(ManualInstallText("linux"), "systemctl") {
		t.Fatal("linux")
	}
	if !strings.Contains(ManualInstallText("darwin"), "LaunchAgents") {
		t.Fatal("darwin")
	}
}
