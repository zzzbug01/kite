package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/agentctl"
	"github.com/zzzbug01/kite/internal/authkeys"
	"github.com/zzzbug01/kite/internal/pairing"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/sshheal"
)

func (s *session) handlePair(payload []byte) {
	var req protocol.PairInstall
	if len(payload) == 0 || len(payload) > 8*1024 || json.Unmarshal(payload, &req) != nil || req.RequestID == "" {
		return
	}
	res := s.installAuthorizedKey(req)
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	_ = s.write(protocol.Frame{Type: protocol.TypePairResult, Payload: b})
}

func (s *session) installAuthorizedKey(req protocol.PairInstall) protocol.PairResult {
	res := protocol.PairResult{RequestID: req.RequestID}
	name, home, admin, err := resolveUser(s.cfg)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	path, kind := authkeys.Path(runtime.GOOS, home, admin, s.cfg.ProgramData)
	comment := pairing.PairComment(req.Label, time.Now())
	if err := authkeys.Upsert(path, req.PublicKey, comment, kind, name); err != nil {
		res.Error = err.Error()
		s.cfg.logger().Info("pair install failed", "user", name, "err", err.Error())
		return res
	}
	res.OK = true
	res.SSHUser = name
	s.cfg.logger().Info("installed paired key", "user", name, "label", pairing.SanitizeLabel(req.Label))
	return res
}

func healthFile(configPath string) string {
	if p := sshheal.File(configPath); p != "" {
		return p
	}
	paths := sshheal.Search()
	if privilegedUser() && len(paths) > 0 {
		return paths[0]
	}
	if len(paths) > 1 {
		return paths[len(paths)-1]
	}
	if len(paths) > 0 {
		return paths[0]
	}
	return "ssh-health.json"
}

func startControl(ctx context.Context, cfg Config) (func(), error) {
	path := cfg.ControlPath
	if path == "" {
		path = defaultControlPath(cfg.ConfigPath)
	}
	h := controlHandler{cfg: cfg}
	return agentctl.Start(ctx, path, h)
}

func defaultControlPath(configPath string) string {
	if strings.TrimSpace(configPath) != "" {
		return filepath.Join(filepath.Dir(configPath), "agent-control.json")
	}
	if privilegedUser() {
		if runtime.GOOS == "windows" {
			root := strings.TrimSpace(os.Getenv("ProgramData"))
			if root == "" {
				root = `C:\ProgramData`
			}
			return filepath.Join(root, "kite", "agent-control.json")
		}
		return "/var/lib/kite/agent-control.json"
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "kite", "agent-control.json")
}

type controlHandler struct {
	cfg Config
}

func (h controlHandler) Pair(ctx context.Context, ttl time.Duration) (string, string, time.Time, error) {
	base, err := pairing.HTTPBase(h.cfg.RelayURL)
	if err != nil {
		return "", "", time.Time{}, err
	}
	clientURL, err := pairing.ClientURL(h.cfg.RelayURL)
	if err != nil {
		return "", "", time.Time{}, err
	}
	sshUser, _, _, _ := resolveUser(h.cfg)
	secs := 0
	if ttl > 0 {
		secs = int(ttl / time.Second)
	}
	resp, err := pairing.PostCreate(ctx, base, pairing.CreateRequest{
		Key:        h.cfg.RegistrationKey,
		Agent:      h.cfg.Name,
		SSHUser:    sshUser,
		ClientURL:  clientURL,
		TTLSeconds: secs,
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	return resp.Code, resp.Agent, resp.ExpiresAt, nil
}

func (h controlHandler) List(context.Context) ([]authkeys.Record, error) {
	path, _, err := h.keysPath()
	if err != nil {
		return nil, err
	}
	return authkeys.List(path)
}

func (h controlHandler) Revoke(_ context.Context, label string) (int, error) {
	path, kind, err := h.keysPath()
	if err != nil {
		return 0, err
	}
	name, _, _, _ := resolveUser(h.cfg)
	return authkeys.Revoke(path, label, kind, name)
}

func (h controlHandler) keysPath() (string, authkeys.Kind, error) {
	name, home, admin, err := resolveUser(h.cfg)
	if err != nil {
		return "", 0, err
	}
	path, kind := authkeys.Path(runtime.GOOS, home, admin, h.cfg.ProgramData)
	_ = name
	return path, kind, nil
}

// AuthorizedKeys is the file pair and unpair edit for cfg.
func AuthorizedKeys(cfg Config) (path string, kind authkeys.Kind, owner string, err error) {
	name, home, admin, err := resolveUser(cfg)
	if err != nil {
		return "", 0, "", err
	}
	path, kind = authkeys.Path(runtime.GOOS, home, admin, cfg.ProgramData)
	return path, kind, name, nil
}

func resolveUser(cfg Config) (name, home string, admin bool, err error) {
	name = leafName(cfg.SSHUser)
	if name == "" {
		u, uerr := user.Current()
		if uerr != nil {
			return "", "", false, errors.New("SSH_USER is required")
		}
		name = leafName(u.Username)
	}
	if isMachineAccount(name) {
		return "", "", false, errors.New("SSH_USER is required when kite-agent runs as a machine account; set ssh_user in the config")
	}
	if cfg.SSHAdmin != nil {
		admin = *cfg.SSHAdmin
	} else if runtime.GOOS == "windows" {
		admin, err = authkeys.DetectAdmin(name)
		if err != nil {
			return "", "", false, err
		}
	}
	if cfg.HomeDir != "" {
		return name, cfg.HomeDir, admin, nil
	}
	home, err = homeFor(name)
	if err != nil {
		return "", "", false, err
	}
	return name, home, admin, nil
}

func homeFor(username string) (string, error) {
	username = leafName(username)
	if u, err := user.Current(); err == nil && leafName(u.Username) == username {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			return h, nil
		}
	}
	if u, err := user.Lookup(username); err == nil && u.HomeDir != "" {
		return u.HomeDir, nil
	}
	var guess string
	switch runtime.GOOS {
	case "darwin":
		guess = filepath.Join("/Users", username)
	case "windows":
		root := os.Getenv("SystemDrive")
		if root == "" {
			root = `C:`
		}
		guess = root + `\Users\` + username
	default:
		if username == "root" {
			guess = "/root"
		} else {
			guess = filepath.Join("/home", username)
		}
	}
	st, err := os.Stat(guess)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("home directory for %s was not found; set SSH_USER to an account on this machine", username)
	}
	return guess, nil
}

func leafName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func isMachineAccount(name string) bool {
	switch strings.ToLower(name) {
	case "system", "localservice", "networkservice":
		return true
	default:
		return false
	}
}
