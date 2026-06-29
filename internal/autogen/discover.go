package autogen

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
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
	DraftPath string // separate MTP/draft gguf in the same dir (mtp-*.gguf), "" if none
	// Vision projector (clip-arch gguf) sitting in the same dir, if any. Loaded via
	// --mmproj to enable image input; drives the auto-generated "-vision" variant.
	MmprojPath   string
	MmprojSizeGB float64
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
			key := strings.ToLower(filepath.ToSlash(row.FullPath))
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
	mtpByDir := map[string]string{}           // dir -> separate MTP draft gguf
	mmprojByDir := map[string]mmprojSidecar{} // dir -> vision projector gguf
	walkErr := filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, matching -ErrorAction SilentlyContinue
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".gguf") {
			return nil
		}
		name := d.Name()
		// Separate MTP draft: record it for pairing, don't serve it as its own model.
		if mtpFileRe.MatchString(name) {
			mtpByDir[filepath.Dir(path)] = path
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
		if meta, e := ReadGgufMetadataCached(path); e == nil && meta.Architecture == "clip" {
			mmprojByDir[filepath.Dir(path)] = mmprojSidecar{
				path:   path,
				sizeGB: round(float64(fi.Size())/gib, 2),
			}
			return nil
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
	// Pair each model with the MTP draft sitting in its own dir (typically one
	// model per dir). Enables --spec-type draft-mtp + -md without hand config.
	for i := range rows {
		dir := filepath.Dir(rows[i].FullPath)
		if d := mtpByDir[dir]; d != "" {
			rows[i].DraftPath = d
		}
		if m, ok := mmprojByDir[dir]; ok {
			rows[i].MmprojPath = m.path
			rows[i].MmprojSizeGB = m.sizeGB
		}
	}
	return rows, nil
}

// mmprojSidecar is a discovered vision projector paired to a model dir.
type mmprojSidecar struct {
	path   string
	sizeGB float64
}
