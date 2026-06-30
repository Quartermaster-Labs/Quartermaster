# internal/autogen

## Purpose

`autogen` generates a complete llama-quartermaster config YAML by discovering local GGUF
models, reading their headers, and computing a per-model llama-server load plan
(`-ngl` / `--n-cpu-moe` / context window / KV quant) from a VRAM budget. It is a
fork-specific Go port of the `domina-llm-eval` PowerShell planner
(`Read-GgufMetadata` / `Get-LlamaLoadPlan` / `Get-DenseCtx` / `Get-KvCostModel`)
plus the `Generate-Config.ps1` orchestration, letting the harness stop
pre-generating config variants by hand. Kept deliberately separable for clean
upstreaming.

## Key files

| File | Role |
|---|---|
| `gguf.go` | GGUF header parser (`ReadGgufMetadata`): decodes the metadata KV section and the tensor section into `Metadata`; package doc lives here. |
| `discover.go` | Walks `modelsRoot` for `.gguf` files, derives model IDs/quants/publishers, collapses split shards, skips mmproj projectors (`DiscoverGgufModels`). |
| `metacache.go` | In-memory cache of parsed metadata keyed by file size+mtime (`ReadGgufMetadataCached`) so repeated regens skip header re-parsing. |
| `kvcost.go` | KV-cache cost model (`GetKvCostModel`) plus context-budget math (`MaxCtxForBudget`, `KvReserveGB`, `RoundedCtx`, `GetDenseCtx`). |
| `plan.go` | VRAM budget → placement: chooses `-ngl`/`--n-cpu-moe` for dense and MoE models (`GetLoadPlan`, `densePlacement`); MoE expert-share table + `effectiveShare`. |
| `generate.go` | Top-level orchestration (`Generate`): builds per-model profiles (solo, ctx tiers, named variants), sizes each, and emits the YAML (models, groups, listeners). |
| `estimate.go` | One-shot preview (`EstimatePlan`) of a candidate tuning for the web editor; reuses the solo-profile sizing path without writing config. |
| `overrides.go` | Control-file types (`GenerateFile`, `Settings`, `Override`, `VariantSpec`, `GroupSpec`), defaults, loading/merging, and `globLike` (PowerShell `-like`). |
| `sidecar.go` | UI-owned overrides file (`quartermaster-overrides.yaml`): read/upsert/delete per-model overrides, the global settings patch, and the managed API keys (`LoadSidecarAPIKeys`/`UpsertSidecarAPIKey`/`DeleteSidecarAPIKey`). |
| `hash.go` | Inputs hashing + hash-gated regen (`InputsHash`, `EnsureConfig`, `CurrentInputsHash`) so a config is only rebuilt when models/settings change. |
| `vram.go` | Live free-VRAM sampling via `internal/perf` (`SampleFreeVramGB`, `resolveAutoVram`) for the `autoVram` setting. |
| `liveoffload.go` | Spawn-time placement recompute (`LiveOffloadArgs`): re-derives `-ngl`/`--n-cpu-moe` from free VRAM *right now* so a stale baked plan can't OOM. |

## Important types & functions

- `Metadata` (`gguf.go:48`) — parsed subset of a GGUF header (arch, block count, expert count, attention dims, RoPE/SWA/SSM fields, `IsMoE`, `IsMTP`, `ExpertWeightShare`). Optional fields are 0 when absent; consumers guard on `> 0`.
- `ReadGgufMetadata` (`gguf.go:350`) — parses the metadata KV section, then `readExpertShare` (`gguf.go:706`) sums tensor bytes to derive the exact expert-weight share.
- `ReadGgufMetadataCached` (`metacache.go:31`) — size+mtime-keyed cache wrapper; this is what `Generate`/`emitModel` and the API actually call.
- `GgufRow` (`discover.go:13`) / `DiscoverGgufModels` (`discover.go:35`) — one served-model row per gguf (shard-1 only), with derived `ID = baseID-quant`.
- `GetLoadPlan` (`plan.go:99`) — derives `-ngl`/`--n-cpu-moe` from a VRAM budget; MoE path uses a 0.5 PCIe-thrash crossover, falling back to naive `-ngl` (`densePlacement`, `plan.go:76`) past it.
- `GetKvCostModel` (`kvcost.go:40`) — KV size as `Slope*ctx + Const` GB, SWA/hybrid-SSM aware; `Const` is 0 for plain attention.
- `GetDenseCtx` (`kvcost.go:165`) — speed-first dense context picker (avoid offloading just to grow ctx).
- `Generate` (`generate.go:57`) — the entry point: discover → per-model `emitModel` → `emitGroupsAndListeners`. `sizeProfile` (`generate.go:350`) holds the dense/MoE/kv-in-ram/no-attn branches; `forceLowActiveMoE` (`generate.go:451`) repairs the planner's crossover fallback for low-active MoE.
- `EstimatePlan` (`estimate.go:37`) — preview load plan for the editor, mirroring the solo profile.
- `GenerateFile`/`Settings`/`Override`/`VariantSpec`/`GroupSpec` (`overrides.go`) — the YAML control surface; `LoadGenerateFile` (`overrides.go:181`) applies defaults and merges the sidecar.
- `EnsureConfig` (`hash.go:107`) — hash-gated generate; `CurrentInputsHash` (`hash.go:84`) lets callers detect a would-be-different config cheaply.
- Sidecar API (`sidecar.go`) — `LoadSidecarOverrides`, `UpsertSidecarOverride`, `DeleteSidecarOverride`, `UpsertSidecarSettings`, `ClearSidecarSettings`.

## Data flow / how it works

1. **Load** — `LoadGenerateFile` reads the hand-authored generate control file, applies PowerShell-default settings, then overlays the UI-owned sidecar: first the `SettingsPatch` (dashboard VRAM/headroom edits), then the sidecar overrides *ahead* of the file's overrides (so UI edits win under first-match resolution). `--models-dir` overrides `settings.modelsRoot`.
2. **Gate** — `EnsureConfig` hashes the resolved models root, raw generate bytes, and sidecar bytes (`InputsHash`). If the stored `.modelhash` matches and the output exists, regeneration is skipped. `autoVram` always forces a regen (the live VRAM snapshot isn't visible to the hash) and re-samples free VRAM via `resolveAutoVram`.
3. **Discover** — `DiscoverGgufModels` walks the models root; `Generate` sorts rows and resolves each row's `Override` by path glob (+ optional quant).
4. **Size** — for each model `emitModel` reads metadata (cached), resolves KV quant (forced matched `q8_0` unless a valid override), derives the KV cost model, then builds a profile set: solo + ctx-tier variants + named custom variants (`Override.Variants` + `settings.defaultVariants`). Each profile runs through `sizeProfile` → `GetLoadPlan` → `forceLowActiveMoE` (or `applyForcedOffload` when `cpuOffload` is pinned).
5. **Emit** — `emitProfile` writes each model's YAML `cmd` block (flags for `-ngl`, `-c`, `-ub/-b`, `-fa`/`-ctk`/`-ctv`, spec-type, reasoning, DRY, threads, `--n-cpu-moe`). Per-model engine knobs on `Override` override the defaults here: `FlashAttn` (`-fa`), `Mmap` (force/suppress `--no-mmap`), `Mlock` (`--mlock`), `Threads` (`-t`), `Parallel` (`--parallel`), `Ub` (`-ub/-b`). Zero/empty = the generator default; the UI config editor writes these into the sidecar override. `Override.ExtraArgs` are appended verbatim after the computed flags (passthrough for knobs autogen doesn't model). The cmd flag list is built by `buildCmdLines` (shared by `emitProfile` and `RenderSoloCmd`); `RenderSoloCmd` re-renders the full solo command for the editor's two-way launch-parameters box preview (server route `PUT /api/models/{model}/preview`) without persisting. `emitGroupsAndListeners` assigns models to groups by name-glob (first match wins) and binds listen addresses; every group is an exclusive swap group.

## Gotchas / conventions

- **PowerShell parity is the spec.** Most functions are direct ports and call that out in their doc comments. `real_models_test.go` asserts parity against real ggufs (`TestReadGgufMetadata_VsPowerShell`, `TestGetLoadPlan_VsPowerShell`, `TestGetKvCostModel_VsPowerShell`). `plan.go`'s `densePlacement` *intentionally diverges* from the PowerShell version (it fixes a per-layer KV-reserve bug) — see the comment at `plan.go:76`.
- **Two MoE share tables.** `plan.go` has `moeExpertShare` (planner) and `generate.go` has `genMoeShare` (generation-side ctx sizing + `forceLowActiveMoE`); the latter adds `qwen35moe`. Both are only a *fallback*: `effectiveShare` prefers the exact `Metadata.ExpertWeightShare` derived from the tensor section, using the arch table only when that is 0.
- **Compute buffer is modeled, not a flat fudge.** Per-profile VRAM overhead adds `computeBufferGB` (`generate.go`): logits (`VocabSize*ub*4`) + activations (`ub*EmbeddingLength*~8*4`) + a fixed CUDA-ctx constant, scaled by `settings.computeBufFactor` (default 1.0). Replaced the old flat `ubSoloOh=0.17`, which undercounted by >1 GB on large-vocab models (Qwen3.5 vocab ~248k → ~1 GB of logits buffer alone) and drove VRAM spillover. `VocabSize` comes from the `token_embd.weight`/`output.weight` tensor elems ÷ `EmbeddingLength` (`gguf.go`); 0 dims → flat `computeFallbackGB`. Tune `computeBufFactor` against the "compute buffer size" llama prints at load. `effectiveUb` is shared by the sizer and emit so the charged ub matches the emitted `-ub`.
- **`-ub 1024` and `--n-cpu-moe` are bench-validated (Qwen3.5-35B-A3B IQ4_XS, 8GB/32GB rig, 5.6k-token prefill).** ub is a *prefill-only* knob — decode (tg) is flat across all ub. Prefill scales monotonically with ub, **no plateau through 1024**: 256→512→1024 ≈ 222→324→493 t/s (512→1024 = +52%); ub=64 collapses to 91 t/s (−74% vs 512), so the "smaller ub is faster" folklore does NOT hold here. Keep ub high where VRAM fits; `computeBufferGB` charges 1024's ~1 GB so the sizer won't over-allocate ctx and spill. Separately, the emitted MoE placement (`-ngl 99 --n-cpu-moe N`, attention + non-expert + a few expert layers on GPU) gave **~2× the decode of pure layer-offload** (`-ngl 12`): 34.5 vs 17.5 t/s same model/rig — i.e. the `--n-cpu-moe` strategy is the real speed lever, not ub. (Throwaway bench: `domina-llm-eval/scripts/Bench-Ubatch.ps1`.)
- **KV quant is forced to matched `q8_0`.** Mismatched K/V or `iq4_nl` is rejected and reset (flash-attention requires matched K/V); a per-model/variant override only takes effect if it is itself valid and matched.
- **Empty `modelsRoot` is valid, not an error** — the server boots with an empty catalog so a setup UI can point it at a folder later; discovery and hashing short-circuit on blank.
- **Determinism for tests** — `Generate` takes the timestamp as an argument (`DefaultNow()` is separate) so output is reproducible.
- **Caching** — metadata is cached by size+mtime (`metacache.go`); replacing a gguf invalidates its entry. The config itself is cached via the `.modelhash` sidecar digest.
- **Sidecar ownership** — `quartermaster-overrides.yaml` is fully owned by the UI and rewritten whole on any edit, kept separate from the comment-rich hand-authored generate file.
- **Sidecar SHADOWS the file row, it does not field-merge.** Override resolution is row-level first-match (sidecar rows prepended), so a sidecar row replaces the matching file row wholesale. A UI save must therefore write a *superset*: the config editor seeds the sidecar row from `ResolveFileOverride` (the matched FILE override, sidecar excluded) before applying the edited fields, so file-only knobs the UI doesn't model (`ctxVariants`, `quant`, file-defined variants like `judge`) aren't dropped. Fleet-wide `settings.defaultVariants` (e.g. `game`) are emitted independently of overrides, so they survive regardless — which is why a buggy save used to lose the ctx tiers + `judge` but keep `game`.
- **Named variants INHERIT the model-wide override.** Each `<model>-<variant>` profile layers its engine knobs over a copy of the resolved model override (`generate.go` ~line 428), so the spec/draft chain, kv quant, reasoning budget, preserve-thinking, etc. flow down at generate time; a variant's own non-blank/non-zero field still wins (sidecar edits drift per-variant freely). Was previously *standalone* over `Override{}`, which dropped the draft chain + kv on every variant but the one the user hand-edited.
- `gguf.go` parses the tensor section too (for expert share); a tensor of unknown ggml type leaves the share at 0 (arch-table fallback) rather than erroring the whole read.
- **Dynamic offload is a spawn-time guard, NOT a regen.** `LiveOffloadArgs` (`liveoffload.go`) runs on every model spawn (wired by `server.WireDynamicOffload` → router `SetSpawnArgs` → process `doStart`, only in `-generate` mode). It parses the emitted argv (`-m`/`-c`/`-ctk`/`-ctv`/`--spec-type`/`--ctx-checkpoints`/`-ngl`/`--n-cpu-moe`), reads the gguf (cached), and re-runs `EstimatePlan` with `TargetVramGB = live free VRAM` (raw — `EstVramGB` already folds the overhead in, so it IS the live footprint). It **only ever offloads MORE** than the baked plan (raises `--n-cpu-moe` / lowers `-ngl`) — ample VRAM or a hand-pinned `cpuOffload` is left untouched. If `EstVramGB > freeGB` even at the planner's max offload, it returns an error and the spawn is **refused** (clean load failure, not an OOM crash). Fails open: non-`.gguf` cmd, no `-ngl`, unreadable gguf, or no GPU telemetry all pass the argv through unchanged. `DraftGB` from a separate draft file isn't reconstructed from argv (uses the 0.34 GB baked default) — minor under-charge for big separate drafts.
- **Recurrent/hybrid models emit `--ctx-checkpoints 0`.** `defaultCtxCheckpoints` keys on `meta.FullAttnInterval > 0` (GatedDeltaNet/SSM, e.g. Qwen3.6): their recurrent state can only restore at its exact saved length, so checkpoint restore lands it at the wrong position → llama-server spams `non-consecutive token position` + reprocesses the whole prompt (0 reuse, upstream llama.cpp #21831). Checkpoints cost VRAM and buy nothing on this arch, so they're disabled. SWA (`kvConstGB>0`, non-recurrent) still gets 6 (it restores fine); plain attention 3. The same `FullAttnInterval>0` flag gates the server's slot-cache partial-prefix seeding (see `internal/server/CLAUDE.md`) — both fixes target the same upstream limitation from opposite ends (config-emit vs runtime restore).

## Connections

- **Depends on:** `internal/perf` and `internal/logmon` (live GPU/VRAM telemetry in `vram.go`); `gopkg.in/yaml.v3`.
- **Called by:** `llama-quartermaster.go` at startup — `EnsureConfig` regenerates the config before the router loads it, and `-watch-models`/reload paths use `CachedConfigHash`/`CurrentInputsHash` to detect changes. `internal/server/configapi.go` is the web-UI config API: it reads/writes sidecar overrides and settings, previews tunings via `EstimatePlan` + `ReadGgufMetadataCached`, and triggers `EnsureConfig` on save.
- Produces the YAML config consumed by `internal/config` / the router/scheduler; its `groups`/`listeners` output backs the fork's multi-listener + cross-port eviction features.
