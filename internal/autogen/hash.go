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
	"sync"
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
//	v7: tts-server checkEndpoint none -> /health (gate readiness, kill 502-on-cold).
//	v14: settings.extraImageModels emit (hand-declared safetensors sd-server blocks).
//	v17: extraImageModels are override-aware (sidecar/file override overlaid) + emit
//	     --clip_g / --sampling-method so the config editor can tune them.
//	v19: MTP models default spec draft-mtp+ngram-mod (chain beats mtp alone); mmap
//	     defaults --no-mmap unless CPU offload (n-cpu-moe or partial layer offload).
//	v21: SAM emits capabilities.segmentation + auto --no-gpu when the card can't
//	     spare headroom beyond the primary VRAM budget (coexist placement).
//	v22: SAM exe fallback derives as sibling of ServerExe (backends dir) instead
//	     of bare "sam3_server" on PATH.
//	v23: SAM no longer bakes --no-gpu at generate (static-budget heuristic always
//	     tripped CPU on a full-budget card even when idle); CPU vs GPU is now a
//	     live spawn-time decision (LiveOffloadArgs .ggml branch, samGpuMinFreeGB).
//	v24: --chat-template-file emits via cmdPath (forward slashes, unescaped) —
//	     %q doubled every backslash and llama-server couldn't open the template.
//	v25: Parakeet ASR models emit a parakeet-server block (capabilities in:[audio]
//	     out:[text]) instead of routing to llama-server as a chat model.
//	v27: image models emit --lora-model-dir (per-model override, else
//	     settings.loraDir, else the model gguf's own directory) so LoRAs are
//	     listable via /sdapi/v1/loras and usable per-request.
//	v28: KV quant validated against llama's full kv_cache_types set (q5_0/q4_1/f32
//	     now accepted instead of silently emitted-but-unmodelled); bf16 KV costs
//	     2.0 B/elem instead of falling back to q8_0's 1.0625 (undersized ctx).
//	v29: KV default is f16 for EVERY arch (was q8_0 dense / f16 MoE), stepping down
//	     to q8_0 only when f16 can't reach denseMinCtx; settings.kvQuant pins it.
//	v30: vllm emit sizes --max-model-len and --gpu-memory-utilization from the VRAM
//	     budget (were the model's trained ctx and a flat 0.90), emits --tokenizer
//	     when set, and skips split-shard ggufs vllm cannot load.
//	v31: TTS is multi-backend like the LLM class — Kokoro/Parler/Orpheus GGUFs are
//	     detected and emitted as TTS.cpp (--model-path, no codec/voices dir), and the
//	     engine is resolved from the registry per model instead of always qwentts.
//	v32: TTS.cpp models emit useModelName (the gguf stem) — its server validates the
//	     request's "model" against its own file-stem map and 400s "Invalid Model".
//	v33: qwentts TTS models emit capabilities.voiceClone (TTS.cpp cannot clone), so
//	     the playground stops offering a clone button for a fixed voice pack.
//	v34: per-model estVramGB + top-level vramBudgetGB (VRAM-budget-aware
//	     multi-load): the router admits a model alongside the resident set while
//	     the estimates fit the budget instead of evicting on group membership.
//	v35: rope scaling extends the ctx ceiling past the model's trained length and
//	     derives --rope-scale from the chosen ctx (was: ctx hard-clamped to
//	     context_length, and a bare --rope-scaling kept llama.cpp's factor of 1).
//	v36: per-model estRamGB beside estVramGB, so the Models table can show what a
//	     partial offload costs in system memory before the model is loaded.
//	v38: the checkpoint reserve is carried into every re-priced placement (a forced
//	     or low-active-MoE offload used to drop it from both estVramGB and estRamGB,
//	     reading ~0.3 GB low), and the long-context budget headroom moved from the
//	     ctx-tier loop into sizeProfile so every long profile — tier, variant, a
//	     pinned long ctx, and the editor preview — sizes against the same budget.
//	v39: longCtxHeadroomGB is a floor on the total safety slack instead of an
//	     addition — it now tops vramOverheadGB up rather than stacking on it, so a
//	     long profile stops paying two independent 0.5 GB pads and recovers the
//	     layer it was offloading for nothing.
//	v40: Qwen 3.8 keeps its own chat template (the built-in fix is now gated on
//	     what the baked template does, not on arch alone) and models whose
//	     template validates a reasoning_effort ladder emit
//	     capabilities.reasoningEffort, which the proxy uses to translate a
//	     client's OpenAI reasoning_effort field into a chat_template_kwarg.
//	v41: llama-server flag migration — --cors-origins localhost locks the spawned
//	     upstream down (it echoed any Origin back with credentials, exposing
//	     /props and /slots to any page the user visits), preserve-thinking moves
//	     from --chat-template-kwargs to --reasoning-preserve, and the deprecated
//	     --no-webui / --mmap / --no-mmap / --mlock / -dio flags collapse into
//	     --no-ui and the --load-mode enum.
//	v43: server-side sampler defaults (--temp/--top-k/--top-p/--min-p/
//	     --presence-penalty). Qwen3-family models now emit --top-k 20 --min-p 0
//	     from the arch baseline: neither knob has an OpenAI-API field, so no
//	     client could reach them and every request silently ran on llama's
//	     top-k 40 / min-p 0.05 instead of the values the model cards specify.
//	v44: sampler baseline gains muse-glimmer (--top-k 64, no min-p — the card
//	     pins one and not the other). Only matters for a config generated under
//	     v43 for a muse-glimmer gguf already on disk; a newly downloaded model
//	     regenerates through the inputs hash anyway.
//	v46: -cms is now emitted ALWAYS, not just when it differs from our own
//	     default. llama-server's --checkpoint-min-step default is 8192, not the
//	     256 the reserve assumed, so every recurrent and plain-attention model
//	     launched 32x wider than checkpointReserveGB charged (~0.5 GB
//	     under-reserved on qwen3.6-27b) and — with upstream #24176/#25472 —
//	     evicted the previous turn's checkpoint on every turn.
//	v47: CPU-only TTS.cpp voices (Kokoro & friends) and CPU-only Parakeet ASR
//	     models move out of the exclusive swap group into persistent,
//	     never-evicting "tts" / "asr" coexistence groups beside the SAM one.
//	     They run on the CPU and are charged no VRAM, yet group membership
//	     alone made a playground read-aloud click (or a dictation) evict the
//	     chat model that produced the reply — and cold-reload it on the next
//	     turn. Coexistence groups are also appended to every listener, since
//	     they bind no port of their own.
//	v49: capabilities.reasoningEffort survives a --chat-template-file override.
//	     The ladder is now read from the drop-in template itself instead of being
//	     dropped wholesale, and templates that fold effort onto their own rungs
//	     without validating it (no raise) have their rungs read from the
//	     assignments. Every Qwen 3.8 running a drop-in template advertised no
//	     ladder at all, so the playground offered a bare thinking on/off.
//	v50: NO chat template is substituted automatically any more, and no model
//	     family gets special treatment. The bundled Qwen 3.5/3.6 drop-in is gone
//	     from the emit path: chat templates are the user's to manage, and the
//	     substitute silently dropped whatever the baked template supported that
//	     it did not. --chat-template-file is emitted only when the user sets a
//	     chatTemplateFile override. Affected models now advertise their own
//	     baked effort ladder rather than none.
//	v51: draft ggufs and mmproj projectors are inherited across a model's family
//	     (another quant of the same model, or a finetune at the same parameter
//	     count) when a model's own dir ships none, gated on the donor and the
//	     recipient agreeing on arch/n_embd/n_layer/n_vocab. Models that had no
//	     sidecar beside them can now emit -md / a -vision twin.
//	v52: MXFP4 is a recognised quant token, and an untabulated ggml tensor type
//	     no longer aborts the tensor walk. Both together: an MXFP4 gguf used to
//	     come back with VocabSize 0, which mis-sized its logits buffer and made
//	     the v51 header gate refuse it every family sidecar.
//	v53: quant tokens are recognised by FAMILY (Q/IQ, TQ, FP with its vendor
//	     prefix, BF/F) from one shared pattern in internal/quant, instead of
//	     from four hand-kept enumerations that had never heard of NVFP4. An
//	     unrecognised token is not cosmetic: the id never gets cut at it, so the
//	     model shares no base key with its own other quants (no sidecar
//	     inheritance) and every ctx tier and vision twin lists as a model of its
//	     own. ggml types 40 (NVFP4) and 41 (Q1_0) are tabulated too, and ids no
//	     longer re-append a quant the file name already spells mid-name, so
//	     "…-NVFP4-MTP-MID-HIGH" keeps the id it had.
//	v54: a "-vision" twin is sized twice — projector in VRAM and projector on
//	     the CPU — and emits --no-mmproj-offload whenever holding the CLIP
//	     tower on the GPU displaced text layers or cost more than a quarter of
//	     the context window. Changes both the emitted argv and the twin's
//	     estVram/ctx, so every existing config has to regenerate.
//	v55: the reserved "vision" variant carries its own mmproj placement, so a
//	     pin set there (gpu/ram/none) now decides the twin's argv - and its
//	     VRAM - instead of the model-wide value alone.
//	v56: --reasoning-preserve is emitted by DEFAULT wherever reasoning is on
//	     (Override.PreserveThinking nil => on); only an explicit false strips.
//	v57: the dense context ladder no longer tops out at 128k, and an install
//	     carrying the old shipped ladder (in the generate file or the settings
//	     sidecar) is migrated onto the new one. Every dense model trained past
//	     128k gets a larger -c and a larger KV reserve, so the emitted argv and
//	     the estimates change for inputs that did not.
//	v60: a model can name its vision projector explicitly (mmprojFile), which
//	     overrides discovery AND creates a "-vision" twin for a model whose
//	     own folder (and whose family) ships no mmproj at all. Auto-pairing is
//	     deliberately not widened (a projector belongs to one vision tower), but
//	     a shared projector kept in a folder of its own is now reachable, so a
//	     config can gain a twin and an --mmproj it did not have.
//	v61: -cms is hoisted back out of extraArgs. The launch-box editor did not
//	     parse the flag, so an edit round trip pushed it into extraArgs, where it
//	     was appended AFTER the computed copy (and grew by one per trip). Any
//	     config carrying one now emits a single -cms, and the hoisted value acts
//	     as the pin it was meant to be.
//	v62: a split gguf is charged the whole set's bytes, not shard 1's. Discovery
//	     represents a set by its first shard (correct: llama-server opens the
//	     siblings itself), but stat'ing that one file sized an 80B MoE at a
//	     quarter of itself, so the plan offloaded it whole and spent the phantom
//	     slack on context. Every split model's -ngl/--n-cpu-moe/-c changes for
//	     inputs that did not.
//	v63: a model larger than one card is split across every eligible GPU. The
//	     VRAM budget is pooled across the set instead of taken from the largest
//	     adapter, and a plan that needs more than one device emits -sm layer
//	     --tensor-split (plus CUDA_DEVICE_ORDER=PCI_BUS_ID, so llama.cpp's
//	     device order matches the telemetry order the split positions mean).
//	     Every model on a multi-GPU box changes budget and argv for inputs that
//	     did not. The split is planned against each card's stable capacity, not
//	     the free reading of the moment the config was generated: a card busy
//	     for that one sample used to bake a single-device plan that spawn time
//	     could not repair, since the live retune rewrites an existing
//	     --tensor-split and cannot add one.

//	v67: custom launch arguments size the plan, not just the argv (autogen/pins.go).
//	     A pinned -c/-ctk/-ub/-ngl/--n-cpu-moe/--parallel/... is folded into the
//	     sizer, so the emitted flags and the baked estVramGB/estRamGB describe the
//	     launch that actually runs instead of the one the sizer would have picked
//	     alone. Every model whose sidecar carries customArgs changes -c/-ngl/
//	     --n-cpu-moe, and a pinned window is no longer rounded to a 4096 multiple,
//	     for inputs that did not.

//	v68: an integrated GPU's shared system-memory pool counts toward its budget.
//	     A 780M with a 2GB BIOS carve-out used to sit under the 3GB inference
//	     floor and be dropped entirely, so an APU-only box sized against no
//	     GPU at all and put every layer on the CPU (issue #37). The reading is
//	     now carve-out + GTT, and the device is one the sizer can plan on, so
//	     -ngl/--tensor-split/-c all change for inputs that did not.

// v69: TRELLIS.2 package directories are discovered and emitted as 3D models.
//
//	A directory holding a pipeline.json plus ckpts/ is now a model of its
//	own (a 3D entry with an image-in / 3d-out capability line) instead of a
//	pile of files nothing serves, and a BiRefNet gguf stops being listed as
//	a language model. Inputs that did not change gain a model.

// v70: TRELLIS.2 models launch with --mesh-postprocess-simplify. The upstream
//
//	default keeps the full mesh (~5.5M triangles and ~180 MB at the 512
//	profile); a served model is more useful at ~1M triangles and ~40 MB.

// v71: a declared settings.encoders.qwenLlm of the matching caption width wins
//
//	over the encoder scan's pick. The scan matches text encoders by hidden width
//	and breaks ties on rank, then rounded size, then path, so a Qwen3-4B and a
//	Qwen3-VL-4B file (both 2560 wide, both 3.99 GiB) resolved to whichever path
//	sorted first: Z-Image was fed the VL file Krea-2 wants while the user's own
//	qwenLlm pin named Qwen3-4B. Configs that declare qwenLlm change --llm for
//	inputs that did not.
//
// v72: a per-model backend pin now reaches every emitted class, not just chat,
//
//	image, tts and 3d: embedding and ASR launch the pinned entry, a
//	hand-declared extra image model carries its pin too, and the single-device
//	env pin is derived from the resolved binary. Switching the ★ default no
//	longer moves a model that named a backend.
//
// v74: MiniMax-H3 generation defaults corrected. --steps is no longer pinned at
//
//	4 (the base model is not distilled; 4 steps belongs to the turbo LoRAs,
//	which arrive per request), and --video-frames moves 25 -> 56 because H3
//	aligns UP to the 17k+5 grid and 25 was silently becoming 39.
//
// v75: text encoders match on captionFactor (exact width, or a whole fraction of
//
//	it for a DiT that concatenates encoder layers) instead of strict equality,
//	and settings.encoders.qwenLlm is no longer substituted when the scan
//	classified it and Llm rejected it. Flux.2 klein 9B now wires Qwen3-8B
//	instead of silently taking the global Qwen3-4B pin.
//
// v76: LTX-2.x is a video family of its own. A joint audio+video DiT wires both
//
//	VAEs and its own with-proj text encoder, generation defaults land on the
//	8k+1 frame grid, and a distilled checkpoint is launched at its own step and
//	cfg numbers instead of the generic video ones.
//
// v77: --max-vram is priced as the graph-cut headroom left after the resident
//
//	weights and GPU-side VAEs, not as the whole card, and --stream-layers is
//	emitted only where the diffusion params are in RAM for it to stream from.
//	Every sd-server line changes; a v76 config was generated before either.
//
// v78: audio.cpp is a backend. A gguf whose header names it
// (general.architecture=audiocpp) now emits an audiocpp_server entry instead of
// falling through to the chat path, with a new audiocpp: block carrying the
// models[] entry its --config needs. A family we do not serve yet (music,
// separation, voice conversion) emits no entry at all, only a comment.
// v79: audio.cpp entries carry --device. Left alone the server takes device 0 of
// its backend, which is the integrated GPU on any box that enumerates one first
// (an APU desktop, a laptop), so the model lands in shared system memory with
// nothing in the log saying so. The index comes from the binary's own
// --list-devices, whose [GPU]/[IGPU] tag says which is discrete.
// v80: the per-process GPU-runtime constant is split by BACKEND, not just by GPU
// vendor - a ROCm/HIP llama-server reserves 0.8GB where the Vulkan one on the
// same card reserves 0.4GB (computeRocmCtxGB). Every estVramGB on a ROCm box
// moves by 0.4GB for unchanged inputs, so the hash has to force the regen.
// v81: Qwen-Image 2.1. Its caption projection is an MLP (txt_in.in_layer), so
// condHiddenFrom can now measure it and the model wires a 4096-wide Qwen3-VL-8B
// instead of emitting a permanent "needs encoder [llm]" warning; and the Wan-3D
// VAE family records its latent channel count, so 2.1's 64-channel RGBA VAE and
// the 16-channel Wan/Qwen-Image one stop being separable only by filename sort.
// Both change the emitted --llm/--vae for qwen_image models.
// v82: Qwen-Image 2.1 is a UNIFIED checkpoint (one file for generation and
// reference editing), so the edit-name heuristics missed it. It now matches
// unifiedEditRe, which both pairs its vision projector (--llm_vision) and flips
// the emitted capability to in: [text, image].
// v83: refEdit "on" now implies --llm_vision. Pinning reference editing without
// also pinning the projector produced a model that could not generate at all on
// Qwen-Image 2.1, which errors rather than silently ignoring the reference.
// v84: prompt enhancers are auto-paired by name (detectEnhancers) and carry a
// per-image-model system prompt, so an image model can gain promptEnhancer,
// promptEnhancerEdit and either prompt key with nothing on disk having changed;
// PE ggufs also leave the text-encoder pool, which moves --llm on Qwen-Image.
// v85: an i2i prompt enhancer auto-pairs to the model's "-vision" twin instead
// of the base profile, so promptEnhancerEdit gains a "-vision" suffix on models
// where nothing on disk changed. The base profile keeps its projector in RAM,
// and an i2i rewriter is handed an image on every call.
// v86: video models now emit the prompt-enhancer block the image path already
// did, so a video model whose override already named an enhancer (saved from the
// model modal, silently dropped at emit) gains promptEnhancer /
// promptEnhancerEdit and either prompt key with nothing on disk having changed.
// v88: a third same-id row falls back to -<publisher>-<repo> then -N instead of
// reusing the second's key, and a clashing or incomplete extraImageModels entry
// leaves a "# SKIPPED" comment instead of vanishing.
// v89: Hugging Face model folders (config.json + safetensors) are discovered
// and served through vllm, or leave a "# SKIPPED" comment with no vllm backend.
// v90: vllm's --gpu-memory-utilization and estVramGB are sized to weights + KV
// + overhead when that is under the budget, not to the whole budget.
const genVersion = "v90"

// hashedInput reports whether a file under a models root can change what
// discovery sees: a gguf, a safetensors (encoder pool components and HF weight
// files), or an HF folder's config.json.
func hashedInput(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".gguf", ".safetensors":
		return true
	}
	return strings.EqualFold(name, hfConfigFile)
}

// InputsHash digests everything that can change the generated config: the set of
// model files under modelsRoot (path + size + mtime, see hashedInput) plus the raw bytes of the
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
			if d.IsDir() || !hashedInput(d.Name()) {
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
// resolved modelsRoot + raw generate file + UI sidecar + the binary's own
// directory. Kept in one place so EnsureConfig and CurrentInputsHash always
// hash identical inputs.
//
// The exe dir is folded in because slotKvPath/DefaultSlotCachePath bake an
// absolute path (next to the binary) into the emitted config at generate
// time; without this, moving or renaming the install dir leaves the stale
// config pointing --slot-save-path/slotCache.path at the old location
// forever, since none of the gguf/generate/sidecar inputs changed.
func buildHashInput(roots []string, rawGenerate, sidecarBytes []byte) []byte {
	out := append([]byte("genver\x00"+genVersion+"\x00"), []byte(strings.Join(roots, "\x00")+"\x00")...)
	out = append(out, rawGenerate...)
	out = append(out, "\x00sidecar\x00"...)
	out = append(out, sidecarBytes...)
	out = append(out, "\x00exedir\x00"...)
	if exe, err := os.Executable(); err == nil {
		out = append(out, []byte(filepath.ToSlash(filepath.Dir(exe)))...)
	}
	return out
}

// regenMu serializes every inputs walk and config write of this process.
// EnsureConfig is reached from several goroutines at once — the hub
// download-completion hook (regenerate + hot-reload over a models tree that
// JUST changed), the watch-models poller, the UI settings save, and the
// backend install goroutine — and each one walks the models tree, prunes the
// sidecar and rewrites the config + hash cache. Two of them at once can interleave
// their writes and tear config.yaml, and a hash walk running over a sidecar
// mid-prune sees bytes that are neither revision. One mutex keeps every walk
// and every write whole; it is held for the duration of the scan, which is
// the slow part, so callers that only need a change check (CurrentInputsHash)
// pay the same price instead of racing the writer.
var regenMu sync.Mutex

// CurrentInputsHash computes the inputs hash for the generate file's present
// state (models folder + generate bytes + sidecar). It is the same value
// EnsureConfig compares against its cache, so callers can cheaply detect whether
// a regen would produce a different config without running one. Serialized
// with EnsureConfig via regenMu: a walk that ran concurrently with a regen
// could hash a sidecar mid-prune and report a change that is already applied.
func CurrentInputsHash(generatePath, modelsDirOverride string) (string, error) {
	regenMu.Lock()
	defer regenMu.Unlock()
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
//
// The whole scan-and-write is serialized under regenMu, so a download that
// lands in the models folder can trigger a completion-hook regen and a
// watch-models poll in the same instant without their writes interleaving.
func EnsureConfig(generatePath, outConfigPath, modelsDirOverride string, logf func(string)) (regenerated bool, err error) {
	regenMu.Lock()
	defer regenMu.Unlock()
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

	// Resolve the eligible GPU set on every regen, not only under autoVram: the
	// split ratio and the extra-device overhead are hardware facts the sizer needs
	// whether or not the BUDGET is being re-measured. ResolveAutoVram does both.
	ResolveAutoVram(&gf.Settings, logf)

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
