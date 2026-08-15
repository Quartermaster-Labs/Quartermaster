# autogen — multi-backend selection & the vllm emitter

`vllm.go`. How a model picks which executable serves it, and what changes when that
executable isn't llama-server. Non-LLM classes are in [`classes.md`](classes.md).

## Resolution order

`Settings.Backends` is the UI-owned registry (llama / vllm / sd / tts / ttscpp / segment /
upscale / custom entries, each with a `Default` per-class flag), loaded from the sidecar
`backendList` in `LoadGenerateFile`. `kindClass` maps kind → class (`llm`/`image`/`tts`/
`segment`/`upscale`).

`emitModel` / `RenderSoloCmd` resolve an LLM's backend via `resolveBackend(s, ov, "llm")`:

1. explicit `Override.Backend` (entry id) wins,
2. else the ★-`Default` entry of the class,
3. else the first entry,
4. else a zero value → **fall back to the legacy `ServerExe`** (single-backend setups unchanged).

The legacy `BackendExes`/`ServerExe`/`SdServerExe`/`TtsServerExe` are **derived** from the
registry (first-per-kind, `deriveBackendExes`), so image/embedding/tts emit is untouched.

**Config is "keyed to backend" for free:** one `Override` holds both llama and vllm fields;
each emitter reads only its own, so switching kind never wipes the dormant set.

## vllm

Kind `vllm` → `emitVllmModel`, a totally different arg set: llama's KV / `-ngl` / spec / DRY
knobs are ignored; only `Ctx`→`--max-model-len`, `VllmGpuUtil`, `VllmTensorParallel`,
`VllmTokenizer` apply. It serves the SAME discovered gguf (`--quantization gguf`), and there
are **no ctx-tier or named variants** for vllm (the llama profile loop that makes them is
skipped). A chosen `llama` build just swaps `s.ServerExe` (local copy) through the normal path.

**Both VRAM-facing flags are budget-derived, not flat:**

- `vllmMaxModelLen` — vllm allocates its KV pool up front from `--max-model-len`, so handing
  it the model's trained window (262144 on a Qwen3.6) is a refused or OOMing startup, not a
  large context. It charges `budget - weights - vllmOverheadGB` (1.5 GB flat for
  activations/CUDA graphs/the profiling peak — vllm's allocator is opaque, so it is a reserve,
  not a model) against llama's f16 KV cost model, `RoundedCtx`es the result, and caps it at the
  trained length. A pinned `Override.Ctx` always wins; weights-over-budget emits the 4096 floor
  plus a note rather than a window implying it fits.
- `vllmGpuUtil` — `budget / total card`, clamped to [0.10, 0.95]. The old flat 0.90 was a
  fraction of TOTAL memory that both ignored a deliberately small budget and could exceed what
  is actually free (vllm validates against free memory and refuses). The card is probed once
  per process via `cachedTotalVramGB` (a `sync.OnceValues` func var — the seam tests stub); no
  GPU reading falls back to the flat 0.90, since there is nothing to take a fraction of.

**Split ggufs are skipped, not emitted.** Discovery represents a shard set by shard 1 alone,
which is all llama.cpp needs (it opens the siblings itself) — vllm would load a fifth of the
weights. `emitVllmModel` writes a `# skipped` comment and leaves the model out of `emitted`;
`RenderSoloCmd` errors for the same case (`isSplitGguf`).

**`--tokenizer` is never guessed.** Upstream recommends the base model's tokenizer over the
one converted out of the gguf, but `GgufRow.Repo` is the local folder name, not a verified HF
id — so it comes only from `Override.VllmTokenizer`.

## `upscale` is registry-only

realesrgan-ncnn-vulkan has no `.gguf`, no load plan and no config `cmd` block. It's stored in
the registry purely so the server can read its exe path (`LoadSidecarBackendList`) and shell
out per request (`internal/server/tools.md`, exec-per-request). **Adding an upscale entry never
changes the generated YAML.**

## Managed vs manual rows

`BackendEntry` also carries `Managed`/`Component`/`Version`/`Variant`, set on a row whose
binary was downloaded by the in-app installer (`internal/backends`) rather than typed in. A
manual row leaves all four zero and the installer never touches it. Server-side handling of
that split is in `internal/server/configapi.md`.
