package autogen

// This file is to sd-server what flagtable.go is to llama-server: the single
// description of the flags the diffusion emitters can produce, grouped under
// the setting each controls.
//
// It exists because the image and video forms were the last consumers of the
// old two-way launch box, whose round trip appended the user's text on every
// save instead of replacing it. A real LTX-2.5 sidecar reached five verbatim
// copies of the same --audio-vae/--lora-model-dir/--video-frames/--fps run,
// which is the "Accumulation" bug class named in ui-svelte/launch-args.md. The
// composer in customargs.go already solves that for llama-server; all it was
// missing here was a table to resolve sd-server's flag spellings against.
//
// Aliases come from `sd-server --help` rather than from memory: a spelling the
// binary does not accept would suppress a generated flag the user meant to
// keep. Note the trap that -l is --listen-ip, NOT --listen-port, and that
// sd-server writes its encoder flags with underscores (--clip_l, --llm_vision)
// while everything else is hyphenated.

// sdFlagTable lists every sd-server flag the diffusion emitters write
// (imageCmdLines, extraImageCmdLines), plus the memory and tiling knobs a user
// is most likely to pin by hand. Keep it grouped by area, not alphabetically:
// the areas are what a reader checks against the emitter.
var sdFlagTable = []FlagDef{
	// Server identity. Structural: no override field backs these, and the
	// router health-checks the port it assigned, so a custom --listen-port is a
	// plumbing flag the save path warns about rather than a normal knob.
	{Name: "-m", Aliases: []string{"--model"}, Knob: "model", Value: true},
	{Name: "--diffusion-model", Knob: "diffusionModel", Value: true},
	{Name: "-l", Aliases: []string{"--listen-ip"}, Knob: "host", Value: true},
	{Name: "--listen-port", Knob: "port", Value: true},

	// External component paths. A diffusion-only gguf loads its VAE and text
	// encoder(s) from separate files, which is why these are paths rather than
	// being baked into the checkpoint.
	{Name: "--vae", Knob: "vaePath", Value: true},
	{Name: "--audio-vae", Knob: "audioVaePath", Value: true},
	{Name: "--clip_l", Knob: "clipLPath", Value: true},
	{Name: "--clip_g", Knob: "clipGPath", Value: true},
	{Name: "--t5xxl", Knob: "t5Path", Value: true},
	{Name: "--llm", Knob: "textEncoderPath", Value: true},
	{Name: "--llm_vision", Knob: "llmVisionPath", Value: true},
	{Name: "--lora-model-dir", Knob: "loraDir", Value: true},

	// Memory placement. --max-vram is NOT a VRAM cap: it is the size one merged
	// graph segment may reach (see graphBudget in image.go), so a user pinning
	// it is overriding the generator's arithmetic, not raising a ceiling.
	{Name: "--max-vram", Knob: "maxVram", Value: true},
	{Name: "--offload-to-cpu", Knob: "offloadToCpu"},
	{Name: "--vae-on-cpu", Knob: "vaeOnCpu"},
	{Name: "--clip-on-cpu", Knob: "teOnCpu"},
	{Name: "--stream-layers", Knob: "streamLayers"},
	{Name: "--eager-load", Knob: "eagerLoad"},
	// --backend and --params-backend are the per-module placement flags
	// (te=cpu, diffusion=ROCm0, vae=disk). The generator emits --backend only;
	// --params-backend and --auto-fit have no field at all, so a user who
	// writes one owns it outright.
	{Name: "--backend", Knob: "sdBackend", Value: true},
	{Name: "--params-backend", Knob: "sdParamsBackend", Value: true},
	{Name: "--auto-fit", Knob: "sdAutoFit"},
	{Name: "--rpc-servers", Knob: "sdRpcServers", Value: true},
	{Name: "--split-mode", Knob: "sdSplitMode", Value: true},

	// Attention and convolution kernels.
	{Name: "--diffusion-fa", Knob: "diffusionFa"},
	{Name: "--fa", Knob: "sdFlashAttn"},
	{Name: "--diffusion-conv-direct", Knob: "sdDiffusionConvDirect"},
	{Name: "--vae-conv-direct", Knob: "sdVaeConvDirect"},

	// VAE tiling. The decode buffer scales with width x height x frames, so
	// these are the knobs that decide whether a long clip decodes at all.
	{Name: "--vae-tiling", Knob: "vaeTiling"},
	{Name: "--temporal-tiling", Knob: "temporalTiling"},
	{Name: "--vae-tile-size", Knob: "sdVaeTileSize", Value: true},
	{Name: "--vae-relative-tile-size", Knob: "sdVaeRelativeTileSize", Value: true},
	{Name: "--vae-tile-overlap", Knob: "sdVaeTileOverlap", Value: true},
	{Name: "--extra-tiling-args", Knob: "sdExtraTilingArgs", Value: true},

	// Generation defaults, applied when a request omits them.
	{Name: "--steps", Knob: "defaultSteps", Value: true},
	{Name: "--cfg-scale", Knob: "defaultCfg", Value: true},
	{Name: "--guidance", Knob: "defaultGuidance", Value: true},
	{Name: "--flow-shift", Knob: "defaultFlowShift", Value: true},
	{Name: "--sampling-method", Knob: "defaultSampler", Value: true},
	{Name: "--scheduler", Knob: "sdScheduler", Value: true},
	{Name: "-W", Aliases: []string{"--width"}, Knob: "defaultWidth", Value: true},
	{Name: "-H", Aliases: []string{"--height"}, Knob: "defaultHeight", Value: true},
	{Name: "--video-frames", Knob: "defaultFrames", Value: true},
	{Name: "--fps", Knob: "defaultFps", Value: true},
	{Name: "--clip-skip", Knob: "sdClipSkip", Value: true},
	{Name: "-s", Aliases: []string{"--seed"}, Knob: "sdSeed", Value: true},
	{Name: "--rng", Knob: "sdRng", Value: true},

	// Threading and logging.
	{Name: "-t", Aliases: []string{"--threads"}, Knob: "threads", Value: true},
	{Name: "--log-level", Knob: "sdLogLevel", Value: true},
	{Name: "-v", Aliases: []string{"--verbose"}, Knob: "sdVerbose"},
	{Name: "--type", Knob: "sdType", Value: true},
	{Name: "--mmap", Knob: "sdMmap"},
}

// LlamaFlags and SdFlags are the two tables the composer can resolve against.
// Which one applies is a property of the backend binary a model launches, so
// the caller picks it (see flagTableFor); nothing infers it from the flags
// themselves, because the two tables genuinely disagree: -t is threads in both
// but -l is --listen-ip to sd-server and nothing to llama-server, and -H is
// --height here and --hf-repo there.
var (
	LlamaFlags = newFlagTable("llama-server", llamaFlagTable)
	SdFlags    = newFlagTable("sd-server", sdFlagTable)
)
