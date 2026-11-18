package sshaddr

import "path/filepath"

func match(pattern, name string) (bool, error) {
	return filepath.Match(pattern, name)
}
