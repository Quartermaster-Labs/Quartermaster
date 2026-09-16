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

// modelGguf is modelFamily for a whole model entry, and it exists because ONE
// backend names its weights somewhere other than the command line:
// audiocpp_server has no --model flag at all, so an audio.cpp model's gguf lives
// in the typed audiocpp: block the spawn hook renders into a JSON config. Asking
// the command alone therefore answered "" for every audio.cpp model, which left
// them ungrouped in the catalog and, worse, made the config editor refuse to
// open them ("model has no gguf path to override") - so the backend picker never
// listed audio.cpp on exactly the models it runs.
//
// The block's path is the same string autogen matched the sidecar override
// against, so an override saved through the editor keys correctly.
func modelGguf(mc config.ModelConfig) string {
	if p := modelFamily(mc.Cmd); p != "" {
		return p
	}
	return mc.AudioCpp.Path
}
