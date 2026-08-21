// Package update keeps a running quartermaster on the latest release by
// swapping its own executable in place — no installer, no wizard, no user
// interaction beyond one click.
//
// # Why not the installer
//
// The first version of this package downloaded the Inno Setup installer and ran
// it. That made every update an interactive reinstall: it re-asked for the
// install directory and the models folder, and it re-ran the backend fetch,
// which deleted the user's working llama.cpp build and replaced it with whatever
// the wizard's defaults happened to be. An update that can silently repoint
// serverExe at a different compute backend is not an update.
//
// # What this does instead
//
// The app is one self-contained binary: the UI is embedded, and backend
// installs, config and user data all live outside it (see internal/backends).
// So a new version IS a new file, and applying one is:
//
//	download the bare binary -> verify sha256 -> rename running exe aside ->
//	rename new binary into its place -> restart
//
// Renaming a running executable is legal on Windows (only deleting/overwriting
// it is not) and on every unix, so one code path covers all platforms. Both
// renames are within the exe's own directory, so they are same-volume and
// atomic, and the aside-copy makes the swap reversible: if the second rename
// fails, the first is undone and the running install is untouched. The stale
// .old file is swept on the next start, once nothing has it mapped.
//
// # Who restarts
//
// A desktop install (tray, a shortcut, a terminal) relaunches itself: teardown
// completes, then main spawns the replacement with the same argv and cwd. A
// supervised install (systemd, WinSW) does NOT — a server that bounces itself
// without being asked is a fault, not a feature. There the swap is staged and
// the UI reports that a restart will finish it; the next restart, whenever the
// operator makes it, comes up on the new binary. Docker is refused outright:
// the image is the unit of update there, and a swapped binary would vanish with
// the container.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pollInterval = 6 * time.Hour
	httpTimeout  = 30 * time.Second
	userAgent    = "quartermaster-updater"
)

// Phase is where an in-flight update has got to. It mirrors backends.Job so the
// UI can render both with the same progress component.
const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseStaging     = "staging"
	PhaseReady       = "ready" // swapped; waiting on the restart
	PhaseError       = "error"
)

// Restart modes: who brings the new binary up.
const (
	RestartAuto   = "auto"   // we relaunch ourselves once teardown finishes
	RestartManual = "manual" // supervised: the operator (or the supervisor) restarts
)

// Status is the current check/apply state, surfaced to the UI via /api/version
// and /api/update/status.
type Status struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Available  bool   `json:"available"`
	ReleaseURL string `json:"release_url"`

	// Blocked is non-empty when self-update cannot work here (Docker, a
	// read-only install directory). It is a reason to show the user, not an
	// error: they can still update by other means.
	Blocked string `json:"blocked,omitempty"`
	// Restart is RestartAuto or RestartManual — what happens after the swap.
	Restart string `json:"restart"`

	// Enabled mirrors Checker.Enabled: false for a dev build, or a platform we
	// publish no asset for. The UI needs the difference between "checked, you
	// are current" and "this build never checks" — both otherwise look like an
	// empty status.
	Enabled bool `json:"enabled"`
	// Checked is when the last successful poll landed, zero if none has. It is
	// what makes a "Check for updates" button honest: without it, a check that
	// found nothing is indistinguishable from a button that did nothing.
	Checked time.Time `json:"checked_at,omitempty"`

	// Progress of an in-flight apply.
	Phase string `json:"phase"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Err   string `json:"error,omitempty"`

	assetURL string // download URL for the binary; never exposed to the client
	sha256   string // expected digest, from the release metadata
}

// Checker polls GitHub releases, holds the latest known status, and applies an
// update on request.
type Checker struct {
	repo    string // "owner/name"
	current string
	client  *http.Client
	log     func(string)

	mu     sync.RWMutex
	status Status

	// applying guards against two concurrent applies (a double-clicked button).
	applying bool
	// relaunch is set once the swap succeeded and this process should be
	// replaced by the new binary after teardown. Read by main via Relaunch.
	relaunch bool
}

// New builds a Checker for repo (owner/name) and the running build version.
func New(repo, current string, log func(string)) *Checker {
	if log == nil {
		log = func(string) {}
	}
	c := &Checker{
		repo:    repo,
		current: current,
		client:  &http.Client{Timeout: httpTimeout},
		log:     log,
	}
	c.status = Status{
		Current: current,
		Phase:   PhaseIdle,
		Blocked: blockedReason(),
		Restart: restartMode(),
	}
	return c
}

// Enabled reports whether update checking applies to this build: a release
// (semver) build, on a platform we publish a binary for, that is not running
// somewhere a binary swap would be wrong. Dev builds (version "local_<hash>")
// never poll, so a working tree is never told it is out of date.
func (c *Checker) Enabled() bool { return c.enabled() }

// enabled is the lock-free half, so Status can report it without Enabled
// re-taking a lock Status already holds.
func (c *Checker) enabled() bool {
	if os.Getenv("LQ_NO_UPDATE_CHECK") != "" {
		return false
	}
	if assetName() == "" {
		return false
	}
	if _, ok := parseSemver(c.current); !ok {
		return false
	}
	return true
}

// Run performs an initial check then polls every pollInterval until ctx is done.
// It is a no-op for builds where Enabled() is false. Any binary left aside by a
// previous update is swept first — by now nothing has it mapped.
func (c *Checker) Run(ctx context.Context) {
	SweepOld(c.log)
	if !c.Enabled() {
		return
	}
	_ = c.checkOnce(ctx)
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.checkOnce(ctx)
		}
	}
}

// Status returns a copy of the latest known status (without internal fields).
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.status
	s.assetURL, s.sha256 = "", ""
	s.Enabled = c.enabled()
	return s
}

// Relaunch reports whether an update was applied and this process should be
// replaced by the new binary once shutdown completes. Read by main after
// teardown, so the replacement starts only after the listen sockets are freed.
func (c *Checker) Relaunch() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaunch
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"` // "sha256:<hex>", newer API only
	} `json:"assets"`
}

func (c *Checker) checkOnce(ctx context.Context) error {
	rel, err := c.fetchLatest(ctx)
	if err != nil {
		c.log("update check failed: " + err.Error())
		return err
	}

	want := assetName()
	var url, digest string
	var size int64
	for _, a := range rel.Assets {
		if a.Name == want {
			url, digest, size = a.URL, a.Digest, a.Size
			break
		}
	}
	// The digest field is only on newer API responses; SHA256SUMS is published
	// alongside the binaries for exactly this fallback. No digest from either
	// source means no apply — we will not execute an unverified download.
	sha := strings.TrimPrefix(digest, "sha256:")
	if sha == "" && url != "" {
		for _, a := range rel.Assets {
			if strings.EqualFold(a.Name, "SHA256SUMS") {
				sha = c.fetchSum(ctx, a.URL, want)
				break
			}
		}
	}

	available := newer(c.current, rel.TagName) && url != "" && sha != ""
	if newer(c.current, rel.TagName) && sha == "" {
		c.log(fmt.Sprintf("release %s has no verifiable %s asset; not offering it", rel.TagName, want))
	}

	c.mu.Lock()
	// Never clobber an apply in flight (or a completed one waiting on restart)
	// with a poll result.
	if c.status.Phase == PhaseIdle || c.status.Phase == PhaseError {
		c.status.Phase, c.status.Err = PhaseIdle, ""
		c.status.Total = size
	}
	c.status.Current = c.current
	c.status.Latest = rel.TagName
	c.status.Available = available
	c.status.ReleaseURL = rel.HTMLURL
	c.status.Blocked = blockedReason()
	c.status.Restart = restartMode()
	c.status.Checked = time.Now()
	c.status.assetURL, c.status.sha256 = url, sha
	c.mu.Unlock()

	if available {
		c.log(fmt.Sprintf("update available: %s -> %s", c.current, rel.TagName))
	}
	return nil
}

// CheckNow polls GitHub immediately and reports whether the poll itself
// succeeded, for a user who clicked "check for updates" rather than waiting out
// the six-hour tick. It returns the network error rather than only logging it:
// a button that silently does nothing when the machine is offline is worse than
// no button.
//
// Safe to call while an apply is in flight -- checkOnce leaves a non-idle phase
// alone -- and safe to call concurrently, the status write being the only
// shared state.
func (c *Checker) CheckNow(ctx context.Context) error {
	if !c.enabled() {
		return errors.New("this build does not check for updates")
	}
	return c.checkOnce(ctx)
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

// fetchSum pulls one file's digest out of a SHA256SUMS asset ("<hex>  <name>"
// per line, sha256sum's own format). "" when absent or malformed.
func (c *Checker) fetchSum(ctx context.Context, src, name string) string {
	if err := validAssetURL(src); err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		// The name may carry sha256sum's binary-mode "*" prefix.
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// assetName is the release asset holding the binary for this platform, or ""
// when we publish none for it. Matched exactly, so the Windows binary
// (quartermaster-windows-amd64.exe) is never confused with the first-install
// wizard (quartermaster-setup-vX.Y.Z.exe) sitting in the same release.
//
// These are ASSET names, not installed filenames. On Windows the installed
// binary is Quartermaster.exe: the two deliberately differ, because exePath()
// swaps whatever path this process runs from, while the asset name has to keep
// matching what every already-installed version goes looking for. Renaming the
// upload to follow the binary would cut those installs off from updates.
func assetName() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "quartermaster-windows-amd64.exe"
	case "linux/amd64":
		return "quartermaster-linux-amd64"
	case "linux/arm64":
		return "quartermaster-linux-arm64"
	case "darwin/arm64":
		return "quartermaster-darwin-arm64"
	}
	return ""
}

// validAssetURL only permits https downloads from GitHub-controlled hosts, so a
// poisoned API response can't point us at an arbitrary binary. The sha256 check
// in apply is the real guarantee; this keeps the request itself in bounds.
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
		return fmt.Errorf("refusing non-GitHub download URL: %s", raw)
	}
	return nil
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

// newer reports whether b is a higher semver than a. False if either is
// unparseable, which is what keeps a dev build (local_<hash>) and a prerelease
// tag (v1.0.0-rc1) from ever being offered as an update.
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

// exePath resolves the running executable, following symlinks so a
// /usr/local/bin/quartermaster -> /opt/... install swaps the real file rather
// than replacing the link.
func exePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p, nil
}
