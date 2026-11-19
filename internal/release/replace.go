package release

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallFile replaces dest with data. On Windows a running executable cannot
// be overwritten, so dest is renamed to dest.old and the new file takes its
// place. The next process start removes dest.old. On other systems the
// replacement is an atomic rename in the same directory.
func InstallFile(dest string, data []byte) error {
	return installFile(dest, data, runtime.GOOS == "windows")
}

func installFile(dest string, data []byte, windows bool) error {
	if len(data) == 0 {
		return fmt.Errorf("new binary is empty")
	}
	dest = filepath.Clean(dest)
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".kite-new-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp, 0o755); err != nil && !windows {
		os.Remove(tmp)
		return err
	}
	if windows {
		old := dest + ".old"
		_ = os.Remove(old)
		if _, err := os.Stat(dest); err == nil {
			if err := os.Rename(dest, old); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("rename running binary aside: %w", err)
			}
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.Rename(old, dest)
			os.Remove(tmp)
			return fmt.Errorf("move new binary into place: %w", err)
		}
		return nil
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(dest, 0o755)
}

// CleanupOld removes the previous executable left by an upgrade.
// Windows renames the running exe to dest.old. The service swap renames it
// to dest.prev and removes that after a successful swap; a start still
// deletes a leftover so the instance folder keeps one binary.
func CleanupOld(exe string) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return
	}
	_ = os.Remove(exe + ".prev")
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}
}
