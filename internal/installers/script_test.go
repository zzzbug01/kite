package installers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallSHReleaseNotFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"), "--version", "v9.9.9")
	cmd.Env = append(os.Environ(), "KITE_RELEASE_BASE="+srv.URL+"/dl")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "release v9.9.9 was not found") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestInstallSHPassesServiceName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	archive := fakeArchive(t)
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	const version = "v0.1.0-rc.9"
	names := []string{
		"linux_amd64", "linux_arm64",
		"darwin_amd64", "darwin_arm64",
	}
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  kite_%s_%s.tar.gz\n", digest, version, name)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			w.Write([]byte(sums.String()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", version,
		"--agent", "--relay", "example.test", "--code", "ABCD-EFGH",
		"--service-name", "kite-agent-test")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+filepath.Join(dir, "bin"),
		"KITE_NO_SUDO=1",
		"KITE_TEST_ARGS="+argsFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("agent was not run: %v\n%s", err, out)
	}
	text := string(got)
	for _, want := range []string{"install", "--relay", "example.test", "--code", "ABCD-EFGH", "--service-name", "kite-agent-test"} {
		if !strings.Contains(text, want) {
			t.Fatalf("args %q missing %q\n%s", text, want, out)
		}
	}
}

func TestInstallPS1UnwrapsReleaseArray(t *testing.T) {
	ps := powershell()
	if ps == "" {
		t.Skip("powershell is not installed")
	}
	probe := filepath.Join(t.TempDir(), "unwrap.ps1")
	body := strings.ReplaceAll(psUnwrap, "INSTALL_PS1", filepath.Join(repoRoot(t), "install.ps1"))
	if err := os.WriteFile(probe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", probe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
}

const psUnwrap = `
$ErrorActionPreference = 'Stop'
$env:KITE_INSTALL_LIB_ONLY = '1'
. 'INSTALL_PS1'
$releases = @(
  [pscustomobject]@{ tag_name = 'v0.1.0-rc.4'; draft = $false; prerelease = $true },
  [pscustomobject]@{ tag_name = 'v0.1.0-rc.3'; draft = $false; prerelease = $true }
)
$wrapped = New-Object object[] 1
$wrapped[0] = $releases
$rels = Expand-KiteList $wrapped
$pre = $null
foreach ($r in $rels) {
  if ($r.draft -is [System.Array]) { throw 'draft is an array; the release list was not unwrapped' }
  if ($r.draft) { continue }
  if ($r.prerelease -and -not $pre) { $pre = [string]$r.tag_name }
}
if ($pre -ne 'v0.1.0-rc.4') { throw "wrapped list selected '$pre'" }
$n = 0
foreach ($item in (Expand-KiteList $releases)) { $n++ }
if ($n -lt 2) { throw "flat list count $n" }
$drafts = New-Object object[] 1
$drafts[0] = @(
  [pscustomobject]@{ tag_name = 'v9'; draft = $true; prerelease = $false },
  [pscustomobject]@{ tag_name = 'v0.1.0-rc.4'; draft = $false; prerelease = $true }
)
$picked = $null
foreach ($r in (Expand-KiteList $drafts)) {
  if ($r.draft) { continue }
  if (-not $picked) { $picked = [string]$r.tag_name }
}
if ($picked -ne 'v0.1.0-rc.4') { throw "draft wrapper selected '$picked'" }
`

func fakeArchive(t *testing.T) []byte {
	t.Helper()
	script := []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$KITE_TEST_ARGS\"\nexit 0\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"kite", "kite-agent"} {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(script))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(script); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func powershell() string {
	for _, name := range []string{"pwsh", "powershell", "pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
