//go:build unix

package sshheal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/zzzbug01/kite/internal/sshaddr"
)

// OS is the real sshd control surface.
type OS struct{}

func (OS) Running(ctx context.Context) (bool, error) {
	ls, err := sshaddr.Listeners(ctx)
	if err == nil && len(ls) > 0 {
		return true, nil
	}
	if runtime.GOOS == "darwin" {
		return false, err
	}
	for _, name := range []string{"ssh", "sshd"} {
		if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", name).Run() == nil {
			return true, nil
		}
	}
	return false, nil
}

func (OS) Start(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("not root; cannot start sshd")
	}
	if runtime.GOOS == "darwin" {
		// Remote Login is a machine setting. Runtime self-heal must not turn
		// it on by itself. setup and install still print the command.
		return fmt.Errorf("Remote Login is off; enable it with: sudo systemsetup -setremotelogin on")
	}
	var last error
	for _, name := range []string{"ssh", "sshd"} {
		out, err := exec.CommandContext(ctx, "systemctl", "enable", "--now", name).CombinedOutput()
		if err == nil {
			return nil
		}
		last = fmt.Errorf("systemctl enable --now %s: %w (%s)", name, err, out)
	}
	if last == nil {
		last = fmt.Errorf("systemctl was not able to start sshd")
	}
	return last
}

func (OS) Detect(ctx context.Context) (string, error) {
	out := sshaddr.Detect(ctx)
	if !out.Confident || out.Addr == "" {
		if out.Detail == "" {
			return "", fmt.Errorf("local ssh detection was inconclusive")
		}
		return "", fmt.Errorf("%s", out.Detail)
	}
	return out.Addr, nil
}
