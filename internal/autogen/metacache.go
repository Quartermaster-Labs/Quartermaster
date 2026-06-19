package autogen

import (
	"os"
	"path/filepath"
	"sync"
)

// Reading GGUF header metadata is the dominant cost of a config regen: every
// save/reload re-parses the header of every model. A regen happens inside the
// running process (UI save, -watch-models, SIGHUP), so an in-memory cache keyed
// by the file fingerprint (size + mtime) lets a repeated regen skip the parse
// entirely and only re-run the cheap sizing math. The fingerprint invalidates
// the entry the moment a GGUF is replaced, so a changed model is always re-read.
var (
	metaCacheMu sync.Mutex
	metaCache   = map[string]cachedMeta{}
)

type cachedMeta struct {
	size  int64
	mtime int64
	meta  Metadata
}

// ReadGgufMetadataCached returns parsed metadata for path, reusing a previously
// parsed result when the file's size and mtime are unchanged. On a miss or a
// stale fingerprint it falls back to a full header read via ReadGgufMetadata and
// caches the result. Safe for concurrent use. When the path can't be stat'd it
// degrades to an uncached read (ReadGgufMetadata reports the real error).
func ReadGgufMetadataCached(path string) (Metadata, error) {
	key := filepath.ToSlash(path)
	fi, statErr := os.Stat(path)
	if statErr == nil {
		metaCacheMu.Lock()
		e, ok := metaCache[key]
		metaCacheMu.Unlock()
		if ok && e.size == fi.Size() && e.mtime == fi.ModTime().UnixNano() {
			return e.meta, nil
		}
	}

	meta, err := ReadGgufMetadata(path)
	if err != nil {
		return Metadata{}, err
	}

	if statErr == nil {
		metaCacheMu.Lock()
		metaCache[key] = cachedMeta{size: fi.Size(), mtime: fi.ModTime().UnixNano(), meta: meta}
		metaCacheMu.Unlock()
	}
	return meta, nil
}
