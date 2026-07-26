package server

import (
	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// modelFamily returns a stable grouping key for a model: the gguf file it loads.
// Every variant of one model (ctx tiers, the game and judge profiles) is the
// same gguf launched with a different context/offload, so the -m/--model path
// is the family key. Image models (sd-server) load via --diffusion-model
// instead, so that path is their key. Returns "" when the command has no model
// path (e.g. a non-llama.cpp upstream), leaving that model ungrouped in the UI.
//
// The parse is memoized in config.ParseCmd — this runs per SSE status build and
// per slot-cache request, so it must not re-shlex the command every time.
func modelFamily(cmd string) string {
	return config.ParseCmd(cmd).ModelPath
}
