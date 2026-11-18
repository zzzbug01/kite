package sshsetup

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Probe collects a Snapshot. Tests substitute a fake.
type Probe interface {
	GOOS() string
	Privileged() bool
	ReadFile(name string) ([]byte, error)
	Stat(name string) (bool, error)
	LookPath(file string) bool
	Run(ctx context.Context, name string, args ...string) (string, error)
	PortOpen(addr string) bool
}

// Ensure collects the local SSH server state and applies the plan when this
// process is privileged. Status lines are always returned, including on error.
func Ensure(ctx context.Context, addr string) (Report, error) {
	return EnsureWith(ctx, realProbe{}, addr)
}

// EnsureWith is Ensure with an injected probe.
func EnsureWith(ctx context.Context, p Probe, addr string) (Report, error) {
	snap, err := Collect(ctx, p, addr)
	if err != nil {
		return Report{Lines: []string{err.Error()}}, err
	}
	rep, planErr := Plan(snap)
	if len(rep.Commands) == 0 || !snap.Privileged {
		return rep, planErr
	}
	for _, c := range rep.Commands {
		if len(c.Argv) == 0 {
			continue
		}
		out, err := p.Run(ctx, c.Argv[0], c.Argv[1:]...)
		if err != nil {
			rep.Lines = append(rep.Lines, c.Display+": "+oneLine(out, err))
			return rep, err
		}
	}
	snap, err = Collect(ctx, p, addr)
	if err != nil {
		return rep, err
	}
	after, afterErr := Plan(snap)
	// Keep the lines that describe what we changed, then the final state.
	after.Lines = append(rep.Lines, after.Lines...)
	return after, afterErr
}

func oneLine(out string, err error) string {
	msg := strings.TrimSpace(out)
	if msg == "" {
		return err.Error()
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// Collect builds a Snapshot from p.
func Collect(ctx context.Context, p Probe, addr string) (Snapshot, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:22"
	}
	snap := Snapshot{
		GOOS:          p.GOOS(),
		Privileged:    p.Privileged(),
		LocalPortOpen: p.PortOpen(addr),
		Addr:          addr,
	}
	switch snap.GOOS {
	case "windows":
		return collectWindows(ctx, p, snap)
	case "darwin":
		return collectDarwin(ctx, p, snap)
	default:
		return collectLinux(ctx, p, snap)
	}
}

func collectWindows(ctx context.Context, p Probe, snap Snapshot) (Snapshot, error) {
	script := `$ErrorActionPreference = 'SilentlyContinue'
$svc = Get-Service -Name sshd
if (-not $svc) {
  'INSTALLED=no'
  'START='
  'STATUS='
  exit 0
}
'INSTALLED=yes'
('START=' + $svc.StartType)
('STATUS=' + $svc.Status)
`
	out, err := p.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil && strings.TrimSpace(out) == "" {
		return snap, err
	}
	snap.Installed, snap.StartType, snap.Status = ParseWindowsStatus(out)
	return snap, nil
}

func collectDarwin(ctx context.Context, p Probe, snap Snapshot) (Snapshot, error) {
	out, err := p.Run(ctx, "systemsetup", "-getremotelogin")
	if err != nil && strings.TrimSpace(out) == "" {
		snap.RemoteLogin = "unknown"
		return snap, nil
	}
	snap.RemoteLogin = ParseRemoteLogin(out)
	return snap, nil
}

func collectLinux(ctx context.Context, p Probe, snap Snapshot) (Snapshot, error) {
	if b, err := p.ReadFile("/etc/os-release"); err == nil {
		snap.Family = FamilyFromOSRelease(string(b))
		if snap.Family == "dnf" && !p.LookPath("dnf") && p.LookPath("yum") {
			snap.Family = "yum"
		}
	}
	snap.HasSSHD = p.LookPath("sshd")
	for _, path := range []string{"/usr/sbin/sshd", "/usr/bin/sshd"} {
		if ok, _ := p.Stat(path); ok {
			snap.HasSSHD = true
		}
	}
	snap.HasSystemctl = p.LookPath("systemctl")
	snap.HasOpenRC = p.LookPath("rc-service") || p.LookPath("rc-update")
	candidates := []string{"sshd", "ssh"}
	if snap.Family == "apt" {
		candidates = []string{"ssh", "sshd"}
	}
	if snap.HasSystemctl {
		for _, name := range candidates {
			activeOut, activeErr := p.Run(ctx, "systemctl", "is-active", name)
			enabledOut, enabledErr := p.Run(ctx, "systemctl", "is-enabled", name)
			active := activeErr == nil && strings.TrimSpace(activeOut) == "active"
			enabled := enabledErr == nil && (strings.TrimSpace(enabledOut) == "enabled" || strings.TrimSpace(enabledOut) == "static" || strings.TrimSpace(enabledOut) == "alias")
			missing := strings.Contains(activeOut, "not-found") || strings.Contains(enabledOut, "not-found") || strings.Contains(strings.ToLower(activeOut+enabledOut), "could not be found")
			if missing && !active && !enabled {
				continue
			}
			snap.Service = name
			snap.ServiceActive = active
			snap.ServiceEnabled = enabled
			break
		}
	} else if snap.HasOpenRC {
		name := "sshd"
		out, err := p.Run(ctx, "rc-service", name, "status")
		snap.Service = name
		snap.ServiceActive = err == nil && (strings.Contains(strings.ToLower(out), "started") || strings.Contains(strings.ToLower(out), "running"))
		enabledOut, enabledErr := p.Run(ctx, "rc-update", "show")
		snap.ServiceEnabled = enabledErr == nil && strings.Contains(enabledOut, name)
	}
	if snap.Service == "" {
		if snap.Family == "apt" {
			snap.Service = "ssh"
		} else {
			snap.Service = "sshd"
		}
	}
	return snap, nil
}

type realProbe struct{}

func (realProbe) GOOS() string { return runtime.GOOS }

func (realProbe) Privileged() bool { return privileged() }

func (realProbe) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (realProbe) Stat(name string) (bool, error) {
	_, err := os.Stat(name)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (realProbe) LookPath(file string) bool {
	_, err := exec.LookPath(file)
	return err == nil
}

func (realProbe) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (realProbe) PortOpen(addr string) bool {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
