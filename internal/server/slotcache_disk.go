package server

// On-disk layout of the slot-cache snapshot directory: file naming, the
// LRU/quota pruning passes, and the seed-selection scan that picks which stored
// preamble a cold load should restore from. Everything here is guarded by
// sc.diskMu (directory-wide passes) — see the lock order in CLAUDE.md.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// prunePreambleFiles keeps only the most-recently-used maxPreambleGenerations
// preamble caches per model, deleting the rest (and their .meta). "Used" = mtime,
// which preamble-hit touches — so this is LRU by access, not by mint time, and a
// hot but old preamble (pi's stable prompt) survives while a stale one is evicted.
func (sc *slotCache) prunePreambleFiles(model string) {
	sc.diskMu.Lock() // see enforceCaps
	defer sc.diskMu.Unlock()
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	prefix := sanitize(model) + "__" + preambleKeyPrefix
	type f struct {
		path  string
		mtime time.Time
	}
	var files []f
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".bin") || !strings.HasPrefix(name, prefix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, f{filepath.Join(sc.dir, name), info.ModTime()})
		}
	}
	if len(files) <= maxPreambleGenerations {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) }) // newest first
	for _, fl := range files[maxPreambleGenerations:] {
		_ = os.Remove(fl.path)
		_ = os.Remove(strings.TrimSuffix(fl.path, ".bin") + ".meta")
	}
}

func (sc *slotCache) fileExists(model, key string) bool {
	_, err := os.Stat(filepath.Join(sc.dir, fileName(model, key)))
	return err == nil
}

// enforceCaps deletes the oldest files (by mtime) until the cache is within both
// the byte budget and the file-count cap. The just-saved file (newest) is never
// the eviction target. keepKey/keepModel name it so it is also skipped explicitly.
func (sc *slotCache) enforceCaps(keepModel, keepKey string) {
	// Directory-wide scan-and-delete: callers hold different per-model locks, so
	// this needs its own to keep two models from racing over the same files.
	sc.diskMu.Lock()
	defer sc.diskMu.Unlock()
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	type f struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []f
	var total int64
	keep := fileName(keepModel, keepKey)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		if strings.Contains(e.Name(), "__"+preambleKeyPrefix) {
			continue // preamble caches are sticky shared seeds; pruned separately
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, f{filepath.Join(sc.dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, fl := range files {
		if total <= sc.maxBytes && len(files) <= sc.maxFiles {
			break
		}
		if filepath.Base(fl.path) == keep {
			continue
		}
		if err := os.Remove(fl.path); err == nil {
			total -= fl.size
			files = files[:len(files)-1] // count drops by one (value irrelevant; we only read len)
			// Drop the preamble sidecar too (best-effort): foo.bin -> foo.meta.
			_ = os.Remove(strings.TrimSuffix(fl.path, ".bin") + ".meta")
		}
	}
}

// fileName is the on-disk name for a (model, conversation) KV snapshot. Keyed by
// the stable conversation anchor so the same chat overwrites its own file.
func fileName(model, key string) string {
	return sanitize(model) + "__" + key + ".bin"
}

// metaName is the preamble sidecar for a (model, conversation) KV snapshot.
func metaName(model, key string) string {
	return sanitize(model) + "__" + key + ".meta"
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string {
	return unsafeFile.ReplaceAllString(s, "_")
}

// short trims a 32-hex anchor to a readable prefix for the monitoring UI.
func short(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// commonPrefixLen returns the number of leading bytes a and b share.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffixLen returns the number of trailing bytes a and b share.
func commonSuffixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// preambleDynDeltaMax bounds the "small dynamic bits" (a date, a session id) that
// may differ between two generations of the SAME agent's preamble. A larger middle
// diff means a genuinely different agent, not a daily refresh of the same one.
const preambleDynDeltaMax = 512

// supersedesPreamble reports whether new and old are the same preamble apart from
// one short dynamic span — a shared prefix and suffix bracketing a tiny middle
// diff on both sides. True => old is a stale generation of new's agent, safe to
// drop. Requires the diff to be small on BOTH sides so a different agent that
// merely shares identical tools (long common suffix) isn't mistaken for a refresh.
func supersedesPreamble(new, old string) bool {
	lcp := commonPrefixLen(new, old)
	lcs := commonSuffixLen(new[lcp:], old[lcp:]) // beyond the shared prefix, no overlap
	return len(new)-lcp-lcs <= preambleDynDeltaMax && len(old)-lcp-lcs <= preambleDynDeltaMax
}

// dropStalePreambles deletes prior preamble caches for the model that are the same
// agent's preamble as `preamble` apart from a small dynamic span (supersedesPreamble)
// — e.g. yesterday's date-stamped generation once today's is minted. keepKey is the
// just-minted file, never a target. Backstop: prunePreambleFiles still caps the rest.
func (sc *slotCache) dropStalePreambles(model, keepKey, preamble string) {
	sc.diskMu.Lock() // see enforceCaps
	defer sc.diskMu.Unlock()
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return
	}
	prefix := sanitize(model) + "__"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta") || !strings.HasPrefix(name, prefix+preambleKeyPrefix) {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".meta")
		if key == keepKey {
			continue
		}
		stored, err := os.ReadFile(filepath.Join(sc.dir, name))
		if err != nil {
			continue
		}
		if supersedesPreamble(preamble, string(stored)) {
			_ = os.Remove(filepath.Join(sc.dir, fileName(model, key)))
			_ = os.Remove(filepath.Join(sc.dir, name))
		}
	}
}

// bestSeed finds the saved session for `model` to seed a cold slot from. Among
// candidates whose stored preamble shares >= seedMinPrefixBytes with the incoming
// `preamble`, it minimizes *over-restore* — KV restored beyond the shared prefix.
// Restoring a long sibling CONVERSATION whose tail diverges from the new prompt
// is wasted I/O on plain-attention models and actively harmful on hybrid/recurrent
// ones: the restored state sits at the sibling's full length, the new prompt
// matches only the shared preamble, and the layers that can't be rewound emit
// "non-consecutive token position" + full reprocess (0 reuse). A preamble-cache
// file has no conversation tail, so restoring it never exceeds the prefix — prefer
// it. We don't store seed token-length, so the proxy is: tail-free first, then
// longest shared prefix, then smallest .bin (least excess tail).
//
// Returns its key and the .bin size; this is the Tier-1 lookup that lets a cold,
// never-seen conversation reuse a similar prior session's static system+tools
// preamble via llama-server's own prefix matching after restore.
func (sc *slotCache) bestSeed(model, preamble string) (key string, bytes int64, ok bool) {
	if preamble == "" {
		return "", 0, false
	}
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		return "", 0, false
	}
	prefix := sanitize(model) + "__"
	bestLen := 0
	var bestPreamble bool
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta") || !strings.HasPrefix(name, prefix) {
			continue
		}
		k := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".meta")
		fi, err := os.Stat(filepath.Join(sc.dir, fileName(model, k)))
		if err != nil || fi.Size() > seedMaxFileBytes {
			continue // no KV file or too big to be worth reading for a prefix
		}
		stored, err := os.ReadFile(filepath.Join(sc.dir, name))
		if err != nil {
			continue
		}
		n := commonPrefixLen(preamble, string(stored))
		if n < seedMinPrefixBytes {
			continue
		}
		isPre := isPreambleKey(k)
		// Preference order: tail-free (preamble cache) beats a tailed conversation;
		// then longer shared prefix; then smaller .bin (least excess KV to restore).
		better := false
		switch {
		case key == "":
			better = true
		case isPre != bestPreamble:
			better = isPre
		case n != bestLen:
			better = n > bestLen
		default:
			better = fi.Size() < bytes
		}
		if better {
			bestLen, bestPreamble, key, bytes = n, isPre, k, fi.Size()
		}
	}
	if key != "" {
		return key, bytes, true
	}
	return "", 0, false
}

// splitFileName reverses fileName: "model__key.bin" -> (model, key). The model
// half is the sanitized id; the key is the trailing hash after the last "__".
func splitFileName(name string) (model, key string) {
	base := strings.TrimSuffix(name, ".bin")
	if i := strings.LastIndex(base, "__"); i >= 0 {
		return base[:i], base[i+2:]
	}
	return base, ""
}
