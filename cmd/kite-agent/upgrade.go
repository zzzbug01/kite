package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zzzbug01/kite/internal/release"
	"github.com/zzzbug01/kite/internal/version"
)

type fetchedBinary struct {
	path string
	done bool
}

func fetchRelease(ctx context.Context, command, ver string, check bool) (fetchedBinary, error) {
	got, err := release.Prepare(ctx, command, ver, version.Version, check)
	if err != nil {
		return fetchedBinary{}, err
	}
	fmt.Fprintln(os.Stdout, got.Message)
	if len(got.Binary) == 0 {
		return fetchedBinary{done: true}, nil
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	path := filepath.Join(os.TempDir(), "kite-upgrade-"+command+ext)
	if err := os.WriteFile(path, got.Binary, 0o755); err != nil {
		return fetchedBinary{}, err
	}
	return fetchedBinary{path: path}, nil
}
