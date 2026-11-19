package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/agentctl"
	"github.com/zzzbug01/kite/internal/agentsvc"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/protocol"
)

func TestNamedPairIgnoresEnvRelay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.env")
	body := "RELAY_ADDR=wss://instance.example/agent\nAGENT_TOKEN=kite_agent_file\nAGENT_NAME=box\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_ADDR", "wss://old.example/client")
	t.Setenv("REGISTRATION_KEY", "env-key")
	cfg, err := config.LoadAgent("")
	if err != nil {
		t.Fatal(err)
	}
	cfg = preferInstanceFile(cfg, path)
	if cfg.RelayAddr != "wss://instance.example/agent" {
		t.Fatalf("relay %q", cfg.RelayAddr)
	}
	secret, kind, err := cfg.Credential()
	if err != nil {
		t.Fatal(err)
	}
	if kind != "agent" || secret != "kite_agent_file" {
		t.Fatalf("%s %s", kind, secret)
	}
	if cfg.Name != "box" {
		t.Fatal(cfg.Name)
	}
}

func TestTryControlPairWaitsForListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	path := filepath.Join(t.TempDir(), "agent-control.json")
	raw, err := json.Marshal(agentctl.Endpoint{Addr: ln.Addr().String(), Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_ = json.NewEncoder(conn).Encode(agentctl.Response{OK: true, Code: "ABCD-EFGH", Agent: "box"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, _, last, perr := tryControlPair(ctx, []string{path}, path, agentctl.Request{Op: "pair"}, 2*time.Second)
	if last != nil || perr != nil || resp.Code != "ABCD-EFGH" {
		t.Fatalf("resp %+v last %v primary %v", resp, last, perr)
	}
}

func TestInstanceBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "kite-agent")
	script := "#!/bin/sh\necho 'kite-agent v0.1.0-rc.5'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := lookupInstanceBinary
	t.Cleanup(func() { lookupInstanceBinary = old })
	lookupInstanceBinary = func(string) string { return bin }
	if got := instanceBinaryVersion("kite-agent-test3"); got != "v0.1.0-rc.5" {
		t.Fatalf("version %q", got)
	}
}

func TestFillPairNameUsesHostname(t *testing.T) {
	got, err := fillPairName("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("empty name")
	}
	if err := protocol.CheckAgentName(got); err != nil {
		t.Fatal(err)
	}
	if got, err := fillPairName("desktop"); err != nil || got != "desktop" {
		t.Fatalf("%q %v", got, err)
	}
}

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
