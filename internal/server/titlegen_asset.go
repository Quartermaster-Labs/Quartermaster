package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The title model (see titlegen.go) used to be a //go:embed of the gguf itself.
// That put 79 MiB of weights in the repo, in every clone, in every release
// archive and in every self-update — for a feature that degrades gracefully
// (chat titles fall back to the chat model client-side when this is absent).
// It is fetched once instead, on first use, and cached beside the generate
// control file. The Windows installer prefetches it so the common install still
// has titles working the moment it starts.
const (
	titlegenAssetName = "titlegen-flan-t5-small-q8_0.gguf"

	// titlegenAssetURL is a release asset on this repo rather than the upstream
	// Hugging Face repo: what we need is a specific f16 conversion quantized to
	// Q8_0, not the source safetensors, and pinning our own copy means the hash
	// below stays valid regardless of what upstream re-uploads.
	titlegenAssetURL = "https://github.com/Quartermaster-Labs/quartermaster/releases/download/assets-v1/" + titlegenAssetName

	// titlegenAssetSHA256 pins the exact bytes. A download that does not match is
	// discarded — a proxy's error page saved as a .gguf is otherwise a confusing
	// crash inside llama-completion much later.
	titlegenAssetSHA256 = "a040e12a77a3da86491a4347296cfd16b76b41e6c6b19b58fe6f2dc072edccb9"
	titlegenAssetSize   = 82866336
)

// titlegenFetchMu serializes the one-time fetch so two turns starting together
// cannot both download.
var titlegenFetchMu sync.Mutex

// titlegenFetchFailed records that we already tried and failed this run. The
// caller is a chat turn: retrying an 80 MiB download on every title request
// while the box is offline would be worse than having no titles.
var titlegenFetchFailed bool

// titlegenCachePath is where the fetched model lives: <dir(generatePath)>/titlegen.
// Deliberately OUTSIDE the models root so autogen's discovery walk never picks
// the title model up and publishes it as a servable chat model — and it cannot
// be served anyway (llama-server has no encoder-decoder path).
func titlegenCachePath(generatePath string) string {
	if generatePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(generatePath), "titlegen", titlegenAssetName)
}

// titlegenCached reports whether the cache holds a complete copy. The size check
// is what makes a killed download recoverable rather than permanently poisoning
// every later run.
func titlegenCached(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() == titlegenAssetSize
}

// FetchTitlegenAsset downloads the title model into the cache beside
// generatePath unless it is already there. Exported for the installer/CLI
// prefetch path; the chat turn calls it lazily through titlegenModelPath.
func FetchTitlegenAsset(generatePath string) (string, error) {
	path := titlegenCachePath(generatePath)
	if path == "" {
		return "", fmt.Errorf("no generate control file: nowhere to cache the title model")
	}
	if titlegenCached(path) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(titlegenAssetURL)
	if err != nil {
		return "", fmt.Errorf("fetch title model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch title model: %s", resp.Status)
	}

	// Write-then-rename so a concurrent reader never sees a partial file, and
	// hash on the way through rather than re-reading 80 MiB afterwards.
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	_, err = io.Copy(f, io.TeeReader(resp.Body, sum))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("write title model: %w", err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != titlegenAssetSHA256 {
		os.Remove(tmp)
		return "", fmt.Errorf("title model checksum mismatch: got %s", got)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// titlegenModelPath resolves the title gguf: the QM_TITLEGEN_MODEL env var wins
// (bring your own title model), else the cached download, fetched on first use.
// Returns "" when there is no model to run — the caller treats that as "no
// server-side title", which is a supported state.
func titlegenModelPath(generatePath string) string {
	if p := strings.TrimSpace(os.Getenv("QM_TITLEGEN_MODEL")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}

	cached := titlegenCachePath(generatePath)
	if cached == "" {
		// No generate control file yet: nowhere to cache. Return without
		// touching titlegenFetchFailed — an early call must not poison the
		// fetch for the rest of the run.
		return ""
	}

	titlegenFetchMu.Lock()
	defer titlegenFetchMu.Unlock()
	if titlegenCached(cached) {
		return cached
	}
	if titlegenFetchFailed {
		return ""
	}
	path, err := FetchTitlegenAsset(generatePath)
	if err != nil {
		titlegenFetchFailed = true
		return ""
	}
	return path
}
