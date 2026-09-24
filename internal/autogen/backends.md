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
registry (first-per-kind, `deriveBackendExes`) and stay as the fallback when no registry row
resolves. Every class runs the same precedence through its own resolver: `image` and `segment`
call `resolveBackend`, `tts` calls `resolveBackendPreferring`, and embedding (class `llm`) and
ASR resolve too. A per-model pin therefore wins everywhere, and **only an auto model follows a
later ★ switch**. One dead end: a `vllm` pin on an embedder keeps the class default, because
there is no vllm embedding emitter (`vllm.go` emits a generate command). The `env:` single-device
pin (`writeSingleDeviceEnv`) goes through the same resolver, so a pinned model does not pick up
the default build's vendor variables either.

`Settings.Backends` also carries the installer's **derived build rows** (`Build: true`, one per
installed build of a managed component), which exist so a model can pin a specific build instead
of taking the activated one. They are ordinary rows to resolution (step 1 pins one by id) and are
appended behind the component's own row, so first-per-kind derivation and the implicit class
default still land on the row the user activated.

**A kind can serve more than one class, and `audiocpp` is the first that does** (`tts` + `asr`).
Two consequences: "is this class already populated" is a question about a SET of classes
(`ClassTaken`, not a `KindClass` equality test), and a resolver running on behalf of another
engine has to drop those rows rather than test the answer (`withoutAudioCpp`, see
[`classes.md`](classes.md)) - otherwise an audio.cpp star makes a Parakeet model skip an
installed parakeet row and fall all the way back to the legacy derived exe.

**Config is "keyed to backend" for free:** one `Override` holds both llama and vllm fields;
each emitter reads only its own, so switching kind never wipes the dormant set.

## vllm

Kind `vllm` → `emitVllmModel`, a totally different arg set: llama's KV / `-ngl` / spec / DRY
knobs are ignored; only `Ctx`→`--max-model-len`, `VllmGpuUtil`, `VllmTensorParallel`,
`VllmTokenizer` apply. It serves the SAME discovered gguf (`--quantization gguf`) or HF
folder (below), and there
are **no ctx-tier or named variants** for vllm (the llama profile loop that makes them is
skipped). A chosen `llama` build just swaps `s.ServerExe` (local copy) through the normal path.

**Both VRAM-facing flags are derived from the budget, not flat:**

- `vllmMaxModelLen` — vllm allocates its KV pool up front from `--max-model-len`, so handing
  it the model's trained window (262144 on a Qwen3.6) is a refused or OOMing startup, not a
  large context. It charges `budget - weights - vllmOverheadGB` (1.5 GB flat for
  activations/CUDA graphs/the profiling peak — vllm's allocator is opaque, so it is a reserve,
  not a model) against llama's f16 KV cost model, `RoundedCtx`es the result, and caps it at the
  trained length. A pinned `Override.Ctx` always wins; weights-over-budget emits the 4096 floor
  plus a note rather than a window implying it fits.
- `vllmGpuUtil` — `footprint / total card`, rounded UP to 2 decimals and clamped to
  [0.10, 0.95]. The footprint (`vllmFootprintGB`) is `min(budget, weights + KvReserveGB(ctx) +
  vllmOverheadGB)`: vllm PREALLOCATES its whole share and fills the rest with KV blocks, so
  handing a 0.5B model the budget made it grab 22.8 GB and the router charge that much. A
  model whose ctx is capped by the trained length now takes only what it needs; a big model
  still lands at the budget. `estVramGB` charges `util × card` (what vllm actually takes after
  rounding), or the footprint when there is no card reading. The old flat 0.90 was a
  fraction of TOTAL memory that both ignored a deliberately small budget and could exceed what
  is actually free (vllm validates against free memory and refuses). The card is probed once
  per process via `cachedTotalVramGB` (a `sync.OnceValues` func var — the seam tests stub); no
  GPU reading falls back to the flat 0.90, since there is nothing to take a fraction of.

**Split ggufs are skipped, not emitted.** Discovery represents a shard set by shard 1 alone,
which is all llama.cpp needs (it opens the siblings itself) — vllm would load a fifth of the
weights. `emitVllmModel` writes a `# skipped` comment and leaves the model out of `emitted`;
`RenderSoloCmd` errors for the same case (`isSplitGguf`).

### Hugging Face folders (`hf.go`)

An unconverted HF model (`config.json` + `*.safetensors`) is a `GgufRow` with `IsHF` and
`FullPath` = the FOLDER, which is what `vllm serve` takes. Detection is deliberately narrow,
because a models tree is full of folders that look like this and are not chat models (TRELLIS's
DINOv3 backbone, ModernBERT classifiers, diffusion components). A folder qualifies only when
all three rules hold:

1. `config.json` names `*ForCausalLM`, or `*ForConditionalGeneration` with a `text_config`
   or `vision_config`. Seq2seq models (T5, Whisper) share that suffix and carry neither.
2. There is at least one direct `*.safetensors`.
3. There is no direct `*.gguf`. A conversion kept beside its source is the same model twice,
   and the gguf is the file every backend can run.

The walk does NOT stop at an HF folder, so a gguf in a subfolder is still found.

- **Routing.** `emitHFModel` resolves with `resolveBackendPreferring(..., "llm", "vllm")`, so
  vllm wins over a ★ llama default. llama.cpp cannot read safetensors at all. With no vllm
  entry the model is `# SKIPPED` with the reason, never emitted as a llama command. An HF row
  skips the gguf pipeline (`skipsGgufPipeline`), including dir-local and family sidecar
  pairing. `filepath.Dir` of a folder is its PARENT, whose projector belongs to another model.
- **Metadata.** `ReadHFMetadata` maps config.json onto `Metadata`, so `vllmMaxModelLen` runs
  the same KV math, reading the nested `text_config` for multimodal wrappers. It is
  conservative where config.json is ambiguous:
  - sliding-window layers are charged as full-attention layers;
  - `layer_types` linear/mamba/ssm layers carry no KV;
  - a `layer_types` list whose length disagrees with the layer count is ignored.

  `TestVllmMaxModelLen_HFParityWithGguf` pins the mapping against the gguf shape.
- **Args.** No `--quantization gguf`, because an HF folder names its own
  `quantization_config`. `hfQuant` labels the row: the AWQ/GPTQ/FP8 method, else the BF16/F16
  dtype. The id is the folder name plus the quant.
- **HF cache layout.** In `models--<owner>--<repo>/snapshots/<commit>/` the folder name is a
  commit hash, so `hfCacheRepo` takes the id from the grandparent instead. Weights there are
  symlinks, so sizes are `os.Stat`, not `DirEntry.Info`.
- **Server side.** `ReadModelMetadata` and `HFRowFor` let path-holding callers (the editor
  preview, the trained-ctx read) handle a folder without a `GgufRow`. `config.ParseCmd` reads
  vllm's positional `serve <model>` as `ModelPath`, without which no vllm entry could be saved
  from the editor. The regen hash also stats `.safetensors` and `config.json`
  (`hashedInput`), or a new folder would never trigger a regen.

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
