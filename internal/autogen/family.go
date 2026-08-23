package autogen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/quant"
)

// Family-scoped sidecar inheritance.
//
// A draft gguf (MTP / DFlash) and a vision projector (mmproj) are published for
// a MODEL, but they land on disk next to ONE download of it. The moment a user
// keeps two quants of the same model in separate folders - the layout every hub
// download produces - or runs a finetune of a model whose base shipped a
// projector, the sidecar is invisible to every copy but the one it came with.
// Dir-local pairing (DiscoverGgufModels) stays the source of truth; this file
// only fills the gaps, so a finetune that ships its own drafter keeps it.
//
// Two gates gate a donation, and both must pass:
//
//  1. NAME - the recipient is either the same model at another quant
//     (ModelBaseKey) or a finetune of the same base at the same parameter count
//     (FamilyKey). These mirror baseKey/familyOf in
//     ui-svelte/src/lib/modelTable.ts, which already groups the models table on
//     the same two axes; keep the pair in sync.
//  2. HEADER - the donor's own model and the recipient agree on arch, embedding
//     length, block count and vocab size. This is the gate that matters: a
//     drafter whose vocab differs does not degrade, it aborts the launch
//     ("tensor 'output.weight' has wrong shape"), and a projector projects into
//     a text tower of one fixed width. Names are a heuristic; the header is not.
//
// Nothing here charges VRAM or emits a flag - it only fills GgufRow, so the
// existing kind/spec gates (matchedDraftSizeGB, effectiveSpec, buildCmdLines)
// still decide what an inherited sidecar is used for. Opting out is per model:
// a spec override without draft-* emits no -md, and the vision twin an
// inherited projector creates can be marked unlisted.

var (
	// A parameter count as publishers write it: 27b, 4b, 0.6b, 350m, gemma's e2b.
	sizeTokenRe = regexp.MustCompile(`(?i)^[a-z]?\d+(?:\.\d+)?[bm]$`)
	// A MoE active-parameter tail: the "a3b" of "qwen3.6-35b-a3b".
	moeTokenRe = regexp.MustCompile(`(?i)^a\d+(?:\.\d+)?b$`)
	// A bare version number that belongs to the name ("gemma-4-12b").
	verTokenRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
)

// quantTokenIndex finds the FIRST quant-shaped part of a split id. First, not
// last: what follows a quant is a build tag ("-MTP", "-preserved", "-MID-HIGH"),
// never a second weight type. Never index 0 - an id that IS a quant has no base
// left.
func quantTokenIndex(parts []string) int {
	return quant.PartIndex(parts)
}

// ModelBaseKey cuts an id at the quant, so the same model at Q4_K_M and at Q8_0
// reduce to one key. It cuts rather than splices out the one part, because both
// variant suffixes and build tags trail the quant.
func ModelBaseKey(id string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(id)), "-")
	for len(parts) > 1 && parts[len(parts)-1] == "gguf" {
		parts = parts[:len(parts)-1]
	}
	i := quantTokenIndex(parts) // already folds a UD/i1 marker in front of the token
	if i > 0 {
		parts = parts[:i]
	}
	// A recipe marker can also arrive already orphaned: discovery strips the
	// quant off "Qwen3.8-27B-UD-Q4_K_XL" and leaves BaseID "qwen3.8-27b-ud".
	// Popping it here is what makes unsloth's dynamic quants twins of the same
	// model's plain quants instead of a family of their own.
	for len(parts) > 1 && quant.PrefixRe.MatchString(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "-")
}

// FamilyKey is the finetune detector: it reduces an id to <model><size>, so
// "thinkingcap-qwen3.6-27b" and "qwen3.6-27b-uncensored-heretic-v2" both resolve
// to "qwen3.6-27b". Deliberately keyed on the parameter count - every finetune
// keeps it, and it is the one token a tuner never rewrites. An id carrying no
// size token is its own family, which is the conservative answer: nothing but an
// exact base-key twin can donate to it.
func FamilyKey(id string) string {
	key := ModelBaseKey(id)
	parts := strings.Split(key, "-")
	i := -1
	for j, p := range parts {
		if sizeTokenRe.MatchString(p) && !moeTokenRe.MatchString(p) {
			i = j
			break
		}
	}
	if i < 1 {
		return key
	}
	start := i - 1
	if i > 1 && verTokenRe.MatchString(parts[i-1]) {
		start = i - 2
	}
	end := i + 1
	if end < len(parts) && moeTokenRe.MatchString(parts[end]) {
		end = i + 2
	}
	return strings.Join(parts[start:end], "-")
}

// inheritTier ranks a donor row for a recipient: 0 = the same model at another
// quant, 1 = the family's un-tuned base model, 2 = another finetune of it,
// -1 = unrelated. Lower wins, so a sidecar always comes from the closest thing
// to the recipient itself: its own other quant first, then the base everyone in
// the family was tuned from, and only then a peer finetune's copy.
func inheritTier(want, donor *GgufRow) int {
	if want.BaseID == "" || donor.BaseID == "" {
		return -1
	}
	donorBase := ModelBaseKey(donor.BaseID)
	if ModelBaseKey(want.BaseID) == donorBase {
		return 0
	}
	f := FamilyKey(want.BaseID)
	if f == "" || f != FamilyKey(donor.BaseID) {
		return -1
	}
	if donorBase == f {
		return 1 // the donor's name reduces to the family key: it IS the base
	}
	return 2
}

// sidecarCompatible reports whether a sidecar published alongside donor also
// loads against want. All four fields are quant-invariant, so two quants of one
// model always agree; a pruned, vocab-extended or re-architected finetune does
// not, and is refused. A field the parser could not fill (0) is refused too - an
// unknown is not a match.
func sidecarCompatible(want, donor Metadata) bool {
	return want.Architecture != "" && strings.EqualFold(want.Architecture, donor.Architecture) &&
		want.EmbeddingLength > 0 && want.EmbeddingLength == donor.EmbeddingLength &&
		want.BlockCount > 0 && want.BlockCount == donor.BlockCount &&
		want.VocabSize > 0 && want.VocabSize == donor.VocabSize
}

// inheritSidecars fills in the draft gguf / vision projector of every row that
// has none of its own, from a compatible row that does. Runs after the dir-local
// pairing pass in DiscoverGgufModels and never overwrites what that pass found.
func inheritSidecars(rows []GgufRow) {
	inheritSidecarsWith(rows, ReadGgufMetadataCached)
}

// inheritSidecarsWith is inheritSidecars over an injectable header reader, so
// the donation rules can be tested without synthesising real gguf headers.
func inheritSidecarsWith(rows []GgufRow, readMeta func(string) (Metadata, error)) {
	var draftDonors, mmprojDonors []int
	for i := range rows {
		if rows[i].IsSam {
			continue
		}
		if rows[i].DraftPath != "" {
			draftDonors = append(draftDonors, i)
		}
		if rows[i].MmprojPath != "" {
			mmprojDonors = append(mmprojDonors, i)
		}
	}
	if len(draftDonors) == 0 && len(mmprojDonors) == 0 {
		return
	}

	// Headers were already parsed by the walk (clip detection), so these are
	// cache hits; the local map only avoids re-deriving the cache key per pair.
	metaOf := map[int]*Metadata{}
	meta := func(i int) *Metadata {
		if m, ok := metaOf[i]; ok {
			return m
		}
		var m *Metadata
		if !rows[i].IsSam {
			if v, err := readMeta(rows[i].FullPath); err == nil {
				m = &v
			}
		}
		metaOf[i] = m
		return m
	}

	// choose picks the donor for row want: nearest tier, then the donor sharing
	// its quant (likeliest to have been downloaded with it), then the largest
	// sibling, then the lowest path so the result is deterministic.
	choose := func(want int, donors []int) int {
		wm := meta(want)
		if wm == nil {
			return -1
		}
		best, bestTier, bestQuant := -1, 0, false
		for _, d := range donors {
			if d == want {
				continue
			}
			tier := inheritTier(&rows[want], &rows[d])
			if tier < 0 {
				continue
			}
			dm := meta(d)
			if dm == nil || !sidecarCompatible(*wm, *dm) {
				continue
			}
			quantMatch := rows[d].Quant != "" && strings.EqualFold(rows[d].Quant, rows[want].Quant)
			if best >= 0 {
				switch {
				case tier != bestTier:
					if tier > bestTier {
						continue
					}
				case quantMatch != bestQuant:
					if !quantMatch {
						continue
					}
				case rows[d].SizeGB != rows[best].SizeGB:
					if rows[d].SizeGB < rows[best].SizeGB {
						continue
					}
				default:
					if rows[d].FullPath >= rows[best].FullPath {
						continue
					}
				}
			}
			best, bestTier, bestQuant = d, tier, quantMatch
		}
		return best
	}

	for i := range rows {
		if rows[i].IsSam {
			continue
		}
		if rows[i].DraftPath == "" {
			if d := choose(i, draftDonors); d >= 0 {
				rows[i].DraftPath = rows[d].DraftPath
				rows[i].DraftKind = rows[d].DraftKind
				rows[i].DraftSizeGB = rows[d].DraftSizeGB
			}
		}
		if rows[i].MmprojPath == "" {
			if d := choose(i, mmprojDonors); d >= 0 {
				rows[i].MmprojPath = rows[d].MmprojPath
				rows[i].MmprojSizeGB = rows[d].MmprojSizeGB
			}
		}
	}
}

// sidecarIndexTTL bounds how stale an inherited answer may be. The config editor
// asks once per model opened, and the walk behind the answer is only worth doing
// once per burst; a fresh download shows up within the window.
const sidecarIndexTTL = 30 * time.Second

// inheritedSidecars is what one discovered model ended up paired with, after
// both the dir-local pass and inheritSidecars have run.
type inheritedSidecars struct {
	draft  draftSidecar
	mmproj mmprojSidecar
}

type sidecarIndexEntry struct {
	at    time.Time
	byKey map[string]inheritedSidecars
}

var (
	sidecarIndexMu sync.Mutex
	sidecarIndexes = map[string]sidecarIndexEntry{}
)

// sidecarIndexFor returns the paired-sidecar map for a root set, memoized for
// sidecarIndexTTL. nil when discovery fails - callers then answer "none", which
// is the same answer they gave before inheritance existed.
func sidecarIndexFor(roots []string) map[string]inheritedSidecars {
	if len(roots) == 0 {
		return nil
	}
	key := strings.Join(roots, "\x00")

	sidecarIndexMu.Lock()
	entry, ok := sidecarIndexes[key]
	if ok && time.Since(entry.at) >= sidecarIndexTTL {
		ok = false
	}
	sidecarIndexMu.Unlock()
	if ok {
		return entry.byKey
	}

	rows, err := DiscoverGgufModelsMulti(roots)
	if err != nil {
		return nil
	}
	byKey := map[string]inheritedSidecars{}
	for _, row := range rows {
		if row.DraftPath == "" && row.MmprojPath == "" {
			continue
		}
		byKey[config.PathKey(row.FullPath)] = inheritedSidecars{
			draft:  draftSidecar{path: row.DraftPath, kind: row.DraftKind, sizeGB: row.DraftSizeGB},
			mmproj: mmprojSidecar{path: row.MmprojPath, sizeGB: row.MmprojSizeGB},
		}
	}
	sidecarIndexMu.Lock()
	sidecarIndexes[key] = sidecarIndexEntry{at: time.Now(), byKey: byKey}
	sidecarIndexMu.Unlock()
	return byKey
}

// DraftSidecarFor answers "which drafter would this model launch with": the one
// in its own dir when there is one, else the one it inherits from a family
// sibling under roots. inherited says which of the two it is, so the UI can say
// so rather than pointing at a folder that visibly holds no drafter.
// DraftSidecarForDir stays the dir-only answer for callers with no roots to scan.
//
// The inherited half costs a models-root walk, so it is memoized (sidecarIndexFor)
// - and reached only when the model's own dir came up empty.
func DraftSidecarFor(roots []string, ggufPath string) (path, kind string, sizeGB float64, inherited bool) {
	if p, k, gb := DraftSidecarForDir(filepath.Dir(ggufPath)); p != "" {
		return p, k, gb, false
	}
	if strings.TrimSpace(ggufPath) == "" {
		return "", "", 0, false
	}
	if d, hit := sidecarIndexFor(roots)[config.PathKey(ggufPath)]; hit && d.draft.path != "" {
		return d.draft.path, d.draft.kind, d.draft.sizeGB, true
	}
	return "", "", 0, false
}

// MmprojSidecarForDir scans one directory (non-recursive) for a vision
// projector, by the same rule the discovery walk uses: a gguf whose header
// reports the "clip" architecture. Name-based detection is deliberately avoided
// - publishers ship projectors as mmproj-*, <model>-mmproj-*, clip-* and bare.
func MmprojSidecarForDir(dir string) (path string, sizeGB float64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.EqualFold(filepath.Ext(ent.Name()), ".gguf") {
			continue
		}
		full := filepath.Join(dir, ent.Name())
		meta, err := ReadGgufMetadataCached(full)
		if err != nil || meta.Architecture != "clip" {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		return full, round(float64(fi.Size())/gib, 2)
	}
	return "", 0
}

// MmprojSidecarFor is DraftSidecarFor for the vision projector: the one in the
// model's own dir, else the one inherited from a family sibling. It answers what
// the "-vision" twin actually loads, which is not something the model's folder
// shows once inheritance is in play.
func MmprojSidecarFor(roots []string, ggufPath string) (path string, sizeGB float64, inherited bool) {
	if p, gb := MmprojSidecarForDir(filepath.Dir(ggufPath)); p != "" {
		return p, gb, false
	}
	if strings.TrimSpace(ggufPath) == "" {
		return "", 0, false
	}
	if d, hit := sidecarIndexFor(roots)[config.PathKey(ggufPath)]; hit && d.mmproj.path != "" {
		return d.mmproj.path, d.mmproj.sizeGB, true
	}
	return "", 0, false
}
