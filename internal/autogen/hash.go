package autogen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hashCacheSuffix is appended to the output config path to store the digest of
// the inputs that produced it.
const hashCacheSuffix = ".modelhash"

// genVersion is folded into the inputs hash so a change to the config-emit logic
// (buildCmdLines/emitProfile output) forces a one-time regen even when the models,
// generate file, and sidecar are byte-identical. The hash otherwise only tracks
// inputs, not the generator, so an emit change would silently ship a stale config.
// Bump this whenever the emitted YAML for unchanged inputs changes.
//
//	v2: -b decoupled from -ub (logical batch fixed at 2048, clamped >=ub, <=ctx).
//	v3: draft-dflash default --spec-draft-n-max 6 -> 5 (own sweep on Qwen3.6-35B-A3B).
//	v4: draft-dflash no longer auto-defaults (real long-session use craters vs mtp
//	    on VRAM pressure); only an explicit spec: draft-dflash override selects it.
//	v5: flux.2 klein name-detected (arch is "flux", same as flux.1) to wire
//	    flux2Vae + qwenLlm instead of fluxVae/clip_l/t5.
const genVersion = "v5"

// InputsHash digests everything that can change the generated config: the set of
// gguf files under modelsRoot (path + size + mtime) plus the raw bytes of the
// generate control file. A stable hash means a regen would produce the same
// config, so it can be skipped.
func InputsHash(modelsRoot string, generateFileBytes []byte) (string, error) {
	return InputsHashRoots([]string{modelsRoot}, generateFileBytes)
}

// InputsHashRoots is InputsHash over multiple scan folders (settings.RootList).
// Each gguf's hash key is prefixed by its root index so identically-named files
// in different roots don't collide.
func InputsHashRoots(roots []string, generateFileBytes []byte) (string, error) {
	type entry struct {
		rel   string
		size  int64
		mtime int64
	}
	var entries []entry
	for ri, modelsRoot := range roots {
		if strings.TrimSpace(modelsRoot) == "" {
			continue
		}
		err := filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".gguf") {
				return nil
			}
			fi, e := d.Info()
			if e != nil {
				return nil
			}
			rel, e := filepath.Rel(modelsRoot, path)
			if e != nil {
				rel = path
			}
			entries = append(entries, entry{fmt.Sprintf("%d\x00%s", ri, filepath.ToSlash(rel)), fi.Size(), fi.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", e.rel, e.size, e.mtime)
	}
	h.Write([]byte("\x00generate\x00"))
	h.Write(generateFileBytes)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readHashCache returns the stored inputs hash, or "" when absent/unreadable.
func readHashCache(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// buildHashInput assembles the byte blob whose digest gates regeneration:
// resolved modelsRoot + raw generate file + UI sidecar. Kept in one place so
// EnsureConfig and CurrentInputsHash always hash identical inputs.
func buildHashInput(roots []string, rawGenerate, sidecarBytes []byte) []byte {
	out := append([]byte("genver\x00"+genVersion+"\x00"), []byte(strings.Join(roots, "\x00")+"\x00")...)
	out = append(out, rawGenerate...)
	return append(append(out, "\x00sidecar\x00"...), sidecarBytes...)
}

// CurrentInputsHash computes the inputs hash for the generate file's present
// state (models folder + generate bytes + sidecar). It is the same value
// EnsureConfig compares against its cache, so callers can cheaply detect whether
// a regen would produce a different config without running one.
func CurrentInputsHash(generatePath, modelsDirOverride string) (string, error) {
	rawGenerate, err := os.ReadFile(generatePath)
	if err != nil {
		return "", fmt.Errorf("reading generate file: %w", err)
	}
	gf, err := LoadGenerateFile(generatePath, modelsDirOverride)
	if err != nil {
		return "", err
	}
	sidecarBytes, _ := os.ReadFile(SidecarPath(generatePath))
	roots := gf.Settings.RootList()
	return InputsHashRoots(roots, buildHashInput(roots, rawGenerate, sidecarBytes))
}

// CachedConfigHash returns the inputs hash recorded alongside the last generated
// config, or "" when no config has been generated yet.
func CachedConfigHash(outConfigPath string) string {
	return readHashCache(outConfigPath + hashCacheSuffix)
}

// EnsureConfig generates outConfigPath from the generate control file when the
// inputs changed (or the config is missing), and skips regeneration otherwise.
// modelsDirOverride (from --models-dir) wins over the file's settings.modelsRoot.
// logf receives one human-readable status line. Returns whether a regen ran.
func EnsureConfig(generatePath, outConfigPath, modelsDirOverride string, logf func(string)) (regenerated bool, err error) {
	rawGenerate, err := os.ReadFile(generatePath)
	if err != nil {
		return false, fmt.Errorf("reading generate file: %w", err)
	}

	// Reap per-model overrides whose gguf was deleted, before hashing — a pruned
	// sidecar changes the hash, so the trim itself triggers the regen below.
	if pruned, err := PruneSidecar(generatePath); err != nil {
		return false, fmt.Errorf("pruning sidecar: %w", err)
	} else if len(pruned) > 0 && logf != nil {
		logf(fmt.Sprintf("pruned %d override(s) for deleted models: %s", len(pruned), strings.Join(pruned, ", ")))
	}

	gf, err := LoadGenerateFile(generatePath, modelsDirOverride)
	if err != nil {
		return false, err
	}

	// The hash covers the resolved modelsRoot too, so a --models-dir change
	// triggers a regen even when the file is unchanged. It also folds in the
	// UI-owned sidecar so editing an override there forces a regen.
	sidecarBytes, _ := os.ReadFile(SidecarPath(generatePath))
	roots := gf.Settings.RootList()
	hashInput := buildHashInput(roots, rawGenerate, sidecarBytes)
	hash, err := InputsHashRoots(roots, hashInput)
	if err != nil {
		return false, fmt.Errorf("hashing models: %w", err)
	}

	// AutoVram bakes a live VRAM snapshot into the config, which the inputs hash
	// can't see — so never short-circuit on the hash when it's enabled; always
	// regen so each boot re-measures available VRAM.
	cachePath := outConfigPath + hashCacheSuffix
	_, statErr := os.Stat(outConfigPath)
	if statErr == nil && !gf.Settings.AutoVram && readHashCache(cachePath) == hash {
		if logf != nil {
			logf(fmt.Sprintf("config up to date (models + generate file unchanged); using %s", outConfigPath))
		}
		return false, nil
	}

	if gf.Settings.AutoVram {
		resolveAutoVram(&gf.Settings, logf)
	}

	if logf != nil {
		if strings.TrimSpace(gf.Settings.ModelsRoot) == "" {
			logf(fmt.Sprintf("no modelsRoot set; generating %s with an empty catalog (set settings.modelsRoot or --models-dir, or pick a folder in the setup UI)", outConfigPath))
		} else {
			logf(fmt.Sprintf("generating %s from %s (models root %s)", outConfigPath, generatePath, gf.Settings.ModelsRoot))
		}
	}
	out, err := Generate(gf, DefaultNow())
	if err != nil {
		return false, fmt.Errorf("generating config: %w", err)
	}
	if err := os.WriteFile(outConfigPath, []byte(out), 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", outConfigPath, err)
	}
	if err := os.WriteFile(cachePath, []byte(hash), 0o644); err != nil {
		return false, fmt.Errorf("writing hash cache: %w", err)
	}
	if logf != nil {
		logf(fmt.Sprintf("wrote %s", outConfigPath))
	}
	return true, nil
}
