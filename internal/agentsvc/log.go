package agentsvc

import (
	"os"
	"path/filepath"
)

const logRotateAt = 5 << 20

// OpenLog opens the service log for append. Permissions are best-effort:
// a logging failure must not be the reason the tunnel stays down, but the
// config file written by install is strict.
func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = protectDir(filepath.Dir(path))
	if st, err := os.Stat(path); err == nil && st.Size() > logRotateAt {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	_ = protectLog(path)
	return f, nil
}
