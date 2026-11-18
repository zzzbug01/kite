// Command kite-agent dials a kite relay and forwards streams to the local SSH server.
// On Windows it can also install and control that process as a service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/agentsvc"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/process"
	"github.com/zzzbug01/kite/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "kite-agent: %s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd, rest, err := agentsvc.Split(args)
	if err != nil {
		return err
	}
	switch cmd {
	case agentsvc.CmdHelp:
		return help(rest)
	case agentsvc.CmdRun:
		inService, err := agentsvc.RunningAsService()
		if err != nil {
			return err
		}
		if inService {
			return runService(rest)
		}
		return runForeground(rest)
	case agentsvc.CmdInstall:
		opt, done, err := ready(rest, true)
		if done || err != nil {
			return err
		}
		return agentsvc.Install(opt)
	case agentsvc.CmdUninstall:
		if err := rejectExtra(rest); err != nil {
			return err
		}
		return agentsvc.Uninstall()
	case agentsvc.CmdStart:
		if err := rejectExtra(rest); err != nil {
			return err
		}
		return agentsvc.Start()
	case agentsvc.CmdStop:
		if err := rejectExtra(rest); err != nil {
			return err
		}
		return agentsvc.Stop()
	case agentsvc.CmdRestart:
		if err := rejectExtra(rest); err != nil {
			return err
		}
		return agentsvc.Restart()
	case agentsvc.CmdStatus:
		if err := rejectExtra(rest); err != nil {
			return err
		}
		text, err := agentsvc.Status()
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, text)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, agentsvc.Usage())
	}
}

func help(args []string) error {
	if len(args) == 1 {
		switch strings.ToLower(args[0]) {
		case "install":
			_, err := agentsvc.ParseOptions([]string{"-h"}, true)
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		case "run":
			_, err := agentsvc.ParseOptions([]string{"-h"}, false)
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
	}
	fmt.Fprint(os.Stderr, agentsvc.Usage())
	return nil
}

func ready(args []string, install bool) (agentsvc.Options, bool, error) {
	opt, err := agentsvc.ParseOptions(args, install)
	if errors.Is(err, flag.ErrHelp) {
		return agentsvc.Options{}, true, nil
	}
	if err != nil {
		return agentsvc.Options{}, false, err
	}
	if opt.ShowVersion {
		fmt.Printf("kite-agent %s\n", version.Version)
		return opt, true, nil
	}
	return opt, false, nil
}

func rejectExtra(args []string) error {
	fs := flag.NewFlagSet("kite-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), agentsvc.Usage())
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	return nil
}

func runForeground(args []string) error {
	opt, done, err := ready(args, false)
	if done || err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), process.ShutdownSignals()...)
	defer stop()
	slog.Info("kite-agent starting", "version", version.Version, "name", opt.Config.Name, "local", opt.Config.LocalSSHAddr)
	return runAgent(ctx, opt)
}

func runService(args []string) error {
	logPath := agentsvc.LogPath(config.PeekConfigFlag(args))
	if f, err := agentsvc.OpenLog(logPath); err != nil {
		agentsvc.LogServiceError(fmt.Errorf("open log %s: %w", logPath, err))
	} else {
		defer f.Close()
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	opt, err := agentsvc.ParseOptions(args, false)
	if err != nil {
		slog.Error("kite-agent configuration", "err", err)
		agentsvc.LogServiceError(err)
		return err
	}
	if opt.ShowVersion {
		fmt.Printf("kite-agent %s\n", version.Version)
		return nil
	}
	slog.Info("kite-agent service starting", "version", version.Version, "name", opt.Config.Name, "local", opt.Config.LocalSSHAddr)
	return agentsvc.Serve(func(ctx context.Context) error {
		return runAgent(ctx, opt)
	})
}

func runAgent(ctx context.Context, opt agentsvc.Options) error {
	return agent.Run(ctx, agent.Config{
		RelayURL:        opt.Config.RelayAddr,
		RegistrationKey: opt.Config.RegistrationKey,
		Name:            opt.Config.Name,
		LocalSSHAddr:    opt.Config.LocalSSHAddr,
		IdleTimeout:     opt.Config.IdleTimeout,
	})
}
