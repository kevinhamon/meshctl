// Package selfupdate replaces the running meshctl binary with a GitHub release.
//
// Releases (.github/workflows/release.yml) attach one raw binary per platform —
// meshctl_<goos>_<goarch>[.exe] — plus a <binary>.sha256 file. Update downloads
// the asset for this platform, verifies its checksum, and swaps it in for the
// running executable. Latest also backs a once-a-day "update available" notice,
// cached so routine commands never wait on the network.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub owner/name releases are fetched from (MESHCTL_UPDATE_REPO overrides).
const Repo = "kevinhamon/meshctl"

// APIBase is the GitHub API root; a var so tests can point it at a fake server.
var APIBase = "https://api.github.com"

// Release is the subset of a GitHub release meshctl needs.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func repo() string {
	if r := strings.TrimSpace(os.Getenv("MESHCTL_UPDATE_REPO")); r != "" {
		return r
	}
	return Repo
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "meshctl-update")
	if strings.HasPrefix(url, APIBase) {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest returns the newest published release, or the release for tag if set.
func Latest(ctx context.Context, tag string) (*Release, error) {
	url := APIBase + "/repos/" + repo() + "/releases/latest"
	if tag != "" {
		url = APIBase + "/repos/" + repo() + "/releases/tags/" + tag
	}
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if r.Tag == "" {
		return nil, fmt.Errorf("release has no tag")
	}
	return &r, nil
}

// AssetName is the release binary for this platform.
func AssetName() string {
	n := "meshctl_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

var reSemver = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// parse returns [major minor patch] for a plain release tag; ok=false for
// anything else (pseudo-versions, prereleases, dev builds).
func parse(v string) (n [3]int, ok bool) {
	m := reSemver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return n, false
	}
	for i := 0; i < 3; i++ {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n, true
}

// IsRelease reports whether v is a plain release version (vX.Y.Z). Anything
// else is a local/dev build, which update never replaces without --force: a
// pseudo-version like v0.0.0-<date>-<sha> sorts below every release but may
// carry newer, unreleased code.
func IsRelease(v string) bool { _, ok := parse(v); return ok }

// Newer reports whether release tag a is newer than release version b. False
// if either is not a plain release version.
func Newer(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// Install downloads this platform's asset from rel, verifies it against the
// published .sha256, and replaces the executable at exe.
func Install(ctx context.Context, rel *Release, exe string) error {
	name := AssetName()
	var bin, sum string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			bin = a.URL
		case name + ".sha256":
			sum = a.URL
		}
	}
	if bin == "" {
		return fmt.Errorf("release %s has no %s asset", rel.Tag, name)
	}
	if sum == "" {
		return fmt.Errorf("release %s has no %s.sha256 — refusing an unverified binary", rel.Tag, name)
	}

	resp, err := get(ctx, sum)
	if err != nil {
		return err
	}
	sumBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if err != nil {
		return err
	}
	fields := strings.Fields(string(sumBody))
	if len(fields) == 0 {
		return fmt.Errorf("empty checksum file for %s", name)
	}
	want := strings.ToLower(fields[0])

	// Stage next to the target so the final rename stays on one filesystem.
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".meshctl-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed into place

	resp, err = get(ctx, bin)
	if err != nil {
		tmp.Close()
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	resp.Body.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s — not installed", name, got, want)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// Windows cannot overwrite a running executable, but it can rename it.
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), exe)
}

// Executable is the real path of the running binary (symlinks resolved, so a
// symlinked install updates its target, not the link).
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// --- cached "update available" notice ---

const checkEvery = 24 * time.Hour

type cache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "meshctl", "update-check.json")
}

// Notice returns a one-line "update available" message when a newer release
// than current exists, or "". It hits the network at most once a day (bounded
// by a short timeout) and otherwise answers from the cache. Silent for dev
// builds, on any error, and when MESHCTL_NO_UPDATE_CHECK is set.
func Notice(current string, now time.Time) string {
	if os.Getenv("MESHCTL_NO_UPDATE_CHECK") != "" || !IsRelease(current) {
		return ""
	}
	path := cachePath()
	var c cache
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &c)
		}
	}
	if now.Sub(c.Checked) > checkEvery {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Checked = now
		if rel, err := Latest(ctx, ""); err == nil {
			c.Latest = rel.Tag
		}
		if path != "" {
			if b, err := json.Marshal(c); err == nil && os.MkdirAll(filepath.Dir(path), 0o755) == nil {
				_ = os.WriteFile(path, b, 0o644)
			}
		}
	}
	if Newer(c.Latest, current) {
		return fmt.Sprintf("meshctl %s is available (you have %s) — run `meshctl update`", c.Latest, current)
	}
	return ""
}
