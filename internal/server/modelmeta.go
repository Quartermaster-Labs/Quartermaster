package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/quant"
)

// quantFromPath extracts the quantization label from a gguf path ("...-Q4_K_M",
// "...-UD-Q4_K_XL", "...-mix-q-k-mtp"), "" when the filename carries none. The
// token shape lives in internal/quant, the one place it is written down (the
// Models table in the UI mirrors it) - a weight type this misses is a model
// whose every ctx tier and vision twin shows up as a row of its own.
//
// The FIRST matching part wins: everything after it is a build tag ("-MTP",
// "-MID-HIGH", "-00001-of-00002") rather than another weight type, so a mid-name
// quant is read off the same part autogen's id derivation leaves in place.
func quantFromPath(path string) string {
	if path == "" {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return quant.FromParts(strings.Split(name, "-"))
}

// identCache memoizes a gguf's header identity per path, for the same reason
// sizeCache exists: modelStatus rebuilds every row on every SSE tick, and while
// autogen's metadata cache makes a repeat read cheap, it still stats the file.
var identCache sync.Map // path -> autogen.Identity

func modelIdentity(path string) autogen.Identity {
	if path == "" {
		return autogen.Identity{}
	}
	if v, ok := identCache.Load(path); ok {
		return v.(autogen.Identity)
	}
	id := autogen.IdentityOf(path)
	identCache.Store(path, id)
	return id
}

// modelKeys is everything the Models table needs to place one model that it
// cannot work out from the id.
//
// modelKey / familyKey are the grouping axes: files sharing modelKey are one
// model (one row, one pill per quant), rows sharing familyKey are finetunes of
// one base. The gguf HEADER answers both when it can - it is the only identity
// a renamed download still carries - and the id-derived rules answer for files
// that carry no identity KVs (diffusion ggufs, hand conversions). The two key
// spaces deliberately share a namespace rather than being tagged apart: when a
// header key and an id key spell the same string, the files ARE the same model,
// and landing them on one row is the right answer.
//
// quant stays exactly what it was - the token in the FILENAME, empty when the
// name carries none - because the table also merges two folders' models on it,
// and that merge must only happen on a name both files actually agreed on.
// quantLabel is the tensor-derived truth, offered alongside as a DISPLAY
// fallback for the files whose name says nothing: it names a hand-mixed build
// "IQ4_XS mix" instead of leaving the pill blank, without ever fusing two
// unrelated builds that happen to compute the same label.
func modelKeys(path, id string) (quant, quantLabel, modelKey, familyKey string) {
	ident := modelIdentity(path)
	quant = quantFromPath(path)
	if quant == "" {
		quantLabel = ident.QuantLabel
	}
	modelKey, familyKey = ident.ModelKey, ident.FamilyKey
	if modelKey == "" {
		modelKey = autogen.ModelBaseKey(id)
	}
	if familyKey == "" {
		familyKey = autogen.FamilyKey(id)
	}
	return quant, quantLabel, modelKey, familyKey
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
