package autogen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// GgufRow describes one discovered served model (one .gguf, or the first shard
// of a split set). Mirrors the PowerShell Get-LocalGgufModels output row.
type GgufRow struct {
	ID        string // BaseID + "-<quant>" (lowercased); BaseID when no quant
	BaseID    string // model name only, no publisher prefix, no quant
	FullPath  string // real file llama-server is given (first shard)
	FileName  string // shard-stripped file name
	Quant     string // detected quant token, upper-case ("" if none)
	SizeGB    float64
	Publisher string
	Repo      string
	// Separate MTP or DFlash draft gguf sitting in the same dir, "" if none.
	// MTP sidecars are named "mtp-*.gguf"; DFlash drafters carry "dflash"
	// anywhere in the name (e.g. "Qwen3.6-35B-A3B-DFlash-Q8_0.gguf").
	DraftPath   string
	DraftKind   string  // "mtp" | "dflash"; "" when DraftPath == ""
	DraftSizeGB float64 // on-disk size of DraftPath; 0 when DraftPath == ""
	// Vision projector (clip-arch gguf) sitting in the same dir, if any. Loaded via
	// --mmproj to enable image input; drives the auto-generated "-vision" variant.
	MmprojPath   string
	MmprojSizeGB float64
	// Qwen3-TTS audio codec (the "qwen-tokenizer-*hz" gguf) sitting in the same
	// dir as a talker, if any. Loaded via tts-server --codec, never served alone.
	CodecPath   string
	CodecSizeGB float64
	// IsSam marks a SAM (Segment Anything) model: a *.ggml file (NOT gguf — sam3.cpp
	// uses the raw ggml format, no metadata header) served by sam3_server. Routed
	// before the gguf metadata read in emitModel, since ReadGgufMetadata can't parse it.
	IsSam bool
}

var (
	shardRe = regexp.MustCompile(`-(\d{5})-of-\d{5}\.gguf$`)
	// Quant tokens: Q4_0, Q6_K, Q4_K_M, IQ3_XS, Q8_0, F16, BF16, F32. Bounded by
	// a separator before and a separator / .gguf after.
	quantRe      = regexp.MustCompile(`(?i)[-_.](IQ\d+(?:_[A-Z0-9]+)*|Q\d+(?:_[A-Z0-9]+)*|F16|BF16|F32)(?:[._-]|\.gguf$)`)
	ggufSuffixRe = regexp.MustCompile(`(?i)-GGUF$`)
	// Separate MTP/draft sidecar (e.g. Gemma-4 ships "mtp-gemma-4-12B-it.gguf"
	// alongside the main model). Loaded via -md + --spec-type draft-mtp, not served alone.
	mtpFileRe = regexp.MustCompile(`(?i)^mtp[-_.]`)
	// "FastMTP" heads ("...-Aggressive-FastMTP-32K.gguf") are skipped outright:
	// neither served nor paired as a draft.
	//
	// The file is a well-formed reduced-vocab draft, not a broken one. It holds
	// exactly the 19 tensors of the single nextn block (blk.64.nextn.*) with its
	// output.weight shrunk to [n_embd, 32768] plus a `d2t` table remapping those
	// draft ids back onto the parent's 248320-token vocab: the EAGLE-3 style
	// convention, where the small head is the whole point of the speedup.
	// Two independent reasons it must not become a -md sidecar:
	//  1. No llama-server build we ship references `d2t` at all, so the loader
	//     validates output.weight against the tokenizer's n_vocab and hard-fails
	//     ("tensor 'output.weight' has wrong shape; expected 5120, 248320, got
	//     5120, 32768"), taking the whole launch down with it.
	//  2. It is redundant regardless: these repos bake the same nextn block into
	//     the main gguf at full head size, so IsMTP is already true and
	//     effectiveSpec drives draft-mtp off the baked layer with no -md.
	//
	// Revisit only if a build starts advertising reduced-vocab drafts. Kept
	// distinct from mtpFileRe so a genuine sidecar still pairs. Deliberately
	// pinned to "fastmtp", not a bare "mtp" infix, which would also swallow real
	// models naming a baked-in head ("...-Native-MTP-Preserved-Q4_K_M.gguf").
	fastMtpFileRe = regexp.MustCompile(`(?i)(^|[-_.])fast[-_.]?mtp([-_.]|\.gguf$)`)
	// Separate DFlash draft sidecar (block-diffusion drafter, e.g.
	// "Qwen3.6-35B-A3B-DFlash-Q8_0.gguf"). Unlike the MTP prefix convention,
	// publishers embed "dflash" as an infix, so match anywhere in the name.
	// Loaded via -md + --spec-type draft-dflash, not served alone.
	dflashFileRe = regexp.MustCompile(`(?i)dflash`)
	// Qwen3-TTS audio codec / tokenizer sidecar (e.g. "qwen-tokenizer-12hz-Q8_0.gguf").
	// Loaded via tts-server --codec alongside the talker, never served as its own
	// model. "hz" scopes the tokenizer match so a normal model file can't trip it.
	ttsCodecFileRe = regexp.MustCompile(`(?i)tokenizer-?\d+hz|codec`)
	// Diffusion text encoders / VAEs (T5-XXL, UMT5, CLIP-L/G, standalone VAE).
	// These are COMPONENTS of an image model — wired in via settings.encoders
	// (see image.go resolveComponents), never served on their own. Without this
	// they parse as ordinary ggufs and get emitted as llama-server "LLM" rows
	// (a t5 encoder has no chat template and no decoder; it can't generate).
	// Narrow on purpose: an "-encoder" tail or a known encoder/VAE stem only, so
	// a real seq2seq LLM (flan-t5-*) is untouched.
	encoderFileRe = regexp.MustCompile(`(?i)(^|[-_.])(t5xxl|t5[-_]?v1[-_.]?1|umt5|clip[-_]?[lg]|text[-_]?encoder|ae|vae|taesd\w*)([-_.]|\.gguf$)|[-_.]encoder([-_.]|\.gguf$)`)
	// GGUF architectures that are encoder-only diffusion components. "clip" is
	// handled separately (it doubles as a vision projector and IS paired).
	// Plain "t5" is deliberately absent: flan-t5 is a real seq2seq LLM
	// llama.cpp serves; only the encoder-only archs are components.
	encoderArch = map[string]bool{"t5encoder": true, "umt5": true}
	// SAM (Segment Anything) model files, served by sam3_server. These are raw
	// *.ggml (not gguf), so they're matched by name — ".ggml" alone is too generic
	// (other ggml projects use it). Add new families here (mobilesam, etc.).
	samFileRe = regexp.MustCompile(`(?i)(sam2|sam3|edgetam|hiera)`)
)

// quantFromName extracts the quant token (upper-cased) from a gguf file name,
// or "" when none is present.
func quantFromName(name string) string {
	if mm := quantRe.FindStringSubmatch(name); mm != nil {
		return strings.ToUpper(mm[1])
	}
	return ""
}

// DiscoverGgufModelsMulti walks each root and returns the union of rows,
// de-duplicated by full path (a model reachable from two overlapping roots is
// served once). Roots are scanned in order; the first occurrence wins.
func DiscoverGgufModelsMulti(roots []string, skipPatterns ...string) ([]GgufRow, error) {
	seen := map[string]bool{}
	var all []GgufRow
	for _, root := range roots {
		rows, err := DiscoverGgufModels(root, skipPatterns...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := config.PathKey(row.FullPath)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, row)
		}
	}
	return all, nil
}

// DiscoverGgufModels walks modelsRoot for *.gguf files and returns one row per
// served model. Vision projectors (clip-arch ggufs) and non-first split shards
// are skipped (projectors are paired to their model dir instead). skipPatterns
// are optional filename globs to exclude.
func DiscoverGgufModels(modelsRoot string, skipPatterns ...string) ([]GgufRow, error) {
	if strings.TrimSpace(modelsRoot) == "" {
		return nil, nil // no models root configured yet => empty catalog
	}
	info, err := filepath.Abs(modelsRoot)
	if err != nil {
		return nil, err
	}
	modelsRoot = info

	var rows []GgufRow
	draftByDir := map[string]draftSidecar{}   // dir -> paired MTP/DFlash draft gguf
	mmprojByDir := map[string]mmprojSidecar{} // dir -> vision projector gguf
	codecByDir := map[string]codecSidecar{}   // dir -> Qwen3-TTS audio codec gguf
	walkErr := filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, matching -ErrorAction SilentlyContinue
		}
		// SAM models are raw *.ggml (no gguf header); served by sam3_server. Emit a
		// row by filename and skip the gguf pipeline (metadata/quant/shard logic).
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".ggml") && samFileRe.MatchString(d.Name()) {
			fi, e := d.Info()
			if e != nil {
				return nil
			}
			base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			id := strings.ToLower(strings.ReplaceAll(base, "_", "-"))
			rows = append(rows, GgufRow{
				ID:       id,
				BaseID:   id,
				FullPath: path,
				FileName: d.Name(),
				SizeGB:   round(float64(fi.Size())/gib, 2),
				IsSam:    true,
			})
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".gguf") {
			return nil
		}
		name := d.Name()
		// Unloadable FastMTP head: not served, not paired. Checked ahead of the
		// draft rules so it can never become a -md sidecar.
		if fastMtpFileRe.MatchString(name) {
			return nil
		}
		// Separate MTP or DFlash draft: record it for pairing, don't serve it as
		// its own model. Checked in this order since an MTP sidecar never also
		// matches the dflash infix.
		if mtpFileRe.MatchString(name) {
			if fi, e := d.Info(); e == nil {
				draftByDir[filepath.Dir(path)] = draftSidecar{path: path, sizeGB: round(float64(fi.Size())/gib, 2), kind: "mtp"}
			}
			return nil
		}
		if dflashFileRe.MatchString(name) {
			if fi, e := d.Info(); e == nil {
				draftByDir[filepath.Dir(path)] = draftSidecar{path: path, sizeGB: round(float64(fi.Size())/gib, 2), kind: "dflash"}
			}
			return nil
		}
		// Qwen3-TTS audio codec: pair it to the talker in its dir, don't serve it.
		if ttsCodecFileRe.MatchString(name) {
			if fi, e := d.Info(); e == nil {
				codecByDir[filepath.Dir(path)] = codecSidecar{path: path, sizeGB: round(float64(fi.Size())/gib, 2)}
			}
			return nil
		}
		// Diffusion text encoder / VAE: an image-model component, not a model.
		// Dropped outright (unlike mmproj/codec sidecars, which are paired) — the
		// image emitter resolves these from settings.encoders by explicit path.
		if encoderFileRe.MatchString(name) {
			return nil
		}
		for _, p := range skipPatterns {
			if ok, _ := filepath.Match(p, name); ok {
				return nil
			}
		}
		// Split shards "<model>-00001-of-00003.gguf": represent the set by shard 1
		// only and strip the shard token before id derivation. FullPath stays the
		// real first-shard file.
		if mm := shardRe.FindStringSubmatch(name); mm != nil {
			if mm[1] != "00001" {
				return nil
			}
			name = shardRe.ReplaceAllString(name, ".gguf")
		}

		fi, statErr := d.Info()
		if statErr != nil {
			return nil
		}

		// Vision projector detection by header arch, not filename — projector
		// naming varies by publisher (mmproj-*, <model>-mmproj-*, clip-*, bare).
		// A clip-arch gguf is paired to its model dir, never served alone. The
		// cached read is reused by the later VRAM planning pass, so it's not an
		// extra parse. A parse error falls through and the file is treated as a
		// normal model candidate.
		if meta, e := ReadGgufMetadataCached(path); e == nil {
			if meta.Architecture == "clip" {
				mmprojByDir[filepath.Dir(path)] = mmprojSidecar{
					path:   path,
					sizeGB: round(float64(fi.Size())/gib, 2),
				}
				return nil
			}
			// Encoder-only arch (t5encoder/umt5): a diffusion text encoder that
			// escaped the filename rule. No decoder → nothing to serve.
			if encoderArch[strings.ToLower(meta.Architecture)] {
				return nil
			}
		}

		quant := quantFromName(name)

		repoDir := filepath.Dir(path)
		repoName := filepath.Base(repoDir)
		pubName := filepath.Base(filepath.Dir(repoDir))

		base := strings.TrimSuffix(name, filepath.Ext(name)) // drop .gguf
		if quant != "" {
			// Strip a trailing [-_.]<quant> from the base name.
			trailRe := regexp.MustCompile(`(?i)[-_.]` + regexp.QuoteMeta(quant) + `$`)
			base = trailRe.ReplaceAllString(base, "")
		}
		baseKey := strings.ToLower(ggufSuffixRe.ReplaceAllString(base, ""))
		idKey := baseKey
		if quant != "" {
			idKey = fmt.Sprintf("%s-%s", baseKey, strings.ToLower(quant))
		}

		rows = append(rows, GgufRow{
			ID:        idKey,
			BaseID:    baseKey,
			FullPath:  path,
			FileName:  name,
			Quant:     quant,
			SizeGB:    round(float64(fi.Size())/gib, 2),
			Publisher: pubName,
			Repo:      repoName,
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking %s: %w", modelsRoot, walkErr)
	}
	// Pair each model with the MTP/DFlash draft sitting in its own dir (typically
	// one model per dir). Enables --spec-type draft-mtp/draft-dflash + -md
	// without hand config.
	for i := range rows {
		dir := filepath.Dir(rows[i].FullPath)
		if d, ok := draftByDir[dir]; ok {
			rows[i].DraftPath = d.path
			rows[i].DraftKind = d.kind
			rows[i].DraftSizeGB = d.sizeGB
		}
		if m, ok := mmprojByDir[dir]; ok {
			rows[i].MmprojPath = m.path
			rows[i].MmprojSizeGB = m.sizeGB
		}
		if c, ok := codecByDir[dir]; ok {
			rows[i].CodecPath = c.path
			rows[i].CodecSizeGB = c.sizeGB
		}
	}
	return rows, nil
}

// DraftSidecarForDir scans one directory (non-recursive) for a paired MTP or
// DFlash draft gguf, same convention as DiscoverGgufModels' walk. Used by the
// config-editor API to answer "is draft-dflash/draft-mtp available for this
// model" without a full models-root walk.
func DraftSidecarForDir(dir string) (path, kind string, sizeGB float64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", 0
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.EqualFold(filepath.Ext(ent.Name()), ".gguf") {
			continue
		}
		name := ent.Name()
		k := ""
		switch {
		case mtpFileRe.MatchString(name):
			k = "mtp"
		case dflashFileRe.MatchString(name):
			k = "dflash"
		default:
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		return filepath.Join(dir, name), k, round(float64(fi.Size())/gib, 2)
	}
	return "", "", 0
}

// mmprojSidecar is a discovered vision projector paired to a model dir.
type mmprojSidecar struct {
	path   string
	sizeGB float64
}

// codecSidecar is a discovered Qwen3-TTS audio codec paired to a talker's dir.
type codecSidecar struct {
	path   string
	sizeGB float64
}

// draftSidecar is a discovered speculative-decoding draft gguf paired to a
// model dir (either an MTP nextn sidecar or a DFlash block-diffusion drafter).
type draftSidecar struct {
	path   string
	sizeGB float64
	kind   string // "mtp" | "dflash"
}
