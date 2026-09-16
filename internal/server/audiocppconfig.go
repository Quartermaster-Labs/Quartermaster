package server

// Spawn-time materialization of audio.cpp's --config file.
//
// audiocpp_server has no --model flag: a model can only be named inside a JSON
// config file. Taken literally that means one JSON file per served model, all of
// them written at generate time, all of them a second copy of facts the config
// already holds and all of them left behind when a model is renamed, re-quanted
// or deleted.
//
// Instead the generated YAML carries a typed `audiocpp:` block (one models[]
// entry, see config.AudioCppConfig) and this file turns it into a real file at
// the moment of spawn, in a cache directory we own, one per model id. The
// process gets exactly one model, which is also the whole of our containment of
// audio.cpp's own model manager: a residency cap over a single model has
// nothing to choose between.
//
// The file is rewritten on every spawn and deliberately NOT deleted on stop. It
// is regenerable by construction, bounded to one per audio.cpp model, and being
// able to read the exact config a running process was launched with is worth
// more than an empty directory.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/apppaths"
	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// audioCppServerConfig is the subset of audio.cpp's server config we write. The
// residency fields are pinned rather than omitted, so the file states the
// containment outright instead of relying on upstream's defaults staying put:
// eager load (the listen socket then means "weights resident", which is what
// makes checkEndpoint /health a real readiness gate), one model, no idle
// unloads, no memory guard of its own. Everything else audio.cpp accepts is an
// argv flag, and argv wins over this file.
type audioCppServerConfig struct {
	LazyLoad        bool                  `json:"lazy_load"`
	MaxLoadedModels int                   `json:"max_loaded_models"`
	IdleUnloadMs    int                   `json:"idle_unload_ms"`
	MinFreeMemoryMB int                   `json:"min_free_memory_mb"`
	Models          []audioCppServerModel `json:"models"`
}

// audioCppConfigDir is where the materialized configs live. It is a variable
// only so a test can redirect it: apppaths.CacheDir() answers from the process
// environment, and a test that took it at its word would write into the real
// user cache directory of whoever ran it.
var audioCppConfigDir = func() string { return filepath.Join(apppaths.CacheDir(), "audiocpp") }

type audioCppServerModel struct {
	ID     string `json:"id"`
	Family string `json:"family"`
	Path   string `json:"path"`
	Task   string `json:"task,omitempty"`
}

// audioCppSpawnArgs writes this model's audio.cpp config and appends --config to
// its argv. A model with no `audiocpp:` block passes through untouched, so every
// other backend is unaffected.
//
// It is one half of the single spawnArgs slot (WireDynamicOffload owns the
// other, the live-VRAM placement guard) rather than a hook of its own: the
// process layer holds exactly one rewriter, and two tenants composing in a known
// order beats two features racing for the same slot.
func (s *Server) audioCppSpawnArgs(modelID string, args []string) ([]string, error) {
	mc, ok := s.config().Models[modelID]
	if !ok || mc.AudioCpp.Empty() {
		return args, nil
	}
	// A hand-written --config in the launch box is the user taking the wheel:
	// they may be pointing at a multi-model file of their own, and appending a
	// second --config would either be ignored or silently win. Leave it alone.
	for _, a := range args {
		if a == "--config" {
			return args, nil
		}
	}
	path, err := writeAudioCppConfig(modelID, mc.AudioCpp)
	if err != nil {
		// Refusing the spawn is the honest outcome: without the file the server
		// starts with an empty models array and 400s every request, which reads
		// as a broken model rather than as a broken disk.
		return nil, fmt.Errorf("audio.cpp: %w", err)
	}
	return append(append([]string{}, args...), "--config", path), nil
}

// writeAudioCppConfig renders the single-model config for modelID and returns
// its path. The write is atomic (temp file + rename) because the server reads
// this file during startup, and a spawn racing a rewrite of the same model's
// file would otherwise hand it a half-written document.
func writeAudioCppConfig(modelID string, ac config.AudioCppConfig) (string, error) {
	dir := audioCppConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	doc := audioCppServerConfig{
		LazyLoad:        false,
		MaxLoadedModels: 1,
		IdleUnloadMs:    0,
		MinFreeMemoryMB: 0,
		Models: []audioCppServerModel{{
			// The id the request's "model" field has to match: our own model id,
			// since that is what the router forwards.
			ID:     modelID,
			Family: ac.Family,
			Path:   ac.Path,
			Task:   ac.Task,
		}},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, audioCppConfigName(modelID))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// audioCppConfigName is a filesystem-safe file name for a model id. A model id
// is free-form (slashes, colons and spaces all appear in real ones), so the
// readable part is sanitized and a hash of the ORIGINAL id is appended: two ids
// that sanitize to the same string would otherwise share one file and each
// spawn would launch the other's model.
func audioCppConfigName(modelID string) string {
	var b strings.Builder
	for _, r := range modelID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(modelID))
	name := strings.Trim(b.String(), "._")
	if name == "" {
		name = "model"
	}
	// Bounded so a long id cannot push the path past a filesystem limit.
	if len(name) > 80 {
		name = name[:80]
	}
	return fmt.Sprintf("%s-%08x.json", name, h.Sum32())
}
