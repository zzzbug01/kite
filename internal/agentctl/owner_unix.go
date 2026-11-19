//go:build !windows

package agentctl

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// ShareControlFile lets the installing user read the control file so
// `kite-agent pair` does not need a second sudo. The directory stays
// traversable (0711) and the file stays 0600, owned by that user. Root
// can still rewrite it.
func ShareControlFile(path, username string) error {
	username = strings.TrimSpace(username)
	if username == "" || username == "root" {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		_ = chmodStateChain(dir)
	}
	return os.Chown(path, uid, gid)
}
