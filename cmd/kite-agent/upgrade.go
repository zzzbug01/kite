package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"strings"

	"github.com/zzzbug01/kite/internal/agentsvc"
	"github.com/zzzbug01/kite/internal/release"
	"github.com/zzzbug01/kite/internal/version"
)

type fetchedBinary struct {
	path string
	tag  string
	done bool
}

func fetchRelease(ctx context.Context, command, ver string, check bool) (fetchedBinary, error) {
	got, err := release.Prepare(ctx, command, ver, version.Version, check)
	if err != nil {
		return fetchedBinary{}, err
	}
	fmt.Fprintln(os.Stdout, got.Message)
	if len(got.Binary) == 0 {
		return fetchedBinary{done: true, tag: got.Tag}, nil
	}
	dir, err := os.MkdirTemp("", "kite-upgrade-")
	if err != nil {
		return fetchedBinary{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil && runtime.GOOS != "windows" {
		os.RemoveAll(dir)
		return fetchedBinary{}, err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	f, err := os.CreateTemp(dir, command+"-*"+ext)
	if err != nil {
		os.RemoveAll(dir)
		return fetchedBinary{}, err
	}
	path := f.Name()
	if _, err := f.Write(got.Binary); err != nil {
		f.Close()
		os.RemoveAll(dir)
		return fetchedBinary{}, err
	}
	if err := f.Chmod(0o755); err != nil && runtime.GOOS != "windows" {
		f.Close()
		os.RemoveAll(dir)
		return fetchedBinary{}, err
	}
	if err := f.Close(); err != nil {
		os.RemoveAll(dir)
		return fetchedBinary{}, err
	}
	return fetchedBinary{path: path, tag: got.Tag}, nil
}

func guardTokenDowngrade(opt agentsvc.UpgradeOptions, tag string) error {
	if opt.Force || tag == "" || release.SupportsAgentToken(tag) {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("KITE_CONFIG"))
	if path == "" {
		if lay, err := agentsvc.DefaultLayout(opt.ServiceName); err == nil {
			path = lay.ConfigPath
		}
	}
	token, key, err := installSecrets(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if token == "" {
		return nil
	}
	if key != "" && key != "CHANGE_ME" {
		return nil
	}
	return fmt.Errorf("%s does not understand agent tokens (added after v0.1.0-rc.3). This install has only an agent token, so the downgrade would leave the agent unable to connect. Pass --force to do it anyway", tag)
}

// installSecrets reads the agent token and registration key from a YAML
// config or from an EnvironmentFile. The process environment is ignored:
// the service does not see the shell that ran upgrade.
func installSecrets(path string) (token, key string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := splitSetting(line)
		if !ok || v == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "agent_token":
			token = v
		case "registration_key":
			key = v
		}
	}
	return token, key, nil
}

func splitSetting(line string) (key, val string, ok bool) {
	i := strings.IndexAny(line, "=:")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	val = strings.TrimSpace(line[i+1:])
	val = strings.Trim(val, `"'`)
	return key, val, key != ""
}
