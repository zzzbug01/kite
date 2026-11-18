package authkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/zzzbug01/kite/internal/pairing"
)

func sampleKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

func TestPathSelection(t *testing.T) {
	admin, kind := Path("windows", `C:\Users\me`, true, `C:\ProgramData`)
	if admin != `C:\ProgramData\ssh\administrators_authorized_keys` || kind != KindWindowsAdmin {
		t.Fatalf("%s %d", admin, kind)
	}
	user, kind := Path("windows", `C:\Users\me`, false, `C:\ProgramData`)
	if user != `C:\Users\me\.ssh\authorized_keys` || kind != KindWindowsUser {
		t.Fatalf("%s %d", user, kind)
	}
	posix, kind := Path("linux", "/home/me", true, "")
	if posix != "/home/me/.ssh/authorized_keys" || kind != KindPOSIX {
		t.Fatalf("%s %d", posix, kind)
	}
	mac, _ := Path("darwin", "/Users/me", true, "")
	if mac != "/Users/me/.ssh/authorized_keys" {
		t.Fatal(mac)
	}
	def, _ := Path("windows", `C:\Users\me`, true, "")
	if def != `C:\ProgramData\ssh\administrators_authorized_keys` {
		t.Fatal(def)
	}
}

func TestUpsertListRevoke(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	key := sampleKey(t)
	when := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)
	comment := pairing.PairComment("laptop", when)
	if err := Upsert(path, key+" injected", comment, KindPOSIX, ""); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "ssh-ed25519") != 1 {
		t.Fatalf("body:\n%s", body)
	}
	if !strings.Contains(string(body), "kite-pair laptop 2026-11-18") {
		t.Fatalf("body:\n%s", body)
	}
	if strings.Contains(string(body), "injected") {
		t.Fatal("client comment was kept")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
		dst, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %o", dst.Mode().Perm())
		}
	}

	if err := Upsert(path, key, pairing.PairComment("laptop", when), KindPOSIX, ""); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if strings.Count(string(body), "ssh-ed25519") != 1 {
		t.Fatalf("duplicated:\n%s", body)
	}

	other := sampleKey(t)
	if err := os.WriteFile(path, []byte(string(body)+other+" manual-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The manual key is the same algorithm but a different blob. Re-adding
	// the paired key must not grow a third line.
	if err := Upsert(path, key, comment, KindPOSIX, ""); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if strings.Count(string(body), "ssh-ed25519") != 2 {
		t.Fatalf("body:\n%s", body)
	}

	recs, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Label != "laptop" || !strings.HasPrefix(recs[0].Fingerprint, "SHA256:") {
		t.Fatalf("%+v", recs)
	}
	n, err := Revoke(path, "laptop", KindPOSIX, "")
	if err != nil || n != 1 {
		t.Fatalf("revoke %d %v", n, err)
	}
	left, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(left), "kite-pair") || !strings.Contains(string(left), "manual-key") {
		t.Fatalf("left:\n%s", left)
	}
	recs, err = List(path)
	if err != nil || len(recs) != 0 {
		t.Fatalf("%v %+v", err, recs)
	}
	if n, err := Revoke(path, "laptop", KindPOSIX, ""); err != nil || n != 0 {
		t.Fatalf("second revoke %d %v", n, err)
	}
}

func TestRejectsSymlinkAndBadKey(t *testing.T) {
	if _, err := ValidateEd25519("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7 comment"); err == nil {
		t.Fatal("rsa should be rejected")
	}
	if _, err := ValidateEd25519("ssh-ed25519 AAAA\nssh-ed25519 BBBB"); err == nil {
		t.Fatal("newline should be rejected")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	link := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink: " + err.Error())
	}
	key := sampleKey(t)
	if err := Upsert(link, key, "kite-pair laptop 2026-11-18", KindPOSIX, ""); err == nil {
		t.Fatal("followed a symlink")
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "keep\n" {
		t.Fatalf("target changed: %q", body)
	}
}

func TestMemberAndACL(t *testing.T) {
	if !Member([]string{`DESKTOP\Alice`, "bob"}, `desktop\alice`) {
		t.Fatal("alice")
	}
	if Member([]string{`DESKTOP\Alice`}, "alic") {
		t.Fatal("prefix must not match")
	}
	args := strings.Join(AdminACLArgs(`C:\ProgramData\ssh\administrators_authorized_keys`), " ")
	if !strings.Contains(args, "*S-1-5-18:F") || !strings.Contains(args, "*S-1-5-32-544:F") {
		t.Fatal(args)
	}
	if strings.Contains(strings.ToLower(args), "firewall") || strings.Contains(args, ":22") {
		t.Fatal(args)
	}
	user := strings.Join(UserACLArgs(`C:\Users\me\.ssh\authorized_keys`, `DESKTOP\me`), " ")
	if !strings.Contains(user, "me:F") {
		t.Fatal(user)
	}
}
