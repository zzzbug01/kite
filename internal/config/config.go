// Package config loads kite settings from a YAML file, the environment, and flags.
//
// Precedence is flags (applied by callers) > environment > config file > defaults.
// Registration secrets belong in the environment or a gitignored file such as
// ~/.config/kite/config.yaml. The Windows service reads
// %ProgramData%\kite\config.yaml (see WindowsMachineConfig) unless install
// recorded a different --config path. Do not commit those files.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zzzbug01/kite/internal/protocol"
)

// PlaceholderKey is the example secret shipped in sample configs.
// Processes refuse to start while it is still set.
const PlaceholderKey = "CHANGE_ME"

// File is the on-disk YAML shape. Every section is optional.
type File struct {
	Relay  RelaySection  `yaml:"relay"`
	Agent  AgentSection  `yaml:"agent"`
	Client ClientSection `yaml:"client"`
}

// RelaySection is the kite-relay portion of a config file.
type RelaySection struct {
	ListenAddr      string `yaml:"listen_addr"`
	RegistrationKey string `yaml:"registration_key"`
	TLSCertFile     string `yaml:"tls_cert_file"`
	TLSKeyFile      string `yaml:"tls_key_file"`
	IdleTimeout     string `yaml:"idle_timeout"`
}

// AgentSection is the kite-agent portion of a config file.
type AgentSection struct {
	RelayAddr       string `yaml:"relay_addr"`
	RegistrationKey string `yaml:"registration_key"`
	Name            string `yaml:"name"`
	LocalSSHAddr    string `yaml:"local_ssh_addr"`
	IdleTimeout     string `yaml:"idle_timeout"`
}

// ClientSection is the kite client portion of a config file.
type ClientSection struct {
	RelayAddr       string `yaml:"relay_addr"`
	RegistrationKey string `yaml:"registration_key"`
	IdleTimeout     string `yaml:"idle_timeout"`
}

// Relay is the resolved kite-relay configuration.
type Relay struct {
	ListenAddr      string
	RegistrationKey string
	TLSCertFile     string
	TLSKeyFile      string
	IdleTimeout     time.Duration
}

// Agent is the resolved kite-agent configuration.
type Agent struct {
	RelayAddr       string
	RegistrationKey string
	Name            string
	LocalSSHAddr    string
	IdleTimeout     time.Duration
}

// Client is the resolved kite client configuration.
type Client struct {
	RelayAddr       string
	RegistrationKey string
	IdleTimeout     time.Duration
}

// LoadRelay resolves relay settings. explicitPath, when non-empty, must exist.
func LoadRelay(explicitPath string) (Relay, error) {
	file, err := loadFile(explicitPath)
	if err != nil {
		return Relay{}, err
	}
	cfg := Relay{
		ListenAddr:      "127.0.0.1:8080",
		IdleTimeout:     protocol.DefaultIdleTimeout,
		RegistrationKey: file.Relay.RegistrationKey,
		TLSCertFile:     file.Relay.TLSCertFile,
		TLSKeyFile:      file.Relay.TLSKeyFile,
	}
	if file.Relay.ListenAddr != "" {
		cfg.ListenAddr = file.Relay.ListenAddr
	}
	if d, err := parseDuration(file.Relay.IdleTimeout); err != nil {
		return Relay{}, fmt.Errorf("relay.idle_timeout: %w", err)
	} else if d > 0 {
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("REGISTRATION_KEY"); v != "" {
		cfg.RegistrationKey = v
	}
	if v := os.Getenv("TLS_CERT_FILE"); v != "" {
		cfg.TLSCertFile = v
	}
	if v := os.Getenv("TLS_KEY_FILE"); v != "" {
		cfg.TLSKeyFile = v
	}
	if v := os.Getenv("IDLE_TIMEOUT"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return Relay{}, fmt.Errorf("IDLE_TIMEOUT: %w", err)
		}
		if d > 0 {
			cfg.IdleTimeout = d
		}
	}
	return cfg, nil
}

// LoadAgent resolves agent settings.
func LoadAgent(explicitPath string) (Agent, error) {
	file, err := loadFile(explicitPath)
	if err != nil {
		return Agent{}, err
	}
	cfg := Agent{
		LocalSSHAddr:    "127.0.0.1:22",
		IdleTimeout:     protocol.DefaultIdleTimeout,
		RelayAddr:       file.Agent.RelayAddr,
		RegistrationKey: file.Agent.RegistrationKey,
		Name:            file.Agent.Name,
	}
	if file.Agent.LocalSSHAddr != "" {
		cfg.LocalSSHAddr = file.Agent.LocalSSHAddr
	}
	if d, err := parseDuration(file.Agent.IdleTimeout); err != nil {
		return Agent{}, fmt.Errorf("agent.idle_timeout: %w", err)
	} else if d > 0 {
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("RELAY_ADDR"); v != "" {
		cfg.RelayAddr = v
	}
	if v := os.Getenv("REGISTRATION_KEY"); v != "" {
		cfg.RegistrationKey = v
	}
	if v := os.Getenv("AGENT_NAME"); v != "" {
		cfg.Name = v
	}
	if v := os.Getenv("LOCAL_SSH_ADDR"); v != "" {
		cfg.LocalSSHAddr = v
	}
	if v := os.Getenv("IDLE_TIMEOUT"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return Agent{}, fmt.Errorf("IDLE_TIMEOUT: %w", err)
		}
		if d > 0 {
			cfg.IdleTimeout = d
		}
	}
	return cfg, nil
}

// LoadClient resolves client settings.
func LoadClient(explicitPath string) (Client, error) {
	file, err := loadFile(explicitPath)
	if err != nil {
		return Client{}, err
	}
	cfg := Client{
		IdleTimeout:     protocol.DefaultIdleTimeout,
		RelayAddr:       file.Client.RelayAddr,
		RegistrationKey: file.Client.RegistrationKey,
	}
	if d, err := parseDuration(file.Client.IdleTimeout); err != nil {
		return Client{}, fmt.Errorf("client.idle_timeout: %w", err)
	} else if d > 0 {
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("RELAY_ADDR"); v != "" {
		cfg.RelayAddr = v
	}
	if v := os.Getenv("REGISTRATION_KEY"); v != "" {
		cfg.RegistrationKey = v
	}
	if v := os.Getenv("IDLE_TIMEOUT"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return Client{}, fmt.Errorf("IDLE_TIMEOUT: %w", err)
		}
		if d > 0 {
			cfg.IdleTimeout = d
		}
	}
	return cfg, nil
}

// ValidateKey rejects an empty key and the published example placeholder.
func ValidateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("REGISTRATION_KEY is required; set it via the environment or a gitignored config file, never commit it")
	}
	if key == PlaceholderKey {
		return errors.New("REGISTRATION_KEY is still the example placeholder CHANGE_ME; set a real secret via the environment or a gitignored config file")
	}
	return nil
}

// NormalizeRelayURL checks a ws:// or wss:// URL and fills defaultPath when
// the URL has no path of its own.
func NormalizeRelayURL(raw, defaultPath string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("RELAY_ADDR is required (for example wss://tunnel.example.com/agent)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("RELAY_ADDR: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	default:
		return "", fmt.Errorf("RELAY_ADDR must use ws:// or wss://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("RELAY_ADDR is missing a host")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = defaultPath
	}
	return u.String(), nil
}

// PeekConfigFlag returns the value of -config / --config without fully parsing flags.
func PeekConfigFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "-config" || a == "--config" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "-config="); ok {
			return v
		}
	}
	return ""
}

func loadFile(explicit string) (File, error) {
	path := explicit
	if path == "" {
		path = os.Getenv("KITE_CONFIG")
	}
	mustExist := path != ""
	if path == "" {
		path = discoverConfig()
		if path == "" {
			return File{}, nil
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !mustExist && errors.Is(err, os.ErrNotExist) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return f, nil
}

// WindowsMachineConfig is the machine-wide agent config used by the Windows
// service account. It is empty on other operating systems. LocalSystem does
// not load a logged-on user's profile, so the service cannot rely on
// %AppData% or a home-directory file.
func WindowsMachineConfig() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	root := strings.TrimSpace(os.Getenv("ProgramData"))
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "kite", "config.yaml")
}

// discoverConfig finds a config outside the git working tree.
// It does not read ./config.yaml implicitly: ssh runs ProxyCommand with an
// arbitrary working directory. User config wins over the Windows machine file
// so an interactive session keeps its own settings; the service passes
// --config explicitly and does not depend on this order.
func discoverConfig() string {
	for _, p := range configSearchPaths() {
		if p != "" && isFile(p) {
			return p
		}
	}
	return ""
}

func configSearchPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "kite", "config.yaml"))
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		p := filepath.Join(dir, "kite", "config.yaml")
		if !containsPath(paths, p) {
			paths = append(paths, p)
		}
	}
	if p := WindowsMachineConfig(); p != "" {
		paths = append(paths, p)
	}
	return paths
}

func containsPath(paths []string, p string) bool {
	for _, existing := range paths {
		if sameConfigPath(existing, p) {
			return true
		}
	}
	return false
}

func sameConfigPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("duration must be positive, got %s", s)
		}
		return d, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as a duration (try 90s)", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("duration must be positive, got %s", s)
	}
	return time.Duration(n) * time.Second, nil
}
