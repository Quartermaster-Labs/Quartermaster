package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Repo is the GitHub repository (owner/name) releases are published to.
//
// It lives here rather than in each caller because the updater and the
// first-run wizard have to agree on it: the wizard downloads the binary the
// updater will later replace in place, and a disagreement would mean an install
// that can never be updated.
const Repo = "Quartermaster-Labs/quartermaster"

// FetchBinary downloads this platform's server binary from repo's latest
// release into dir and returns the path it wrote.
//
// This is what makes a standalone setup download work off Windows. There the
// wizard carries an Inno package as an embedded payload; here it has nothing to
// carry, so it fetches the same asset the updater does. A tarball or a dev tree
// never reaches this: the caller copies the binary sitting beside it instead,
// which is both faster and the only thing that works offline.
//
// The verification rule is the updater's, deliberately not a weaker one. An
// asset with no sha256 from either the API's per-asset digest or the SHA256SUMS
// published beside it is refused rather than executed, because the whole point
// of the wizard is that the thing it lands is the thing the project released.
func FetchBinary(ctx context.Context, repo, dir string, onProgress func(done, total int64), log func(string)) (string, error) {
	if log == nil {
		log = func(string) {}
	}
	want := assetName()
	if want == "" {
		return "", fmt.Errorf("no binary is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	c := New(repo, "", log)
	rel, err := c.fetchLatest(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up the latest release: %w", err)
	}

	var src, digest string
	for _, a := range rel.Assets {
		if a.Name == want {
			src, digest = a.URL, a.Digest
			break
		}
	}
	if src == "" {
		return "", fmt.Errorf("release %s carries no %s asset", rel.TagName, want)
	}
	sha := strings.TrimPrefix(digest, "sha256:")
	if sha == "" {
		for _, a := range rel.Assets {
			if strings.EqualFold(a.Name, "SHA256SUMS") {
				sha = c.fetchSum(ctx, a.URL, want)
				break
			}
		}
	}
	if sha == "" {
		return "", fmt.Errorf("release %s publishes no checksum for %s", rel.TagName, want)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	// Downloaded to .part and renamed, so an interrupted fetch cannot leave a
	// truncated binary sitting at the name the wizard is about to launch.
	dst := filepath.Join(dir, want)
	tmp := dst + ".part"
	log(fmt.Sprintf("downloading %s %s", want, rel.TagName))
	if err := c.download(ctx, src, tmp, onProgress); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("downloading %s: %w", want, err)
	}
	got, err := fileSHA256(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if !strings.EqualFold(got, sha) {
		os.Remove(tmp)
		return "", fmt.Errorf("checksum mismatch for %s: got %s, expected %s", want, got, sha)
	}
	// Before the rename: a file that appears under its final name is one the
	// user may double-click, and it has to already be runnable.
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("making %s executable: %w", want, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("writing %s: %w", dst, err)
	}
	log(fmt.Sprintf("installed %s %s", want, rel.TagName))
	return dst, nil
}
