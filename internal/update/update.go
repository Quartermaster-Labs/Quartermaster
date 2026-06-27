// Package update checks the project's GitHub releases for a newer build and,
// on Windows release builds, downloads and launches the setup installer.
//
// It is deliberately narrow: the check only runs for release builds (a
// vMAJOR.MINOR.PATCH version, set by the release ldflags) on Windows, since the
// only update mechanism is the Inno Setup installer (.exe). Dev/local builds and
// non-Windows builds report "no update" and never poll.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pollInterval = 24 * time.Hour
	httpTimeout  = 30 * time.Second
	userAgent    = "llama-quartermaster-updater"
)

// Status is the current check result, surfaced to the UI via /api/version.
type Status struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Available  bool   `json:"available"`
	ReleaseURL string `json:"release_url"`

	assetURL string // download URL for the setup .exe; not exposed to the client
}

// Checker polls GitHub releases and holds the latest known status.
type Checker struct {
	repo    string // "owner/name"
	current string
	client  *http.Client
	log     func(string)

	mu     sync.RWMutex
	status Status
}

// New builds a Checker for repo (owner/name) and the running build version.
func New(repo, current string, log func(string)) *Checker {
	if log == nil {
		log = func(string) {}
	}
	return &Checker{
		repo:    repo,
		current: current,
		client:  &http.Client{Timeout: httpTimeout},
		log:     log,
		status:  Status{Current: current},
	}
}

// Enabled reports whether update checking applies to this build: a release
// (semver) build on Windows, unless disabled via LQ_NO_UPDATE_CHECK.
func (c *Checker) Enabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	if os.Getenv("LQ_NO_UPDATE_CHECK") != "" {
		return false
	}
	_, ok := parseSemver(c.current)
	return ok
}

// Run performs an initial check then polls every pollInterval until ctx is done.
// It is a no-op for builds where Enabled() is false.
func (c *Checker) Run(ctx context.Context) {
	if !c.Enabled() {
		return
	}
	c.checkOnce(ctx)
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.checkOnce(ctx)
		}
	}
}

// Status returns a copy of the latest known status (without the internal URL).
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.status
	s.assetURL = ""
	return s
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (c *Checker) checkOnce(ctx context.Context) {
	rel, err := c.fetchLatest(ctx)
	if err != nil {
		c.log("update check failed: " + err.Error())
		return
	}
	asset := setupAsset(rel)
	available := newer(c.current, rel.TagName) && asset != ""

	c.mu.Lock()
	c.status = Status{
		Current:    c.current,
		Latest:     rel.TagName,
		Available:  available,
		ReleaseURL: rel.HTMLURL,
		assetURL:   asset,
	}
	c.mu.Unlock()

	if available {
		c.log(fmt.Sprintf("update available: %s -> %s", c.current, rel.TagName))
	}
}

func (c *Checker) fetchLatest(ctx context.Context) (*ghRelease, error) {
	api := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// DownloadAndLaunch downloads the setup installer for the latest release into a
// temp file and launches it (Windows only). The caller should shut the server
// down shortly after so the installer can replace the running executable.
func (c *Checker) DownloadAndLaunch(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("auto-update is only supported on Windows")
	}
	c.mu.RLock()
	st := c.status
	c.mu.RUnlock()
	if !st.Available || st.assetURL == "" {
		return fmt.Errorf("no update available")
	}
	if err := validAssetURL(st.assetURL); err != nil {
		return err
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("llama-quartermaster-setup-%s.exe", sanitize(st.Latest)))
	if err := c.download(ctx, st.assetURL, path); err != nil {
		return fmt.Errorf("download installer: %w", err)
	}

	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch installer: %w", err)
	}
	c.log("launched installer " + path + "; shutting down to allow replacement")
	return nil
}

func (c *Checker) download(ctx context.Context, src, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	// Larger timeout than the API client: the installer is tens of MB.
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// validAssetURL only permits https downloads from GitHub-controlled hosts, so a
// poisoned API response can't make us run an arbitrary executable.
func validAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	ok := host == "github.com" ||
		strings.HasSuffix(host, ".github.com") ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if u.Scheme != "https" || !ok {
		return fmt.Errorf("refusing non-GitHub installer URL: %s", raw)
	}
	return nil
}

// setupAsset returns the browser download URL of the setup .exe asset, or "".
func setupAsset(rel *ghRelease) string {
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".exe") && strings.Contains(n, "setup") {
			return a.URL
		}
	}
	return ""
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parseSemver(v string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// newer reports whether b is a higher semver than a. False if either is unparseable.
func newer(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pb[i] != pa[i] {
			return pb[i] > pa[i]
		}
	}
	return false
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitize(s string) string { return unsafeChars.ReplaceAllString(s, "_") }
