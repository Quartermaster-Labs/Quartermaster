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
}

var (
	shardRe = regexp.MustCompile(`-(\d{5})-of-\d{5}\.gguf$`)
	// Quant tokens: Q4_0, Q6_K, Q4_K_M, IQ3_XS, Q8_0, F16, BF16, F32. Bounded by
	// a separator before and a separator / .gguf after.
	quantRe      = regexp.MustCompile(`(?i)[-_.](IQ\d+(?:_[A-Z0-9]+)*|Q\d+(?:_[A-Z0-9]+)*|F16|BF16|F32)(?:[._-]|\.gguf$)`)
	ggufSuffixRe = regexp.MustCompile(`(?i)-GGUF$`)
)

// DiscoverGgufModels walks modelsRoot for *.gguf files and returns one row per
// served model. mmproj projector files and non-first split shards are skipped.
// skipPatterns are filename globs (default {"mmproj-*"}).
func DiscoverGgufModels(modelsRoot string, skipPatterns ...string) ([]GgufRow, error) {
	if strings.TrimSpace(modelsRoot) == "" {
		return nil, nil // no models root configured yet => empty catalog
	}
	if len(skipPatterns) == 0 {
		skipPatterns = []string{"mmproj-*"}
	}
	info, err := filepath.Abs(modelsRoot)
	if err != nil {
		return nil, err
	}
	modelsRoot = info

	var rows []GgufRow
	walkErr := filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, matching -ErrorAction SilentlyContinue
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".gguf") {
			return nil
		}
		name := d.Name()
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

		quant := ""
		if mm := quantRe.FindStringSubmatch(name); mm != nil {
			quant = strings.ToUpper(mm[1])
		}

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
	return rows, nil
}
