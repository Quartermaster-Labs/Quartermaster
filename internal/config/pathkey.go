package config

import (
	"path/filepath"
	"runtime"
	"strings"
)

// caseInsensitiveFS reports whether this platform's filesystem treats paths
// differing only in case as the same file. True on Windows and on macOS, whose
// default APFS/HFS+ volumes are case-insensitive; false on Linux and the BSDs,
// where Qwen.gguf and qwen.gguf are two distinct files.
var caseInsensitiveFS = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// PathKey normalises a filesystem path for use as an identity key: separators
// are folded to "/" always, but case is folded ONLY where the filesystem is
// case-insensitive.
//
// The case guard is the whole point. Lowercasing unconditionally is correct on
// Windows and silently destructive on Linux: it made two genuinely different
// models collide on one dedup key in DiscoverGgufModelsMulti, so one of them
// vanished from the catalog with nothing logged. Separator folding stays
// unconditional — it is a no-op on Linux, and on Windows a config may spell the
// same path with either slash.
func PathKey(p string) string {
	p = filepath.ToSlash(p)
	if caseInsensitiveFS {
		p = strings.ToLower(p)
	}
	return p
}

// PathEqual reports whether two paths name the same file as far as the host
// filesystem is concerned. It is the comparison form of PathKey and carries the
// same platform-sensitivity; use it instead of strings.EqualFold anywhere the
// operands are paths rather than extensions.
func PathEqual(a, b string) bool { return PathKey(a) == PathKey(b) }
