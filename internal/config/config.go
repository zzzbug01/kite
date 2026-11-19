// Package config loads kite settings from a YAML file, the environment, and flags.
//
// Precedence is flags (applied by callers) > environment > config file > defaults.
// Registration secrets belong in the environment or a gitignored file such as
// ~/.config/kite/config.yaml. The Windows service reads
// %ProgramData%\kite\config.yaml (see WindowsMachineConfig) unless install
// recorded a different --config path. Do not commit those files.
package config

import (
	"bytes"
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
	ListenAddr              string `yaml:"listen_addr"`
	RegistrationKey         string `yaml:"registration_key"`
	RegistrationKeyPrevious string `yaml:"registration_key_previous"`
	StateDir                string `yaml:"state_dir"`
	TrustedProxies          string `yaml:"trusted_proxies"`
	TLSCertFile             string `yaml:"tls_cert_file"`
	TLSKeyFile              string `yaml:"tls_key_file"`
	IdleTimeout             string `yaml:"idle_timeout"`
}

// AgentSection is the kite-agent portion of a config file.
type AgentSection struct {
	RelayAddr       string `yaml:"relay_addr"`
	RegistrationKey string `yaml:"registration_key"`
	// AgentToken is a per-agent secret from kite-relay invite. It replaces
	// the registration key for agents installed with a one-time code.
	AgentToken   string `yaml:"agent_token,omitempty"`
	Name         string `yaml:"name"`
	LocalSSHAddr string `yaml:"local_ssh_addr"`
	LocalSSHAuto bool   `yaml:"local_ssh_addr_auto"`
	IdleTimeout  string `yaml:"idle_timeout"`
	SSHUser      string `yaml:"ssh_user"`
}

// ClientSection is the kite client portion of a config file.
// ClientAgent is one paired laptop. Token is a per-client secret, not the
// registration key.
type ClientAgent struct {
	Token        string `yaml:"token"`
	User         string `yaml:"user,omitempty"`
	IdentityFile string `yaml:"identity_file,omitempty"`
	RelayAddr    string `yaml:"relay_addr,omitempty"`
}

type ClientSection struct {
	RelayAddr       string                 `yaml:"relay_addr"`
	RegistrationKey string                 `yaml:"registration_key"`
	IdleTimeout     string                 `yaml:"idle_timeout"`
	Agents          map[string]ClientAgent `yaml:"agents,omitempty"`
}

// Relay is the resolved kite-relay configuration.
type Relay struct {
	ListenAddr              string
	RegistrationKey         string
	RegistrationKeyPrevious string
	StateDir                string
	TrustedProxies          string
	TLSCertFile             string
	TLSKeyFile              string
	IdleTimeout             time.Duration
}

// Agent is the resolved kite-agent configuration.
type Agent struct {
	RelayAddr       string
	RegistrationKey string
	// AgentToken is set when this agent was installed with a one-time code.
	// The relay stores only a hash of it, bound to Name.
	AgentToken string
	// TokenFromFile is true when the config file holds an agent token.
	// RELAY_ADDR and REGISTRATION_KEY from the environment do not override
	// that install. An explicit --registration-key flag still can.
	TokenFromFile bool
	// KeyFromFlag is set by the command when --registration-key was passed.
	KeyFromFlag bool
	// NameFromEnv is true when AGENT_NAME was set, so install --code must
	// match a name bound to the code instead of adopting it.
	NameFromEnv bool
	Name        string
	// ControlUser is the person who should be able to run kite-agent pair
	// without a further sudo. Install writes it as KITE_CONTROL_USER.
	ControlUser  string
	LocalSSHAddr string
	// LocalSSHAuto is true when kite chose LocalSSHAddr by inspecting sshd.
	// An explicit --local, LOCAL_SSH_ADDR without LOCAL_SSH_ADDR_AUTO, or a
	// config value without the auto flag clears it.
	LocalSSHAuto  bool
	LocalExplicit bool
	IdleTimeout   time.Duration
	SSHUser       string
}

// Client is the resolved kite client configuration.
type Client struct {
	RelayAddr       string
	RegistrationKey string
	// FileRelayAddr and FileRegistrationKey are the values from the config
	// file before environment defaults are applied. A paired host uses these.
	FileRelayAddr       string
	FileRegistrationKey string
	IdleTimeout         time.Duration
	Agents              map[string]ClientAgent
}

// LoadRelay resolves relay settings. explicitPath, when non-empty, must exist.
func LoadRelay(explicitPath string) (Relay, error) {
	file, err := loadFile(explicitPath, true)
	if err != nil {
		return Relay{}, err
	}
	cfg := Relay{
		ListenAddr:              "127.0.0.1:8080",
		IdleTimeout:             protocol.DefaultIdleTimeout,
		RegistrationKey:         file.Relay.RegistrationKey,
		RegistrationKeyPrevious: file.Relay.RegistrationKeyPrevious,
		StateDir:                file.Relay.StateDir,
		TrustedProxies:          file.Relay.TrustedProxies,
		TLSCertFile:             file.Relay.TLSCertFile,
		TLSKeyFile:              file.Relay.TLSKeyFile,
	}
	if cfg.StateDir == "" {
		cfg.StateDir = defaultStateDir()
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
	if v := os.Getenv("REGISTRATION_KEY_PREVIOUS"); v != "" {
		cfg.RegistrationKeyPrevious = v
	}
	if v := os.Getenv("STATE_DIR"); v != "" {
		cfg.StateDir = v
	}
	if v := os.Getenv("TRUSTED_PROXIES"); v != "" {
		cfg.TrustedProxies = v
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
	return loadAgent(explicitPath, true)
}

// LoadAgentIsolated resolves agent settings without discovering another
// instance's config. A new --service-name install uses this so it does not
// inherit the default machine's name and SSH address.
func LoadAgentIsolated(explicitPath string) (Agent, error) {
	return loadAgent(explicitPath, false)
}

func loadAgent(explicitPath string, discover bool) (Agent, error) {
	file, err := loadFile(explicitPath, discover)
	if err != nil {
		return Agent{}, err
	}
	cfg := Agent{
		LocalSSHAddr:    "127.0.0.1:22",
		IdleTimeout:     protocol.DefaultIdleTimeout,
		RelayAddr:       file.Agent.RelayAddr,
		RegistrationKey: file.Agent.RegistrationKey,
		AgentToken:      file.Agent.AgentToken,
		TokenFromFile:   strings.TrimSpace(file.Agent.AgentToken) != "",
		Name:            file.Agent.Name,
		SSHUser:         file.Agent.SSHUser,
	}
	if file.Agent.LocalSSHAddr != "" {
		cfg.LocalSSHAddr = file.Agent.LocalSSHAddr
		if file.Agent.LocalSSHAuto {
			cfg.LocalSSHAuto = true
		} else {
			cfg.LocalExplicit = true
		}
	}
	if d, err := parseDuration(file.Agent.IdleTimeout); err != nil {
		return Agent{}, fmt.Errorf("agent.idle_timeout: %w", err)
	} else if d > 0 {
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("RELAY_ADDR"); v != "" && !cfg.TokenFromFile {
		cfg.RelayAddr = v
	}
	if v := os.Getenv("REGISTRATION_KEY"); v != "" && !cfg.TokenFromFile {
		cfg.RegistrationKey = v
	}
	if v := os.Getenv("AGENT_TOKEN"); v != "" {
		cfg.AgentToken = v
	}
	if v := os.Getenv("AGENT_NAME"); v != "" {
		cfg.NameFromEnv = true
		cfg.Name = v
	}
	if v := os.Getenv("LOCAL_SSH_ADDR"); v != "" {
		cfg.LocalSSHAddr = v
		if envFlag(os.Getenv("LOCAL_SSH_ADDR_AUTO")) {
			cfg.LocalSSHAuto = true
			cfg.LocalExplicit = false
		} else {
			cfg.LocalSSHAuto = false
			cfg.LocalExplicit = true
		}
	}
	if v := os.Getenv("SSH_USER"); v != "" {
		cfg.SSHUser = v
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
	file, err := loadFile(explicitPath, true)
	if err != nil {
		return Client{}, err
	}
	cfg := Client{
		IdleTimeout:         protocol.DefaultIdleTimeout,
		RelayAddr:           file.Client.RelayAddr,
		FileRelayAddr:       file.Client.RelayAddr,
		RegistrationKey:     file.Client.RegistrationKey,
		FileRegistrationKey: file.Client.RegistrationKey,
		Agents:              file.Client.Agents,
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

// CredentialFor returns the secret used to dial agent. A paired client token
// wins over the shared registration key so a laptop can keep working after
// the shared key is rotated.
func (c Client) CredentialFor(agent string) (secret, kind string, err error) {
	_, secret, kind, err = c.Endpoint(agent)
	return secret, kind, err
}

// Endpoint is the relay URL and credential for one agent. A paired host
// uses the relay address and token stored in the config file. Environment
// variables only fill those in when the file has no host entry.
func (c Client) Endpoint(agent string) (relay, secret, kind string, err error) {
	if c.Agents != nil {
		if a, ok := c.Agents[agent]; ok && strings.TrimSpace(a.Token) != "" {
			relay = firstNonEmpty(a.RelayAddr, c.FileRelayAddr)
			if relay == "" {
				relay = c.RelayAddr
			}
			if relay == "" {
				return "", "", "", fmt.Errorf("no relay address for %s; pass --relay or set it in the config file", agent)
			}
			return relay, a.Token, "client", nil
		}
	}
	if strings.TrimSpace(c.RegistrationKey) != "" {
		if strings.TrimSpace(c.RelayAddr) == "" {
			return "", "", "", fmt.Errorf("no relay address for %s; set RELAY_ADDR or client.relay_addr", agent)
		}
		return c.RelayAddr, c.RegistrationKey, "registration", nil
	}
	return "", "", "", fmt.Errorf("no credential for %s; run kite connect or set REGISTRATION_KEY", agent)
}

// FileRelay reads the relay URL stored in a YAML config or an agent env file.
// The process environment is ignored, so a hint printed for one instance is
// not replaced by RELAY_ADDR from the shell that ran the command.
func FileRelay(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(b)
	var doc struct {
		Agent struct {
			RelayAddr string `yaml:"relay_addr"`
		} `yaml:"agent"`
	}
	if yaml.Unmarshal(b, &doc) == nil {
		if v := strings.TrimSpace(doc.Agent.RelayAddr); v != "" {
			return v
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "RELAY_ADDR" {
			continue
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}

func envFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// UpdateEnvLocalSSH rewrites LOCAL_SSH_ADDR in an agent env file and keeps
// LOCAL_SSH_ADDR_AUTO=1 so a later start still follows sshd. A missing file
// is left alone.
func UpdateEnvLocalSSH(path, addr string) error {
	addr = strings.TrimSpace(addr)
	if strings.TrimSpace(path) == "" || addr == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(b), "\n")
	foundAddr := false
	foundAuto := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "LOCAL_SSH_ADDR_AUTO=") {
			lines[i] = "LOCAL_SSH_ADDR_AUTO=1"
			foundAuto = true
			continue
		}
		if strings.HasPrefix(trim, "LOCAL_SSH_ADDR=") {
			lines[i] = "LOCAL_SSH_ADDR=" + addr
			foundAddr = true
		}
	}
	if !foundAddr {
		lines = append(lines, "LOCAL_SSH_ADDR="+addr)
	}
	if !foundAuto {
		lines = append(lines, "LOCAL_SSH_ADDR_AUTO=1")
	}
	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return writePrivate(path, []byte(body))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// SetSSHUser writes agent.ssh_user when it is empty or a machine account.
// A real user already in the file is left unchanged.
func SetSSHUser(path, sshUser string) error {
	sshUser = strings.TrimSpace(sshUser)
	if path == "" || sshUser == "" || strings.HasSuffix(sshUser, "$") {
		return errors.New("ssh user is required")
	}
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	agent, _ := root["agent"].(map[string]any)
	if agent == nil {
		agent = map[string]any{}
	}
	cur, _ := agent["ssh_user"].(string)
	cur = strings.TrimSpace(cur)
	if cur != "" && !strings.HasSuffix(cur, "$") && !strings.EqualFold(cur, "system") && !strings.EqualFold(cur, "localservice") && !strings.EqualFold(cur, "networkservice") {
		return nil
	}
	agent["ssh_user"] = sshUser
	root["agent"] = agent
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writePrivate(path, buf.Bytes())
}

// SetLocalSSH updates agent.local_ssh_addr in an existing file and records
// whether kite chose the address. Other sections are preserved.
func SetLocalSSH(path, addr string, auto bool) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(addr) == "" {
		return errors.New("config path and address are required")
	}
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	agent, _ := root["agent"].(map[string]any)
	if agent == nil {
		agent = map[string]any{}
	}
	agent["local_ssh_addr"] = addr
	if auto {
		agent["local_ssh_addr_auto"] = true
	} else {
		delete(agent, "local_ssh_addr_auto")
	}
	root["agent"] = agent
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writePrivate(path, buf.Bytes())
}

// ClientUpdate is one paired agent written into the client config section.
type ClientUpdate struct {
	RelayAddr    string
	Agent        string
	Token        string
	User         string
	IdentityFile string
}

// MergeClient writes or updates the client section of path. Other sections
// are preserved. The file mode is 0600. The token is a per-client secret.
func MergeClient(path string, upd ClientUpdate) error {
	if strings.TrimSpace(upd.Agent) == "" || strings.TrimSpace(upd.Token) == "" {
		return errors.New("client update requires an agent name and token")
	}
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
	client, _ := root["client"].(map[string]any)
	if client == nil {
		client = map[string]any{}
	}
	if strings.TrimSpace(upd.RelayAddr) != "" {
		client["relay_addr"] = upd.RelayAddr
	}
	agents, _ := client["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
	}
	entry := map[string]any{"token": upd.Token}
	if upd.RelayAddr != "" {
		entry["relay_addr"] = upd.RelayAddr
	}
	if upd.User != "" {
		entry["user"] = upd.User
	}
	if upd.IdentityFile != "" {
		entry["identity_file"] = upd.IdentityFile
	}
	agents[upd.Agent] = entry
	client["agents"] = agents
	root["client"] = client
	var buf bytes.Buffer
	buf.WriteString("# kite client configuration. Contains a per-client token. Do not commit.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writePrivate(path, buf.Bytes())
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			os.Remove(tmp)
			return err2
		}
	}
	return os.Chmod(path, 0o600)
}

func defaultStateDir() string {
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("ProgramData"))
		if root == "" {
			root = `C:\ProgramData`
		}
		return filepath.Join(root, "kite", "state")
	}
	return "/var/lib/kite"
}

// Credential is the secret this agent presents to the relay.
// A token stored in the config file wins over REGISTRATION_KEY and
// RELAY_ADDR from the environment. --registration-key still overrides.
// An install that used a one-time code stores only the token.
func (a Agent) Credential() (secret, kind string, err error) {
	key := strings.TrimSpace(a.RegistrationKey)
	token := strings.TrimSpace(a.AgentToken)
	// A token stored in the config wins over a registration key that arrived
	// from the environment. --registration-key still overrides.
	if token != "" && a.TokenFromFile && !a.KeyFromFlag {
		return token, "agent", nil
	}
	if key != "" && key != PlaceholderKey {
		return key, "registration", nil
	}
	if token != "" {
		return token, "agent", nil
	}
	if key == PlaceholderKey {
		return "", "", errors.New("REGISTRATION_KEY is still the example placeholder CHANGE_ME; set a real secret or install with --code from kite-relay invite")
	}
	return "", "", errors.New("REGISTRATION_KEY or an agent token is required; set the key, or install with --code from kite-relay invite")
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

// ParseRelayURL accepts a bare host, host:port, http(s)://, or ws(s):// URL,
// with or without a path. https becomes wss and http becomes ws. A missing
// scheme defaults to wss. The path is left as the caller wrote it.
func ParseRelayURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("RELAY_ADDR is required (for example wss://tunnel.example.com/agent)")
	}
	if !strings.Contains(raw, "://") {
		raw = "wss://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("RELAY_ADDR: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "wss", "https":
		u.Scheme = "wss"
	case "ws", "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("RELAY_ADDR must be a host or an http(s)/ws(s) URL, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("RELAY_ADDR is missing a host")
	}
	u.Fragment = ""
	return u, nil
}

// NormalizeRelayURL checks a relay address and fills defaultPath when the
// URL has no path of its own. Full ws:// and wss:// URLs keep an explicit path.
func NormalizeRelayURL(raw, defaultPath string) (string, error) {
	u, err := ParseRelayURL(raw)
	if err != nil {
		return "", err
	}
	if u.Path == "" || u.Path == "/" {
		if defaultPath == "" {
			defaultPath = "/"
		}
		if !strings.HasPrefix(defaultPath, "/") {
			defaultPath = "/" + defaultPath
		}
		u.Path = defaultPath
		u.RawPath = ""
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

func loadFile(explicit string, discover bool) (File, error) {
	path := explicit
	if path == "" {
		path = os.Getenv("KITE_CONFIG")
	}
	mustExist := path != ""
	if path == "" && discover {
		path = discoverConfig()
		if path == "" {
			return File{}, nil
		}
	}
	if path == "" {
		return File{}, nil
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
