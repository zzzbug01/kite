//go:build !windows

package agentsvc

import "os"

func protectDir(path string) error {
	return os.Chmod(path, 0o700)
}

func protectConfig(path string) error {
	return os.Chmod(path, 0o600)
}

func protectLog(path string) error {
	return os.Chmod(path, 0o644)
}

func replaceFile(tmp, dst string) error {
	return os.Rename(tmp, dst)
}
