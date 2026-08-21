package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// stageDir is the scratch folder next to the executable. It must be a sibling
// of the exe, not %TEMP%: both halves of the swap are renames, and a rename is
// only atomic (and only guaranteed to work at all) within one volume.
const stageDir = ".qm-update"

// oldSuffix marks the outgoing binary. It cannot be deleted while this process
// has it mapped, so it is renamed aside now and swept on the next start.
const oldSuffix = ".old"

// downloadTimeout bounds the fetch. The binary is tens of MB; the ceiling is
// there to stop a stalled connection from pinning the apply forever.
const downloadTimeout = 20 * time.Minute

// progressEvery throttles progress callbacks so a 40 MB download doesn't
// generate a million status writes.
const progressEvery = 512 << 10

// Apply downloads the latest release binary, verifies it, and swaps it into
// place. It returns once the new binary is installed — it does NOT restart
// anything; see Relaunch and the package doc for who does.
//
// Runs on its own context, not the request's: a browser tab closing mid-download
// must not abort an update that is already replacing files on disk.
func (c *Checker) Apply(ctx context.Context) error {
	c.mu.Lock()
	if c.applying {
		c.mu.Unlock()
		return fmt.Errorf("an update is already in progress")
	}
	st := c.status
	if !st.Available || st.assetURL == "" || st.sha256 == "" {
		c.mu.Unlock()
		return fmt.Errorf("no verified update available")
	}
	if st.Blocked != "" {
		c.mu.Unlock()
		return fmt.Errorf("cannot self-update here: %s", st.Blocked)
	}
	c.applying = true
	c.status.Phase, c.status.Err, c.status.Done = PhaseDownloading, "", 0
	c.mu.Unlock()

	err := c.apply(ctx, st)

	c.mu.Lock()
	c.applying = false
	if err != nil {
		c.status.Phase, c.status.Err = PhaseError, err.Error()
	} else {
		c.status.Phase, c.status.Err = PhaseReady, ""
		c.status.relaunchable()
		c.relaunch = c.status.Restart == RestartAuto
	}
	c.mu.Unlock()

	if err != nil {
		c.log("update failed: " + err.Error())
	} else if c.status.Restart == RestartAuto {
		c.log("update staged; restarting into " + st.Latest)
	} else {
		c.log("update staged; restart the service to run " + st.Latest)
	}
	return err
}

// relaunchable re-reads the restart mode at apply time rather than trusting the
// one cached at startup — an install can be adopted by a supervisor between the
// two.
func (s *Status) relaunchable() { s.Restart = restartMode() }

func (c *Checker) apply(ctx context.Context, st Status) error {
	exe, err := exePath()
	if err != nil {
		return fmt.Errorf("locate running executable: %w", err)
	}
	dir := filepath.Dir(exe)
	stage := filepath.Join(dir, stageDir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("create staging dir (is the install directory writable?): %w", err)
	}

	newBin := filepath.Join(stage, filepath.Base(exe)+".new")
	_ = os.Remove(newBin)
	if err := c.download(ctx, st.assetURL, newBin, func(done, total int64) {
		c.mu.Lock()
		c.status.Done, c.status.Total = done, total
		c.mu.Unlock()
	}); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(newBin) // no-op once the swap renames it away

	c.mu.Lock()
	c.status.Phase = PhaseVerifying
	c.mu.Unlock()
	got, err := fileSHA256(newBin)
	if err != nil {
		return fmt.Errorf("hash download: %w", err)
	}
	if !strings.EqualFold(got, st.sha256) {
		return fmt.Errorf("checksum mismatch (got %s, want %s)", got, st.sha256)
	}
	// The download arrives without an executable bit on unix.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(newBin, 0o755); err != nil {
			return fmt.Errorf("chmod: %w", err)
		}
	}

	c.mu.Lock()
	c.status.Phase = PhaseStaging
	c.mu.Unlock()
	return swap(exe, newBin, stage)
}

// swap replaces exe with newBin, reversibly.
//
// Renaming the running executable aside is what makes this possible: Windows
// refuses to delete or overwrite a mapped image but allows renaming it, and
// unix allows all three. Both renames stay inside the exe's directory, so
// neither can fail on a cross-device boundary, and the aside-copy is the
// rollback: if the second rename fails, the first is undone and the install is
// exactly as it was.
func swap(exe, newBin, stage string) error {
	old := filepath.Join(stage, filepath.Base(exe)+oldSuffix)
	_ = os.Remove(old) // a leftover from a previous update, now unmapped

	if err := renameRetry(exe, old); err != nil {
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := renameRetry(newBin, exe); err != nil {
		if rbErr := renameRetry(old, exe); rbErr != nil {
			// Both renames failed: the install has no binary at its own path.
			// Say so loudly and name the file to move back by hand.
			return fmt.Errorf("install new binary: %w; ROLLBACK ALSO FAILED (%v) — move %s back to %s manually", err, rbErr, old, exe)
		}
		return fmt.Errorf("install new binary (rolled back, install unchanged): %w", err)
	}
	return nil
}

// renameRetry works around the transient sharing violations Windows produces
// when a scanner or an indexer has the file open for a moment. Unix takes the
// first attempt every time.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// SweepOld removes binaries left aside by a previous update. Called at startup,
// when the replaced image is no longer mapped by anything and can finally be
// deleted. Failures are logged and ignored: a leftover .old is inert.
func SweepOld(log func(string)) {
	exe, err := exePath()
	if err != nil {
		return
	}
	sweepDir(filepath.Join(filepath.Dir(exe), stageDir), log)
}

// sweepDir is SweepOld against an explicit staging directory.
func sweepDir(stage string, log func(string)) {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return
	}
	var swept int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), oldSuffix) && !strings.HasSuffix(e.Name(), ".new") {
			continue
		}
		if err := os.Remove(filepath.Join(stage, e.Name())); err == nil {
			swept++
		}
	}
	if swept > 0 && log != nil {
		log(fmt.Sprintf("update: cleaned up %d file(s) from the previous update", swept))
	}
	// Empty now? Drop the folder too, so a settled install has no scratch dir.
	if rest, err := os.ReadDir(stage); err == nil && len(rest) == 0 {
		_ = os.Remove(stage)
	}
}

// download streams src to dst, reporting progress. Total is the Content-Length
// (0 when unknown) so the UI can show a bar before the first byte lands.
func (c *Checker) download(ctx context.Context, src, dst string, onProgress func(done, total int64)) error {
	if err := validAssetURL(src); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, on: onProgress}
	if _, err := io.Copy(f, pr); err != nil {
		return err
	}
	return f.Sync()
}

type progressReader struct {
	r       io.Reader
	total   int64
	done    int64
	lastRep int64
	on      func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.on != nil && (p.done-p.lastRep >= progressEvery || err != nil) {
		p.lastRep = p.done
		p.on(p.done, p.total)
	}
	return n, err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
