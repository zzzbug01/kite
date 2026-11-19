package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSelectPrereleaseOnlyWhenNoStable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/zzzbug01/kite/releases" {
			http.NotFound(w, r)
			return
		}
		list := []ghRelease{
			{TagName: "v0.2.0-rc.1", Prerelease: true, Assets: asset("v0.2.0-rc.1", srvURL(r))},
			{TagName: "v0.1.0-rc.3", Prerelease: true, Assets: asset("v0.1.0-rc.3", srvURL(r))},
			{TagName: "v0.1.0-rc.2", Prerelease: true, Assets: asset("v0.1.0-rc.2", srvURL(r))},
			{TagName: "v0.9.0-draft", Draft: true, Prerelease: false},
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	rel, err := c.Select(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.2.0-rc.1" || !rel.Prerelease {
		t.Fatalf("%+v", rel)
	}

	// A stable release, even when it is not the newest tag, wins over prereleases.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := []ghRelease{
			{TagName: "v0.2.0-rc.1", Prerelease: true},
			{TagName: "v0.1.0", Prerelease: false},
			{TagName: "v0.1.1-rc.4", Prerelease: true},
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	defer srv2.Close()
	c2 := &Client{BaseURL: srv2.URL, HTTP: srv2.Client()}
	rel, err = c2.Select(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.1.0" || rel.Prerelease {
		t.Fatalf("stable should win: %+v", rel)
	}
	pinned, err := c.Select(context.Background(), "0.1.0-rc.2")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Tag != "v0.1.0-rc.2" {
		t.Fatal(pinned.Tag)
	}
}

func TestDownloadChecksumAndMissingAsset(t *testing.T) {
	payload := tarGz(t, map[string]string{"kite": "#!/bin/kite\n"})
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	name := ArchiveName("v0.1.0-rc.3", "linux", "amd64")

	var mu sync.Mutex
	mode := ""
	setMode := func(v string) {
		mu.Lock()
		mode = v
		mu.Unlock()
	}
	getMode := func() string {
		mu.Lock()
		defer mu.Unlock()
		return mode
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			rel := ghRelease{
				TagName:    "v0.1.0-rc.3",
				Prerelease: true,
				Assets: []struct {
					Name string `json:"name"`
					URL  string `json:"browser_download_url"`
				}{
					{Name: name, URL: "http://" + r.Host + "/asset"},
					{Name: "SHA256SUMS", URL: "http://" + r.Host + "/sums"},
				},
			}
			if getMode() == "missing" {
				rel.Assets = rel.Assets[1:]
			}
			_ = json.NewEncoder(w).Encode([]ghRelease{rel})
		case r.URL.Path == "/sums":
			hash := good
			if getMode() == "bad" {
				hash = strings.Repeat("ab", 32)
			}
			_, _ = w.Write([]byte(hash + "  " + name + "\n"))
		case r.URL.Path == "/asset":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()

	setMode("bad")
	rel, err := c.Select(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DownloadVerified(ctx, rel, name); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}

	setMode("missing")
	rel, err = c.Select(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DownloadVerified(ctx, rel, name); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("missing: %v", err)
	}

	setMode("")
	rel, err = c.Select(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.DownloadVerified(ctx, rel, name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Extract(blob, "kite")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#!/bin/kite\n" {
		t.Fatalf("%q", got)
	}
}

func TestOlderAndSame(t *testing.T) {
	if !SameVersion("v0.1.0-rc.3", "v0.1.0-rc.3") || SameVersion("dev", "v0.1.0") {
		t.Fatal("same")
	}
	if !Older("v0.1.0-rc.2", "v0.1.0-rc.10") || !Older("v0.1.0-rc.3", "v0.1.0") {
		t.Fatal("older")
	}
	if Older("dev", "v0.1.0") || Older("af09a24", "v0.1.0-rc.3") || !KnownBuild("v0.1.0-rc.3") || KnownBuild("dev") {
		t.Fatal("unknown builds are not older and are not release builds")
	}
	// A git-describe build is newer than its base tag, so upgrade must not go backwards.
	if Older("v0.1.0-rc.3-2-gaf09a24", "v0.1.0-rc.3") || Older("v0.1.0-rc.4-pr3", "v0.1.0-rc.3") {
		t.Fatal("describe and pr builds must not look older than rc.3")
	}
	if !Older("v0.1.0-rc.3-2-gaf09a24", "v0.1.0-rc.4") {
		t.Fatal("describe after rc.3 is still older than rc.4")
	}
	if Older("v0.1.0", "v0.1.0-rc.9") || Older("v0.2.0", "v0.1.9") {
		t.Fatal("not older")
	}
	if SupportsAgentToken("v0.1.0-rc.3") || SupportsAgentToken("v0.1.0-rc.2") {
		t.Fatal("rc.3 and earlier have no agent tokens")
	}
	if !SupportsAgentToken("v0.1.0-rc.4") || !SupportsAgentToken("v0.1.0") {
		t.Fatal("later releases have agent tokens")
	}
}

func TestCleanupOldRemovesPrev(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kite-agent.exe")
	if err := os.WriteFile(exe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".prev", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old", []byte("older"), 0o755); err != nil {
		t.Fatal(err)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".prev"); !os.IsNotExist(err) {
		t.Fatalf("prev left behind: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
}

func TestInstallFileRenamesAside(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "kite")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installFile(dest, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "new" {
		t.Fatalf("%q %v", got, err)
	}
	old, err := os.ReadFile(dest + ".old")
	if err != nil || string(old) != "old" {
		t.Fatalf("old %q %v", old, err)
	}
}

func asset(tag, base string) []struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
} {
	name := ArchiveName(tag, "linux", "amd64")
	return []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{
		{Name: name, URL: base + "/asset"},
		{Name: "SHA256SUMS", URL: base + "/sums"},
	}
}

func srvURL(r *http.Request) string { return "http://" + r.Host }

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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
