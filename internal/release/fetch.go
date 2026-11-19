// Package release finds kite GitHub releases, checks SHA256SUMS, and
// replaces a local binary. HTTP requests use the process proxy
// (HTTPS_PROXY, HTTP_PROXY, NO_PROXY) and a bounded timeout.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultAPI is the GitHub REST root.
	DefaultAPI = "https://api.github.com"
	// DefaultRepo is the repository that publishes kite releases.
	DefaultRepo = "zzzbug01/kite"
	maxAPIBody  = 4 << 20
	maxAsset    = 64 << 20
)

// Client downloads release metadata and assets.
type Client struct {
	// BaseURL defaults to DefaultAPI. Tests point it at an httptest server.
	BaseURL string
	// Repo is "owner/name". It defaults to DefaultRepo.
	Repo string
	// HTTP is optional. A nil client uses ProxyFromEnvironment and timeouts.
	HTTP *http.Client
}

// Release is one published GitHub release.
type Release struct {
	Tag        string
	Prerelease bool
	Assets     map[string]string
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Select finds version, or the newest release when version is empty.
// Prereleases are used only when the repository has no stable release,
// unless version names one of them.
func (c *Client) Select(ctx context.Context, version string) (Release, error) {
	list, err := c.list(ctx)
	if err != nil {
		return Release{}, err
	}
	version = strings.TrimSpace(version)
	if version != "" {
		version = canonTag(version)
		for _, rel := range list {
			if rel.Draft {
				continue
			}
			if canonTag(rel.TagName) == version {
				return toRelease(rel), nil
			}
		}
		one, err := c.byTag(ctx, version)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				return Release{}, fmt.Errorf("release %s was not found", version)
			}
			return Release{}, err
		}
		if one.Draft {
			return Release{}, fmt.Errorf("release %s is a draft", version)
		}
		return toRelease(one), nil
	}
	chosen, ok := pick(list)
	if !ok {
		return Release{}, errors.New("no kite release was published")
	}
	return toRelease(chosen), nil
}

func pick(list []ghRelease) (ghRelease, bool) {
	var stables, pres []ghRelease
	for _, rel := range list {
		if rel.Draft || strings.TrimSpace(rel.TagName) == "" {
			continue
		}
		if rel.Prerelease {
			pres = append(pres, rel)
			continue
		}
		stables = append(stables, rel)
	}
	if len(stables) > 0 {
		return newest(stables), true
	}
	if len(pres) > 0 {
		return newest(pres), true
	}
	return ghRelease{}, false
}

func newest(list []ghRelease) ghRelease {
	best := list[0]
	bestV, bestOK := parseSemver(best.TagName)
	for _, rel := range list[1:] {
		v, ok := parseSemver(rel.TagName)
		if !ok {
			continue
		}
		if !bestOK || semverLess(bestV, v) {
			best = rel
			bestV = v
			bestOK = true
		}
	}
	return best
}

func toRelease(rel ghRelease) Release {
	assets := make(map[string]string, len(rel.Assets))
	for _, a := range rel.Assets {
		if a.Name != "" && a.URL != "" {
			assets[a.Name] = a.URL
		}
	}
	return Release{Tag: rel.TagName, Prerelease: rel.Prerelease, Assets: assets}
}

// ArchiveName is the release asset for one OS and architecture.
// version is the tag, including the leading v.
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("kite_%s_%s_%s%s", version, goos, goarch, ext)
}

// DownloadVerified fetches asset and SHA256SUMS from rel and returns the
// archive bytes. A missing asset or a checksum mismatch is an error.
func (c *Client) DownloadVerified(ctx context.Context, rel Release, asset string) ([]byte, error) {
	if strings.TrimSpace(asset) == "" {
		return nil, errors.New("release asset name is required")
	}
	assetURL, ok := rel.Assets[asset]
	if !ok || assetURL == "" {
		return nil, fmt.Errorf("release %s has no asset %s", rel.Tag, asset)
	}
	sumURL, ok := rel.Assets["SHA256SUMS"]
	if !ok || sumURL == "" {
		return nil, fmt.Errorf("release %s has no SHA256SUMS", rel.Tag)
	}
	sumsText, err := c.get(ctx, sumURL, maxAPIBody)
	if err != nil {
		return nil, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	want, ok := parseSums(string(sumsText))[asset]
	if !ok {
		return nil, fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	blob, err := c.get(ctx, assetURL, maxAsset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	sum := sha256.Sum256(blob)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return nil, fmt.Errorf("checksum mismatch for %s", asset)
	}
	return blob, nil
}

func (c *Client) list(ctx context.Context) ([]ghRelease, error) {
	url := strings.TrimRight(c.base(), "/") + "/repos/" + c.repo() + "/releases?per_page=30"
	body, err := c.get(ctx, url, maxAPIBody)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || body[0] != '[' {
		return nil, fmt.Errorf("release list was not a JSON array")
	}
	var list []ghRelease
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	return list, nil
}

func (c *Client) byTag(ctx context.Context, tag string) (ghRelease, error) {
	url := strings.TrimRight(c.base(), "/") + "/repos/" + c.repo() + "/releases/tags/" + tag
	body, err := c.get(ctx, url, maxAPIBody)
	if err != nil {
		return ghRelease{}, fmt.Errorf("release %s: %w", tag, err)
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return ghRelease{}, fmt.Errorf("parse release %s: %w", tag, err)
	}
	if canonTag(rel.TagName) != canonTag(tag) {
		return ghRelease{}, fmt.Errorf("release %s was not found", tag)
	}
	return rel, nil
}

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kite-release")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response from %s exceeded %d bytes", url, limit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		var er struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &er) == nil && er.Message != "" {
			msg = er.Message
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("%s: %s", url, msg)
	}
	return body, nil
}

func (c *Client) base() string {
	if strings.TrimSpace(c.BaseURL) == "" {
		return DefaultAPI
	}
	return c.BaseURL
}

func (c *Client) repo() string {
	if strings.TrimSpace(c.Repo) == "" {
		return DefaultRepo
	}
	return c.Repo
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		},
	}
}

func parseSums(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func canonTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "dev" {
		return ""
	}
	if !strings.HasPrefix(s, "v") && looksNumeric(s) {
		return "v" + s
	}
	return s
}

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= '0' && s[0] <= '9'
}

// SameVersion reports whether current and tag name the same release.
// "dev" and a non-tag build do not match a release tag.
func SameVersion(current, tag string) bool {
	c, okC := parseBuild(current)
	t, okT := parseBuild(tag)
	if !okC || !okT {
		return false
	}
	return !buildLess(c, t) && !buildLess(t, c)
}

// KnownBuild reports whether current is a semver tag or a git-describe
// version (tag-N-gHASH). "dev" and a bare git SHA are not known builds.
func KnownBuild(current string) bool {
	_, ok := parseBuild(current)
	return ok
}

// Older reports whether current is an older build than latest.
// Unknown builds (dev, a bare SHA) are not older; callers must not
// auto-upgrade them.
func Older(current, latest string) bool {
	c, okC := parseBuild(current)
	l, okL := parseBuild(latest)
	if !okC || !okL {
		return false
	}
	return buildLess(c, l)
}

// SupportsAgentToken reports whether a release understands install-code
// agent tokens. Those tokens arrived after v0.1.0-rc.3.
func SupportsAgentToken(version string) bool {
	cur, ok := parseBuild(version)
	if !ok {
		return true
	}
	limit, ok := parseBuild("v0.1.0-rc.3")
	if !ok {
		return true
	}
	return buildLess(limit, cur)
}

// build is a semver tag plus how many commits git-describe added after it.
// A describe version is newer than its base tag and older than the next tag.
type build struct {
	sem     semver
	commits int
}

func parseBuild(s string) (build, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "dev" || isHexSHA(s) {
		return build{}, false
	}
	if base, n, ok := splitDescribe(s); ok {
		sv, ok := parseSemver(base)
		if !ok {
			return build{}, false
		}
		return build{sem: sv, commits: n}, true
	}
	sv, ok := parseSemver(s)
	if !ok {
		return build{}, false
	}
	return build{sem: sv}, true
}

func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// splitDescribe matches git describe: v0.1.0-rc.3-2-gaf09a24.
func splitDescribe(s string) (base string, commits int, ok bool) {
	rest, hash, found := strings.Cut(s, "-g")
	if !found || !isHexSHA(hash) || strings.Contains(hash, "-") {
		return "", 0, false
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil || n < 0 {
		return "", 0, false
	}
	base = rest[:i]
	if _, ok := parseSemver(base); !ok {
		return "", 0, false
	}
	return base, n, true
}

func buildLess(a, b build) bool {
	if semverLess(a.sem, b.sem) {
		return true
	}
	if semverLess(b.sem, a.sem) {
		return false
	}
	return a.commits < b.commits
}

type semver struct {
	major, minor, patch int
	pre                 []string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return semver{}, false
	}
	main, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var sv semver
	var err error
	if sv.major, err = strconv.Atoi(parts[0]); err != nil {
		return semver{}, false
	}
	if sv.minor, err = strconv.Atoi(parts[1]); err != nil {
		return semver{}, false
	}
	if sv.patch, err = strconv.Atoi(parts[2]); err != nil {
		return semver{}, false
	}
	if pre != "" {
		sv.pre = strings.Split(pre, ".")
	}
	return sv, true
}

func semverLess(a, b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	// A release is newer than any prerelease of the same numbers.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return false
	case len(a.pre) == 0:
		return false
	case len(b.pre) == 0:
		return true
	}
	n := len(a.pre)
	if len(b.pre) < n {
		n = len(b.pre)
	}
	for i := 0; i < n; i++ {
		aNum, aErr := strconv.Atoi(a.pre[i])
		bNum, bErr := strconv.Atoi(b.pre[i])
		aIsNum := aErr == nil
		bIsNum := bErr == nil
		switch {
		case aIsNum && bIsNum:
			if aNum != bNum {
				return aNum < bNum
			}
		case aIsNum && !bIsNum:
			return true // numeric identifiers sort before non-numeric
		case !aIsNum && bIsNum:
			return false
		default:
			if a.pre[i] != b.pre[i] {
				return a.pre[i] < b.pre[i]
			}
		}
	}
	return len(a.pre) < len(b.pre)
}
