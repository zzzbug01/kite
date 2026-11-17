// Command kite-agent dials a kite relay and forwards streams to the local SSH server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/process"
	"github.com/zzzbug01/kite/internal/protocol"
	"github.com/zzzbug01/kite/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "kite-agent: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.LoadAgent(config.PeekConfigFlag(args))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("kite-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.String("config", "", "YAML config file (or KITE_CONFIG). Keep it gitignored; do not commit secrets")
	fs.StringVar(&cfg.RelayAddr, "relay", cfg.RelayAddr, "relay WebSocket URL (env RELAY_ADDR), for example wss://tunnel.example.com/agent")
	fs.StringVar(&cfg.RegistrationKey, "registration-key", cfg.RegistrationKey, "shared registration key (env REGISTRATION_KEY)")
	fs.StringVar(&cfg.Name, "name", cfg.Name, "agent name (env AGENT_NAME); must match the ssh Host alias")
	fs.StringVar(&cfg.LocalSSHAddr, "local", cfg.LocalSSHAddr, "local SSH address to dial (env LOCAL_SSH_ADDR). The relay cannot change this")
	idle := fs.Duration("idle-timeout", cfg.IdleTimeout, "reconnect after this long with no received frames (env IDLE_TIMEOUT)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-agent [flags]\n\n")
		fmt.Fprintf(fs.Output(), "Dials the relay and forwards incoming streams only to LOCAL_SSH_ADDR\n")
		fmt.Fprintf(fs.Output(), "(default 127.0.0.1:22). Set REGISTRATION_KEY and RELAY_ADDR via the environment\n")
		fmt.Fprintf(fs.Output(), "or a gitignored config file. Never commit them.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Printf("kite-agent %s\n", version.Version)
		return nil
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	cfg.IdleTimeout = *idle
	if err := config.ValidateKey(cfg.RegistrationKey); err != nil {
		return err
	}
	if !protocol.ValidName(cfg.Name) {
		return errors.New("AGENT_NAME is required and must not contain whitespace")
	}

	ctx, stop := signal.NotifyContext(context.Background(), process.ShutdownSignals()...)
	defer stop()

	slog.Info("kite-agent starting", "version", version.Version, "name", cfg.Name, "local", cfg.LocalSSHAddr)
	return agent.Run(ctx, agent.Config{
		RelayURL:        cfg.RelayAddr,
		RegistrationKey: cfg.RegistrationKey,
		Name:            cfg.Name,
		LocalSSHAddr:    cfg.LocalSSHAddr,
		IdleTimeout:     cfg.IdleTimeout,
	})
}
