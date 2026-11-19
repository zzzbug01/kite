package agentsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitUpgradeResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "upgrade.log")
	if err := os.WriteFile(logPath, []byte("old upgrade ok: ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	offset := int64(len("old upgrade ok: ignore\n"))
	go func() {
		time.Sleep(30 * time.Millisecond)
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString("upgrade failed: token downgrade refused\n")
		f.Close()
	}()
	err := waitUpgradeResult(logPath, offset, time.Second)
	if err == nil || !strings.Contains(err.Error(), "token downgrade refused") {
		t.Fatalf("got %v", err)
	}

	if err := os.WriteFile(logPath, []byte("upgrade ok: kite-agent running\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := waitUpgradeResult(logPath, 0, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRemovePrivateStage(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "kite-upgrade-abc")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(stage, "kite-agent.exe")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "kept")
	if err := os.Mkdir(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	removePrivateStage(exe)
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage still present: %v", err)
	}
	removePrivateStage(filepath.Join(keep, "kite-agent.exe"))
	if _, err := os.Stat(keep); err != nil {
		t.Fatal(err)
	}
}
