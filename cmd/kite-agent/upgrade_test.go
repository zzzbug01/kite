package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zzzbug01/kite/internal/agentsvc"
)

func TestPairHelpNamesTheInstance(t *testing.T) {
	got := pairFailure(
		"/var/lib/kite-agent/kite-agent-test3/agent-control.json",
		errors.New("open /var/lib/kite-agent/kite-agent-test3/agent-control.json: permission denied"),
		errors.New("open /var/lib/kite/agent-control.json: no such file or directory"),
	)
	if !strings.Contains(got, "/var/lib/kite-agent/kite-agent-test3/agent-control.json") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Fatal(got)
	}
	if strings.Contains(got, "/var/lib/kite/agent-control.json") {
		t.Fatal(got)
	}
}

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

func TestUpgradeConfigPathUnixLayout(t *testing.T) {
	t.Setenv("KITE_CONFIG", "")
	if runtime.GOOS == "windows" {
		def, err := agentsvc.DefaultLayout("")
		if err != nil {
			t.Fatal(err)
		}
		if upgradeConfigPath("") != def.ConfigPath {
			t.Fatalf("default %s", upgradeConfigPath(""))
		}
		named, err := agentsvc.DefaultLayout("kite-agent-test3")
		if err != nil {
			t.Fatal(err)
		}
		if upgradeConfigPath("kite-agent-test3") != named.ConfigPath {
			t.Fatalf("named %s", upgradeConfigPath("kite-agent-test3"))
		}
		return
	}
	if got := upgradeConfigPath(""); got != "/etc/kite/agent.env" {
		t.Fatalf("default %s", got)
	}
	if got := upgradeConfigPath("kite-agent"); got != "/etc/kite/agent.env" {
		t.Fatalf("kite-agent %s", got)
	}
	if got := upgradeConfigPath("kite-agent-test3"); got != "/etc/kite/kite-agent-test3/agent.env" {
		t.Fatalf("named %s", got)
	}
}

func TestGuardReadsUnixInstanceFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix env layout")
	}
	root := t.TempDir()
	def := filepath.Join(root, "agent.env")
	namedDir := filepath.Join(root, "kite-agent-test3")
	if err := os.Mkdir(namedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(namedDir, "agent.env")
	body := "AGENT_TOKEN=kite_agent_file\nRELAY_ADDR=wss://relay.example/agent\n"
	if err := os.WriteFile(def, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(named, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("REGISTRATION_KEY", "from-the-shell")
	old := configPathForUpgrade
	t.Cleanup(func() { configPathForUpgrade = old })
	configPathForUpgrade = func(service string) string {
		if service == "" || service == "kite-agent" {
			return def
		}
		return filepath.Join(root, service, "agent.env")
	}
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{ServiceName: "kite-agent"}, "v0.1.0-rc.3"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("default instance: %v", err)
	}
	if err := guardTokenDowngrade(agentsvc.UpgradeOptions{ServiceName: "kite-agent-test3"}, "v0.1.0-rc.3"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("named instance: %v", err)
	}
	opt := agentsvc.UpgradeOptions{From: filepath.Join(root, "missing"), Version: "v0.1.0-rc.3", ServiceName: "kite-agent-test3"}
	if err := guardTokenDowngrade(opt, targetTag(opt)); err == nil {
		t.Fatal("--from --version did not consult the named instance")
	}
}

func TestTargetTagPrefersFlagThenBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "kite-agent")
	script := "#!/bin/sh\necho 'kite-agent v0.1.0-rc.3'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := targetTag(agentsvc.UpgradeOptions{From: bin, Version: "v0.1.0-rc.4"}); got != "v0.1.0-rc.4" {
		t.Fatalf("flag %s", got)
	}
	if got := targetTag(agentsvc.UpgradeOptions{From: bin}); got != "v0.1.0-rc.3" {
		t.Fatalf("binary %s", got)
	}
}
