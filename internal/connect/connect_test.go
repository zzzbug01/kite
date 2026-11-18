package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zzzbug01/kite/internal/config"
	"github.com/zzzbug01/kite/internal/pairing"
)

func TestRunWritesConfigAndSSH(t *testing.T) {
	t.Setenv("RELAY_ADDR", "")
	t.Setenv("REGISTRATION_KEY", "")
	t.Setenv("KITE_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var got pairing.RedeemRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pair/redeem" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		if strings.Contains(got.PublicKey, "PRIVATE") || strings.Contains(got.Code, "kite_") {
			http.Error(w, "secret", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(pairing.RedeemResponse{
			Agent:       "my-pc",
			SSHUser:     "me",
			RelayURL:    "wss://tunnel.example.com/client",
			ClientToken: "kite_testtoken",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	sshPath := filepath.Join(dir, "ssh", "config")
	// An existing host must be preserved and backed up.
	if err := os.MkdirAll(filepath.Dir(sshPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sshPath, []byte("Host other\n  User x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{
		Code:          "ABCD-EFGH",
		RelayURL:      srv.URL,
		Label:         "laptop",
		ConfigPath:    cfgPath,
		SSHConfigPath: sshPath,
		Executable:    filepath.Join(dir, "kite"),
		IdentityDir:   filepath.Dir(sshPath),
		Now:           time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "ABCD-EFGH" || !strings.HasPrefix(got.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("sent %+v", got)
	}
	if !res.HostAdded || res.Agent != "my-pc" || res.User != "me" {
		t.Fatalf("%+v", res)
	}
	if res.SSHBackup == "" {
		t.Fatal("missing backup")
	}
	body, err := os.ReadFile(sshPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "\nHost my-pc\n")+strings.Count(text, "Host my-pc\n") < 1 {
		t.Fatal(text)
	}
	if !strings.Contains(text, "Host other") || !strings.Contains(text, "IdentitiesOnly yes") || !strings.Contains(text, "%n") {
		t.Fatal(text)
	}
	ident, err := os.ReadFile(res.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ident), got.PublicKey) && !strings.Contains(string(ident), "PRIVATE") {
		t.Fatal("identity file looks public")
	}
	if !strings.Contains(string(ident), "PRIVATE KEY") {
		t.Fatal("identity is not a private key")
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	secret, kind, err := cfg.CredentialFor("my-pc")
	if err != nil || kind != "client" || secret != "kite_testtoken" {
		t.Fatalf("%s %s %v", secret, kind, err)
	}
	if cfg.RelayAddr != "wss://tunnel.example.com/client" {
		t.Fatal(cfg.RelayAddr)
	}

	// A second connect must not duplicate the Host block. The fake server
	// accepts another redeem; the ssh file stays a single Host my-pc.
	res2, err := Run(context.Background(), Options{
		Code:          "ABCD-EFGH",
		RelayURL:      srv.URL,
		Label:         "laptop",
		ConfigPath:    cfgPath,
		SSHConfigPath: sshPath,
		Executable:    filepath.Join(dir, "kite"),
		IdentityDir:   filepath.Dir(sshPath),
		Now:           time.Date(2026, 11, 18, 9, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.HostAdded {
		t.Fatal("duplicated host")
	}
	body, _ = os.ReadFile(sshPath)
	if strings.Count(string(body), "Host my-pc") != 1 {
		t.Fatalf("hosts:\n%s", body)
	}
}
