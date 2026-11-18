//go:build unix

package sshaddr

import (
	"context"
	"os/exec"
	"runtime"
)

// Listeners returns addresses sshd is currently listening on.
// A missing tool or a permission error yields no addresses so config
// detection still works.
func Listeners(ctx context.Context) ([]string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-c", "sshd").Output()
		if err != nil {
			return nil, nil
		}
		return ParseLsof(string(out)), nil
	}
	out, err := exec.CommandContext(ctx, "ss", "-H", "-ltnp").Output()
	if err != nil {
		return nil, nil
	}
	return ParseSS(string(out)), nil
}
