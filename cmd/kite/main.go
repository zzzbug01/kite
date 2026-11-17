// Command kite is an OpenSSH ProxyCommand client for a kite relay.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/zzzbug01/kite/internal/client"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/process"
	"github.com/zzzbug01/kite/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "kite: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadClient(config.PeekConfigFlag(args))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("kite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.String("config", "", "YAML config file (or KITE_CONFIG). Keep it gitignored; do not commit secrets")
	fs.StringVar(&cfg.RelayAddr, "relay", cfg.RelayAddr, "relay WebSocket URL (env RELAY_ADDR), for example wss://tunnel.example.com/client")
	fs.StringVar(&cfg.RegistrationKey, "registration-key", cfg.RegistrationKey, "shared registration key (env REGISTRATION_KEY)")
	idle := fs.Duration("idle-timeout", cfg.IdleTimeout, "close after this long with no received frames (env IDLE_TIMEOUT)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite [flags] <agent-name>\n\n")
		fmt.Fprintf(fs.Output(), "OpenSSH ProxyCommand client. The agent name is the ssh Host alias (%%n).\n")
		fmt.Fprintf(fs.Output(), "OpenSSH does not expand environment variables inside ProxyCommand.\n")
		fmt.Fprintf(fs.Output(), "Set RELAY_ADDR and REGISTRATION_KEY in the environment (for example your\n")
		fmt.Fprintf(fs.Output(), "shell profile) or a gitignored config file. Never commit them.\n\n")
		fmt.Fprintf(fs.Output(), "    Host my-pc\n")
		fmt.Fprintf(fs.Output(), "        User me\n")
		fmt.Fprintf(fs.Output(), "        ProxyCommand kite %%n\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Printf("kite %s\n", version.Version)
		return nil
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("agent name is required")
	}
	cfg.IdleTimeout = *idle
	if err := config.ValidateKey(cfg.RegistrationKey); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), process.ShutdownSignals()...)
	defer stop()

	st, err := client.Dial(ctx, client.Config{
		RelayURL:        cfg.RelayAddr,
		RegistrationKey: cfg.RegistrationKey,
		Agent:           fs.Arg(0),
		IdleTimeout:     cfg.IdleTimeout,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	return client.Pipe(ctx, st, os.Stdin, os.Stdout)
}
