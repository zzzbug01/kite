//go:build windows

package sshaddr

import (
	"context"
	"os/exec"
	"strings"
)

// Listeners returns addresses sshd.exe is listening on.
func Listeners(ctx context.Context) ([]string, error) {
	task, err := exec.CommandContext(ctx, "tasklist", "/FI", "IMAGENAME eq sshd.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, nil
	}
	pids := map[string]bool{}
	for _, line := range strings.Split(string(task), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "INFO:") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		pid := strings.Trim(parts[1], `"`)
		if pid != "" {
			pids[pid] = true
		}
	}
	if len(pids) == 0 {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil, nil
	}
	return ParseNetstat(string(out), pids), nil
}
