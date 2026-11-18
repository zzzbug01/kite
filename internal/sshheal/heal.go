// Package sshheal restarts a stopped local sshd and follows an
// auto-detected port when a dial fails. Checks are throttled.
package sshheal

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zzzbug01/kite/internal/config"
)

const interval = 30 * time.Second

// System is the OS surface the healer needs. Tests substitute a fake.
type System interface {
	Running(ctx context.Context) (bool, error)
	Start(ctx context.Context) error
	Detect(ctx context.Context) (string, error)
}

// Health is the last local-SSH result written for `kite-agent status`.
type Health struct {
	Status  string    `json:"status"`
	Addr    string    `json:"addr,omitempty"`
	From    string    `json:"from,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
	History []Event   `json:"history,omitempty"`
}

// Line is the status text. Status values match the operator-facing phrases.
func (h Health) Line() string {
	switch h.Status {
	case "ok":
		if h.Addr != "" {
			return "Local SSH: ok (" + h.Addr + ")"
		}
		return "Local SSH: ok"
	case "sshd_stopped":
		return "Local SSH: sshd stopped"
	case "started":
		if h.Addr != "" {
			return "Local SSH: started by kite (" + h.Addr + ")"
		}
		return "Local SSH: started by kite"
	case "port_changed":
		return "Local SSH: port changed from " + h.From + " to " + h.Addr
	case "failed":
		if h.Detail == "" {
			return "Local SSH: failed"
		}
		return "Local SSH: failed: " + h.Detail
	default:
		return "Local SSH: unknown (the agent has not reported yet)"
	}
}

// Healer remembers the current dial address and the last recovery attempt.
type Healer struct {
	sys        System
	log        *slog.Logger
	every      time.Duration
	now        func() time.Time
	healthPath string
	configPath string
	envFile    string
	auto       bool

	mu         sync.Mutex
	last       time.Time
	addr       string
	lastStatus string
	lastAddr   string
}

// New builds a healer. Recovery attempts are at most once per 30s.
func New(sys System, addr string, auto bool, healthPath, configPath string, log *slog.Logger, now func() time.Time) *Healer {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Healer{
		sys:        sys,
		log:        log,
		every:      interval,
		now:        now,
		healthPath: healthPath,
		configPath: configPath,
		auto:       auto,
		addr:       addr,
	}
}

// WithEnvFile is the agent env file updated when an auto-detected port moves.
func (h *Healer) WithEnvFile(path string) *Healer {
	if h != nil {
		h.envFile = path
	}
	return h
}

// WithEvery overrides the throttle. Tests use this.
func (h *Healer) WithEvery(d time.Duration) *Healer {
	if d > 0 {
		h.every = d
	}
	return h
}

// Addr is the address the agent should dial.
func (h *Healer) Addr() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addr
}

// MarkOK records a successful dial. Repeated ok results are not rewritten.
func (h *Healer) MarkOK(addr string) {
	if h == nil {
		return
	}
	h.record(Health{Status: "ok", Addr: addr, At: h.now()})
}

// Recover runs at most once per throttle window. It may start sshd and, when
// the address was chosen automatically, switch to a newly detected port.
// retry is true when the caller should dial once more.
func (h *Healer) Recover(ctx context.Context, dialErr error) (retry bool) {
	if h == nil {
		return false
	}
	now := h.now()
	h.mu.Lock()
	if !h.last.IsZero() && now.Sub(h.last) < h.every {
		addr := h.addr
		h.mu.Unlock()
		h.log.Info("local ssh heal skipped", "reason", "throttled", "addr", addr)
		detail := "dial failed"
		if dialErr != nil {
			detail = dialErr.Error()
		}
		h.record(Health{Status: "failed", Addr: addr, Detail: detail, At: now})
		return false
	}
	h.last = now
	current := h.addr
	auto := h.auto
	h.mu.Unlock()

	running, err := h.sys.Running(ctx)
	if err != nil {
		h.log.Info("local ssh: could not query sshd", "err", err)
	}
	started := false
	if !running {
		h.log.Info("local ssh: sshd stopped", "addr", current)
		h.record(Health{Status: "sshd_stopped", Addr: current, At: now})
		if err := h.sys.Start(ctx); err != nil {
			h.log.Info("local ssh: start sshd failed", "err", err)
			h.record(Health{Status: "failed", Addr: current, Detail: err.Error(), At: h.now()})
		} else {
			started = true
			h.log.Info("local ssh: started by kite", "addr", current)
		}
	}
	if auto {
		detected, derr := h.sys.Detect(ctx)
		if derr != nil {
			h.log.Info("local ssh: detect failed", "err", derr)
		} else if detected != "" && detected != current {
			h.log.Info("local ssh: port changed", "from", current, "to", detected)
			h.mu.Lock()
			h.addr = detected
			h.mu.Unlock()
			if h.configPath != "" {
				if err := config.SetLocalSSH(h.configPath, detected, true); err != nil {
					h.log.Info("local ssh: could not store the new address", "err", err)
				}
			}
			if h.envFile != "" {
				if err := config.UpdateEnvLocalSSH(h.envFile, detected); err != nil {
					h.log.Info("local ssh: could not store the new address", "err", err)
				}
			}
			h.record(Health{Status: "port_changed", From: current, Addr: detected, At: h.now()})
			return true
		}
	}
	if started {
		h.record(Health{Status: "started", Addr: current, At: h.now()})
		return true
	}
	if !running {
		return false
	}
	detail := "dial failed"
	if dialErr != nil {
		detail = dialErr.Error()
	}
	h.log.Info("local ssh: dial failed", "addr", current, "err", detail)
	h.record(Health{Status: "failed", Addr: current, Detail: detail, At: h.now()})
	return false
}

func (h *Healer) record(hv Health) {
	h.mu.Lock()
	if hv.Status == "ok" && h.lastStatus == "ok" && h.lastAddr == hv.Addr {
		h.mu.Unlock()
		return
	}
	h.lastStatus = hv.Status
	h.lastAddr = hv.Addr
	path := h.healthPath
	h.mu.Unlock()
	if path == "" {
		return
	}
	if hv.At.IsZero() {
		hv.At = h.now()
	}
	prev := Read(path)
	if hv.Status == "ok" {
		hv.History = prev.History
	} else {
		hv.History = append(prev.History, Event{
			Status: hv.Status,
			Addr:   hv.Addr,
			From:   hv.From,
			Detail: hv.Detail,
			At:     hv.At,
		})
		if len(hv.History) > 8 {
			hv.History = hv.History[len(hv.History)-8:]
		}
	}
	_ = Write(path, hv)
}

// Event is one earlier local-SSH action kept after a later ok.
type Event struct {
	Status string    `json:"status"`
	Addr   string    `json:"addr,omitempty"`
	From   string    `json:"from,omitempty"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Text is the current line plus the short history.
func (h Health) Text() string {
	line := h.Line()
	if len(h.History) == 0 {
		return line
	}
	var b strings.Builder
	b.WriteString(line)
	for _, e := range h.History {
		b.WriteByte('\n')
		b.WriteString("  ")
		if !e.At.IsZero() {
			b.WriteString(e.At.In(time.Local).Format(time.RFC3339))
			b.WriteString("  ")
		}
		b.WriteString(Health{Status: e.Status, Addr: e.Addr, From: e.From, Detail: e.Detail}.Line())
	}
	return b.String()
}

// Write stores health next to the agent config. The file has no secrets.
func Write(path string, h Health) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// File is the health record next to an agent config.
func File(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), "ssh-health.json")
}

// Search is where status looks when it does not have the agent's config path.
func Search() []string {
	var out []string
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("ProgramData"))
		if root == "" {
			root = `C:\ProgramData`
		}
		return []string{filepath.Join(root, "kite", "ssh-health.json")}
	}
	out = append(out, "/var/lib/kite-agent/ssh-health.json")
	out = append(out, "/var/lib/kite/ssh-health.json")
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		out = append(out, filepath.Join(dir, "kite", "ssh-health.json"))
	}
	return out
}

// LoadFirst returns the first health file that exists.
func LoadFirst(paths []string) Health {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return Read(p)
		}
	}
	return Health{}
}

// Read loads a health file. A missing file is the unknown state.
func Read(path string) Health {
	b, err := os.ReadFile(path)
	if err != nil {
		return Health{}
	}
	var h Health
	if json.Unmarshal(b, &h) != nil {
		return Health{}
	}
	return h
}
