package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SidecarName is the UI-managed overrides file. It sits next to the generate
// control file and is fully owned by the web UI's per-model editor, so it can
// be rewritten with yaml.Marshal without disturbing the hand-authored,
// comment-rich generate file. Its rows are merged ahead of the generate file's
// overrides (first match wins => UI edits take precedence).
const SidecarName = "quartermaster-overrides.yaml"

// sidecar is the on-disk shape of the UI-owned file: per-model overrides plus an
// optional global settings patch (targetVramGB / headroom edits from the
// dashboard). Both are UI-managed, so the file is rewritten whole on any change.
type sidecar struct {
	Settings *SettingsPatch `yaml:"settings,omitempty"`
	// DefaultVariants, when non-nil, replaces the generate file's fleet-wide
	// settings.defaultVariants wholesale (the UI sends the full list). Kept at the
	// top level rather than inside SettingsPatch so a dashboard VRAM "reset" can't
	// wipe it. ponytail: omitempty means an empty list isn't persisted, so removing
	// the last fleet-wide variant from the UI reverts to the file — edit the
	// generate file to delete one entirely.
	DefaultVariants []VariantSpec `yaml:"defaultVariants,omitempty"`
	// CategoryRoots are the UI folder-picker's per-category scan folders. Top-level
	// (not in SettingsPatch) so a dashboard VRAM reset preserves them. omitempty =>
	// clearing the last entry reverts to the generate file's categoryRoots.
	CategoryRoots map[string]string `yaml:"categoryRoots,omitempty"`
	// APIKeys, when non-nil, replaces the generate file's apiKeys wholesale (the
	// UI sends the full list). omitempty => deleting the last key reverts to the
	// file's apiKeys.
	APIKeys   []APIKeyEntry `yaml:"apiKeys,omitempty"`
	Overrides []Override    `yaml:"overrides"`
}

// loadSidecar reads the whole sidecar, returning a zero value when absent.
func loadSidecar(generatePath string) (sidecar, error) {
	data, err := os.ReadFile(SidecarPath(generatePath))
	if err != nil {
		if os.IsNotExist(err) {
			return sidecar{}, nil
		}
		return sidecar{}, err
	}
	var sc sidecar
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return sidecar{}, fmt.Errorf("parsing %s: %w", SidecarName, err)
	}
	return sc, nil
}

// writeSidecar persists the whole sidecar (UI-owned, plain marshal).
func writeSidecar(generatePath string, sc sidecar) error {
	out, err := yaml.Marshal(sc)
	if err != nil {
		return err
	}
	return os.WriteFile(SidecarPath(generatePath), out, 0o644)
}

// SidecarPath returns the sidecar path for a given generate control file path
// (same directory, fixed name).
func SidecarPath(generatePath string) string {
	return filepath.Join(filepath.Dir(generatePath), SidecarName)
}

// LoadSidecarOverrides reads the sidecar overrides, returning an empty slice
// when the file is absent.
func LoadSidecarOverrides(generatePath string) ([]Override, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.Overrides, nil
}

// LoadSidecarSettings returns the sidecar's global settings patch, or nil when
// none is set.
func LoadSidecarSettings(generatePath string) (*SettingsPatch, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.Settings, nil
}

// UpsertSidecarOverride inserts or replaces (by Match, case-insensitive) the
// override for one model in the sidecar. Returns the resulting list.
func UpsertSidecarOverride(generatePath string, ov Override) ([]Override, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range sc.Overrides {
		if matchKeyEqual(sc.Overrides[i].Match, ov.Match) {
			sc.Overrides[i] = ov
			replaced = true
			break
		}
	}
	if !replaced {
		sc.Overrides = append(sc.Overrides, ov)
	}
	if err := writeSidecar(generatePath, sc); err != nil {
		return nil, err
	}
	return sc.Overrides, nil
}

// DeleteSidecarOverride removes the sidecar override matching match (the
// "reset to default" action). Returns whether a row was removed.
func DeleteSidecarOverride(generatePath, match string) (removed bool, err error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return false, err
	}
	kept := sc.Overrides[:0:0]
	for _, r := range sc.Overrides {
		if matchKeyEqual(r.Match, match) {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		return false, nil
	}
	sc.Overrides = kept
	if err := writeSidecar(generatePath, sc); err != nil {
		return false, err
	}
	return true, nil
}

// matchIsDeadPath reports whether an override's Match is a concrete gguf file
// path (not a glob) that no longer exists on disk. Glob patterns (containing
// `*`/`?`) and bare name fragments are never considered dead — only explicit
// file paths the UI wrote for a single model can be pruned safely.
func matchIsDeadPath(match string) bool {
	if strings.ContainsAny(match, "*?") {
		return false
	}
	if !filepath.IsAbs(match) && !strings.HasSuffix(strings.ToLower(match), ".gguf") {
		return false
	}
	_, err := os.Stat(filepath.FromSlash(match))
	return os.IsNotExist(err)
}

// PruneSidecar drops sidecar overrides whose explicit-path Match points at a
// gguf that no longer exists on disk, so deleting a model file also reaps its
// per-model override on the next config regen. Glob overrides and fleet-wide
// defaults are left untouched. Returns the matches removed (nil when none) and
// writes the sidecar back only when something changed.
func PruneSidecar(generatePath string) ([]string, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	var removed []string
	kept := sc.Overrides[:0:0]
	for _, r := range sc.Overrides {
		if matchIsDeadPath(r.Match) {
			removed = append(removed, r.Match)
			continue
		}
		kept = append(kept, r)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	sc.Overrides = kept
	if err := writeSidecar(generatePath, sc); err != nil {
		return nil, err
	}
	return removed, nil
}

// LoadSidecarDefaultVariants returns the sidecar's fleet-wide default-variant
// list, or nil when none is set (inherit the generate file's).
func LoadSidecarDefaultVariants(generatePath string) ([]VariantSpec, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.DefaultVariants, nil
}

// UpsertSidecarDefaultVariants replaces the fleet-wide default-variant list in
// the sidecar (UI sends the full list), preserving overrides + settings patch.
func UpsertSidecarDefaultVariants(generatePath string, vs []VariantSpec) error {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return err
	}
	sc.DefaultVariants = vs
	return writeSidecar(generatePath, sc)
}

// LoadSidecarCategoryRoots returns the sidecar's per-category scan folders, or
// nil when none are set.
func LoadSidecarCategoryRoots(generatePath string) (map[string]string, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.CategoryRoots, nil
}

// UpsertSidecarRoot sets (or, when path is "", clears) the scan folder for one
// category, preserving overrides + settings. Returns the resulting map.
func UpsertSidecarRoot(generatePath, category, path string) (map[string]string, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		delete(sc.CategoryRoots, category)
	} else {
		if sc.CategoryRoots == nil {
			sc.CategoryRoots = map[string]string{}
		}
		sc.CategoryRoots[category] = path
	}
	if err := writeSidecar(generatePath, sc); err != nil {
		return nil, err
	}
	return sc.CategoryRoots, nil
}

// LoadSidecarAPIKeys returns the sidecar's API-key list, or nil when none is
// set (inherit the generate file's apiKeys).
func LoadSidecarAPIKeys(generatePath string) ([]APIKeyEntry, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	return sc.APIKeys, nil
}

// UpsertSidecarAPIKey inserts or replaces (by Name, case-insensitive) one API
// key, preserving the rest of the sidecar. Returns the resulting list.
func UpsertSidecarAPIKey(generatePath string, entry APIKeyEntry) ([]APIKeyEntry, error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range sc.APIKeys {
		if strings.EqualFold(sc.APIKeys[i].Name, entry.Name) {
			sc.APIKeys[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		sc.APIKeys = append(sc.APIKeys, entry)
	}
	if err := writeSidecar(generatePath, sc); err != nil {
		return nil, err
	}
	return sc.APIKeys, nil
}

// DeleteSidecarAPIKey removes the API key with the given name (case-insensitive).
// Returns whether a row was removed.
func DeleteSidecarAPIKey(generatePath, name string) (removed bool, err error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return false, err
	}
	kept := sc.APIKeys[:0:0]
	for _, k := range sc.APIKeys {
		if strings.EqualFold(k.Name, name) {
			removed = true
			continue
		}
		kept = append(kept, k)
	}
	if !removed {
		return false, nil
	}
	sc.APIKeys = kept
	if err := writeSidecar(generatePath, sc); err != nil {
		return false, err
	}
	return true, nil
}

// UpsertSidecarSettings stores the global settings patch (dashboard edits),
// preserving existing per-model overrides.
func UpsertSidecarSettings(generatePath string, patch SettingsPatch) error {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return err
	}
	// The dashboard VRAM editor doesn't touch DryDefault, so carry the existing
	// value forward rather than wiping it on a VRAM save.
	if sc.Settings != nil && patch.DryDefault == nil {
		patch.DryDefault = sc.Settings.DryDefault
	}
	sc.Settings = &patch
	return writeSidecar(generatePath, sc)
}

// ClearSidecarSettings removes the global settings patch (reset to default),
// preserving per-model overrides. Returns whether a patch was present.
func ClearSidecarSettings(generatePath string) (removed bool, err error) {
	sc, err := loadSidecar(generatePath)
	if err != nil {
		return false, err
	}
	if sc.Settings == nil {
		return false, nil
	}
	sc.Settings = nil
	if err := writeSidecar(generatePath, sc); err != nil {
		return false, err
	}
	return true, nil
}

// matchKeyEqual compares two sidecar Match keys. UI-written keys are exact gguf
// paths (no globs), so case- and separator-insensitive equality is the right
// identity test.
func matchKeyEqual(a, b string) bool {
	return strings.EqualFold(filepath.ToSlash(a), filepath.ToSlash(b))
}
