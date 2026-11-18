package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("" +
		"relay:\n" +
		"  listen_addr: 127.0.0.1:9\n" +
		"  registration_key: file-key-value\n" +
		"  idle_timeout: 30s\n" +
		"agent:\n" +
		"  relay_addr: ws://127.0.0.1:9/agent\n" +
		"  registration_key: file-key-value\n" +
		"  name: from-file\n" +
		"  local_ssh_addr: 127.0.0.1:2222\n" +
		"client:\n" +
		"  relay_addr: ws://127.0.0.1:9/client\n" +
		"  registration_key: file-key-value\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REGISTRATION_KEY", "env-key-value")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:8081")
	t.Setenv("IDLE_TIMEOUT", "45")
	t.Setenv("RELAY_ADDR", "wss://tunnel.example.com/agent")
	t.Setenv("AGENT_NAME", "from-env")
	t.Setenv("LOCAL_SSH_ADDR", "")
	t.Setenv("TLS_CERT_FILE", "")
	t.Setenv("TLS_KEY_FILE", "")
	t.Setenv("KITE_CONFIG", "")

	relay, err := LoadRelay(path)
	if err != nil {
		t.Fatal(err)
	}
	if relay.RegistrationKey != "env-key-value" {
		t.Fatalf("key %q", relay.RegistrationKey)
	}
	if relay.ListenAddr != "127.0.0.1:8081" {
		t.Fatalf("listen %q", relay.ListenAddr)
	}
	if relay.IdleTimeout != 45*time.Second {
		t.Fatalf("idle %s", relay.IdleTimeout)
	}

	agent, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "from-env" || agent.LocalSSHAddr != "127.0.0.1:2222" {
		t.Fatalf("%+v", agent)
	}
	if agent.RelayAddr != "wss://tunnel.example.com/agent" {
		t.Fatalf("relay %q", agent.RelayAddr)
	}

	client, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if client.RegistrationKey != "env-key-value" {
		t.Fatalf("client key %q", client.RegistrationKey)
	}
}

func TestMissingExplicitConfig(t *testing.T) {
	if _, err := LoadRelay(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing --config")
	}
}

func TestValidateKey(t *testing.T) {
	if err := ValidateKey(""); err == nil {
		t.Fatal("empty")
	}
	if err := ValidateKey(PlaceholderKey); err == nil {
		t.Fatal("placeholder")
	}
	if err := ValidateKey("test-key"); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRelayURL(t *testing.T) {
	got, err := NormalizeRelayURL("wss://tunnel.example.com", "/agent")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://tunnel.example.com/agent" {
		t.Fatalf("%s", got)
	}
	got, err = NormalizeRelayURL("ws://127.0.0.1:8080/agent", "/client")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://127.0.0.1:8080/agent" {
		t.Fatalf("%s", got)
	}
	if _, err := NormalizeRelayURL("https://tunnel.example.com", "/agent"); err == nil {
		t.Fatal("https should be rejected")
	}
}

func TestPeekConfigFlag(t *testing.T) {
	if got := PeekConfigFlag([]string{"-listen", "127.0.0.1:1", "--config", "a.yaml"}); got != "a.yaml" {
		t.Fatalf("%q", got)
	}
	if got := PeekConfigFlag([]string{"-config=b.yaml"}); got != "b.yaml" {
		t.Fatalf("%q", got)
	}
	if got := PeekConfigFlag([]string{"--", "--config", "c.yaml"}); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestConfigSearchSkipsMachinePathOffWindows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	programData := t.TempDir()
	t.Setenv("ProgramData", programData)

	paths := configSearchPaths()
	if runtime.GOOS == "windows" {
		want := filepath.Join(programData, "kite", "config.yaml")
		if len(paths) == 0 || !strings.EqualFold(paths[len(paths)-1], want) {
			t.Fatalf("paths %v, want machine config %s", paths, want)
		}
		return
	}
	if WindowsMachineConfig() != "" {
		t.Fatal("WindowsMachineConfig should be empty off Windows")
	}
	for _, p := range paths {
		if strings.Contains(strings.ToLower(p), "programdata") {
			t.Fatalf("non-windows search path %q", p)
		}
	}
}

func TestDefaultsWithoutFile(t *testing.T) {
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("REGISTRATION_KEY", "")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("IDLE_TIMEOUT", "")
	t.Setenv("TLS_CERT_FILE", "")
	t.Setenv("TLS_KEY_FILE", "")
	// Point home at an empty dir so a developer config is not picked up.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := LoadRelay("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.IdleTimeout != 90*time.Second {
		t.Fatalf("%+v", cfg)
	}
	if cfg.RegistrationKey != "" {
		t.Fatalf("unexpected key %q", cfg.RegistrationKey)
	}
}
