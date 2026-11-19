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

func TestInstallSHClientDoesNotTouchAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := filepath.Join(dir, "path")
	if err := os.Mkdir(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldAgent := filepath.Join(pathDir, "kite-agent")
	if err := os.WriteFile(oldAgent, []byte("#!/bin/sh\necho 'kite-agent v0.0.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, version := serveArchive(t)
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"), "--version", version)
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
		"PATH="+pathDir+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(bin, "kite")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "kite-agent")); !os.IsNotExist(err) {
		t.Fatalf("client install replaced kite-agent: %v", err)
	}
	if !strings.Contains(string(out), "was not updated") || !strings.Contains(string(out), "kite-agent upgrade") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestInstallSHAgentUpgradeFailureIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(bin, "kite-agent")
	script := "#!/bin/sh\nif [ \"$1\" = upgrade ]; then echo upgrade-refused >&2; exit 1; fi\nprintf '%s\\n' \"$*\" > \"$KITE_TEST_ARGS\"\n"
	if err := os.WriteFile(installed, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, version := serveArchive(t)
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", version, "--agent", "--relay", "example.test", "--code", "ABCD-EFGH")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("upgrade failure was ignored:\n%s", out)
	}
	if !strings.Contains(string(out), "upgrade failed") {
		t.Fatalf("output:\n%s", out)
	}
	body, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "upgrade-refused") {
		t.Fatal("installed agent was replaced after a failed upgrade")
	}
}

func TestInstallSHNamedInstallLeavesDefaultBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := []byte("#!/bin/sh\necho DEFAULT-AGENT\n")
	if err := os.WriteFile(filepath.Join(bin, "kite-agent"), marker, 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args.txt")
	srv, version := serveArchive(t)
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", version, "--agent", "--relay", "example.test", "--code", "ABCD-EFGH",
		"--service-name", "kite-agent-test3")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
		"KITE_TEST_ARGS="+argsFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(bin, "kite-agent"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(marker) {
		t.Fatalf("named install replaced the default agent:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(bin, "kite")); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("staged agent was not run: %v\n%s", err, out)
	}
	text := string(args)
	for _, want := range []string{"install", "--service-name", "kite-agent-test3", "--code", "ABCD-EFGH"} {
		if !strings.Contains(text, want) {
			t.Fatalf("args %q missing %q\n%s", text, want, out)
		}
	}
	if !strings.Contains(string(out), "kite-agent pair --service-name kite-agent-test3") {
		t.Fatalf("hint dropped --service-name:\n%s", out)
	}
}

func TestInstallSHNamedExistingInstanceUpgradesWithoutCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	sys := filepath.Join(dir, "sys")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	// systemctl can see the unit. The binary under /usr/local/lib/kite is not visible.
	systemctl := "#!/bin/sh\nif [ \"$1\" = cat ] && [ \"$2\" = kite-agent-test3.service ]; then exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(sys, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args.txt")
	marker := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KITE_TEST_ARGS\"\nif [ \"$1\" = upgrade ]; then exit 0; fi\necho DEFAULT-AGENT\n"
	if err := os.WriteFile(filepath.Join(bin, "kite-agent"), []byte(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, version := serveArchive(t)
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", version, "--agent", "--service-name", "kite-agent-test3")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
		"KITE_TEST_ARGS="+argsFile,
		"PATH="+sys+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(bin, "kite-agent"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "DEFAULT-AGENT") {
		t.Fatalf("default binary was replaced:\n%s", body)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, "upgrade") || !strings.Contains(text, "--service-name") || !strings.Contains(text, "kite-agent-test3") {
		t.Fatalf("args %q\n%s", text, out)
	}
	if strings.Contains(text, " install ") || strings.HasPrefix(strings.TrimSpace(text), "install ") {
		t.Fatalf("re-run without --code redeemed again: %q", text)
	}
	if !strings.Contains(string(out), "existing token was kept") || !strings.Contains(string(out), "pair --service-name kite-agent-test3") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestInstallSHNamedUpgradeIgnoresStaleDefaultBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	sys := filepath.Join(dir, "sys")
	if err := os.Mkdir(bin, 0o755); err != nil || os.Mkdir(sys, 0o755) != nil {
		t.Fatal(err)
	}
	systemctl := "#!/bin/sh\nif [ \"$1\" = cat ] && [ \"$2\" = kite-agent-test3.service ]; then exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(sys, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	staleMark := filepath.Join(dir, "stale.txt")
	stale := "#!/bin/sh\necho STALE >> \"$KITE_STALE_MARK\"\necho 'kite-agent: unexpected arguments' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "kite-agent"), []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args.txt")
	srv := serveArchiveVersion(t, "v0.1.0-rc.9")
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", "v0.1.0-rc.9", "--agent", "--service-name", "kite-agent-test3")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
		"KITE_TEST_ARGS="+argsFile,
		"KITE_STALE_MARK="+staleMark,
		"PATH="+sys+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upgrade used the stale binary: %v\n%s", err, out)
	}
	if _, err := os.Stat(staleMark); !os.IsNotExist(err) {
		t.Fatal("stale /usr/local/bin kite-agent was executed")
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "upgrade") || !strings.Contains(string(args), "--service-name kite-agent-test3") {
		t.Fatalf("downloaded binary args %q\n%s", args, out)
	}
}

func TestInstallSHNamedDowngradePrintsTokenGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the Linux and macOS installer")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	sys := filepath.Join(dir, "sys")
	if err := os.Mkdir(bin, 0o755); err != nil || os.Mkdir(sys, 0o755) != nil {
		t.Fatal(err)
	}
	systemctl := "#!/bin/sh\nif [ \"$1\" = cat ] && [ \"$2\" = kite-agent-test3.service ]; then exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(sys, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(envFile, []byte("AGENT_TOKEN=kite_agent_file\nRELAY_ADDR=wss://relay.example/agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := serveArchiveVersion(t, "v0.1.0-rc.3")
	defer srv.Close()
	cmd := exec.Command("sh", filepath.Join(repoRoot(t), "install.sh"),
		"--version", "v0.1.0-rc.3", "--agent", "--service-name", "kite-agent-test3")
	cmd.Env = append(os.Environ(),
		"KITE_RELEASE_BASE="+srv.URL+"/dl",
		"KITE_BIN_DIR="+bin,
		"KITE_NO_SUDO=1",
		"KITE_INSTANCE_ENV="+envFile,
		"PATH="+sys+":"+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("downgrade was allowed:\n%s", out)
	}
	if !strings.Contains(string(out), "does not understand agent tokens") || !strings.Contains(string(out), "--force") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestInstallPS1WindowsPairHint(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "sudo kite-agent pair") {
		t.Fatal("Windows installer still tells the user to use sudo")
	}
	if !strings.Contains(text, "elevated PowerShell") {
		t.Fatal(text)
	}
}

func TestInstallPS1NamedPlanDoesNotCopyDefaultAgent(t *testing.T) {
	ps := powershell()
	if ps == "" {
		t.Skip("powershell is not installed")
	}
	probe := filepath.Join(t.TempDir(), "plan.ps1")
	body := strings.ReplaceAll(psPlan, "INSTALL_PS1", filepath.Join(repoRoot(t), "install.ps1"))
	if err := os.WriteFile(probe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", probe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
}

const psPlan = `
$ErrorActionPreference = 'Stop'
$env:KITE_INSTALL_LIB_ONLY = '1'
. 'INSTALL_PS1'
$named = Get-KiteInstallTargets 'kite' 'kite-agent-test3' $true
if ($named.CopyDefaultAgent) { throw 'named install would copy the default agent binary' }
if ($named.ServiceDir -notmatch 'kite-agent-test3$') { throw "service dir $($named.ServiceDir)" }
if ($named.DefaultAgent -match 'kite-agent-test3') { throw "default agent path $($named.DefaultAgent)" }
$def = Get-KiteInstallTargets 'kite' '' $true
if (-not $def.CopyDefaultAgent) { throw 'default install should update its own agent binary' }
if ($def.ServiceDir -ne 'kite') { throw "default dir $($def.ServiceDir)" }
$client = Get-KiteInstallTargets 'kite' 'kite-agent-test3' $false
if ($client.CopyDefaultAgent) { throw 'client install would copy an agent binary' }
$hint = Get-KiteInstanceHint 'kite-agent pair' 'kite-agent-test3'
if ($hint -ne 'kite-agent pair --service-name kite-agent-test3') { throw "hint $hint" }
$plain = Get-KiteInstanceHint 'kite-agent upgrade' 'kite-agent'
if ($plain -ne 'kite-agent upgrade') { throw "plain $plain" }
$files = Get-KiteProgramFiles
if ([string]::IsNullOrWhiteSpace($files)) { throw 'ProgramFiles resolved empty' }
`

func TestInstallPS1ElevationCapture(t *testing.T) {
	ps := powershell()
	if ps == "" {
		t.Skip("powershell is not installed")
	}
	dir := t.TempDir()
	bodyOut := filepath.Join(dir, "body.ps1")
	script := filepath.Join(repoRoot(t), "install.ps1")
	cmd := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.Env = append(os.Environ(),
		"OS=Windows_NT",
		"KITE_AGENT=1",
		"KITE_RELAY=example.test",
		"KITE_CODE=ABCD-EFGH",
		"KITE_TEST_ADMIN=0",
		"KITE_TEST_NO_ELEVATE=1",
		"KITE_TEST_BODY_OUT="+bodyOut,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the elevation guard to stop:\n%s", out)
	}
	if !strings.Contains(string(out), "KITE_TEST_NO_ELEVATE") {
		t.Fatalf("output:\n%s", out)
	}
	captured, err := os.ReadFile(bodyOut)
	if err != nil {
		t.Fatal(err)
	}
	text := string(captured)
	if !strings.Contains(text, "Expand-KiteList") || !strings.Contains(text, "KITE_ELEVATED") {
		t.Fatalf("captured installer is not the script body:\n%s", text[:min(400, len(text))])
	}
	if strings.Contains(text, "caller.ps1") || strings.TrimSpace(text) == script {
		t.Fatalf("captured the caller instead of the installer:\n%s", text[:min(200, len(text))])
	}

	again := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
	again.Env = append(os.Environ(),
		"OS=Windows_NT",
		"KITE_AGENT=1",
		"KITE_RELAY=example.test",
		"KITE_CODE=ABCD-EFGH",
		"KITE_TEST_ADMIN=0",
		"KITE_ELEVATED=1",
	)
	out, err = again.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "did not relaunch") {
		t.Fatalf("recursion guard: %v\n%s", err, out)
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

func serveArchive(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	const version = "v0.1.0-rc.9"
	return serveArchiveVersion(t, version), version
}

func serveArchiveVersion(t *testing.T, version string) *httptest.Server {
	t.Helper()
	archive := fakeArchive(t)
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	names := []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"}
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
	return srv
}

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
