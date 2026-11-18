package sshaddr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
)

// osFS uses the real filesystem.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

// DefaultPath is the sshd_config for this operating system.
func DefaultPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("ProgramData")
		if root == "" {
			root = `C:\ProgramData`
		}
		return filepath.Join(root, "ssh", "sshd_config")
	}
	return "/etc/ssh/sshd_config"
}

// Detect combines sshd_config with the current sshd listeners.
// A missing config file falls back to the listeners or 127.0.0.1:22.
func Detect(ctx context.Context) (string, error) {
	sockets, _ := Listeners(ctx)
	p, err := Load(osFS{}, DefaultPath())
	if err != nil {
		if len(sockets) == 0 {
			return "127.0.0.1:22", nil
		}
		return Address(Parsed{}, sockets), nil
	}
	return Address(p, sockets), nil
}
