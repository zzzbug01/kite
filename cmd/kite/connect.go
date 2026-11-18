package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/connect"
)

func runConnect(args []string) error {
	cfg, err := config.LoadClient(config.PeekConfigFlag(args))
	if err != nil {
		// loadFile wraps the os error, and os.IsNotExist does not unwrap that.
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		cfg = config.Client{}
	}
	fs := flag.NewFlagSet("kite connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := config.PeekConfigFlag(args)
	fs.StringVar(&configPath, "config", configPath, "client config file to write (default ~/.config/kite/config.yaml)")
	relay := fs.String("relay", cfg.RelayAddr, "relay WebSocket URL (env RELAY_ADDR), for example wss://tunnel.example.com/client")
	label := fs.String("label", "", "name recorded on the key (default: this computer's hostname)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite connect [--relay URL] [--label name] <code>\n\n")
		fmt.Fprintf(fs.Output(), "Redeems a pairing code from kite-agent pair. An ed25519 key is generated\n")
		fmt.Fprintf(fs.Output(), "on this computer; only the public key is sent. The private key is saved\n")
		fmt.Fprintf(fs.Output(), "at ~/.ssh/kite_<agent> with mode 0600. A Host block is appended to\n")
		fmt.Fprintf(fs.Output(), "~/.ssh/config unless that Host already exists. The existing file is\n")
		fmt.Fprintf(fs.Output(), "copied aside first.\n\n")
		fmt.Fprintf(fs.Output(), "The code looks like ABCD-EFGH. It is not a registration key, it expires,\n")
		fmt.Fprintf(fs.Output(), "and it works once. The relay address is not inside the code, so pass\n")
		fmt.Fprintf(fs.Output(), "--relay the first time or set RELAY_ADDR.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("pairing code is required")
	}
	if *relay == "" {
		*relay = os.Getenv("RELAY_ADDR")
	}
	if configPath == "" {
		configPath = defaultClientConfig()
	}
	sshPath, err := defaultSSHConfig()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}
	absConfig := configPath
	if abs, err := filepath.Abs(configPath); err == nil {
		absConfig = abs
	}
	proxyConfig := ""
	if explicitConfig(args) && !samePath(absConfig, defaultClientConfig()) {
		proxyConfig = absConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := connect.Run(ctx, connect.Options{
		Code:          fs.Arg(0),
		RelayURL:      *relay,
		Label:         *label,
		ConfigPath:    configPath,
		SSHConfigPath: sshPath,
		Executable:    exe,
		ProxyConfig:   proxyConfig,
		Now:           time.Now(),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Paired with %s as %s.\n", res.Agent, res.User)
	fmt.Fprintf(os.Stdout, "Private key: %s\n", res.Identity)
	if res.KeyBackup != "" {
		fmt.Fprintf(os.Stdout, "Previous key moved to: %s\n", res.KeyBackup)
	}
	fmt.Fprintf(os.Stdout, "Client credential saved in %s\n", res.ConfigPath)
	if res.HostAdded {
		fmt.Fprintf(os.Stdout, "SSH config: added Host %s to %s\n", res.Agent, res.SSHConfig)
		if res.SSHBackup != "" {
			fmt.Fprintf(os.Stdout, "SSH config backup: %s\n", res.SSHBackup)
		}
	} else {
		fmt.Fprintf(os.Stdout, "SSH config: Host %s already exists in %s; left it unchanged.\n", res.Agent, res.SSHConfig)
	}
	fmt.Fprintf(os.Stdout, "\nTry:\n  ssh %s exit\n", res.Agent)
	return nil
}

func explicitConfig(args []string) bool {
	return config.PeekConfigFlag(args) != ""
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func defaultClientConfig() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "kite", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".config", "kite", "config.yaml")
}

func defaultSSHConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}
