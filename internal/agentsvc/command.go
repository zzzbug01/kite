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
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/agentctl"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/sshheal"
)

const (
	// ServiceName is the default Windows service name. Existing installs keep
	// this name and the original ProgramData and Program Files paths.
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
	CmdList      Command = "list"
	CmdUpgrade   Command = "upgrade"
	CmdSetup     Command = "setup"
	CmdPair      Command = "pair"
	CmdUnpair    Command = "unpair"
	CmdHelp      Command = "help"
)

// Options is the parsed agent configuration for run and install.
type Options struct {
	Config       config.Agent
	ConfigPath   string // explicit --config, or empty when discovery/env was used
	InstallDir   string
	ServiceName  string
	ShowVersion  bool
	SkipSSHSetup bool
	// Code is a one-time install code from kite-relay invite. It is redeemed
	// by the install command, not while flags are parsed.
	Code string
}

// UpgradeOptions selects the binary swap for one service instance.
// From empty means download the release. Apply is the Windows scheduled-task worker.
type UpgradeOptions struct {
	ServiceName string
	From        string
	Apply       bool
	Version     string
	Check       bool
}

// StatusInfo is a service query result. Zero means not installed.
type StatusInfo struct {
	Name      string
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
	case "run", "install", "uninstall", "start", "stop", "restart", "status", "list", "upgrade", "setup", "pair", "unpair":
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
  kite-agent setup
  kite-agent install --relay HOST --code ABCD-EFGH
  kite-agent install [flags]
  kite-agent pair
  kite-agent unpair --list
  kite-agent unpair --revoke <label>
  kite-agent uninstall
  kite-agent start
  kite-agent stop
  kite-agent restart
  kite-agent status
  kite-agent list
  kite-agent upgrade [--version vX] [--check]
  kite-agent upgrade --from <new-binary>

kite-agent dials a kite relay and forwards streams only to the local SSH
server. With no command it runs in the foreground.

setup checks the local SSH server. install runs setup, then registers the
service. On Windows that is the Service Control Manager. On Linux and macOS,
install as root writes the systemd unit or launchd daemon. --relay may be a
bare host (wss and /agent are filled in). --code redeems a one-time install
code from kite-relay invite and stores an agent token instead of
REGISTRATION_KEY. AGENT_NAME defaults to the hostname. SSH_USER defaults to
the invoking user. pair prints a one-time code; on the other computer run
"kite connect CODE". An agent installed with --code can pair with its token.

pair talks to the running agent on 127.0.0.1 when that control file is
readable, so the installing user does not need a further elevation prompt.
Otherwise it uses the registration key or agent token from the environment
or config, which means the agent must already be connected. The pairing
code is not the registration key.

On Windows, install registers service "kite-agent". The service starts at
boot (automatic, delayed) without a logged-on user and does not open a
console window:

  kite-agent install
  kite-agent status
  kite-agent list
  kite-agent stop
  kite-agent start
  kite-agent restart
  kite-agent uninstall
  kite-agent upgrade
  kite-agent upgrade --from C:\path\to\new\kite-agent.exe

Pass --service-name to install a second instance beside the default one.
The default name keeps %ProgramData%\kite\config.yaml and
%ProgramFiles%\kite\kite-agent.exe. Any other name uses
%ProgramData%\kite\<service-name>\ and %ProgramFiles%\kite\<service-name>\.
status, start, stop, restart, uninstall, and upgrade accept the same flag
and touch only that instance.

install, start, stop, restart, uninstall, and upgrade need an elevated
Administrator prompt. status and list do not. list prints every service
named kite-agent or kite-agent-*, one tab-separated line each: service
name, agent name, state, start type. install writes the registration key
or agent token to the instance config file and records only that path on
the service command line. Environment variables and flags are saved into the file
because the service account does not see your shell environment.
Do not commit the file.

install removes a legacy logon scheduled task named kite-agent only when
installing the default service, so a test instance cannot remove the stable
setup. upgrade schedules a one-shot SYSTEM task that stops the service,
replaces the exe, and starts it again. The SSH session can drop when the
tunnel restarts; the task still finishes.

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
	fs.StringVar(&cfg.RelayAddr, "relay", cfg.RelayAddr, "relay address (env RELAY_ADDR): host, host:port, https://, or wss://tunnel.example.com/agent")
	// The key is not the flag default. flag prints defaults in help, and the
	// registration key must not show up there.
	var keyFlag string
	fs.StringVar(&keyFlag, "registration-key", "", "shared registration key (env REGISTRATION_KEY)")
	fs.StringVar(&cfg.Name, "name", cfg.Name, "agent name (env AGENT_NAME); must match the ssh Host alias")
	fs.StringVar(&cfg.LocalSSHAddr, "local", cfg.LocalSSHAddr, "local SSH address to dial (env LOCAL_SSH_ADDR). The relay cannot change this")
	idle := fs.Duration("idle-timeout", cfg.IdleTimeout, "reconnect after this long with no received frames (env IDLE_TIMEOUT)")
	var installDir string
	var serviceName string
	var skipSSH bool
	var code string
	fs.StringVar(&serviceName, "service-name", "", "instance name (default kite-agent). Another name gets its own program, config, and state")
	fs.StringVar(&cfg.SSHUser, "ssh-user", cfg.SSHUser, "local account that receives paired SSH keys (env SSH_USER); default is the invoking user")
	if install {
		fs.StringVar(&installDir, "install-dir", "", `directory for kite-agent.exe (default %ProgramFiles%\kite, or %ProgramFiles%\kite\<service-name>)`)
		fs.BoolVar(&skipSSH, "skip-ssh-setup", false, "do not install or start the local SSH server")
		fs.StringVar(&code, "code", "", "one-time install code from kite-relay invite (replaces REGISTRATION_KEY)")
	}
	fs.Usage = func() {
		if install {
			fmt.Fprintf(fs.Output(), "Usage: kite-agent install [flags]\n\n")
			switch runtime.GOOS {
			case "windows":
				fmt.Fprintf(fs.Output(), "Register the Windows service and start it. Requires an elevated\n")
				fmt.Fprintf(fs.Output(), "Administrator prompt. Pass --relay and --code from kite-relay invite,\n")
				fmt.Fprintf(fs.Output(), "or set REGISTRATION_KEY. The secret is written to\n")
				fmt.Fprintf(fs.Output(), "%%ProgramData%%\\kite\\config.yaml (or --config) and is not placed\n")
				fmt.Fprintf(fs.Output(), "on the service command line. Do not commit that file.\n")
				fmt.Fprintf(fs.Output(), "--service-name installs a second instance and does not replace kite-agent.\n\n")
			case "darwin":
				fmt.Fprintf(fs.Output(), "Install the launchd daemon and start it. Requires root.\n")
				fmt.Fprintf(fs.Output(), "The default binary is /usr/local/bin/kite-agent and the env file is /etc/kite/agent.env.\n")
				fmt.Fprintf(fs.Output(), "--service-name installs another instance at /usr/local/lib/kite/<name>/kite-agent\n")
				fmt.Fprintf(fs.Output(), "with /etc/kite/<name>/agent.env and state in /var/lib/kite-agent/<name>.\n")
				fmt.Fprintf(fs.Output(), "An auto-detected address is stored as LOCAL_SSH_ADDR_AUTO=1 so the agent can follow sshd.\n\n")
			default:
				fmt.Fprintf(fs.Output(), "Install the systemd unit and start it. Requires root.\n")
				fmt.Fprintf(fs.Output(), "The default binary is /usr/local/bin/kite-agent and the env file is /etc/kite/agent.env.\n")
				fmt.Fprintf(fs.Output(), "--service-name installs another instance at /usr/local/lib/kite/<name>/kite-agent\n")
				fmt.Fprintf(fs.Output(), "with its own unit, /etc/kite/<name>/agent.env, and state in /var/lib/kite-agent/<name>.\n")
				fmt.Fprintf(fs.Output(), "An auto-detected address is stored as LOCAL_SSH_ADDR_AUTO=1 so the agent can follow sshd.\n\n")
			}
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
		if f.Name == "local" {
			cfg.LocalExplicit = true
			cfg.LocalSSHAuto = false
		}
	})
	cfg.IdleTimeout = *idle
	canon, err := CanonicalServiceName(serviceName)
	if err != nil {
		return Options{}, err
	}
	code = strings.TrimSpace(code)
	if cfg.RegistrationKey == config.PlaceholderKey && (code != "" || strings.TrimSpace(cfg.AgentToken) != "") {
		cfg.RegistrationKey = ""
	}
	relay, err := config.NormalizeRelayURL(cfg.RelayAddr, "/agent")
	if err != nil {
		return Options{}, err
	}
	cfg.RelayAddr = relay
	if strings.TrimSpace(cfg.Name) == "" {
		name, err := agent.DefaultAgentName()
		if err != nil {
			return Options{}, err
		}
		cfg.Name = name
	}
	if err := protocol.CheckAgentName(cfg.Name); err != nil {
		return Options{}, err
	}
	if cfg.SSHUser == "" || agent.IsMachineAccount(cfg.SSHUser) {
		if u := agent.InvokingUser(); u != "" {
			cfg.SSHUser = u
		}
	}
	if code == "" {
		if _, _, err := cfg.Credential(); err != nil {
			return Options{}, err
		}
	}
	if strings.TrimSpace(serviceName) == "" {
		if n, ok := InstanceFromConfig(configPath); ok {
			canon = n
		}
	}
	return Options{Config: cfg, ConfigPath: configPath, InstallDir: installDir, ServiceName: canon, SkipSSHSetup: skipSSH, Code: code}, nil
}

// InstanceFromConfig reports the instance whose install path this is.
// A path that is not one of those layouts returns ok false.
func InstanceFromConfig(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	if runtime.GOOS != "windows" {
		return agentctl.InstanceOfEnv(path)
	}
	clean := filepath.Clean(path)
	if def, err := DefaultLayout(ServiceName); err == nil && pathsEqual(clean, def.ConfigPath) {
		return ServiceName, true
	}
	if !strings.EqualFold(filepath.Base(clean), "config.yaml") {
		return "", false
	}
	name := filepath.Base(filepath.Dir(clean))
	canon, err := CanonicalServiceName(name)
	if err != nil || canon == ServiceName {
		return "", false
	}
	lay, err := DefaultLayout(canon)
	if err != nil || !pathsEqual(clean, lay.ConfigPath) {
		return "", false
	}
	return canon, true
}

func pathsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// StatusHealthPaths are the local-SSH health files for this status request,
// most specific first. --config selects its instance and does not fall through
// to a different instance's file.
func StatusHealthPaths(serviceName, configPath string) []string {
	name := serviceName
	if name == "" {
		name = ServiceName
	}
	if strings.TrimSpace(configPath) != "" {
		if resolved, ok := InstanceFromConfig(configPath); ok {
			name = resolved
		} else {
			return []string{sshheal.File(configPath)}
		}
	}
	if runtime.GOOS == "windows" {
		lay, err := DefaultLayout(name)
		if err != nil {
			return nil
		}
		return []string{sshheal.File(lay.ConfigPath)}
	}
	path := agentctl.UnixHealthPath(name)
	if name == ServiceName {
		return []string{path, "/var/lib/kite/ssh-health.json"}
	}
	return []string{path}
}

// controlHelp is the text for one service-control subcommand. Each command
// describes itself. status is the only one that prints local SSH health.
func controlHelp(command string) string {
	return controlHelpFor(command, runtime.GOOS)
}

func controlHelpFor(command, goos string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: kite-agent %s [--service-name name]", command)
	if command == "status" {
		b.WriteString(" [--config path]")
	}
	b.WriteString("\n\n")
	switch command {
	case "status":
		if goos == "windows" {
			b.WriteString("status shows the Windows service state and this instance's local SSH health.\n")
			b.WriteString("--config selects that instance's files when the path is the one install wrote.\n\n")
		} else {
			b.WriteString("status shows this instance's local SSH health. --config /etc/kite/<name>/agent.env\n")
			b.WriteString("selects that instance, not the default. Health is stored in\n")
			b.WriteString("/var/lib/kite-agent or /var/lib/kite-agent/<name>.\n\n")
		}
	case "start", "stop", "restart", "uninstall":
		writeControlAction(&b, command, goos)
	default:
		fmt.Fprintf(&b, "%s acts on one instance.\n\n", command)
	}
	return b.String()
}

func writeControlAction(b *strings.Builder, command, goos string) {
	switch goos {
	case "windows":
		fmt.Fprintf(b, "%s the Windows service for this instance. --service-name selects\n", command)
		b.WriteString("kite-agent or a side-by-side instance and does not touch the others.\n\n")
	case "darwin":
		fmt.Fprintf(b, "%s this instance with launchctl. The default label is com.kite.agent;\n", command)
		b.WriteString("a named instance is com.kite.<name>.\n")
		switch command {
		case "start":
			b.WriteString("  sudo launchctl bootstrap system /Library/LaunchDaemons/com.kite.agent.plist\n")
		case "stop":
			b.WriteString("  sudo launchctl bootout system/com.kite.agent\n")
		case "restart":
			b.WriteString("  sudo launchctl kickstart -k system/com.kite.agent\n")
		default:
			b.WriteString("  sudo launchctl bootout system/com.kite.agent\n")
			b.WriteString("  sudo rm /Library/LaunchDaemons/com.kite.agent.plist\n")
		}
		fmt.Fprintf(b, "kite-agent %s controls the service when run on Windows.\n\n", command)
	default:
		fmt.Fprintf(b, "%s this instance with systemctl. The default unit is kite-agent.\n", command)
		switch command {
		case "uninstall":
			b.WriteString("  sudo systemctl disable --now kite-agent\n")
			b.WriteString("  sudo rm /etc/systemd/system/kite-agent.service\n")
			b.WriteString("A named instance uses /etc/systemd/system/<name>.service,\n")
			b.WriteString("/usr/local/lib/kite/<name>/kite-agent, /etc/kite/<name>/agent.env,\n")
			b.WriteString("and /var/lib/kite-agent/<name>.\n")
		default:
			fmt.Fprintf(b, "  sudo systemctl %s kite-agent\n", command)
			fmt.Fprintf(b, "  sudo systemctl %s <service-name>\n", command)
		}
		fmt.Fprintf(b, "kite-agent %s controls the service when run on Windows.\n\n", command)
	}
}

// ParseControl reads --service-name for status, start, stop, restart, and uninstall.
func ParseControl(command string, args []string) (string, error) {
	name, _, err := ParseControlOptions(command, args)
	return name, err
}

// ParseControlOptions also reads --config so status can find that instance's
// local-SSH health file. --config is optional.
func ParseControlOptions(command string, args []string) (name, configPath string, err error) {
	fs := flag.NewFlagSet("kite-agent "+command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var serviceName string
	configPath = config.PeekConfigFlag(args)
	fs.StringVar(&serviceName, "service-name", "", "instance to act on (default kite-agent)")
	fs.StringVar(&configPath, "config", configPath, "instance config path; status uses it to find local SSH health")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), controlHelp(command))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() != 0 {
		return "", "", errors.New("unexpected arguments")
	}
	name, err = CanonicalServiceName(serviceName)
	return name, configPath, err
}

func listHelp(goos string) string {
	var b strings.Builder
	b.WriteString("Usage: kite-agent list\n\n")
	switch goos {
	case "windows":
		b.WriteString("list prints Windows services named kite-agent and kite-agent-*.\n")
		b.WriteString("No elevation is required. Each line is tab-separated:\n")
		b.WriteString("service name, agent name, state, start type.\n\n")
	case "darwin":
		b.WriteString("list enumerates installed instances. On macOS, launchctl print system\n")
		b.WriteString("shows com.kite.agent and com.kite.<name>. kite-agent list itself\n")
		b.WriteString("enumerates Windows services.\n\n")
	default:
		b.WriteString("list enumerates installed instances. On Linux:\n")
		b.WriteString("  systemctl list-units 'kite-agent*'\n")
		b.WriteString("kite-agent list itself enumerates Windows services.\n\n")
	}
	return b.String()
}

func upgradeHelp(goos string) string {
	var b strings.Builder
	b.WriteString("Usage: kite-agent upgrade [--version vX] [--check] [--service-name name]\n")
	b.WriteString("       kite-agent upgrade --from <new-binary> [--service-name name]\n\n")
	b.WriteString("With no --from, upgrade downloads the matching GitHub release, checks\n")
	b.WriteString("SHA256SUMS, and installs that binary. --version pins a tag. --check\n")
	b.WriteString("prints the current and available versions and does not replace anything.\n")
	b.WriteString("--from keeps the offline path: use a binary you already have.\n\n")
	switch goos {
	case "windows":
		b.WriteString("upgrade schedules a one-shot SYSTEM task that stops the service, copies the new\n")
		b.WriteString("executable over the installed one, and starts the service. The task keeps\n")
		b.WriteString("running if the SSH session that launched the upgrade is disconnected.\n\n")
	case "darwin":
		b.WriteString("upgrade replaces one instance's binary and restarts the launchd job.\n")
		b.WriteString("The default path is /usr/local/bin/kite-agent. A named instance is\n")
		b.WriteString("/usr/local/lib/kite/<name>/kite-agent. This needs root.\n\n")
	default:
		b.WriteString("upgrade replaces one instance's binary and restarts the systemd unit.\n")
		b.WriteString("The default path is /usr/local/bin/kite-agent. A named instance is\n")
		b.WriteString("/usr/local/lib/kite/<name>/kite-agent. This needs root.\n\n")
	}
	return b.String()
}

// ParseList accepts kite-agent list with no arguments.
func ParseList(args []string) error {
	fs := flag.NewFlagSet("kite-agent list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), listHelp(runtime.GOOS))
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	return nil
}

// ListedService is one installed kite-agent Windows service.
type ListedService struct {
	Name      string
	AgentName string
	State     string
	StartType string
}

// IsKiteAgentService reports whether name is the default service or a
// side-by-side instance (kite-agent-*).
func IsKiteAgentService(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == ServiceName || strings.HasPrefix(name, ServiceName+"-")
}

// FormatList renders list rows. Columns are tab-separated so a start type
// such as "automatic (delayed)" stays one field. The registration key is
// not included.
func FormatList(rows []ListedService) string {
	if len(rows) == 0 {
		return "No kite-agent services are installed.\n"
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
	var b strings.Builder
	fmt.Fprintf(&b, "SERVICE\tAGENT\tSTATE\tSTART\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", row.Name, listField(row.AgentName), listField(row.State), listField(row.StartType))
	}
	return b.String()
}

// instanceCommand is the control command for one service. A non-default
// instance always includes --service-name so the hint does not act on kite-agent.
func instanceCommand(verb, name string) string {
	if name == "" || name == ServiceName {
		return "kite-agent " + verb
	}
	return "kite-agent " + verb + " --service-name " + name
}

// FormatLegacyLogonTask is the extra list line for the old logon task.
func FormatLegacyLogonTask(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return ""
	}
	return "legacy logon task " + ServiceName + ": " + status + "\n"
}

func listField(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// agentNameFromConfig reads agent.name and nothing else. A missing or
// unreadable file yields "-" so list still prints the service.
func agentNameFromConfig(path string) string {
	if strings.TrimSpace(path) == "" {
		return "-"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "-"
	}
	var doc struct {
		Agent struct {
			Name string `yaml:"name"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil || strings.TrimSpace(doc.Agent.Name) == "" {
		return "-"
	}
	return doc.Agent.Name
}

// ParseUpgrade reads upgrade flags. --apply is the worker a scheduled task runs.
func ParseUpgrade(args []string) (UpgradeOptions, error) {
	fs := flag.NewFlagSet("kite-agent upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var serviceName, from, version string
	apply := fs.Bool("apply", false, "internal: perform the swap in this process (the scheduled task runs this)")
	check := fs.Bool("check", false, "print the available version and do not replace the binary")
	fs.StringVar(&serviceName, "service-name", "", "instance to upgrade (default kite-agent)")
	fs.StringVar(&from, "from", "", "path to the new kite-agent binary (offline; omit to download a release)")
	fs.StringVar(&version, "version", "", "release tag to install, for example v0.1.0 (default: newest release)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), upgradeHelp(runtime.GOOS))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return UpgradeOptions{}, err
	}
	if fs.NArg() != 0 {
		return UpgradeOptions{}, errors.New("unexpected arguments")
	}
	canon, err := CanonicalServiceName(serviceName)
	if err != nil {
		return UpgradeOptions{}, err
	}
	from = strings.TrimSpace(from)
	version = strings.TrimSpace(version)
	if version == "" {
		version = strings.TrimSpace(os.Getenv("KITE_VERSION"))
	}
	if *apply && from == "" {
		return UpgradeOptions{}, errors.New("--from is required with --apply")
	}
	if *check && from != "" {
		return UpgradeOptions{}, errors.New("--check does not take --from")
	}
	return UpgradeOptions{ServiceName: canon, From: from, Apply: *apply, Version: version, Check: *check}, nil
}

// CanonicalServiceName accepts the default name and a short instance name.
// Empty means the default. Names that could escape a directory are rejected.
func CanonicalServiceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ServiceName, nil
	}
	if name == "." || name == ".." || len(name) > 80 {
		return "", fmt.Errorf("invalid Windows service name %q", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
			if i == 0 {
				return "", fmt.Errorf("service name %q must start with a letter or digit", name)
			}
		default:
			return "", fmt.Errorf("service name %q must contain only letters, digits, dots, hyphens, and underscores", name)
		}
	}
	return name, nil
}

// CleansLegacyTask reports whether install should remove the old logon task.
// Only the default instance does that, so a test install cannot delete the
// stable service's scheduled task.
func CleansLegacyTask(name string) bool {
	return name == ServiceName
}

// Layout is where one Windows service instance keeps its files.
type Layout struct {
	Name       string
	ConfigPath string
	LogPath    string
	InstallDir string
}

// InstanceLayout returns paths for a service name. The default name keeps the
// original files. Any other name is nested under kite\<service-name> so it
// cannot overwrite the stable exe or config.
func InstanceLayout(name, programData, programFiles string) (Layout, error) {
	canon, err := CanonicalServiceName(name)
	if err != nil {
		return Layout{}, err
	}
	if strings.TrimSpace(programData) == "" {
		programData = `C:\ProgramData`
	}
	if strings.TrimSpace(programFiles) == "" {
		programFiles = `C:\Program Files`
	}
	var cfg, dir string
	if canon == ServiceName {
		cfg = filepath.Join(programData, "kite", "config.yaml")
		dir = filepath.Join(programFiles, "kite")
	} else {
		cfg = filepath.Join(programData, "kite", canon, "config.yaml")
		dir = filepath.Join(programFiles, "kite", canon)
	}
	return Layout{
		Name:       canon,
		ConfigPath: cfg,
		LogPath:    filepath.Join(filepath.Dir(cfg), logFileName),
		InstallDir: dir,
	}, nil
}

// DefaultLayout uses ProgramData and ProgramFiles from the environment.
func DefaultLayout(name string) (Layout, error) {
	return InstanceLayout(name, os.Getenv("ProgramData"), os.Getenv("ProgramFiles"))
}

// ServiceArgs is the service command line after the executable.
// The default instance omits --service-name so an existing registration
// stays valid. The registration key is not included.
func ServiceArgs(configPath, serviceName string) []string {
	args := []string{"--config", configPath}
	if serviceName != "" && serviceName != ServiceName {
		args = append(args, "--service-name", serviceName)
	}
	return args
}

// ServiceNameFromArgs reads --service-name without rejecting other flags.
func ServiceNameFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--service-name" || a == "-service-name" {
			if i+1 < len(args) {
				if name, err := CanonicalServiceName(args[i+1]); err == nil {
					return name
				}
			}
			return ServiceName
		}
		if v, ok := strings.CutPrefix(a, "--service-name="); ok {
			if name, err := CanonicalServiceName(v); err == nil {
				return name
			}
		}
	}
	return ServiceName
}

// FormatStatus renders a service query for the terminal.
func FormatStatus(s StatusInfo) string {
	name := s.Name
	if name == "" {
		name = ServiceName
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Service: %s\n", name)
	if !s.Installed {
		fmt.Fprintf(&b, "Installed: no\n")
		fmt.Fprintf(&b, "Install it from an elevated prompt: %s\n", instanceCommand("install", name))
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
		fmt.Fprintf(&b, "Start it from an elevated prompt: %s\n", instanceCommand("start", name))
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
	agentDoc := map[string]any{
		"relay_addr":     agent.RelayAddr,
		"name":           agent.Name,
		"local_ssh_addr": agent.LocalSSHAddr,
		"idle_timeout":   formatDuration(agent.IdleTimeout),
	}
	if strings.TrimSpace(agent.RegistrationKey) != "" {
		agentDoc["registration_key"] = agent.RegistrationKey
	}
	if strings.TrimSpace(agent.AgentToken) != "" {
		agentDoc["agent_token"] = agent.AgentToken
	}
	if agent.LocalSSHAuto {
		agentDoc["local_ssh_addr_auto"] = true
	}
	if strings.TrimSpace(agent.SSHUser) != "" {
		agentDoc["ssh_user"] = agent.SSHUser
	}
	root["agent"] = agentDoc
	var buf bytes.Buffer
	buf.WriteString("# kite service configuration. This file contains the registration key or agent token.\n")
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
