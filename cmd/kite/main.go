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

func clientUsage() string {
	return `Usage:
  kite [flags] <agent-name>
  kite connect [--relay URL] [--label name] <code>

OpenSSH ProxyCommand client. The agent name is the ssh Host alias (%n).
"connect" is reserved and is not an agent name.

OpenSSH does not expand environment variables inside ProxyCommand.
A paired client reads its per-client token from ~/.config/kite/config.yaml.
REGISTRATION_KEY still works for clients that have not paired.

    Host my-pc
        User me
        IdentityFile ~/.ssh/kite_my-pc
        IdentitiesOnly yes
        ProxyCommand kite %n

kite connect CODE redeems a one-time pairing code from kite-agent pair.
The private key stays on this computer. Pass --relay the first time
(wss://tunnel.example.com); later connects reuse the saved config.

`
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "kite: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "connect" {
		return runConnect(args[1:])
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(os.Stderr, clientUsage())
		return nil
	}
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
		fmt.Fprint(fs.Output(), clientUsage())
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
	secret, authKind, err := cfg.CredentialFor(fs.Arg(0))
	if err != nil {
		return err
	}
	if authKind == "registration" {
		if err := config.ValidateKey(secret); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), process.ShutdownSignals()...)
	defer stop()

	st, err := client.Dial(ctx, client.Config{
		RelayURL:        cfg.RelayAddr,
		RegistrationKey: secret,
		Auth:            authKind,
		Agent:           fs.Arg(0),
		IdleTimeout:     cfg.IdleTimeout,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	return client.Pipe(ctx, st, os.Stdin, os.Stdout)
}
