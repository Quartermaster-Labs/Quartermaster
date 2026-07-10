package server

import (
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// modelFamily returns a stable grouping key for a model: the gguf file it loads.
// Every variant of one model (ctx tiers, the game and judge profiles) is the
// same gguf launched with a different context/offload, so the -m/--model path
// is the family key. Image models (sd-server) load via --diffusion-model
// instead, so that path is their key. Returns "" when the command has no model
// path (e.g. a non-llama.cpp upstream), leaving that model ungrouped in the UI.
func modelFamily(cmd string) string {
	args, err := config.SanitizeCommand(cmd)
	if err != nil {
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m" || a == "--model" || a == "--diffusion-model":
			if i+1 < len(args) {
				return normalizeModelPath(args[i+1])
			}
			return ""
		case strings.HasPrefix(a, "--model="):
			return normalizeModelPath(strings.TrimPrefix(a, "--model="))
		case strings.HasPrefix(a, "--diffusion-model="):
			return normalizeModelPath(strings.TrimPrefix(a, "--diffusion-model="))
		case strings.HasPrefix(a, "-m="):
			return normalizeModelPath(strings.TrimPrefix(a, "-m="))
		}
	}
	return ""
}

// normalizeModelPath canonicalizes a gguf path so the family key is stable
// regardless of OS path separators.
func normalizeModelPath(p string) string {
	return filepath.ToSlash(strings.TrimSpace(p))
}
