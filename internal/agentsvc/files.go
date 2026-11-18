package agentsvc

import (
	"fmt"
	"os"
	"path/filepath"
)

// writePrivateFile creates path with permissions that hide a registration key
// from other users on the machine. On Windows the DACL grants SYSTEM,
// Administrators, and the installing user. On Unix the mode is 0600.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := protectDir(dir); err != nil {
		return fmt.Errorf("set permissions on %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := protectConfig(tmp); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("set permissions on %s: %w", tmp, err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if err := protectConfig(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("set permissions on %s: %w", tmp, err)
	}
	if err := replaceFile(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := protectConfig(path); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}
