package agentsvc

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/protocol"
)

const (
	// ServiceName is the Windows service name and the systemd unit base name.
	ServiceName = "kite-agent"
	logFileName = "kite-agent.log"
)

// Command is a kite-agent subcommand. An empty argument list means run.
type Command string

const (
	CmdRun       Command = "run"
	CmdInstall   Command = "install"
	CmdUninstall Command = "uninstall"
	CmdStart     Command = "start"
	CmdStop      Command = "stop"
	CmdRestart   Command = "restart"
	CmdStatus    Command = "status"
	CmdHelp      Command = "help"
)

// Options is the parsed agent configuration for run and install.
type Options struct {
	Config      config.Agent
	ConfigPath  string // explicit --config, or empty when discovery/env was used
	InstallDir  string
	ShowVersion bool
}

// StatusInfo is a service query result. Zero means not installed.
type StatusInfo struct {
	Installed bool
	State     string
	StartType string
	Account   string
	Binary    string
	Config    string
	Log       string
	PID       uint32
	ExitCode  uint32
}

// Split separates a leading subcommand from the flags that follow it.
// Flags without a subcommand select foreground run, which is the historical
// kite-agent invocation.
func Split(args []string) (Command, []string, error) {
	if len(args) == 0 {
		return CmdRun, nil, nil
	}
	switch strings.ToLower(args[0]) {
	case "run", "install", "uninstall", "start", "stop", "restart", "status":
		return Command(strings.ToLower(args[0])), args[1:], nil
	case "help", "-h", "--help":
		return CmdHelp, args[1:], nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return CmdRun, args, nil
		}
		return "", nil, fmt.Errorf("unknown command %q\n\n%s", args[0], Usage())
	}
}

// Usage is the top-level help text.
func Usage() string {
	return `Usage:
  kite-agent [flags]
  kite-agent run [flags]
  kite-agent install [flags]
  kite-agent uninstall
  kite-agent start
  kite-agent stop
  kite-agent restart
  kite-agent status

kite-agent dials a kite relay and forwards streams only to the local SSH
server. With no command it runs in the foreground.

On Windows, install registers service "kite-agent". The service starts at
boot (automatic, delayed) without a logged-on user and does not open a
console window:

  kite-agent install
  kite-agent status
  kite-agent stop
  kite-agent start
  kite-agent restart
  kite-agent uninstall

install, start, stop, restart, and uninstall need an elevated Administrator
prompt. status does not. install writes the registration key to
%ProgramData%\kite\config.yaml (or to --config) and records only that path
on the service command line. Environment variables and flags are saved into
the file because the service account does not see your shell environment.
Do not commit the file.

install also removes a legacy logon scheduled task named kite-agent when one
exists, so an older setup stops opening a console window at login.

On Linux use deploy/kite-agent.service with systemctl. On macOS use
deploy/com.kite.agent.plist with launchctl.

Run "kite-agent run -h" or "kite-agent install -h" for flags.
`
}

// ParseOptions loads agent settings from the config file, the environment,
// and flags. The key and agent name are validated unless --version was set.
func ParseOptions(args []string, install bool) (Options, error) {
	cfg, err := config.LoadAgent(config.PeekConfigFlag(args))
	if err != nil {
		return Options{}, err
	}
	name := "kite-agent"
	if install {
		name = "kite-agent install"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	configPath := config.PeekConfigFlag(args)
	fs.StringVar(&configPath, "config", configPath, "YAML config file (or KITE_CONFIG). Keep it gitignored; do not commit secrets")
	fs.StringVar(&cfg.RelayAddr, "relay", cfg.RelayAddr, "relay WebSocket URL (env RELAY_ADDR), for example wss://tunnel.example.com/agent")
	// The key is not the flag default. flag prints defaults in help, and the
	// registration key must not show up there.
	var keyFlag string
	fs.StringVar(&keyFlag, "registration-key", "", "shared registration key (env REGISTRATION_KEY)")
	fs.StringVar(&cfg.Name, "name", cfg.Name, "agent name (env AGENT_NAME); must match the ssh Host alias")
	fs.StringVar(&cfg.LocalSSHAddr, "local", cfg.LocalSSHAddr, "local SSH address to dial (env LOCAL_SSH_ADDR). The relay cannot change this")
	idle := fs.Duration("idle-timeout", cfg.IdleTimeout, "reconnect after this long with no received frames (env IDLE_TIMEOUT)")
	var installDir string
	if install {
		fs.StringVar(&installDir, "install-dir", "", `directory for kite-agent.exe (default %ProgramFiles%\kite)`)
	}
	fs.Usage = func() {
		if install {
			fmt.Fprintf(fs.Output(), "Usage: kite-agent install [flags]\n\n")
			fmt.Fprintf(fs.Output(), "Register the Windows service and start it. Requires an elevated\n")
			fmt.Fprintf(fs.Output(), "Administrator prompt. The registration key is written to\n")
			fmt.Fprintf(fs.Output(), "%%ProgramData%%\\kite\\config.yaml (or --config) and is not placed\n")
			fmt.Fprintf(fs.Output(), "on the service command line. Do not commit that file.\n\n")
		} else {
			fmt.Fprintf(fs.Output(), "Usage: kite-agent [flags]\n\n")
			fmt.Fprintf(fs.Output(), "Dials the relay and forwards incoming streams only to LOCAL_SSH_ADDR\n")
			fmt.Fprintf(fs.Output(), "(default 127.0.0.1:22). Set REGISTRATION_KEY and RELAY_ADDR via the environment\n")
			fmt.Fprintf(fs.Output(), "or a gitignored config file. Never commit them.\n\n")
			fmt.Fprintf(fs.Output(), "On Windows, \"kite-agent install\" runs this process as a service with no console window.\n")
			fmt.Fprintf(fs.Output(), "Stop that service before a foreground debug run, or the relay keeps only one connection.\n\n")
		}
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if *showVersion {
		return Options{ShowVersion: true}, nil
	}
	if fs.NArg() != 0 {
		return Options{}, errors.New("unexpected arguments")
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "registration-key" {
			cfg.RegistrationKey = keyFlag
		}
	})
	cfg.IdleTimeout = *idle
	if err := config.ValidateKey(cfg.RegistrationKey); err != nil {
		return Options{}, err
	}
	relay, err := config.NormalizeRelayURL(cfg.RelayAddr, "/agent")
	if err != nil {
		return Options{}, err
	}
	cfg.RelayAddr = relay
	if !protocol.ValidName(cfg.Name) {
		return Options{}, errors.New("AGENT_NAME is required and must not contain whitespace")
	}
	return Options{Config: cfg, ConfigPath: configPath, InstallDir: installDir}, nil
}

// FormatStatus renders a service query for the terminal.
func FormatStatus(s StatusInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Service: %s\n", ServiceName)
	if !s.Installed {
		fmt.Fprintf(&b, "Installed: no\n")
		fmt.Fprintf(&b, "Install it from an elevated prompt: kite-agent install\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Installed: yes\n")
	if s.State != "" {
		fmt.Fprintf(&b, "State: %s\n", s.State)
	}
	if s.StartType != "" {
		fmt.Fprintf(&b, "Start type: %s\n", s.StartType)
	}
	if s.Account != "" {
		fmt.Fprintf(&b, "Account: %s\n", s.Account)
	}
	if s.PID != 0 {
		fmt.Fprintf(&b, "PID: %d\n", s.PID)
	}
	if s.ExitCode != 0 {
		fmt.Fprintf(&b, "Exit code: %d\n", s.ExitCode)
	}
	if s.Binary != "" {
		fmt.Fprintf(&b, "Binary: %s\n", s.Binary)
	}
	if s.Config != "" {
		fmt.Fprintf(&b, "Config: %s\n", s.Config)
	}
	if s.Log != "" {
		fmt.Fprintf(&b, "Log: %s\n", s.Log)
	}
	switch s.State {
	case "running":
		fmt.Fprintf(&b, "The service is running in the background with no console window.\n")
	case "stopped":
		fmt.Fprintf(&b, "Start it from an elevated prompt: kite-agent start\n")
	}
	return b.String()
}

// DefaultLogPath is the service log next to the default Windows config file.
func DefaultLogPath() string {
	cfg := config.WindowsMachineConfig()
	if cfg == "" {
		return logFileName
	}
	return filepath.Join(filepath.Dir(cfg), logFileName)
}

// LogPath is the log file next to an explicit config, or DefaultLogPath.
func LogPath(configFlag string) string {
	configFlag = strings.TrimSpace(configFlag)
	if configFlag == "" {
		return DefaultLogPath()
	}
	return filepath.Join(filepath.Dir(configFlag), logFileName)
}

// WriteAgentFile stores the resolved agent settings at path. An existing file
// keeps its relay and client sections. The registration key is inside the
// file; callers must not put it on a service command line.
func WriteAgentFile(path string, agent config.Agent) error {
	root := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse existing config %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	root["agent"] = map[string]any{
		"relay_addr":       agent.RelayAddr,
		"registration_key": agent.RegistrationKey,
		"name":             agent.Name,
		"local_ssh_addr":   agent.LocalSSHAddr,
		"idle_timeout":     formatDuration(agent.IdleTimeout),
	}
	var buf bytes.Buffer
	buf.WriteString("# kite service configuration. This file contains the registration key.\n")
	buf.WriteString("# Do not commit it. The Windows service reads it as LocalSystem.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writePrivateFile(path, buf.Bytes())
}

func formatDuration(d time.Duration) string {
	if d > 0 && d%time.Second == 0 && d < time.Hour {
		return fmt.Sprintf("%ds", int64(d/time.Second))
	}
	if d <= 0 {
		return ""
	}
	return d.String()
}

func rejectPlaceholderRelay(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if strings.EqualFold(u.Hostname(), "tunnel.example.com") {
		return errors.New("RELAY_ADDR still uses the placeholder host tunnel.example.com; set your relay URL with --relay, RELAY_ADDR, or the config file")
	}
	return nil
}

func pathInUserProfile(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	path = filepath.Clean(path)
	home = filepath.Clean(home)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		home = strings.ToLower(home)
	}
	if path == home {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, home+sep)
}

// parseServiceCommand splits the service ImagePath into the executable and
// the --config value. It understands the quoted form Windows stores for
// paths that contain spaces.
func parseServiceCommand(line string) (binary, configPath string) {
	args := splitCommandLine(line)
	if len(args) == 0 {
		return strings.TrimSpace(line), ""
	}
	binary = args[0]
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--config" || a == "-config" {
			if i+1 < len(args) {
				configPath = args[i+1]
			}
			return binary, configPath
		}
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			return binary, v
		}
		if v, ok := strings.CutPrefix(a, "-config="); ok {
			return binary, v
		}
	}
	return binary, ""
}

// splitCommandLine splits a Windows service ImagePath. It is not a full
// CommandLineToArgvW implementation; it matches the quoted command line
// kite-agent install registers (paths may contain spaces, not trailing
// backslashes or embedded quotes).
func splitCommandLine(s string) []string {
	var args []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			inQuote = !inQuote
		case '\\':
			if i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			b.WriteByte(c)
		case ' ', '\t':
			if inQuote {
				b.WriteByte(c)
				continue
			}
			if b.Len() > 0 {
				args = append(args, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	return args
}

func errWindowsOnly(action string) error {
	return fmt.Errorf("kite-agent %s controls the Windows service. On Linux, copy deploy/kite-agent.service to /etc/systemd/system and run systemctl enable --now kite-agent. On macOS, copy deploy/com.kite.agent.plist to ~/Library/LaunchAgents and load it with launchctl. Run kite-agent with no command to stay in the foreground", action)
}
