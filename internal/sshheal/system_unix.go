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
		out, err := exec.CommandContext(ctx, "systemsetup", "-setremotelogin", "on").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemsetup -setremotelogin on: %w (%s)", err, out)
		}
		return nil
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
	return sshaddr.Detect(ctx)
}
