package agentsvc

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/config"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		args []string
		cmd  Command
		rest []string
	}{
		{nil, CmdRun, nil},
		{[]string{"--relay", "ws://127.0.0.1/agent"}, CmdRun, []string{"--relay", "ws://127.0.0.1/agent"}},
		{[]string{"install", "--name", "pc"}, CmdInstall, []string{"--name", "pc"}},
		{[]string{"STATUS"}, CmdStatus, nil},
		{[]string{"help"}, CmdHelp, nil},
		{[]string{"--help"}, CmdHelp, nil},
	}
	for _, tc := range cases {
		cmd, rest, err := Split(tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if cmd != tc.cmd {
			t.Fatalf("%v: got %s", tc.args, cmd)
		}
		if strings.Join(rest, " ") != strings.Join(tc.rest, " ") {
			t.Fatalf("%v: rest %v", tc.args, rest)
		}
	}
	if _, _, err := Split([]string{"frob"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatal(err)
	}
}

func TestUsageMentionsServiceNotScheduledTask(t *testing.T) {
	text := Usage()
	for _, want := range []string{"kite-agent status", "kite-agent list", "kite-agent install", "--service-name", "kite-agent upgrade", `%ProgramData%\kite\config.yaml`, "console window"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(text), "schtasks /create") {
		t.Fatal(text)
	}
}

func TestParseOptions(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("RELAY_ADDR", "ws://127.0.0.1:8080")
	t.Setenv("REGISTRATION_KEY", "test-key-value")
	t.Setenv("AGENT_NAME", "my-pc")
	t.Setenv("LOCAL_SSH_ADDR", "")
	t.Setenv("IDLE_TIMEOUT", "")

	opt, err := ParseOptions(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Config.RelayAddr != "ws://127.0.0.1:8080/agent" || opt.Config.Name != "my-pc" {
		t.Fatalf("%+v", opt.Config)
	}
	if opt.Config.RegistrationKey != "test-key-value" {
		t.Fatal("key not loaded")
	}

	opt, err = ParseOptions([]string{"--registration-key", "flag-key-value", "--name", "from-flag", "--install-dir", `D:\kite`}, true)
	if err != nil {
		t.Fatal(err)
	}
	if opt.Config.Name != "from-flag" || opt.InstallDir != `D:\kite` || opt.Config.RegistrationKey != "flag-key-value" {
		t.Fatalf("%+v", opt)
	}

	if _, err := ParseOptions([]string{"--install-dir", `D:\kite`}, false); err == nil {
		t.Fatal("run should reject --install-dir")
	}
	t.Setenv("REGISTRATION_KEY", "CHANGE_ME")
	if _, err := ParseOptions(nil, false); err == nil {
		t.Fatal("placeholder key should be rejected")
	}
	t.Setenv("REGISTRATION_KEY", "test-key-value")
	t.Setenv("AGENT_NAME", "bad name")
	if _, err := ParseOptions(nil, false); err == nil {
		t.Fatal("whitespace name should be rejected")
	}

	opt, err = ParseOptions([]string{"--version"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !opt.ShowVersion {
		t.Fatal("expected version")
	}
	if _, err := ParseOptions([]string{"-h"}, false); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}

func TestHelpHidesRegistrationKey(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("RELAY_ADDR", "ws://127.0.0.1:8080/agent")
	t.Setenv("REGISTRATION_KEY", "test-key-value")
	t.Setenv("AGENT_NAME", "my-pc")

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	_, parseErr := ParseOptions([]string{"-h"}, true)
	w.Close()
	os.Stderr = old
	body, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(parseErr, flag.ErrHelp) {
		t.Fatal(parseErr)
	}
	if strings.Contains(string(body), "test-key-value") {
		t.Fatalf("help printed the registration key:\n%s", body)
	}
}

func TestWriteAgentFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := []byte("relay:\n  listen_addr: 127.0.0.1:9\nclient:\n  relay_addr: ws://127.0.0.1:9/client\nagent:\n  name: old\n")
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	key := "abc:def#ghi"
	err := WriteAgentFile(path, config.Agent{
		RelayAddr:       "wss://relay.example/agent",
		RegistrationKey: key,
		Name:            "my-pc",
		LocalSSHAddr:    "127.0.0.1:22",
		IdleTimeout:     90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{key, "127.0.0.1:9", "ws://127.0.0.1:9/client", "my-pc", "90s", "Do not commit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
		dst, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %o", dst.Mode().Perm())
		}
	}
	isolateConfigEnv(t)
	t.Setenv("REGISTRATION_KEY", "")
	t.Setenv("RELAY_ADDR", "")
	t.Setenv("AGENT_NAME", "")
	t.Setenv("LOCAL_SSH_ADDR", "")
	t.Setenv("IDLE_TIMEOUT", "")
	loaded, err := config.LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RegistrationKey != key || loaded.Name != "my-pc" || loaded.IdleTimeout != 90*time.Second {
		t.Fatalf("%+v", loaded)
	}
}

func TestParseServiceCommand(t *testing.T) {
	bin, cfg := parseServiceCommand(`"C:\Program Files\kite\kite-agent.exe" --config C:\ProgramData\kite\config.yaml`)
	if bin != `C:\Program Files\kite\kite-agent.exe` || cfg != `C:\ProgramData\kite\config.yaml` {
		t.Fatalf("%q %q", bin, cfg)
	}
	bin, cfg = parseServiceCommand(`"C:\Program Files\kite\kite-agent.exe" --config "D:\My Data\config.yaml"`)
	if bin != `C:\Program Files\kite\kite-agent.exe` || cfg != `D:\My Data\config.yaml` {
		t.Fatalf("%q %q", bin, cfg)
	}
	bin, cfg = parseServiceCommand(`C:\kite\kite-agent.exe --config=C:\kite\config.yaml`)
	if bin != `C:\kite\kite-agent.exe` || cfg != `C:\kite\config.yaml` {
		t.Fatalf("%q %q", bin, cfg)
	}
}

func TestFormatStatus(t *testing.T) {
	got := FormatStatus(StatusInfo{})
	if !strings.Contains(got, "Installed: no") || !strings.Contains(got, "kite-agent install") {
		t.Fatal(got)
	}
	if strings.Contains(got, "registration_key") {
		t.Fatal(got)
	}
	got = FormatStatus(StatusInfo{
		Installed: true,
		State:     "running",
		StartType: "automatic (delayed)",
		Account:   "LocalSystem",
		Binary:    `C:\Program Files\kite\kite-agent.exe`,
		Config:    `C:\ProgramData\kite\config.yaml`,
		Log:       `C:\ProgramData\kite\kite-agent.log`,
		PID:       42,
	})
	for _, want := range []string{"State: running", "automatic (delayed)", "LocalSystem", "PID: 42", "no console window", `config.yaml`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	got = FormatStatus(StatusInfo{Name: "kite-agent-test", Installed: true, State: "stopped"})
	if !strings.Contains(got, "kite-agent start --service-name kite-agent-test") {
		t.Fatalf("stopped hint missing service name:\n%s", got)
	}
	if strings.Contains(got, "kite-agent start\n") {
		t.Fatalf("stopped hint omitted --service-name:\n%s", got)
	}
}

func TestRejectPlaceholderAndProfile(t *testing.T) {
	if err := rejectPlaceholderRelay("wss://tunnel.example.com/agent"); err == nil {
		t.Fatal("expected placeholder rejection")
	}
	if err := rejectPlaceholderRelay("wss://relay.example/agent"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if !pathInUserProfile(filepath.Join(home, ".config", "kite", "config.yaml")) {
		t.Fatal("expected profile path")
	}
	if pathInUserProfile(filepath.Join(t.TempDir(), "config.yaml")) {
		t.Fatal("temp path should not count as the profile")
	}
	if pathInUserProfile("") {
		t.Fatal("empty path")
	}
}

func TestControlPlaneOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service control is implemented on Windows")
	}
	in, err := RunningAsService()
	if err != nil || in {
		t.Fatalf("in=%v err=%v", in, err)
	}
	for name, err := range map[string]error{
		"install":   Install(Options{}),
		"uninstall": Uninstall(""),
		"start":     Start(""),
		"stop":      Stop(""),
		"restart":   Restart(""),
		"service":   Serve("", nil),
		"upgrade":   Upgrade(UpgradeOptions{From: "kite-agent.exe"}),
	} {
		if err == nil || !strings.Contains(err.Error(), "Windows") {
			t.Fatalf("%s: %v", name, err)
		}
		low := strings.ToLower(err.Error())
		if !strings.Contains(low, "systemctl") || !strings.Contains(low, "launchctl") {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(low, "scheduled task") {
			t.Fatalf("%s should not send operators back to a scheduled task: %v", name, err)
		}
	}
	text, err := Status("")
	if err == nil || text != "" || !strings.Contains(err.Error(), "status") {
		t.Fatalf("%q %v", text, err)
	}
}

func TestOpenLogAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kite-agent.log")
	f, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("one\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("two\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "one\ntwo\n" {
		t.Fatalf("%q", b)
	}
}

func TestInstanceLayout(t *testing.T) {
	def, err := InstanceLayout("", `C:\ProgramData`, `C:\Program Files`)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != ServiceName {
		t.Fatal(def.Name)
	}
	if def.ConfigPath != filepath.Join(`C:\ProgramData`, "kite", "config.yaml") || def.LogPath != filepath.Join(`C:\ProgramData`, "kite", "kite-agent.log") {
		t.Fatal(def)
	}
	if def.InstallDir != filepath.Join(`C:\Program Files`, "kite") {
		t.Fatal(def.InstallDir)
	}
	testInst, err := InstanceLayout("kite-agent-test", `C:\ProgramData`, `C:\Program Files`)
	if err != nil {
		t.Fatal(err)
	}
	if testInst.ConfigPath != filepath.Join(`C:\ProgramData`, "kite", "kite-agent-test", "config.yaml") {
		t.Fatal(testInst.ConfigPath)
	}
	if testInst.LogPath != filepath.Join(`C:\ProgramData`, "kite", "kite-agent-test", "kite-agent.log") {
		t.Fatal(testInst.LogPath)
	}
	if testInst.InstallDir != filepath.Join(`C:\Program Files`, "kite", "kite-agent-test") {
		t.Fatal(testInst.InstallDir)
	}
	if testInst.InstallDir == def.InstallDir || testInst.ConfigPath == def.ConfigPath {
		t.Fatal("test instance overwrites the default files")
	}
	if _, err := InstanceLayout(`kite\agent`, `C:\ProgramData`, `C:\Program Files`); err == nil {
		t.Fatal("path separator should be rejected")
	}
	if CleansLegacyTask("kite-agent-test") {
		t.Fatal("test install must not remove the legacy task")
	}
	if !CleansLegacyTask(ServiceName) {
		t.Fatal("default install cleans the legacy task")
	}
	defCfg := filepath.Join(`C:\ProgramData`, "kite", "config.yaml")
	if strings.Join(ServiceArgs(defCfg, ServiceName), " ") != "--config "+defCfg {
		t.Fatal(ServiceArgs(defCfg, ServiceName))
	}
	got := ServiceArgs(testInst.ConfigPath, testInst.Name)
	if len(got) != 4 || got[2] != "--service-name" || got[3] != "kite-agent-test" {
		t.Fatal(got)
	}
}

func TestUpgradeCommand(t *testing.T) {
	if _, err := ParseUpgrade(nil); err == nil {
		t.Fatal("expected --from")
	}
	opt, err := ParseUpgrade([]string{"--from", `D:\new\kite-agent.exe`, "--service-name", "kite-agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Apply || opt.ServiceName != "kite-agent-test" || opt.From != `D:\new\kite-agent.exe` {
		t.Fatalf("%+v", opt)
	}
	task := UpgradeTaskName(opt.ServiceName)
	if task == ServiceName || !strings.Contains(task, "kite-agent-test") {
		t.Fatal(task)
	}
	xmlText, err := UpgradeTaskXML(`D:\new\kite-agent.exe`, opt.From, opt.ServiceName)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"S-1-5-18", "HighestAvailable", "AllowStartOnDemand", "upgrade --apply", "kite-agent-test", `D:\new\kite-agent.exe`} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("xml missing %q\n%s", want, xmlText)
		}
	}
	if err := validateUpgradeTaskXML(xmlText); err != nil {
		t.Fatal(err)
	}
	// The path from the Windows 11 self-test. schtasks rejected the previous
	// document at LogonType:ServiceAccount.
	real, err := UpgradeTaskXML(`C:\Users\98596\kite-test\kite-agent-upgrade.exe`, `C:\Users\98596\kite-test\kite-agent-upgrade.exe`, "kite-agent-test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(real, "LogonType") || strings.Contains(real, "ServiceAccount") {
		t.Fatalf("rejected principal leaked into xml:\n%s", real)
	}
	if strings.Contains(xmlText, "REGISTRATION_KEY") || strings.Contains(xmlText, "registration_key") {
		t.Fatal(xmlText)
	}
	quoted := quoteWindowsArg(`C:\Program Files\kite\kite-agent.exe`)
	if quoted != `"C:\Program Files\kite\kite-agent.exe"` {
		t.Fatal(quoted)
	}
	lay, err := InstanceLayout(ServiceName, `C:\ProgramData`, `C:\Program Files`)
	if err != nil {
		t.Fatal(err)
	}
	if UpgradeResultFile(lay) != filepath.Join(`C:\ProgramData`, "kite", "upgrade.log") {
		t.Fatal(UpgradeResultFile(lay))
	}
}

func TestListFormatting(t *testing.T) {
	if !IsKiteAgentService("kite-agent") || !IsKiteAgentService("Kite-Agent-Test") {
		t.Fatal("expected kite-agent names")
	}
	for _, name := range []string{"kite", "kite-agentfoo", "kite-upgrade-kite-agent", "sshd"} {
		if IsKiteAgentService(name) {
			t.Fatalf("should not list %s", name)
		}
	}
	got := FormatList(nil)
	if got != "No kite-agent services are installed.\n" {
		t.Fatal(got)
	}
	got = FormatList([]ListedService{
		{Name: "kite-agent-test", AgentName: "my-pc-test", State: "stopped", StartType: "automatic (delayed)"},
		{Name: "kite-agent", AgentName: "my-pc", State: "running", StartType: "automatic (delayed)"},
	})
	want := "SERVICE\tAGENT\tSTATE\tSTART\n" +
		"kite-agent\tmy-pc\trunning\tautomatic (delayed)\n" +
		"kite-agent-test\tmy-pc-test\tstopped\tautomatic (delayed)\n"
	if got != want {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "registration") {
		t.Fatal(got)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("agent:\n  name: my-pc\n  registration_key: test-key-value\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if agentNameFromConfig(path) != "my-pc" {
		t.Fatal(agentNameFromConfig(path))
	}
	if agentNameFromConfig(filepath.Join(dir, "missing.yaml")) != "-" {
		t.Fatal("missing config")
	}
}

func TestFormatLegacyLogonTask(t *testing.T) {
	if FormatLegacyLogonTask("") != "" {
		t.Fatal("empty status")
	}
	got := FormatLegacyLogonTask("Running")
	if got != "legacy logon task kite-agent: Running\n" {
		t.Fatal(got)
	}
}

func TestListUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("list is implemented on Windows")
	}
	text, err := List()
	if err == nil || text != "" {
		t.Fatalf("%q %v", text, err)
	}
	if !strings.Contains(err.Error(), "not supported") || !strings.Contains(err.Error(), "kite-agent-*") {
		t.Fatal(err)
	}
}

func TestParseControlServiceName(t *testing.T) {
	name, err := ParseControl("status", []string{"--service-name", "kite-agent-test"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "kite-agent-test" {
		t.Fatal(name)
	}
	name, err = ParseControl("stop", nil)
	if err != nil || name != ServiceName {
		t.Fatalf("%s %v", name, err)
	}
	if _, err := ParseControl("start", []string{"--service-name", `..\kite-agent`}); err == nil {
		t.Fatal("expected rejection")
	}
}

func isolateConfigEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("ProgramData", t.TempDir())
}
