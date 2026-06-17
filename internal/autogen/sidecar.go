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

// sidecar is the on-disk shape of the UI overrides file.
type sidecar struct {
	Overrides []Override `yaml:"overrides"`
}

// SidecarPath returns the sidecar path for a given generate control file path
// (same directory, fixed name).
func SidecarPath(generatePath string) string {
	return filepath.Join(filepath.Dir(generatePath), SidecarName)
}

// LoadSidecarOverrides reads the sidecar overrides, returning an empty slice
// when the file is absent.
func LoadSidecarOverrides(generatePath string) ([]Override, error) {
	data, err := os.ReadFile(SidecarPath(generatePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sc sidecar
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", SidecarName, err)
	}
	return sc.Overrides, nil
}

// writeSidecarOverrides persists the full sidecar override list (UI-owned, so a
// plain marshal is fine — no comments to preserve).
func writeSidecarOverrides(generatePath string, rows []Override) error {
	out, err := yaml.Marshal(sidecar{Overrides: rows})
	if err != nil {
		return err
	}
	return os.WriteFile(SidecarPath(generatePath), out, 0o644)
}

// UpsertSidecarOverride inserts or replaces (by Match, case-insensitive) the
// override for one model in the sidecar. Returns the resulting list.
func UpsertSidecarOverride(generatePath string, ov Override) ([]Override, error) {
	rows, err := LoadSidecarOverrides(generatePath)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range rows {
		if matchKeyEqual(rows[i].Match, ov.Match) {
			rows[i] = ov
			replaced = true
			break
		}
	}
	if !replaced {
		rows = append(rows, ov)
	}
	if err := writeSidecarOverrides(generatePath, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteSidecarOverride removes the sidecar override matching match (the
// "reset to default" action). Returns whether a row was removed.
func DeleteSidecarOverride(generatePath, match string) (removed bool, err error) {
	rows, err := LoadSidecarOverrides(generatePath)
	if err != nil {
		return false, err
	}
	kept := rows[:0:0]
	for _, r := range rows {
		if matchKeyEqual(r.Match, match) {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	if !removed {
		return false, nil
	}
	if err := writeSidecarOverrides(generatePath, kept); err != nil {
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
