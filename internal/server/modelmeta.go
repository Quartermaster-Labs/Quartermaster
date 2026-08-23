package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/quartermaster-labs/quartermaster/internal/quant"
)

// Quantization / weight-type token as it appears in a gguf filename
// ("...-Q4_K_M.gguf", "...-IQ3_XXS-00001-of-00002.gguf", "...-BF16.gguf"), plus
// the recipe markers written immediately before one: unsloth's "UD" (dynamic)
// and mradermacher's "i1" (imatrix), as in "…-UD-Q4_K_XL.gguf".
//
// Both come from internal/quant, the one place the token shape is written down
// (the Models table in the UI mirrors it) — a weight type this misses is a model
// whose every ctx tier and vision twin shows up as a row of its own.
//
// Matched against whole '-'-separated parts of the name (never '_', which is
// INSIDE the token) so a model whose name merely contains something
// quant-shaped is not mistaken for one.
var (
	quantPart   = quant.TokenRe
	quantPrefix = quant.PrefixRe
)

// quantFromPath extracts the quantization label from a gguf path, "" when the
// filename carries none. The FIRST matching part wins: everything after it is
// a build tag ("-MTP", "-MID-HIGH", "-00001-of-00002") rather than another
// weight type, so a mid-name quant ("…-NVFP4-MTP-MID-HIGH") is read off the
// same part autogen's id derivation leaves in place.
func quantFromPath(path string) string {
	if path == "" {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parts := strings.Split(name, "-")
	for i, part := range parts {
		if !quantPart.MatchString(part) {
			continue
		}
		if i > 0 && quantPrefix.MatchString(parts[i-1]) {
			return strings.ToUpper(parts[i-1] + "-" + part)
		}
		return strings.ToUpper(part)
	}
	return ""
}

// sizeCache memoizes on-disk gguf sizes: modelStatus runs on every SSE tick and
// stat-ing every model file each time would hammer the disk for a number that
// cannot change while the file is loaded.
var sizeCache sync.Map // path -> float64 (GiB, -1 = stat failed)

// fileSizeGB returns a gguf's on-disk size in GiB, 0 when the path is empty or
// unreadable. Multi-part files (`-00001-of-0000N.gguf`) are summed so the row
// shows the whole model, not its first shard.
func fileSizeGB(path string) float64 {
	if path == "" {
		return 0
	}
	if v, ok := sizeCache.Load(path); ok {
		gb := v.(float64)
		if gb < 0 {
			return 0
		}
		return gb
	}
	gb := statSizeGB(path)
	sizeCache.Store(path, gb)
	if gb < 0 {
		return 0
	}
	return gb
}

var shardSuffix = regexp.MustCompile(`-\d{5}-of-(\d{5})\.gguf$`)

func statSizeGB(path string) float64 {
	var total int64
	if m := shardSuffix.FindStringSubmatch(path); m != nil {
		matches, err := filepath.Glob(shardSuffix.ReplaceAllString(path, `-*-of-`+m[1]+`.gguf`))
		if err == nil && len(matches) > 0 {
			for _, p := range matches {
				if fi, err := os.Stat(p); err == nil {
					total += fi.Size()
				}
			}
		}
	}
	if total == 0 {
		fi, err := os.Stat(path)
		if err != nil {
			return -1
		}
		total = fi.Size()
	}
	return float64(total) / (1024 * 1024 * 1024)
}
