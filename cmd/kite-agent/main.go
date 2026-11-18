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
	"runtime"
	"strings"

	"github.com/zzzbug01/kite/internal/agent"
	"github.com/zzzbug01/kite/internal/agentsvc"
	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/process"
	"github.com/zzzbug01/kite/internal/sshaddr"
	"github.com/zzzbug01/kite/internal/sshheal"
	"github.com/zzzbug01/kite/internal/sshsetup"
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
	case agentsvc.CmdSetup:
		return runSetup(rest)
	case agentsvc.CmdInstall:
		opt, done, err := ready(rest, true)
		if done || err != nil {
			return err
		}
		if !opt.SkipSSHSetup {
			rep, setupErr := sshsetup.Ensure(context.Background(), opt.Config.LocalSSHAddr)
			fmt.Fprint(os.Stdout, rep.Text())
			if setupErr != nil {
				return setupErr
			}
		}
		if opt.Config.SSHUser == "" || agent.IsMachineAccount(opt.Config.SSHUser) {
			if u := defaultSSHUser(); u != "" {
				opt.Config.SSHUser = u
			}
		}
		if opt.Config.SSHUser == "" {
			fmt.Fprintln(os.Stdout, "ssh user: not set; set SSH_USER before pairing if this process runs as root or LocalSystem")
		} else {
			fmt.Fprintf(os.Stdout, "ssh user: %s\n", opt.Config.SSHUser)
		}
		if !opt.Config.LocalExplicit {
			out := sshaddr.Detect(context.Background())
			if out.Detail != "" {
				fmt.Fprintln(os.Stdout, out.Detail)
			}
			if out.Confident && out.Addr != "" {
				opt.Config.LocalSSHAddr = out.Addr
				opt.Config.LocalSSHAuto = true
				fmt.Fprintf(os.Stdout, "local ssh: %s (auto)\n", out.Addr)
			} else {
				fmt.Fprintf(os.Stdout, "local ssh: not marking %s as auto; set LOCAL_SSH_ADDR if that port is wrong\n", opt.Config.LocalSSHAddr)
			}
		} else {
			fmt.Fprintf(os.Stdout, "local ssh: %s (configured)\n", opt.Config.LocalSSHAddr)
		}
		return agentsvc.Install(opt)
	case agentsvc.CmdPair:
		return runPair(rest)
	case agentsvc.CmdUnpair:
		return runUnpair(rest)
	case agentsvc.CmdUninstall:
		name, done, err := controlName("uninstall", rest)
		if done || err != nil {
			return err
		}
		return agentsvc.Uninstall(name)
	case agentsvc.CmdStart:
		name, done, err := controlName("start", rest)
		if done || err != nil {
			return err
		}
		return agentsvc.Start(name)
	case agentsvc.CmdStop:
		name, done, err := controlName("stop", rest)
		if done || err != nil {
			return err
		}
		return agentsvc.Stop(name)
	case agentsvc.CmdRestart:
		name, done, err := controlName("restart", rest)
		if done || err != nil {
			return err
		}
		return agentsvc.Restart(name)
	case agentsvc.CmdStatus:
		name, cfgPath, done, err := controlOptions("status", rest)
		if done || err != nil {
			return err
		}
		var text string
		if runtime.GOOS == "windows" {
			var err error
			text, err = agentsvc.Status(name)
			if err != nil {
				return err
			}
		} else {
			text = "Instance: " + name + "\nStart and stop this instance with systemctl on Linux or launchctl on macOS.\n"
		}
		paths := agentsvc.StatusHealthPaths(name, cfgPath)
		fmt.Fprint(os.Stdout, text)
		fmt.Fprintln(os.Stdout, sshheal.LoadFirst(paths).Text())
		return nil
	case agentsvc.CmdList:
		if err := agentsvc.ParseList(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		text, err := agentsvc.List()
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stdout, text)
		return nil
	case agentsvc.CmdUpgrade:
		opt, err := agentsvc.ParseUpgrade(rest)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
		rememberSSHUser(opt.ServiceName, "")
		return agentsvc.Upgrade(opt)
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
		case "upgrade":
			_, err := agentsvc.ParseUpgrade([]string{"-h"})
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		case "list":
			if err := agentsvc.ParseList([]string{"-h"}); err != nil && !errors.Is(err, flag.ErrHelp) {
				return err
			}
			return nil
		case "setup":
			return runSetup([]string{"-h"})
		case "pair":
			return runPair([]string{"-h"})
		case "unpair":
			return runUnpair([]string{"-h"})
		case "status", "start", "stop", "restart", "uninstall":
			_, err := agentsvc.ParseControl(strings.ToLower(args[0]), []string{"-h"})
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

func controlOptions(command string, args []string) (string, string, bool, error) {
	name, cfg, err := agentsvc.ParseControlOptions(command, args)
	if errors.Is(err, flag.ErrHelp) {
		return "", "", true, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return name, cfg, false, nil
}

func controlName(command string, args []string) (string, bool, error) {
	name, _, done, err := controlOptions(command, args)
	return name, done, err
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
	serviceName := agentsvc.ServiceNameFromArgs(args)
	logPath := agentsvc.LogPath(config.PeekConfigFlag(args))
	if f, err := agentsvc.OpenLog(logPath); err != nil {
		agentsvc.LogServiceError(serviceName, fmt.Errorf("open log %s: %w", logPath, err))
	} else {
		defer f.Close()
		slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	opt, err := agentsvc.ParseOptions(args, false)
	if err != nil {
		slog.Error("kite-agent configuration", "err", err)
		agentsvc.LogServiceError(serviceName, err)
		return err
	}
	if opt.ShowVersion {
		fmt.Printf("kite-agent %s\n", version.Version)
		return nil
	}
	slog.Info("kite-agent service starting", "version", version.Version, "name", opt.Config.Name, "local", opt.Config.LocalSSHAddr, "service", opt.ServiceName)
	return agentsvc.Serve(opt.ServiceName, func(ctx context.Context) error {
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
		SSHUser:         opt.Config.SSHUser,
		EnableControl:   true,
		ConfigPath:      opt.ConfigPath,
		LocalSSHAuto:    opt.Config.LocalSSHAuto,
		EnableHeal:      true,
		ServiceName:     opt.ServiceName,
	})
}
