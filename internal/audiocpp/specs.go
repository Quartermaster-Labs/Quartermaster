// Package audiocpp reads the model catalog that ships INSIDE an installed
// audio.cpp backend.
//
// audio.cpp publishes its weights as one curated GGUF repo per publisher rather
// than one repo per model, so browsing the hub for them is close to useless: the
// files land as a flat list of a few hundred names with nothing saying which
// family a name belongs to, which precision is which, or that a given model
// needs a vocab.txt beside it. The answer to all three is upstream's
// model_specs/*.json, and every release ships that directory next to the binary
// (bin/audiocpp-server/<version>/model_specs/).
//
// Reading it from the INSTALL, not from an embedded copy, is the point: the
// catalog is then exactly as new as the backend the user has installed, a
// backend update ships new families for free, and there is no second copy of
// upstream's data in this repo to drift. Nothing here is required to RUN a
// model - a downloaded gguf identifies its own family from its header
// (audiocpp.model_spec.family, see internal/autogen/audiocpp.go). This package
// exists only so the model browser can offer the download in the first place.
package audiocpp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirName is the catalog directory upstream ships beside the binary.
const DirName = "model_specs"

// SpecsDir is the catalog directory for an installed audio.cpp executable.
func SpecsDir(exe string) string {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), DirName)
}

// Package is one downloadable set of files: a family at a precision.
type Package struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Precision   string `json:"precision,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Repo        string `json:"repo"`
	Revision    string `json:"revision,omitempty"`
	Gated       bool   `json:"gated,omitempty"`
	// Files are repo-relative paths, and the whole set is ONE download: a
	// number of the gguf packages ship a sidecar (an f5_tts vocab.txt) that the
	// model is unusable without.
	Files []string `json:"files"`
}

// Family is one model_specs entry, trimmed to what a download picker needs.
type Family struct {
	Family      string    `json:"family"`
	DisplayName string    `json:"displayName"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"`
	Status      string    `json:"status,omitempty"`
	Tasks       []string  `json:"tasks,omitempty"`
	Languages   []string  `json:"languages,omitempty"`
	Packages    []Package `json:"packages"`
}

// The upstream JSON, as far as we read it. Everything else in those files (ui
// hints, runtime tags, capability maps, docs links) is upstream's own webui
// talking to itself.
type rawSpec struct {
	Family          string   `json:"family"`
	DisplayName     string   `json:"display_name"`
	Description     string   `json:"description"`
	Category        string   `json:"category"`
	Status          string   `json:"status"`
	Tasks           []string `json:"tasks"`
	Languages       []string `json:"languages"`
	PackageDefaults struct {
		Download rawDownload `json:"download"`
	} `json:"package_defaults"`
	Packages []struct {
		ID          string      `json:"id"`
		DisplayName string      `json:"display_name"`
		Description string      `json:"description"`
		Default     bool        `json:"default"`
		Format      string      `json:"format"`
		Precision   string      `json:"precision"`
		Files       []string    `json:"files"`
		Download    rawDownload `json:"download"`
	} `json:"packages"`
}

type rawDownload struct {
	Kind     string `json:"kind"`
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
	Gated    bool   `json:"gated"`
}

// Load reads every spec in dir. A directory that is not there is not an error:
// it means this install predates the catalog, or the backend is not installed,
// and the caller renders an empty catalog rather than a failure.
//
// A single unreadable or malformed spec is skipped rather than failing the load,
// for the same reason: one file upstream changed the shape of must not cost the
// user the other seventy-one.
func Load(dir string) ([]Family, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Family
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var raw rawSpec
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		if fam, ok := convert(raw); ok {
			out = append(out, fam)
		}
	}
	// Stable display order; the directory order is alphabetical by file name,
	// which is the family id rather than the name a human reads.
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].DisplayName, out[j].DisplayName; a != b {
			return a < b
		}
		return out[i].Family < out[j].Family
	})
	return out, nil
}

func convert(raw rawSpec) (Family, bool) {
	fam := Family{
		Family:      strings.TrimSpace(raw.Family),
		DisplayName: strings.TrimSpace(raw.DisplayName),
		Description: strings.TrimSpace(raw.Description),
		Category:    strings.TrimSpace(raw.Category),
		Status:      strings.TrimSpace(raw.Status),
		Tasks:       raw.Tasks,
		Languages:   raw.Languages,
	}
	if fam.Family == "" {
		return Family{}, false
	}
	if fam.DisplayName == "" {
		fam.DisplayName = fam.Family
	}
	for _, p := range raw.Packages {
		// GGUF only. audio.cpp also loads safetensors directories, but OUR model
		// discovery walks ggufs (internal/autogen), so a safetensors package
		// would download several gigabytes that never become a model row.
		if !strings.EqualFold(strings.TrimSpace(p.Format), "gguf") {
			continue
		}
		dl := p.Download
		if strings.TrimSpace(dl.Kind) == "" {
			dl.Kind = raw.PackageDefaults.Download.Kind
		}
		if strings.TrimSpace(dl.Repo) == "" {
			dl.Repo = raw.PackageDefaults.Download.Repo
			if strings.TrimSpace(dl.Revision) == "" {
				dl.Revision = raw.PackageDefaults.Download.Revision
			}
			dl.Gated = dl.Gated || raw.PackageDefaults.Download.Gated
		}
		// Upstream states "unsupported" for families whose weights it cannot
		// redistribute, and leaves the block off entirely for a few others.
		// Both mean the same thing here: there is no URL to offer.
		if !strings.EqualFold(strings.TrimSpace(dl.Kind), "huggingface_snapshot") {
			continue
		}
		if strings.TrimSpace(dl.Repo) == "" || len(p.Files) == 0 {
			continue
		}
		name := strings.TrimSpace(p.DisplayName)
		if name == "" {
			name = p.ID
		}
		fam.Packages = append(fam.Packages, Package{
			ID:          strings.TrimSpace(p.ID),
			DisplayName: name,
			Description: strings.TrimSpace(p.Description),
			Precision:   strings.TrimSpace(p.Precision),
			Default:     p.Default,
			Repo:        strings.TrimSpace(dl.Repo),
			Revision:    strings.TrimSpace(dl.Revision),
			Gated:       dl.Gated,
			Files:       p.Files,
		})
	}
	// A family with nothing downloadable is not a catalog row: it would render
	// as a name with no button.
	if len(fam.Packages) == 0 {
		return Family{}, false
	}
	// The package upstream marks default first, the rest in spec order (which is
	// curated: base model first, then the quantizations).
	sort.SliceStable(fam.Packages, func(i, j int) bool {
		return fam.Packages[i].Default && !fam.Packages[j].Default
	})
	return fam, true
}
