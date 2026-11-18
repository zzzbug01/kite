//go:build windows

package sshheal

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/zzzbug01/kite/internal/sshaddr"
)

// OS is the real sshd control surface. The service account can start sshd;
// a non-elevated foreground process gets the command's error.
type OS struct{}

func (OS) Running(ctx context.Context) (bool, error) {
	ls, err := sshaddr.Listeners(ctx)
	if err == nil && len(ls) > 0 {
		return true, nil
	}
	out, qerr := exec.CommandContext(ctx, "sc.exe", "query", "sshd").CombinedOutput()
	if qerr != nil {
		return false, nil
	}
	return containsRunning(string(out)), err
}

func containsRunning(s string) bool {
	for _, line := range splitLines(s) {
		if contains(line, "RUNNING") {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (OS) Start(ctx context.Context) error {
	// Always Automatic. A Manual service is the reboot bug: kite is up and
	// 127.0.0.1:22 is refused.
	if out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"Set-Service -Name sshd -StartupType Automatic; Start-Service sshd").CombinedOutput(); err != nil {
		return fmt.Errorf("start sshd: %w (%s)", err, out)
	}
	return nil
}

func (OS) Detect(ctx context.Context) (string, error) {
	return sshaddr.Detect(ctx)
}
