# internal/autogen

## Purpose

`autogen` generates a complete quartermaster config YAML by discovering local GGUF models,
reading their headers, and computing a per-model load plan (`-ngl` / `--n-cpu-moe` / context
window / KV quant) from a VRAM budget. It is a fork-specific Go port of the `domina-llm-eval`
PowerShell planner (`Read-GgufMetadata` / `Get-LlamaLoadPlan` / `Get-DenseCtx` /
`Get-KvCostModel`) plus the `Generate-Config.ps1` orchestration, letting the harness stop
pre-generating config variants by hand. Kept deliberately separable for clean upstreaming.

## Which doc

| Doc | Read it when you're touching |
|---|---|
| this file | discovery, gguf parsing, the control files (generate + sidecar), the profile loop, regen gating |
| [`sizing.md`](sizing.md) | VRAM math — compute buffer, `-ub`/`-b`, KV quant, checkpoints, drafters, RoPE, `estVramGB` |
| [`liveoffload.md`](liveoffload.md) | the spawn-time placement guard, `minGpuFraction`, live-VRAM budget capping |
| [`backends.md`](backends.md) | which exe serves a model, the registry, the vllm emitter |
| [`classes.md`](classes.md) | non-LLM emitters — SAM, image/diffusion, embedding, TTS (2 engines), ASR |

## Key files

| File | Role |
|---|---|
| `gguf.go` | GGUF header parser (`ReadGgufMetadata`): metadata KV section + tensor section → `Metadata`, plus `scanChatTemplate`. Package doc lives here. |
| `discover.go` | Walks the models root(s) for `.gguf`, derives model IDs/quants/publishers, collapses split shards, skips mmproj projectors and diffusion encoders/VAEs. `DiscoverGgufModels` (single root) + `DiscoverGgufModelsMulti` (main root + per-UI-category `categoryRoots`). |
| `family.go` | Sidecar inheritance across a model's family: `ModelBaseKey`/`FamilyKey` (Go twins of `baseKey`/`familyOf` in `ui-svelte/src/lib/modelTable.ts`), the header compatibility gate, `inheritSidecars` (fills blank `DraftPath`/`MmprojPath` from a compatible sibling) and `DraftSidecarFor` (the roots-aware `DraftSidecarForDir`). |
| `metacache.go` | In-memory metadata cache keyed by file size+mtime (`ReadGgufMetadataCached`). |
| `kvcost.go` | KV-cache cost model (`GetKvCostModel`) + context-budget math (`MaxCtxForBudget`, `KvReserveGB`, `RoundedCtx`, `GetDenseCtx`, `defaultKvQuant`). → `sizing.md` |
| `plan.go` | VRAM budget → placement: `-ngl`/`--n-cpu-moe` for dense and MoE (`GetLoadPlan`, `densePlacement`); MoE expert-share table + `effectiveShare`. → `sizing.md` |
| `generate.go` | Top-level orchestration (`Generate`): builds per-model profiles (solo, ctx tiers, named variants), sizes each, emits the YAML. `emitModel`/`RenderSoloCmd` **dispatch by model class** (SAM → image → embedding → TTS → ASR → LLM). This file is the profile loop; the three phases live in the siblings below. |
| `generate_sizing.go` | Phase 1 — sizing math: `sizeProfile`, `--ctx-checkpoints` count + `checkpointReserveGB`, `forceLowActiveMoE`/`applyForcedOffload`/`estForOffload`, `computeBufferGB`, `MmprojVramGB`, `cpuMmprojWins`, `draftOverheadGB`. Pure arithmetic over `Metadata` + `Settings`. → `sizing.md` |
| `generate_cmd.go` | Phase 2 — command rendering: `buildCmdLines` (the per-class argv builder) and `RenderSoloCmd` (same with a `${PORT}` placeholder, for the UI preview + ad-hoc commands), plus `effectiveUb`, `effectiveSpec`/`specHas`, `cmdPath`, `needsQwenFixedChatTemplate`, `defaultSamplerFor`/`samplerLines`. |
| `generate_emit.go` | Phase 3 — YAML emission: non-model sections (`emitSlotCache`, `emitAPIKeys`, `emitGroupsAndListeners`, `writeGroup`) and per-model bits (`emitProfile`, `writeDisplayName`, `writeEstVram`/`writeEstRam`, `effortLevels`, `formatCtxTag`, `slugify`). |
| `estimate.go` | One-shot preview (`EstimatePlan`) of a candidate tuning for the web editor; reuses the solo-profile sizing path without writing config. |
| `overrides.go` | Control-file types (`GenerateFile`, `Settings`, `Override`, `VariantSpec`, `GroupSpec`), defaults, loading/merging, `globLike` (PowerShell `-like`). `Settings.RootList()`/`CategoryRoots`/`CategoryOrder` = multi-root scan folders. `Override` carries granular DRY, granular spec, and the image fields. |
| `appsettings.go` | **Process-level** settings (`AppSettings`): listen + playground addresses, the dashboard access policy, models-folder watching, update polling, HF token. Not part of `Settings` and never emitted into the config — `main()` reads them before the config exists, via `LoadAppSettings` (generate file's `settings.app`, then the sidecar's `app:` block). `UpsertSidecarApp`/`LoadSidecarApp` are the dashboard's side. |
| `sidecar.go` | UI-owned overrides file (`quartermaster-overrides.yaml`): per-model overrides, the global settings patch, managed API keys, and the `BackendEntry` registry. |
| `hash.go` | Inputs hashing + hash-gated regen (`InputsHash`, `EnsureConfig`, `CurrentInputsHash`). |
| `vram.go` | Live free-VRAM sampling via `internal/perf` (`SampleFreeVramGB`, `resolveAutoVram`) for the `autoVram` setting. Budgets against the **idle high-water mark** (`noteFreeVramGB`), never the raw sample — autoVram re-resolves on every `EnsureConfig` *and* every estimate preview, both of which run while models are loaded. |
| `liveoffload.go` | Spawn-time placement recompute (`LiveOffloadArgs`). → `liveoffload.md` |
| `vllm.go` | Backend selection (`resolveBackend`, `resolveBackendPreferring`, `kindClass`) + the vllm emitter. → `backends.md` |
| `rope.go` | `ropeCeiling`/`ropeFactor` — the only path that lifts the trained-ctx ceiling. → `sizing.md` |
| `audio.go`, `asr.go`, `sam.go`, `image.go`, `embedding.go` | Non-LLM class emitters. → `classes.md` |

## Important types & functions

- `Metadata` (`gguf.go`) — parsed subset of a GGUF header (arch, block count, expert count,
  attention dims, RoPE/SWA/SSM fields, `IsMoE`, `IsMTP`, `ExpertWeightShare`, `PoolingType`,
  `ChatTemplatePreservesThinking`/`ChatTemplateEffortLevels`). Optional fields are 0 when
  absent; consumers guard on `> 0`. `PoolingType` is the authoritative embedder signal.
- `ReadGgufMetadata` — parses the metadata KV section, then `readExpertShare` sums tensor
  bytes to derive the exact expert-weight share.
- `ReadGgufMetadataFrom(rs io.ReadSeeker, path string, sizeBytes int64)` — the same parser over
  an **already-open source**, which is what lets the model browser size a repo file it has not
  downloaded (`internal/server/hubapi.md` hands it a `bytes.Reader` over a Range-fetched
  prefix). `ReadGgufMetadata` is a thin `os.Open`+`os.Stat` wrapper around it.
  **`sizeBytes` is the file's FULL length, not the length of what the reader holds** — it
  becomes `FileSizeGB`, the weights figure every sizing path charges, so passing the prefix
  length would report a model that fits any budget. A truncated source surfaces as
  `io.ErrUnexpectedEOF`, the caller's signal to re-fetch a longer prefix (a header carries no
  length of its own).
- `ReadGgufMetadataCached` (`metacache.go`) — size+mtime-keyed wrapper; what `Generate`/
  `emitModel` and the API actually call.
- `GgufRow` / `DiscoverGgufModels` (`discover.go`) — one served-model row per gguf (shard-1
  only), `ID = baseID-quant`; `DraftPath`/`DraftKind`/`DraftSizeGB` auto-pair a same-dir MTP
  sidecar or DFlash drafter, `MmprojPath` a same-dir clip projector. When a dir ships neither,
  `inheritSidecars` (`family.go`) borrows one from a compatible family member — see the gotcha
  below.
- Vision twin projector placement (`generate.go` profile loop + `cpuMmprojWins`) — every
  `-vision` twin is sized TWICE: once with the CLIP projector resident in VRAM (`MmprojGB`
  charged to `Overhead`) and once with it on the CPU. The CPU sizing wins, and the twin emits
  `--no-mmproj-offload`, when the GPU-resident projector displaced text layers (lower `-ngl` /
  more `--n-cpu-moe`) or cost more than a quarter of the context window. Placement first: that
  tax is per token, the CPU encode is a one-off per image. `LiveOffloadArgs` and the editor
  preview (`configapi_estimate.go`) both skip the `MmprojGB` charge when the argv carries the
  flag, so all three price the same launch. `Override.Mmproj` pins the decision per model —
  `gpu` / `ram` skip the dual sizing, `none` emits no twin at all (unlike an unlisted twin,
  which still builds). Surfaced as the "Image projector" dropdown on the model config modal's
  Default tab, shown only when the model has a projector. `VariantSpec.Mmproj` repeats the knob
  on the reserved `vision` variant and OUTRANKS the model-wide pin there (blank = inherit); it
  is deliberately absent from every other variant tab, because no other variant's profile loads
  a projector at all. Careful: an image-class model routes through `emitImageModel` before the
  twin gate, so a variant literally named `vision` is an ordinary image variant for those —
  assert on `--mmproj`, not on the `-vision` id.
- `GetLoadPlan` (`plan.go`) — `-ngl`/`--n-cpu-moe` from a VRAM budget; MoE path uses a 0.5
  PCIe-thrash crossover, falling back to naive `-ngl` (`densePlacement`) past it.
- `Generate` (`generate.go`) — discover → per-model `emitModel` → `emitGroupsAndListeners`.
- `EstimatePlan` (`estimate.go`) — preview load plan for the editor, mirroring the solo profile.
- `LoadGenerateFile` (`overrides.go`) — applies defaults and merges the sidecar.
- `EnsureConfig` (`hash.go`) — hash-gated generate; `CurrentInputsHash` lets callers detect a
  would-be-different config cheaply.
- Sidecar API (`sidecar.go`) — `LoadSidecarOverrides`, `UpsertSidecarOverride`,
  `DeleteSidecarOverride`, `UpsertSidecarSettings`, `ClearSidecarSettings`,
  `LoadSidecarCategoryRoots`/`UpsertSidecarRoot`, `LoadSidecarBackends`/`UpsertSidecarBackends`
  (backend exe paths — top-level so a VRAM reset can't wipe them; overlaid onto `Settings`
  BEFORE `applyDefaults` so a blank sd/tts derives as a sibling of a UI-set llama exe),
  `LoadSidecarAPIKeys`/`UpsertSidecarAPIKey`/`DeleteSidecarAPIKey`,
  `LoadSidecarBackendSources`/`UpsertSidecarBackendSources` (**tracked GitHub repos the in-app
  installer downloads builds from** — deliberately separate from `BackendList`: a
  `BackendSource` is a *place to get builds*, a `BackendEntry` is an *executable path the
  launcher uses*; installing from a source writes the entry. Rows with no id/repo or no variant
  carrying a derived pattern are dropped on write).

## Data flow

1. **Load** — `LoadGenerateFile` reads the hand-authored generate control file, applies
   PowerShell-default settings, then overlays the UI-owned sidecar: first the `SettingsPatch`
   (dashboard VRAM/headroom edits), then the sidecar overrides *ahead* of the file's overrides
   (so UI edits win under first-match resolution). `--models-dir` overrides `settings.modelsRoot`.
2. **Gate** — `EnsureConfig` hashes the resolved models root(s), raw generate bytes and sidecar
   bytes (`InputsHash`/`InputsHashRoots` when extra category roots exist). If the stored
   `.modelhash` matches and the output exists, regeneration is skipped. `autoVram` always forces
   a regen (the live VRAM snapshot isn't visible to the hash) and re-samples via `resolveAutoVram`.
3. **Discover** — `DiscoverGgufModels`/`DiscoverGgufModelsMulti` walk the roots; `Generate`
   sorts rows and resolves each row's `Override` by path glob (+ optional quant).
4. **Size** — `emitModel` dispatches by class, reads metadata (cached), resolves KV quant,
   derives the KV cost model, then builds a profile set: solo + ctx-tier variants + named custom
   variants (`Override.Variants` + `settings.defaultVariants`). Each profile runs
   `sizeProfile` → `GetLoadPlan` → `forceLowActiveMoE` (or `applyForcedOffload` when `cpuOffload`
   is pinned). Non-LLM classes use their own paths — see `classes.md`.
5. **Emit** — `emitProfile` writes each model's YAML `cmd` block. Per-model engine knobs on
   `Override` override the defaults here: `FlashAttn` (`-fa`), `Mmap`, `Mlock`, `Threads` (`-t`),
   `Parallel`, `Ub` (`-ub/-b`), granular DRY, the sampler defaults (`--temp`/`--top-k`/`--top-p`/
   `--min-p`/`--presence-penalty`, layered over the arch baseline) and granular speculative
   sub-knobs. Zero/empty = the generator default (but see the sampler note below: those are
   pointers, and 0 is a value). `Override.ExtraArgs` are appended verbatim after the computed flags.
   `buildCmdLines` is shared by `emitProfile` and `RenderSoloCmd` (the editor's launch-parameters
   preview, `PUT /api/models/{model}/preview`, no persistence). `emitGroupsAndListeners` assigns
   models to groups by name-glob (first match wins) and binds listen addresses; every glob-matched
   group is an exclusive swap group. The synthesised **coexistence** groups are the exception:
   SAM models, CPU-only TTS.cpp voices and CPU-only Parakeet ASR models are pulled out of the glob
   assignment (`coexistSets` → `sam` / `tts` / `asr`, one group per non-empty class) and emitted by
   `writeCoexistGroup` as `exclusive:false`, `persistent:true`, `swap:false`, so they neither evict
   nor are evicted, and are appended to every listener since they bind no port of their own.

## Gotchas / conventions

- **PowerShell parity is the spec.** Most functions are direct ports and say so in their doc
  comments. `real_models_test.go` asserts parity against real ggufs
  (`TestReadGgufMetadata_VsPowerShell`, `TestGetLoadPlan_VsPowerShell`,
  `TestGetKvCostModel_VsPowerShell`). Documented divergences are called out at the call site.
- **Emit-logic changes need a `genVersion` bump.** The regen hash covers inputs (gguf files +
  generate file + sidecar) but NOT the generator code, so a change to `buildCmdLines`/
  `emitProfile` output alone leaves the hash stable → `EnsureConfig` skips regen → the stale
  `config.yaml` survives even after a rebuild+restart. `genVersion` (folded into
  `buildHashInput`) forces a one-time regen. **Bump it whenever the emitted YAML changes for
  identical inputs.**
- **Sidecar SHADOWS the file row, it does not field-merge.** Override resolution is row-level
  first-match (sidecar rows prepended), so a sidecar row replaces the matching file row
  wholesale. A UI save must therefore write a *superset*: the config editor seeds the sidecar
  row from `ResolveFileOverride` (the matched FILE override, sidecar excluded) before applying
  the edited fields, so file-only knobs the UI doesn't model (`ctxVariants`, `quant`,
  file-defined variants like `judge`) aren't dropped. Fleet-wide `settings.defaultVariants`
  (e.g. `game`) are emitted independently of overrides, so they survive regardless — which is
  why a buggy save used to lose the ctx tiers + `judge` but keep `game`.
- **Sidecar ownership** — `quartermaster-overrides.yaml` is fully owned by the UI and rewritten
  whole on any edit, kept separate from the comment-rich hand-authored generate file.
- **Named variants INHERIT the model-wide override.** Each `<model>-<variant>` profile layers its
  engine knobs over a copy of the resolved model override, so the spec/draft chain, kv quant,
  reasoning budget, preserve-thinking etc. flow down at generate time; a variant's own
  non-blank/non-zero field still wins. Was previously *standalone* over `Override{}`, which
  dropped the draft chain + kv on every variant but the one the user hand-edited.
- **Drafters and projectors are FAMILY-wide, dir-local first.** A sidecar is published for a
  model but downloaded next to one copy of it, so a second quant in its own folder — or a
  finetune of a base that shipped an mmproj — used to see nothing. `inheritSidecars`
  (`family.go`) fills only blank `DraftPath`/`MmprojPath`, so a model that ships its own drafter
  always keeps it, and picks the closest donor: another quant of the same model (`ModelBaseKey`),
  then the family's un-tuned base, then a peer finetune. **The name match alone never authorizes
  a donation** — the donor's model and the recipient must also agree on arch, embedding length,
  block count and vocab size (`sidecarCompatible`). That gate is not cosmetic: a drafter whose
  vocab differs doesn't degrade, it aborts the launch (`tensor 'output.weight' has wrong shape`).
  Consequences to keep in mind: a model that never had a `-vision` twin can grow one, `-md`
  appears where no drafter is visible in the folder, and anything summing `DraftSizeGB` across
  rows must dedupe on `DraftPath` (one file, many rows — `internal/setup/probe.go`). Per-model
  opt-out is the existing spec override (no `draft-*` → no `-md`) and marking the vision twin
  unlisted. `ModelBaseKey`/`FamilyKey` mirror `baseKey`/`familyOf` in
  `ui-svelte/src/lib/modelTable.ts` — change one, change both.
- **A new quant type needs TWO tables, and the header gate is why.** `quantRe` (`discover.go`)
  names it, `ggmlTypeSize` (`gguf.go`) sizes its tensors. Miss the second and every gguf in that
  quant comes back with `VocabSize` 0 — which mis-sizes the logits buffer AND makes
  `sidecarCompatible` refuse it every family sidecar, silently. The tensor walk no longer aborts
  on an untabulated type (only the MoE expert share degrades to 0), but adding the type is still
  the fix. MXFP4 was the case that found this.
- **`AppSettings` is a REPLACE, and `SettingsPatch` is a MERGE — do not unify them.** The dashboard
  renders the process-level block as one form and PUTs all of it, and its fields have no meaningful
  "unset from this section" state: clearing `adminAllow` must actually clear it, which a merge
  cannot express. `mergeAppSettings` (file → sidecar) is written out field by field rather than
  reflected, because "unset" differs per field (`""`, `0`, `nil`) and one blanket rule turns a
  deliberately-emptied `AdminAllow` back into the old value. Precedence:
  `argv > sidecar app block > settings.app > built-in default` — argv wins so
  `quartermaster.exe -listen 127.0.0.1:1250` can always rescue an install whose stored address no
  longer binds.
- **`ReplaceSidecarSettings` exists because `MergeSettingsPatch` can only SET.** A per-section
  "restore defaults" (the advanced sizer knobs) has to nil that section's fields and store the
  result verbatim; merging a patch full of nils is a no-op. Use it only after a read-modify-write
  that preserves every other section's fields.
- **Empty `modelsRoot` is valid, not an error** — the server boots with an empty catalog so a
  setup UI can point it at a folder later; discovery and hashing short-circuit on blank.
- **`Parallel` > 1: sized per slot, emitted as a pool.** `--kv-unified` makes `-c` ONE KV buffer
  shared by all `--parallel N` slots, so N conversations of the sized window need N x that pool.
  `Generate` multiplies the per-token KV cost by `profileParallel` **before** `sizeProfile` (so the
  clamp shrinks the per-slot ctx to what VRAM holds) and `emitProfile` multiplies the result back
  out into `-c ctx*parallel`. Both read the slot count through `EffectiveParallel`/`profileParallel`
  (variant wins over the model-wide override, capped at `MaxParallelSlots`) — if one side ever reads
  a different number, the server either over-commits VRAM or hands slot 1 the whole card.
  `/api/models` divides `-c` back down so the UI reports the per-conversation window.
- **Determinism for tests** — `Generate` takes the timestamp as an argument (`DefaultNow()` is
  separate) so output is reproducible.
- **Caching** — metadata by size+mtime (`metacache.go`); replacing a gguf invalidates its entry.
  The config itself is cached via the `.modelhash` sidecar digest.
- `gguf.go` parses the tensor section too (for expert share); a tensor of unknown ggml type
  leaves the share at 0 (arch-table fallback) rather than erroring the whole read.

### Sampler defaults

- **Only `--top-k` and `--min-p` get an arch baseline** (`defaultSamplerFor`,
  `generate_cmd.go`): Qwen3-family models (`qwen3*`, all 3.x minors) emit `--top-k 20 --min-p 0`,
  `muse-glimmer*` emits `--top-k 64` and NO min-p (its card pins one and not the other),
  everything else emits nothing. The asymmetry is the point — neither knob has an OpenAI-API field, so a client
  physically cannot set them and llama-server's defaults (top-k 40, min-p 0.05) are what every
  request got, for every model, regardless of what the model card says. Temperature/top-p/
  presence-penalty are sent by nearly every client, so a launch flag would be overwritten on
  arrival; they stay opt-in per model. They are also the mode-dependent ones (Qwen documents
  temp 1.0 thinking vs 0.7 instruct), and one process serves both modes, so there is no single
  correct value to seed.
- **`Temp`/`TopK`/`TopP`/`MinP`/`PresencePenalty` are POINTERS, unlike the zero-gated knobs
  around them.** 0 is a meaningful, non-default value for every one (`--min-p 0` disables
  truncation, `--temp 0` is greedy), so `0 => inherit` would make the exact values model cards
  recommend unexpressible. Everything downstream must follow: the variant merge tests `!= nil`,
  the DTO patch tests `!= nil`, and the UI reads them with `??` not `||` (`advFromOverride`,
  `vsamp`/`vsampSet` in `ModelConfigModal.svelte`).
- The UI's launch-command box parses these back (`parseCmdFields`), capturing them as a **delta
  vs what the generator already emits** (`genDefaultNum`, same trick as `genDefaultKv` for
  `-ctk`) — otherwise blurring the box for any unrelated reason freezes the arch baseline into
  an explicit per-model pin that never tracks a future baseline change.
- **Do not add speculative baseline entries.** A survey of ~19 current local models (Llama
  3.x/4, Mistral, Magistral, Gemma 3, Phi-4, DeepSeek R1/V3.1, GLM-4.6, gpt-oss, Kimi-K2,
  Nemotron Nano, Qwen2.5/QwQ) found no top-k or min-p recommendation outside Qwen3 and Muse
  Glimmer — those cards publish temperature and top-p, which clients send anyway. The empty
  default is the researched answer, not an unfinished table. Add a case only with a model card
  in hand, and match on the arch string llama.cpp actually registers (check the GGUF metadata
  or the upstream PR — the card's prose name is not the arch name).

### Chat template & reasoning effort

- **No chat template is ever substituted automatically.** `--chat-template-file` is emitted
  only when the model has a `chatTemplateFile` override. Chat templates are the user's to
  download and wire up; shipping a curated replacement for one vendor's family played
  favourites and silently dropped whatever the baked template supported that the replacement
  did not (Qwen 3.8's reasoning-effort ladder, for one).
- **`scanChatTemplate` still reads the baked template**, but only for the effort ladder.
  `ReadGgufMetadata` decodes `tokenizer.chat_template` and derives `ChatTemplateEffortLevels`
  (plus `ChatTemplatePreservesThinking`, which nothing consumes today) — the flag and the level
  list are kept, never the ~10–100 KB source. Note that arch cannot distinguish Qwen minors:
  3.5, 3.6 **and** 3.8 all report `qwen35`/`qwen35moe`, which is why nothing here branches on
  arch to decide template behaviour.
- **The ladder always describes the template that will actually run.** A
  `--chat-template-file` replaces the baked template wholesale, so the gguf's levels say nothing
  about the live renderer — `effortLevels` (`generate_emit.go`) therefore scans the **override
  file** in the gguf template's place (memoized per path; one file is shared by every ctx variant
  of every model using it). Whatever survives that scan becomes the emitted
  `capabilities.reasoningEffort`. There is no longer a special case forcing `nil` for the old
  bundled Qwen template, so a model with no override now advertises the ladder its own baked
  template actually implements.
- **The effort ladder is read out of the template, never hardcoded.** Two shapes, in order:
  - *Strict* — `effortLevelsRe` matches 3.8's `reasoning_effort not in ('xhigh', 'medium', 'low')`
    validation line, so the advertised set is exactly what the jinja accepts.
  - *Tolerant* — a template that reads `reasoning_effort` and folds it onto its own rungs without
    validating it (the Qwen 3.8 drop-ins in circulation) declares no tuple, so `effortAssignRe`
    reads the literals **assigned** to an effort-named variable (`set _initial_effort = 'xhigh'`),
    not the ones compared against it — the comparisons are padded with OpenAI-ladder aliases the
    template folds away. Gated on the template raising nothing about effort (`effortRaiseRe`,
    which deliberately ignores the unrelated content raises every real template carries): with no
    guard in play, a rung read wrong degrades to the template's own default rather than the 500
    llama.cpp returns for an unknown kwarg value.

  A template that reads `reasoning_effort`, names no rungs, and validates none advertises
  **nothing**. This is what `internal/server/http-core.md` (`reasoning_effort.go`) snaps an
  OpenAI-ladder value onto.

## Connections

- **Depends on:** `internal/perf` and `internal/logmon` (live GPU/VRAM telemetry in `vram.go`);
  `gopkg.in/yaml.v3`.
- **Called by:** `quartermaster.go` at startup — `EnsureConfig` regenerates the config before
  the router loads it, and `-watch-models`/reload paths use `CachedConfigHash`/`CurrentInputsHash`
  to detect changes. `internal/server` config API reads/writes sidecar overrides and settings,
  previews tunings via `EstimatePlan` + `ReadGgufMetadataCached`, and triggers `EnsureConfig` on
  save (`internal/server/configapi.md`).
- Produces the YAML consumed by `internal/config` / the router/scheduler; its `groups`/`listeners`
  output backs the fork's multi-listener + cross-port eviction features.
