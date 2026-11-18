package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/pairing"
)

func runTokens(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kite-relay tokens list | kite-relay tokens revoke <id-or-label>")
	}
	switch args[0] {
	case "list":
		return tokensList(args[1:])
	case "revoke":
		return tokensRevoke(args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, "Usage: kite-relay tokens list [--state-dir dir]\n       kite-relay tokens revoke [--state-dir dir] <id-or-label>\n")
		return nil
	default:
		return fmt.Errorf("unknown tokens command %q", args[0])
	}
}

func tokensList(args []string) error {
	dir, err := stateDirFlag("kite-relay tokens list", args, false)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	recs, err := pairing.Open(dir).List()
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Fprintln(os.Stdout, "No client tokens.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGENT\tLABEL\tSSH_USER\tCREATED")
	for _, rec := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", rec.ID, rec.Agent, rec.Label, empty(rec.SSHUser), rec.CreatedAt.UTC().Format(time.RFC3339))
	}
	return w.Flush()
}

func tokensRevoke(args []string) error {
	dir, rest, err := stateDirFlagRest("kite-relay tokens revoke", args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: kite-relay tokens revoke <id-or-label>")
	}
	rec, err := pairing.Open(dir).Revoke(rest[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Revoked %s (%s on %s).\n", rec.ID, rec.Label, rec.Agent)
	fmt.Fprintf(os.Stdout, "The SSH key may still be installed. On that machine: kite-agent unpair --revoke %s\n", rec.Label)
	return nil
}

func stateDirFlag(name string, args []string, wantArg bool) (string, error) {
	dir, rest, err := stateDirFlagRest(name, args)
	if err != nil {
		return "", err
	}
	if !wantArg && len(rest) != 0 {
		return "", errors.New("unexpected arguments")
	}
	return dir, nil
}

func stateDirFlagRest(name string, args []string) (string, []string, error) {
	cfg, err := config.LoadRelay(config.PeekConfigFlag(args))
	if err != nil {
		return "", nil, err
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("state-dir", cfg.StateDir, "directory containing clients.json (env STATE_DIR)")
	fs.String("config", "", "YAML config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", nil, err
		}
		return "", nil, err
	}
	return *dir, fs.Args(), nil
}

func empty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
