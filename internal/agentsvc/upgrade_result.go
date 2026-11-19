package agentsvc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// waitUpgradeResult blocks until the upgrade log, past offset, records
// success or failure. The scheduled task writes those lines and then exits.
func waitUpgradeResult(path string, offset int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			text := string(b)
			if offset > 0 {
				if int64(len(text)) <= offset {
					text = ""
				} else {
					text = text[offset:]
				}
			}
			if strings.Contains(text, "upgrade ok:") {
				return nil
			}
			if i := strings.Index(text, "upgrade failed:"); i >= 0 {
				line := text[i:]
				if nl := strings.IndexByte(line, '\n'); nl >= 0 {
					line = line[:nl]
				}
				return errors.New(strings.TrimSpace(line))
			}
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the upgrade result in " + path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// removePrivateStage deletes a kite-upgrade-* download directory.
// A path the operator passed to --from is left alone.
func removePrivateStage(from string) {
	dir := filepath.Dir(from)
	if !strings.Contains(filepath.Base(dir), "kite-upgrade-") {
		return
	}
	var err error
	for i := 0; i < 40; i++ {
		err = os.RemoveAll(dir)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
