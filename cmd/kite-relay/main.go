// Command kite-relay accepts agent and client WebSockets and splices SSH streams.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/process"
	"github.com/zzzbug01/kite/internal/relay"
	"github.com/zzzbug01/kite/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "kite-relay: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "tokens" {
		return runTokens(args[1:])
	}
	cfg, err := config.LoadRelay(config.PeekConfigFlag(args))
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("kite-relay", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.String("config", "", "YAML config file (or KITE_CONFIG). Keep it gitignored; do not commit secrets")
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "listen address (env LISTEN_ADDR)")
	fs.StringVar(&cfg.RegistrationKey, "registration-key", cfg.RegistrationKey, "shared registration key (env REGISTRATION_KEY)")
	fs.StringVar(&cfg.RegistrationKeyPrevious, "registration-key-previous", cfg.RegistrationKeyPrevious, "previous registration key still accepted during rotation (env REGISTRATION_KEY_PREVIOUS)")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "directory for hashed client tokens (env STATE_DIR)")
	fs.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile, "TLS certificate (env TLS_CERT_FILE); plain WS if unset")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile, "TLS private key (env TLS_KEY_FILE)")
	idle := fs.Duration("idle-timeout", cfg.IdleTimeout, "close a connection after this long with no received frames (env IDLE_TIMEOUT)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite-relay [flags]\n\n")
		fmt.Fprintf(fs.Output(), "Listens for kite agents and clients. Default is plain WebSocket on 127.0.0.1:8080\n")
		fmt.Fprintf(fs.Output(), "for nginx or Caddy to terminate TLS. Set REGISTRATION_KEY in the environment\n")
		fmt.Fprintf(fs.Output(), "or a gitignored config file. Never commit it.\n\n")
		fmt.Fprintf(fs.Output(), "  kite-relay tokens list\n")
		fmt.Fprintf(fs.Output(), "  kite-relay tokens revoke <id-or-label>\n\n")
		fmt.Fprintf(fs.Output(), "Client tokens issued by kite connect are stored as hashes in STATE_DIR\n")
		fmt.Fprintf(fs.Output(), "(default /var/lib/kite). REGISTRATION_KEY_PREVIOUS is accepted during\n")
		fmt.Fprintf(fs.Output(), "a key rotation so agents can move one at a time.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Printf("kite-relay %s\n", version.Version)
		return nil
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	cfg.IdleTimeout = *idle
	if err := config.ValidateKey(cfg.RegistrationKey); err != nil {
		return err
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return errors.New("TLS_CERT_FILE and TLS_KEY_FILE must both be set to serve TLS, or both be unset for plain WS behind a reverse proxy")
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), process.ShutdownSignals()...)
	defer stop()

	slog.Info("kite-relay listening", "version", version.Version, "addr", ln.Addr().String(), "tls", cfg.TLSCertFile != "")
	srv := relay.New(relay.Config{
		RegistrationKey: cfg.RegistrationKey,
		PreviousKey:     cfg.RegistrationKeyPrevious,
		StateDir:        cfg.StateDir,
		IdleTimeout:     cfg.IdleTimeout,
	})
	return srv.Serve(ctx, ln, cfg.TLSCertFile, cfg.TLSKeyFile)
}
