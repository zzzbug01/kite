package sshcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeAddsOnceAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	initial := "# keep me\nHost other\n  User x\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	host := Host{
		Alias:        "my-pc",
		User:         "me",
		IdentityFile: filepath.Join(dir, "kite_my-pc"),
		ProxyCommand: filepath.Join(dir, "kite"),
	}
	res, err := Merge(path, host, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Backup == "" {
		t.Fatalf("%+v", res)
	}
	bak, err := os.ReadFile(res.Backup)
	if err != nil || string(bak) != initial {
		t.Fatalf("backup %q %v", bak, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "Host other") || !strings.Contains(text, "# keep me") {
		t.Fatalf("lost existing config:\n%s", text)
	}
	if strings.Count(text, "Host my-pc\n") != 1 {
		t.Fatalf("host count:\n%s", text)
	}
	if !strings.Contains(text, "IdentitiesOnly yes") || !strings.Contains(text, "ProxyCommand ") || !strings.Contains(text, "%n") {
		t.Fatalf("block:\n%s", text)
	}
	if !strings.Contains(text, "User me") {
		t.Fatal(text)
	}

	again, err := Merge(path, host, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed || again.Backup != "" {
		t.Fatalf("second merge changed the file: %+v", again)
	}
	body2, _ := os.ReadFile(path)
	if string(body2) != text {
		t.Fatal("file changed on the second merge")
	}
}

func TestHasHost(t *testing.T) {
	src := "# Host my-pc\nHost *.example\n  User x\nHost my-pc other\n  User me\n"
	if !HasHost(src, "my-pc") {
		t.Fatal("expected exact token")
	}
	if HasHost(src, "my-phone") {
		t.Fatal("pattern must not count")
	}
	if HasHost(src, "example") {
		t.Fatal("comment or glob matched")
	}
}

func TestQuotesSpacedProxy(t *testing.T) {
	block := Block(Host{
		Alias:        "my-pc",
		User:         "me",
		IdentityFile: `C:\Users\me\.ssh\kite_my-pc`,
		ProxyCommand: `C:\Program Files\kite\kite.exe`,
	})
	if !strings.Contains(block, `IdentityFile C:/Users/me/.ssh/kite_my-pc`) {
		t.Fatal(block)
	}
	if !strings.Contains(block, `ProxyCommand "C:/Program Files/kite/kite.exe" %n`) {
		t.Fatal(block)
	}
}

func TestRejectsBadHost(t *testing.T) {
	_, err := Merge(filepath.Join(t.TempDir(), "config"), Host{
		Alias: "has space", User: "me", IdentityFile: "a", ProxyCommand: "b",
	}, time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
}
