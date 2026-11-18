package sshaddr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// Outcome is what install should do with a detected address.
type Outcome struct {
	Addr      string
	Confident bool
	Detail    string
}

// Detect combines sshd_config, process-attributed listeners, and an SSH
// banner probe of other local listeners. Confident is false when the result
// would only be a guess of port 22.
func Detect(ctx context.Context) Outcome {
	sockets, _ := Listeners(ctx)
	p, err := Load(osFS{}, DefaultPath())
	cfgOK := err == nil
	loop, _ := LoopbackListeners()
	return Decide(p, cfgOK, sockets, loop, func(addr string) bool {
		return ProbeBanner(ctx, addr)
	})
}

// Decide picks an address from evidence. banner may be nil.
func Decide(cfg Parsed, cfgOK bool, processSockets, listenSockets []string, banner func(string) bool) Outcome {
	if addr := preferSocket(processSockets); addr != "" {
		return Outcome{Addr: addr, Confident: true, Detail: "local ssh: sshd is listening on " + addr}
	}
	if cfgOK {
		addr := Address(cfg, nil)
		if banner != nil && !banner(addr) {
			return Outcome{Addr: addr, Confident: true, Detail: "local ssh: sshd_config selects " + addr + " (nothing answered yet)"}
		}
		return Outcome{Addr: addr, Confident: true, Detail: "local ssh: sshd_config selects " + addr}
	}
	var hits []string
	seen := map[string]bool{}
	for _, sock := range listenSockets {
		addr := preferOne(sock)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		if banner != nil && banner(addr) {
			hits = append(hits, addr)
		}
	}
	switch len(hits) {
	case 1:
		return Outcome{Addr: hits[0], Confident: true, Detail: "local ssh: SSH banner on " + hits[0]}
	case 0:
		return Outcome{Addr: "127.0.0.1:22", Confident: false, Detail: "local ssh: could not read sshd_config or see an sshd listener; not marking 127.0.0.1:22 as auto-detected"}
	default:
		return Outcome{Addr: "127.0.0.1:22", Confident: false, Detail: "local ssh: several ports answered with an SSH banner (" + strings.Join(hits, ", ") + "); set LOCAL_SSH_ADDR"}
	}
}

func preferOne(sock string) string {
	return preferSocket([]string{sock})
}
