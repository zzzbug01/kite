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

	"github.com/zzzbug01/kite/internal/release"
	"github.com/zzzbug01/kite/internal/version"
)

func runUpgrade(args []string) error {
	fs := flag.NewFlagSet("kite upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	versionFlag := fs.String("version", "", "release tag to install, for example v0.1.0 (default: newest release)")
	check := fs.Bool("check", false, "print the available version and do not replace this binary")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: kite upgrade [--version vX] [--check]\n\n")
		fmt.Fprintf(fs.Output(), "Downloads the kite binary for this OS from GitHub, checks SHA256SUMS,\n")
		fmt.Fprintf(fs.Output(), "and replaces this executable. Prints the old version and the new one.\n")
		fmt.Fprintf(fs.Output(), "Already running the selected release does nothing. --check does not\n")
		fmt.Fprintf(fs.Output(), "download the binary. HTTPS_PROXY is honored.\n\n")
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
	ver := strings.TrimSpace(*versionFlag)
	if ver == "" {
		ver = strings.TrimSpace(os.Getenv("KITE_VERSION"))
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !*check {
		if err := canWrite(exe); err != nil {
			return fmt.Errorf("%w\nre-run with permission to replace %s", err, exe)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	got, err := release.Prepare(ctx, "kite", ver, version.Version, *check)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, got.Message)
	if len(got.Binary) == 0 {
		return nil
	}
	if err := release.InstallFile(exe, got.Binary); err != nil {
		return fmt.Errorf("%w\nre-run with permission to replace %s", err, exe)
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stdout, "Replaced %s. The previous executable was renamed aside and is removed the next time kite starts.\n", exe)
	} else {
		fmt.Fprintf(os.Stdout, "Replaced %s\n", exe)
	}
	return nil
}

func canWrite(exe string) error {
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".kite-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
