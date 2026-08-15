package autogen

// Launch-command rendering: turns a sized profile (ctx, offload split, KV
// types) into the llama-server / sd-server / tts-server argv lines that land in
// the generated config. RenderSoloCmd is the same path with a ${PORT}
// placeholder, used by the UI's cogwheel preview and ad-hoc commands.

import (
	"fmt"
	"strings"
)

// isVLModel reports whether a model is a dedicated vision-language model (its
// core identity is image chat: Qwen2/3-VL, InternVL, CogVLM), as opposed to a
// text LLM that merely ships an mmproj sidecar for optional vision. Only the
// former gets its vision twin's ctx capped to VisionCtx. VL archs carry "vl" in
// their gguf arch (qwen2vl/qwen3vl/qwen3vlmoe) and such models are conventionally
// named "*-VL"; a plain LLM-with-vision (gemma3, mllama, mistral) matches neither.
func isVLModel(name, arch string) bool {
	return strings.Contains(strings.ToLower(name), "vl") ||
		strings.Contains(strings.ToLower(arch), "vl")
}

// cmdPath renders a filesystem path as ONE cmd argument. NOT `%q`: the emitted
// cmd is re-split by shlex (Windows rules on Windows), where a backslash inside
// double quotes is a literal separator — so Go's escaping turned D:\Models\x
// into D:\\Models\\x and llama-server failed to open it. Separators are
// normalized to "/" (accepted by Windows too, and what slotKvPath already does),
// and quotes are kept for paths with spaces.
func cmdPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return `"` + strings.ReplaceAll(p, `"`, "") + `"` // a `"` can't appear in a real path
}

// qwenFixedChatTemplateFile is the server-cwd-relative path to froggeric's
// community chat-template fix for Qwen 3.5/3.6 (Apache-2.0, inherited from
// Qwen; see templates/CREDITS.md). The official Qwen 3.5/3.6 templates
// mutate already-rendered history on every turn, so llama.cpp's prefix
// cache never matches and the whole prompt reprocesses each request; this
// drop-in template renders history deterministically instead.
const qwenFixedChatTemplateFile = "templates/qwen-fixed-chat-template.jinja"

// needsQwenFixedChatTemplate reports whether a model should get
// qwenFixedChatTemplateFile instead of its baked-in gguf template.
//
// Arch alone can't decide this. llama.cpp has not split out a separate arch per
// Qwen minor: 3.5, 3.6 and 3.8 ggufs all report "qwen35" (dense) or "qwen35moe"
// (MoE) — verified against real local ggufs, since none are exercised by
// real_models_test.go's fixture set. But 3.8 fixed the history mutation
// upstream: its template preserves prior-turn <think> by default
//
//	{%- if preserve_thinking is undefined or preserve_thinking is true ...
//
// where 3.5/3.6 opt in instead ("preserve_thinking is defined and ..."), so 3.8
// already renders history deterministically and needs no override. Overriding it
// anyway is not free: the drop-in template has no reasoning_effort support, so
// 3.8's low/medium/xhigh effort levels are silently dropped.
//
// Hence: match the arch family, then defer to what the baked template actually
// does. An unrecognised template (no preserve_thinking logic at all) keeps the
// override — the pre-existing, safe behaviour.
func needsQwenFixedChatTemplate(meta Metadata) bool {
	switch strings.ToLower(meta.Architecture) {
	case "qwen35", "qwen35moe":
		return !meta.ChatTemplatePreservesThinking
	default:
		return false
	}
}

// smallCardVramGB is the budget below which a high-ctx profile falls back to
// ub=512. The ub=1024 compute buffer (~0.5 GB larger) only fails to fit on a
// genuinely small card at 64k+; bigger cards keep ub=1024 everywhere (bench:
// ub=512 costs ~52% prefill vs 1024).
const smallCardVramGB = 12.0

// effectiveUb picks the physical batch. Default 1024. Drop to 512 at high ctx
// ONLY when the VRAM budget is small (< smallCardVramGB) — the auto-sized solo
// profile (Ctx=0) learns its real ctx after sizing, so ctx>=longCtxThreshold is
// checked alongside prof.IsLong. budgetGB<=0 (unknown) keeps 1024 (prefer
// speed). An explicit Ub override always wins.
func effectiveUb(prof profile, ov *Override, ctx int, budgetGB float64) int {
	ub := 1024
	if (prof.IsLong || ctx >= longCtxThreshold) && budgetGB > 0 && budgetGB < smallCardVramGB {
		ub = 512
	}
	if ov != nil && ov.Ub > 0 {
		ub = ov.Ub
	}
	if prof.Ub > 0 {
		ub = prof.Ub
	}
	return ub
}

// buildCmdLines returns the launch command as a list of flag lines (no leading
// indentation), shared by emitProfile (which writes them as a YAML `cmd: >`
// block) and RenderSoloCmd (which joins them for the editor preview). Any
// Override.ExtraArgs are appended verbatim as a final line.
func buildCmdLines(s Settings, meta Metadata, row GgufRow, prof profile, ctx, ngl, ncpuMoe int, kvK, kvV string, kvInRam bool, ov *Override) []string {
	cpuMoeFlag := ""
	if ncpuMoe > 0 {
		cpuMoeFlag = fmt.Sprintf(" --n-cpu-moe %d", ncpuMoe)
	}

	// mmap only helps when some weights live on the CPU: mmap gives lazy
	// demand-paging + natural OS page-caching of the on-CPU tensors. With NO CPU
	// offload the weights are copied straight into VRAM, so mmap only leaves a
	// redundant file-backed copy in host page cache — waste. So default to
	// --no-mmap and only keep mmap when CPU offload is actually happening:
	//   - --n-cpu-moe > 0 (experts on CPU), or
	//   - partial layer offload (ngl <= blocks => some layers on CPU).
	// Unknown block count (parse miss) assumes GPU-resident => --no-mmap.
	// Explicit Mmap:"on"/"off" always wins over this default.
	cpuOffload := ncpuMoe > 0 || (meta.BlockCount > 0 && int64(ngl) <= meta.BlockCount)
	noMmapFlag := ""
	if !cpuOffload {
		noMmapFlag = "--no-mmap "
	}
	if ov != nil && ov.Mmap == "off" {
		noMmapFlag = "--no-mmap "
	} else if ov != nil && ov.Mmap == "on" {
		noMmapFlag = ""
	}
	mlockFlag := ""
	if ov != nil && ov.Mlock {
		mlockFlag = "--mlock "
	}
	kvoFlag := ""
	if kvInRam {
		kvoFlag = "--no-kv-offload "
	}

	fa := "on"
	if ov != nil && ov.FlashAttn != "" {
		fa = ov.FlashAttn
	}
	// One slot by default. Multi-agent harnesses (Qwen Code = main + memory subagent)
	// would benefit from 2 slots to avoid preamble thrash, but our disk save/restore
	// hardcodes /slots/0 — with 2 slots, restores race the other slot's live generation
	// (preamble-restore errors) and may target the wrong slot. Per-model override still
	// available for anyone who wants more slots and accepts that tradeoff.
	parallel := 1
	if ov != nil && ov.Parallel > 0 {
		parallel = ov.Parallel
	}
	threads := s.Threads
	if ov != nil && ov.Threads > 0 {
		threads = ov.Threads
	}

	ub := effectiveUb(prof, ov, ctx, s.TargetVramGB)
	// -b (logical batch) decoupled from -ub (physical/compute tile). A larger logical
	// batch pipelines more ub-micro-batches per decode(), overlapping CPU expert-fetch
	// with GPU compute on MoE offload — measured +38% pp at ub1024, +20% at ub512, for
	// ZERO extra VRAM (the compute buffer is sized by ub, not b; b only sizes the
	// token/pos arrays). 2048 is the plateau (bench: b past 2048 flat). Clamp >=ub and
	// <=ctx. ponytail: fixed 2048, expose a per-model b override only if a model wants
	// less pipelining depth.
	bTok := 2048
	if bTok < ub {
		bTok = ub
	}
	if bTok > ctx {
		bTok = ctx
	}

	spec := effectiveSpec(meta, ov, row.DraftKind)
	if prof.Spec != "" {
		spec = prof.Spec
	}

	reason := prof.ReasoningFmt
	if reason == "" && ov != nil {
		reason = ov.ReasoningFmt
	}
	rfmt := "auto"
	reasoningFlag := ""
	switch {
	case reason == "off":
		rfmt = "none"
		reasoningFlag = " --reasoning off"
	case reason != "":
		rfmt = reason
	}

	modelPath := strings.ReplaceAll(row.FullPath, "\\", "/")

	lines := []string{
		s.ServerExe,
		fmt.Sprintf("-m %s", modelPath),
		"--port ${PORT}",
		"--host 127.0.0.1",
		fmt.Sprintf("-ngl %d", ngl),
		fmt.Sprintf("-c %d", ctx),
		fmt.Sprintf("-ub %d -b %d", ub, bTok),
		fmt.Sprintf("-fa %s -ctk %s -ctv %s", fa, kvK, kvV),
		fmt.Sprintf("--parallel %d %s%s%s--kv-unified --no-warmup --no-webui --metrics --props", parallel, noMmapFlag, mlockFlag, kvoFlag),
	}
	// Vision twin loads the projector for image input.
	if prof.Vision && row.MmprojPath != "" {
		lines = append(lines, fmt.Sprintf("--mmproj %s", strings.ReplaceAll(row.MmprojPath, "\\", "/")))
	}
	// spec is a "+"-joined list of backends (draft-mtp / draft-dflash / ngram-map-k4v
	// / ngram-mod / none); llama-server accepts them chained, so emit one --spec-type
	// each and the sub-knobs for whichever backends are present.
	for _, st := range strings.Split(spec, "+") {
		lines = append(lines, fmt.Sprintf("--spec-type %s", st))
	}
	// draft-mtp and draft-dflash both drive off a draft model checked each step
	// against the main model, so share the -md/-ngld wiring; only the sane
	// default draft length differs. DFlash proposes a whole diffusion block per
	// pass so it tolerates a longer chain than single-token MTP, but 5 is the
	// measured sweet spot on Qwen3.6-35B-A3B (own n-max sweep, 3/4/5/6): reasoning
	// tg jumps ~15% at n=5 vs n=3/4 and ties n=6, while n=5 also edges out n=6 on
	// creative tg — higher (6+, 12/15) over-drafts, accept rate falls faster than
	// length rises, so the wasted GPU verify compute makes TG worse.
	if isDraftSpec := specHas(spec, "draft-mtp") || specHas(spec, "draft-dflash"); isDraftSpec {
		nmax := 2
		if specHas(spec, "draft-dflash") {
			nmax = 5
		}
		if ov != nil && ov.SpecDraftNMax > 0 {
			nmax = ov.SpecDraftNMax
		}
		lines = append(lines, fmt.Sprintf("--spec-draft-n-max %d", nmax))
		// A separate draft file (MTP sidecar like Gemma-4, or any DFlash drafter —
		// DFlash has no baked-in variant, it's always separate): baked-in MTP
		// models need no -md. Only attach the file when its kind matches the active
		// draft backend — a draft-dflash gguf must NOT be loaded as a draft-mtp
		// draft (arch mismatch: the sidecar is arch=dflash) and vice versa; a
		// baked-in MTP layer (DraftKind=="") correctly emits no -md. Pin the draft
		// fully to VRAM (-ngld 99): spec decode is serial (draft proposes, main
		// verifies each step), so a CPU-resident draft stalls the GPU main model
		// every round. DraftSizeGB charges this via draftOverheadGB.
		mdMatches := (specHas(spec, "draft-mtp") && row.DraftKind == "mtp") ||
			(specHas(spec, "draft-dflash") && row.DraftKind == "dflash")
		if row.DraftPath != "" && mdMatches {
			lines = append(lines, fmt.Sprintf("-md %s", strings.ReplaceAll(row.DraftPath, "\\", "/")))
			lines = append(lines, "-ngld 99")
			// Draft KV quant (-ctkd/-ctvd). "" => llama's f16 default; a matched
			// quant here shrinks the resident draft's KV VRAM (fa is global, so the
			// same flash-attn that gates main quant KV covers the draft too).
			if ov != nil && ov.KvKDraft != "" {
				lines = append(lines, fmt.Sprintf("-ctkd %s", ov.KvKDraft))
			}
			if ov != nil && ov.KvVDraft != "" {
				lines = append(lines, fmt.Sprintf("-ctvd %s", ov.KvVDraft))
			}
		}
	}
	if specHas(spec, "ngram-map-k4v") && ov != nil {
		if ov.SpecDefault {
			lines = append(lines, "--spec-default")
		}
		if ov.SpecNgramSizeN > 0 {
			lines = append(lines, fmt.Sprintf("--spec-ngram-map-k4v-size-n %d", ov.SpecNgramSizeN))
		}
		if ov.SpecNgramSizeM > 0 {
			lines = append(lines, fmt.Sprintf("--spec-ngram-map-k4v-size-m %d", ov.SpecNgramSizeM))
		}
		if ov.SpecNgramMinHits > 0 {
			lines = append(lines, fmt.Sprintf("--spec-ngram-map-k4v-min-hits %d", ov.SpecNgramMinHits))
		}
	}
	lines = append(lines, fmt.Sprintf("--jinja --reasoning-format %s%s", rfmt, reasoningFlag))
	// Cap thinking tokens when a budget is set and reasoning is on.
	if ov != nil && ov.ReasoningBudget > 0 && reason != "off" {
		lines = append(lines, fmt.Sprintf("--reasoning-budget %d", ov.ReasoningBudget))
	}
	// preserve_thinking keeps prior-turn <think> in history (Qwen3.6+); pointless
	// when thinking is off. Escaped double-quotes survive both Windows + POSIX shlex.
	if ov != nil && ov.PreserveThinking && reason != "off" {
		lines = append(lines, `--chat-template-kwargs "{\"preserve_thinking\":true}"`)
	}
	// Always emit so runtime matches our reserve (else llama-server defaults to 32).
	ckptConstGB := GetKvCostModel(meta, kvK, kvV).ConstGB
	ckptRecurrent := meta.FullAttnInterval > 0
	ckpts := effectiveCtxCheckpoints(prof, defaultCtxCheckpoints(ckptConstGB, ckptRecurrent))
	lines = append(lines, fmt.Sprintf("--ctx-checkpoints %d", ckpts))
	// Spacing is charged per checkpoint by checkpointReserveGB, so emit whatever it
	// assumed. An ov-only pin (a preview profile built without the field) still
	// wins over the arch default.
	if ckpts > 0 {
		stepProf := prof
		if stepProf.CheckpointMinStep == 0 && ov != nil {
			stepProf.CheckpointMinStep = ov.CheckpointMinStep
		}
		if step := effectiveCheckpointMinStep(stepProf, ckptConstGB, ckptRecurrent); step != checkpointMinStep {
			lines = append(lines, fmt.Sprintf("-cms %d", step))
		}
	}
	// DRY sampler: defaults to settings.DryDefault (nil => off) and a per-model
	// ov.Dry wins (nil => the default, false => off, true => on). Values default
	// to 0.8 / 1.75 / 3; a non-zero override replaces each independently.
	dryOn := s.DryDefault != nil && *s.DryDefault
	if ov != nil && ov.Dry != nil {
		dryOn = *ov.Dry
	}
	if dryOn {
		mult, base, allow := 0.8, 1.75, 3
		if ov != nil {
			if ov.DryMultiplier != 0 {
				mult = ov.DryMultiplier
			}
			if ov.DryBase != 0 {
				base = ov.DryBase
			}
			if ov.DryAllowedLength != 0 {
				allow = ov.DryAllowedLength
			}
		}
		lines = append(lines, fmt.Sprintf("--dry-multiplier %g --dry-base %g --dry-allowed-length %d", mult, base, allow))
	}
	lines = append(lines, fmt.Sprintf("-t %d%s", threads, cpuMoeFlag))
	// Slot KV persistence: expose llama-server's save/restore slot endpoints. Path
	// is quoted (it lives under a per-user dir that may contain spaces) and matches
	// the emitted slotCache.path the server's LRU uses. Per-model SlotCache defaults
	// on (nil), so the master switch alone enables every model; a model opts out
	// with slotCache:false.
	if s.SlotCache.Enable && (ov == nil || ov.SlotCache == nil || *ov.SlotCache) {
		lines = append(lines, fmt.Sprintf("--slot-save-path %q", slotKvPath(s.SlotCache)))
	}
	// A user-supplied template always wins over the arch-derived built-in fix.
	// Quoted: user paths routinely contain spaces.
	if ov != nil && strings.TrimSpace(ov.ChatTemplateFile) != "" {
		lines = append(lines, "--chat-template-file "+cmdPath(ov.ChatTemplateFile))
	} else if needsQwenFixedChatTemplate(meta) {
		lines = append(lines, fmt.Sprintf("--chat-template-file %s", qwenFixedChatTemplateFile))
	}
	// Advanced / power-user knobs. Each is gated on a set override field (zero/
	// empty => omit), so a model with none of them set emits exactly as before.
	if ov != nil {
		if ov.ThreadsBatch > 0 {
			lines = append(lines, fmt.Sprintf("-tb %d", ov.ThreadsBatch))
		}
		if ov.Prio > 0 {
			lines = append(lines, fmt.Sprintf("--prio %d", ov.Prio))
		}
		if ov.DirectIo {
			lines = append(lines, "-dio")
		}
		if ov.NoOpOffload {
			lines = append(lines, "--no-op-offload")
		}
		if ov.NoRepack {
			lines = append(lines, "--no-repack")
		}
		if ov.CacheReuse > 0 {
			lines = append(lines, fmt.Sprintf("--cache-reuse %d", ov.CacheReuse))
		}
		if ov.CacheRamMB > 0 {
			lines = append(lines, fmt.Sprintf("-cram %d", ov.CacheRamMB))
		}
		switch ov.CacheIdleSlots {
		case "on":
			lines = append(lines, "--cache-idle-slots")
		case "off":
			lines = append(lines, "--no-cache-idle-slots")
		}
		if ov.SwaFull {
			lines = append(lines, "--swa-full")
		}
		switch ov.ContextShift {
		case "on":
			lines = append(lines, "--context-shift")
		case "off":
			lines = append(lines, "--no-context-shift")
		}
		if ov.SpecDraftNMin > 0 {
			lines = append(lines, fmt.Sprintf("--spec-draft-n-min %d", ov.SpecDraftNMin))
		}
		if ov.SlotPromptSimilarity > 0 {
			lines = append(lines, fmt.Sprintf("-sps %g", ov.SlotPromptSimilarity))
		}
		if ov.RopeScaling != "" {
			lines = append(lines, fmt.Sprintf("--rope-scaling %s", ov.RopeScaling))
		}
		if ov.RopeScale > 0 {
			lines = append(lines, fmt.Sprintf("--rope-scale %g", ov.RopeScale))
		} else if ropeExtends(ov.RopeScaling) {
			// Derive the factor from the ctx actually chosen. Emitting the type
			// alone leaves llama.cpp on the model's own factor (1.0 unless the
			// publisher fine-tuned for extension), i.e. a longer window over
			// untrained positions.
			if f := ropeFactor(meta, ctx); f > 0 {
				lines = append(lines, fmt.Sprintf("--rope-scale %g", f))
			}
		}
		if ov.RopeFreqBase > 0 {
			lines = append(lines, fmt.Sprintf("--rope-freq-base %g", ov.RopeFreqBase))
		}
		if ov.YarnOrigCtx > 0 {
			lines = append(lines, fmt.Sprintf("--yarn-orig-ctx %d", ov.YarnOrigCtx))
		}
		if ov.SplitMode != "" {
			lines = append(lines, fmt.Sprintf("-sm %s", ov.SplitMode))
		}
		if ov.TensorSplit != "" {
			lines = append(lines, fmt.Sprintf("-ts %s", ov.TensorSplit))
		}
		if ov.MainGpu > 0 {
			lines = append(lines, fmt.Sprintf("-mg %d", ov.MainGpu))
		}
		if ov.OverrideTensor != "" {
			lines = append(lines, fmt.Sprintf("-ot %s", ov.OverrideTensor))
		}
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// RenderSoloCmd previews the full launch command for a candidate override,
// reusing the solo-profile sizer so the editor's launch-parameters box matches
// what a save would emit. Returns the command on one line (with `${PORT}` intact).
func RenderSoloCmd(s Settings, meta Metadata, row GgufRow, ov Override) (string, error) {
	// SAM models render a sam3_server command (no metadata; matched by IsSam).
	if row.IsSam {
		return strings.Join(samCmdLines(s, row, &ov), " "), nil
	}
	// Diffusion models render an sd-server command, not a llama-server one.
	if imgArch := effectiveImageArch(meta); isImageArch(imgArch) {
		lines, _, _, _ := imageCmdLines(s, row, &ov, imgArch, row.FullPath)
		return strings.Join(lines, " "), nil
	}
	// Embedders render a minimal --embeddings command (no KV/spec sizing).
	if IsEmbeddingModel(meta) {
		return strings.Join(embeddingCmdLines(s, row, &ov, meta), " "), nil
	}
	// Speech models render a tts-server command: qwentts (talker + paired codec)
	// or TTS.cpp (--model-path), whichever engine the model resolves to.
	if IsTTSModel(meta, row.FileName) {
		return strings.Join(ttsCmdLines(s, row, &ov, meta), " "), nil
	}
	// Parakeet ASR models render a parakeet-server command (model + port only).
	if IsASRModel(meta, row.FileName) {
		return strings.Join(asrCmdLines(s, row, &ov), " "), nil
	}
	// vllm-backed LLMs render a vllm command; a chosen llama build swaps the exe.
	be := resolveBackend(s, &ov, "llm")
	if strings.EqualFold(be.Kind, "vllm") {
		// Same refusal the emitter makes: vllm loads only the shard it is given,
		// so there is no command to preview for a split set.
		if isSplitGguf(row) {
			return "", fmt.Errorf("vllm cannot load split gguf shards; merge %s with llama-gguf-split --merge or pick a llama backend", row.FileName)
		}
		return strings.Join(vllmCmdLines(s, row, &ov, row.FullPath, be, meta), " "), nil
	}
	if be.Exe != "" {
		s.ServerExe = be.Exe
	}
	// Same default as emitModel — this used to hardcode q8_0 with no MoE branch,
	// so the editor previewed a KV type the emitted config never used.
	soloTarget := s.TargetVramGB
	if ov.VramTargetGB > 0 {
		soloTarget = ov.VramTargetGB
	}
	kvDef := defaultKvQuant(s, meta, soloTarget, s.VramOverheadGB+draftOverheadGB(effectiveSpec(meta, &ov, row.DraftKind), row.DraftSizeGB))
	kvK, kvV := kvDef, kvDef
	if ov.KvK != "" {
		kvK = ov.KvK
	}
	if ov.KvV != "" {
		kvV = ov.KvV
	}
	if !ValidKvPair(kvK, kvV) {
		kvK, kvV = kvDef, kvDef
	}
	perTokGB, kvConstGB := 0.0, 0.0
	if m := GetKvCostModel(meta, kvK, kvV); m.OK {
		perTokGB, kvConstGB = m.SlopeGB, m.ConstGB
	}
	// Rope scaling lifts the trained-length ceiling; without it this is nativeCtx.
	modelMax := ropeCeiling(meta, ov.RopeScaling, ov.Ctx)
	target := s.TargetVramGB
	if ov.VramTargetGB > 0 {
		target = ov.VramTargetGB
	}
	specOh := draftOverheadGB(effectiveSpec(meta, &ov, row.DraftKind), row.DraftSizeGB)
	prof := profile{
		Name:           "preview",
		Target:         target,
		Overhead:       s.VramOverheadGB + specOh,
		Ctx:            ov.Ctx,
		CpuOffload:     ov.CpuOffload,
		KvK:            kvK,
		KvV:            kvV,
		IsLong:         ov.Ctx >= longCtxThreshold,
		CtxCheckpoints: ov.CtxCheckpoints,

		CheckpointMinStep: ov.CheckpointMinStep,
	}
	prof.Overhead += computeBufferGB(meta, effectiveUb(prof, &ov, prof.Ctx, s.TargetVramGB), s.ComputeBufFactor)
	ctx, plan, kvReserve, _, err := sizeProfile(meta, s, prof, perTokGB, kvConstGB, modelMax, ov.KvInRam)
	if err != nil {
		return "", err
	}
	ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
	if ov.CpuOffload > 0 {
		ngl, ncpuMoe = applyForcedOffload(meta, ov.CpuOffload)
	}
	return strings.Join(buildCmdLines(s, meta, row, prof, ctx, ngl, ncpuMoe, kvK, kvV, ov.KvInRam, &ov), " "), nil
}

// specHas reports whether a "+"-joined spec list contains backend b.
func specHas(spec, b string) bool {
	for _, s := range strings.Split(spec, "+") {
		if s == b {
			return true
		}
	}
	return false
}

// formatCtxTag renders a short ctx tag: 8192->"8k", 131072->"128k", 1048576->"1m".
// effectiveSpec resolves the spec-type: MTP-capable models (baked-in nextn
// layer or a paired mtp-*.gguf sidecar) default to draft-mtp+ngram-mod (the
// chain beats mtp alone), everything else to ngram-mod. A paired DFlash drafter
// is never auto-selected — see below.
// An explicit override spec wins (set spec: "ngram-mod" to force ngram on a
// drafted model, or spec: "draft-dflash" to opt into DFlash).
//
// A DFlash sidecar sitting in the model's dir is a deliberate choice (the user
// downloaded it) but is NOT auto-selected: it wins a short flat-prompt bench
// (GPU-bound: +17% vs mtp) but its resident draft weights + own full-context KV
// crowd an already-tight VRAM budget over a long real session, cratering to a
// GPU-mem-oversubscription cliff mtp never hits (mtp is a baked-in head with no
// separate weights or KV). Confirmed in real multi-turn agent use on
// Qwen3.6-35B-A3B-100k: switching qwen3.6-35b-a3b-ud-q4_k_s-100k off dflash back
// to mtp fixed a production slowdown. An explicit `spec: draft-dflash` override
// still works — this only removes it as the automatic pick.
func effectiveSpec(meta Metadata, ov *Override, draftKind string) string {
	// Explicit override always wins.
	if ov != nil && ov.Spec != "" {
		return ov.Spec
	}
	switch {
	case meta.IsMTP || draftKind == "mtp":
		// draft-mtp CHAINED with model-less ngram-mod: benched better than mtp
		// alone. The MTP head drafts short high-confidence continuations; ngram-mod
		// fills the gaps mtp misses (verbatim repeats — code, quoted context, tool
		// echoes) at zero extra VRAM (no draft weights/KV). llama-server verifies
		// both proposals each step, so the union lifts accept rate over either solo.
		return "draft-mtp+ngram-mod"
	}
	return "ngram-mod"
}
