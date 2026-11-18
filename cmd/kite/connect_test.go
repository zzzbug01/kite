package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConnectCreatesMissingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("RELAY_ADDR", "")
	t.Setenv("REGISTRATION_KEY", "")
	t.Setenv("USERPROFILE", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pair/redeem" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"agent":        "my-pc",
			"ssh_user":     "me",
			"relay_url":    "ws://tunnel.example.com/client",
			"client_token": "kite_testtoken",
		})
	}))
	defer srv.Close()
	cfg := filepath.Join(home, "nested", "kite.yaml")
	err := runConnect([]string{"--config", cfg, "--relay", srv.URL, "ABCD-EFGH"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(filepath.Dir(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %o", st.Mode().Perm())
		}
		if dir.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %o", dir.Mode().Perm())
		}
	}
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "kite_testtoken") || !strings.Contains(string(body), "my-pc") {
		t.Fatal(string(body))
	}
}
