package sshaddr

import (
	"path"
	"path/filepath"
	"strings"
)

func match(pattern, name string) (bool, error) {
	// sshd Include globs keep the slashes of the config. filepath.Match on
	// Windows treats only '\' as a separator, so a '/' pattern would let '*'
	// cross directories or miss the file.
	if strings.Contains(pattern, "/") || strings.Contains(name, "/") {
		return path.Match(pattern, name)
	}
	return filepath.Match(pattern, name)
}
