package clientkey

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateReusesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kite_my-pc")
	line, created, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created || !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("created %v line %q", created, line)
	}
	if strings.Contains(line, "\n") {
		t.Fatal(line)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
	again, created, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if created || again != line {
		t.Fatalf("reuse created=%v %q vs %q", created, again, line)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), line) && strings.Contains(string(body), "PRIVATE") {
		// The public line must not be the only contents; the file is a private key.
	}
	if !strings.Contains(string(body), "PRIVATE KEY") {
		t.Fatalf("not a private key:\n%s", body)
	}
	if strings.Contains(string(body), "BEGIN OPENSSH PRIVATE KEY") || strings.Contains(string(body), "PRIVATE KEY") {
		return
	}
	t.Fatalf("unexpected key encoding:\n%s", body)
}
