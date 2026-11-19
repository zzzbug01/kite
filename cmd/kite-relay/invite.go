package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/invite"
)

func runInvite(args []string) error {
	cfg, err := config.LoadRelay(config.PeekConfigFlag(args))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("kite-relay invite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ttl := fs.Duration("ttl", invite.DefaultTTL, "how long the install code stays valid (maximum 24h)")
	name := fs.String("name", "", "optional agent name allowed to redeem this code")
	public := fs.String("public-host", publicHost(), "host operators pass to --relay (env KITE_PUBLIC_URL). The example uses tunnel.example.com when this is unset")
	dir := fs.String("state-dir", cfg.StateDir, "directory for install.json (env STATE_DIR)")
	fs.String("config", "", "YAML config file")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-relay invite [--ttl 1h] [--name agent] [--state-dir dir]\n\n")
		fmt.Fprintf(fs.Output(), "Prints a single-use install code. The code is written to\n")
		fmt.Fprintf(fs.Output(), "STATE_DIR/install.json as a hash, under a lock shared with the\n")
		fmt.Fprintf(fs.Output(), "running relay, so it works immediately and survives a restart.\n")
		fmt.Fprintf(fs.Output(), "Run this as root or as the user that owns STATE_DIR.\n")
		fmt.Fprintf(fs.Output(), "Default lifetime is 1h. The maximum is 24h.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *ttl < invite.MinTTL || *ttl > invite.MaxTTL {
		return errors.New("ttl must be between 1 minute and 24 hours")
	}
	code, exp, err := invite.Open(*dir).Issue(*name, *ttl, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Install code: %s\n", code)
	fmt.Fprintf(os.Stdout, "Expires: %s (one time)\n", exp.Local().Format(time.RFC3339))
	if *name != "" {
		fmt.Fprintf(os.Stdout, "Agent: %s\n", *name)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "On the computer to control:")
	host := strings.TrimSpace(*public)
	if host == "" {
		host = "tunnel.example.com"
	}
	fmt.Fprintf(os.Stdout, "  curl -fsSL https://raw.githubusercontent.com/zzzbug01/kite/main/install.sh | sh -s -- --agent --relay %s --code %s\n", host, code)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Windows, from PowerShell (the script relaunches elevated):")
	fmt.Fprintf(os.Stdout, "  $env:KITE_AGENT='1'; $env:KITE_RELAY='%s'; $env:KITE_CODE='%s'; irm https://raw.githubusercontent.com/zzzbug01/kite/main/install.ps1 | iex\n", host, code)
	return nil
}

func publicHost() string {
	for _, key := range []string{"KITE_PUBLIC_URL", "PUBLIC_URL"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			continue
		}
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		v = strings.TrimPrefix(v, "wss://")
		v = strings.TrimPrefix(v, "ws://")
		v = strings.Trim(v, "/")
		if i := strings.IndexByte(v, '/'); i >= 0 {
			v = v[:i]
		}
		return v
	}
	return ""
}
