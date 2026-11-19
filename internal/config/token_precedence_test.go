package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenBeatsEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("agent:\n" +
		"  name: box\n" +
		"  relay_addr: wss://from-file.example/agent\n" +
		"  agent_token: kite_agent_file\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_ADDR", "wss://from-env.example/agent")
	t.Setenv("REGISTRATION_KEY", "env-key")
	t.Setenv("AGENT_NAME", "")
	cfg, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelayAddr != "wss://from-file.example/agent" {
		t.Fatalf("relay %q", cfg.RelayAddr)
	}
	if cfg.RegistrationKey != "" {
		t.Fatalf("key %q", cfg.RegistrationKey)
	}
	secret, kind, err := cfg.Credential()
	if err != nil {
		t.Fatal(err)
	}
	if kind != "agent" || secret != "kite_agent_file" {
		t.Fatalf("credential %s %s", kind, secret)
	}
}

func TestEnvStillFillsKeyInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("agent:\n  name: box\n  relay_addr: wss://from-file.example/agent\n  registration_key: file-key\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_ADDR", "wss://from-env.example/agent")
	t.Setenv("REGISTRATION_KEY", "env-key")
	cfg, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RelayAddr != "wss://from-env.example/agent" || cfg.RegistrationKey != "env-key" {
		t.Fatalf("%+v", cfg)
	}
}
