package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/zzzbug01/kite/internal/invite"
)

func runAgents(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kite-relay agents list | kite-relay agents revoke <name-or-id>")
	}
	switch args[0] {
	case "list":
		err := agentsList(args[1:])
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "revoke":
		err := agentsRevoke(args[1:])
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, "Usage: kite-relay agents list [--state-dir dir]\n       kite-relay agents revoke [--state-dir dir] <name-or-id>\n\nAgent tokens are the hashes in STATE_DIR/install.json issued by install codes.\nrevoke accepts the NAME or the ID column from agents list.\nRevoking one does not remove that machine's SSH keys or client tokens.\n")
		return nil
	default:
		return fmt.Errorf("unknown agents command %q", args[0])
	}
}

func agentsList(args []string) error {
	dir, err := stateDirFlag("kite-relay agents list", args, false)
	if err != nil {
		return err
	}
	recs, err := invite.Open(dir).List()
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Fprintln(os.Stdout, "No agent tokens.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tCREATED")
	for _, rec := range recs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", rec.Name, rec.ID, rec.CreatedAt.UTC().Format(time.RFC3339))
	}
	return w.Flush()
}

func agentsRevoke(args []string) error {
	dir, rest, err := stateDirFlagRest("kite-relay agents revoke", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: kite-relay agents revoke <name-or-id>")
	}
	rec, err := invite.Open(dir).Revoke(rest[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Revoked agent token %s (%s).\n", rec.ID, rec.Name)
	fmt.Fprintln(os.Stdout, "That machine can no longer connect until it is installed again.")
	fmt.Fprintln(os.Stdout, "SSH keys and kite-relay client tokens were not removed.")
	return nil
}
