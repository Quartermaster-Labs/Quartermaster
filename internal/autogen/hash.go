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
const genVersion = "v40"

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
