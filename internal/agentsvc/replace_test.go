package agentsvc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFilesRollsBack(t *testing.T) {
	dir := t.TempDir()
	dstKite := filepath.Join(dir, "kite.exe")
	dstAgent := filepath.Join(dir, "kite-agent.exe")
	if err := os.WriteFile(dstKite, []byte("old-kite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstAgent, []byte("old-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "staged")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "kite.exe"), []byte("new-kite"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := replaceFiles([][2]string{
		{filepath.Join(src, "kite.exe"), dstKite},
		{filepath.Join(src, "missing-agent.exe"), dstAgent},
	})
	if err == nil {
		t.Fatal("expected the missing agent binary to fail the swap")
	}
	if got := readFile(t, dstKite); got != "old-kite" {
		t.Fatalf("kite.exe = %q, want the previous bytes", got)
	}
	if got := readFile(t, dstAgent); got != "old-agent" {
		t.Fatalf("kite-agent.exe = %q, want the previous bytes", got)
	}
}

func TestReplaceFilesCommitsBoth(t *testing.T) {
	dir := t.TempDir()
	dstKite := filepath.Join(dir, "kite.exe")
	dstAgent := filepath.Join(dir, "kite-agent.exe")
	if err := os.WriteFile(dstKite, []byte("old-kite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstAgent, []byte("old-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "staged")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "kite.exe"), []byte("new-kite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "kite-agent.exe"), []byte("new-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup, err := replaceFiles([][2]string{
		{filepath.Join(src, "kite.exe"), dstKite},
		{filepath.Join(src, "kite-agent.exe"), dstAgent},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := readFile(t, dstKite); got != "new-kite" {
		t.Fatalf("kite.exe = %q", got)
	}
	if got := readFile(t, dstAgent); got != "new-agent" {
		t.Fatalf("kite-agent.exe = %q", got)
	}
	if _, err := os.Stat(dstKite + ".prev"); !os.IsNotExist(err) {
		t.Fatalf("backup left behind: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
