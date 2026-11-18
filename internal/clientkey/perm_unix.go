//go:build unix

package clientkey

import "os"

func tighten(path string) error {
	return os.Chmod(path, 0o600)
}
