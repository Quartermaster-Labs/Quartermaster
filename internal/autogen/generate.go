package autogen

import (
	"fmt"
	"sort"
	"strings"
)

// genMoeShare is Generate-Config.ps1's own expert-weight share table (line 190).
// It differs from the planner's table (plan.go) by including qwen35moe; the
// generation-side ctx sizing and the force-low-active-MoE recompute use this one.
var genMoeShare = map[string]float64{
	"gemma4":    0.88,
	"qwen3":     0.90,
	"qwen3moe":  0.90,
	"qwen35moe": 0.90,
	"llama":     0.80,
	"lfm2":      0.78,
	"lfm2moe":   0.78,
}

// genMoeShareFor mirrors Generate-Config's Get-MoeShare (default 0.85).
func genMoeShareFor(arch string) float64 {
	if s, ok := genMoeShare[strings.ToLower(arch)]; ok {
		return s
	}
	return 0.85
}

// profile is one emitted quartermaster entry for a model: the solo variant or an
// optional ctx-tier variant. Target/Overhead are the VRAM budget the sizing math
// uses; the flags drive ub/spec/reasoning.
type profile struct {
	Name     string
	Target   float64
	Overhead float64
	Unlisted bool
	Ctx      int // 0 = auto-size; >0 forces that ctx via the manual-cap path
	// IsLong marks a profile whose context is pinned at or above
	// longCtxThreshold (a ctx tier, a long named variant, or a long Ctx on the
	// model itself). It tops the VRAM budget's safety slack up to
	// longCtxHeadroomGB (see longCtxTarget) and drops -ub to 512 on a
	// small-VRAM card.
	IsLong bool
	// Per-variant overrides. Empty/zero => inherit the model-wide override. Set
	// only by named custom variants (Override.Variants); emitProfile and the
	// kv-cost sizing prefer these over the model-wide values.
	KvK, KvV     string
	Spec         string
	ReasoningFmt string
	Ub           int // physical batch size override (0 => default)
	CpuOffload   int // >0 pins layers offloaded to CPU, overriding the sizer
	// CtxCheckpoints, when non-nil, emits --ctx-checkpoints N (0 disables the KV
	// prompt-prefix checkpoint cache). nil => inherit the model-wide value, else
	// the llama-server default (32). See effectiveCtxCheckpoints.
	CtxCheckpoints *int
	// CheckpointMinStep, when > 0, is the resolved -cms (checkpoint spacing in
	// prompt tokens) for this profile. 0 => the arch default from
	// defaultCheckpointMinStep. Both the emitted flag and the VRAM reserve read
	// it, so they can't drift apart.
	CheckpointMinStep int
	// Variant, when non-nil, is the named-variant source this profile was built
	// from. Its engine knobs (kvInRam/flash/mmap/mlock/threads/parallel/extraArgs)
	// layer over the model-wide override at emit so a variant carries the full
	// launch shape. Solo/ctx-tier profiles leave this nil and use the override.
	Variant *VariantSpec
	// Vision marks the auto-generated "-vision" twin: emits --mmproj <projector>
	// and an image-input capabilities block so the playground can attach images.
	Vision bool
	// MmprojGB is the projector footprint (weights + CLIP compute reserve) this
	// vision twin charges as VRAM overhead. Kept separate from Overhead so the
	// sizer can price the twin a second time with the projector on the CPU.
	MmprojGB float64
	// MmprojPin is the model override's explicit placement for the projector
	// ("gpu"/"ram"); "" leaves the sizer's auto fallback in charge.
	MmprojPin string
	// CpuMmproj emits --no-mmproj-offload: the CLIP projector runs on the CPU, so
	// it costs no VRAM and the twin keeps the ctx/layer placement it would have
	// had without vision, at the price of a slow (host-side) image encode. Set by
	// the sizer only when the GPU-resident projector actually costs placement or
	// a quarter of the context window (see cpuMmprojWins).
	CpuMmproj bool
}

// Generate discovers models under gf.Settings.ModelsRoot and returns a complete
// quartermaster config YAML. Port of Generate-Config.ps1. nowRFC is stamped into
// the header comment (passed in so the function stays deterministic/testable).
func Generate(gf GenerateFile, nowRFC string) (string, error) {
	s := gf.Settings
	rows, err := DiscoverGgufModelsMulti(s.RootList())
	if err != nil {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ID != rows[j].ID {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Publisher < rows[j].Publisher
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# Quartermaster config - generated %s\n", nowRFC)
	fmt.Fprintf(&b, "# TargetVramGB=%g  MaxRamGB=%g  Threads=%d\n", s.TargetVramGB, s.MaxRamGB, s.Threads)
	b.WriteString("# Regen: Quartermaster startup (hash-gated)\n\n")
	fmt.Fprintf(&b, "healthCheckTimeout: %d\n\n", s.HealthCheckTimeout)
	emitVramBudget(&b, s)
	emitSlotCache(&b, s.SlotCache)
	emitAPIKeys(&b, s.APIKeys)
	b.WriteString("models:\n")

	var emitted []string
	var coexist coexistSets
	seen := map[string]bool{}

	for _, row := range rows {
		ov := ResolveOverride(row, gf.Overrides)
		if ov != nil && ov.Skip {
			continue
		}
		name := row.ID
		if seen[name] {
			pubTag := slugify(row.Publisher)
			name = fmt.Sprintf("%s-%s", row.ID, pubTag)
		}
		seen[name] = true

		// A single unparseable/misdetected gguf must not nuke the whole config
		// (and with it startup): note it in-band and skip, keeping every other
		// model servable.
		if err := emitModel(&b, s, gf, row, ov, name, &emitted, &coexist); err != nil {
			fmt.Fprintf(&b, "\n  # SKIPPED %q: %v\n", name, err)
			continue
		}
		if row.IsSam {
			coexist.Sam = append(coexist.Sam, name)
		}
	}

	emitExtraImageModels(&b, s, gf.Overrides, seen, &emitted)

	emitGroupsAndListeners(&b, s, emitted, coexist)
	return b.String(), nil
}

// emitModel reads metadata once and emits every profile (solo, ctx tiers, game)
// for one discovered gguf.
func emitModel(b *strings.Builder, s Settings, gf GenerateFile, row GgufRow, ov *Override, name string, emitted *[]string, coexist *coexistSets) error {
	// SAM models are raw *.ggml with no gguf header — route them before the
	// metadata read (ReadGgufMetadata would fail). Served by sam3_server.
	if row.IsSam {
		emitSamModel(b, s, row, ov, name, emitted)
		return nil
	}

	meta, err := ReadGgufMetadataCached(row.FullPath)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	// Diffusion GGUFs go to sd-server, not llama-server: no KV cache / -ngl
	// sizing applies. Detect by arch and emit a separate block. Unknown image
	// archs fall through to the llama path, where the YAML "# arch=..." comment
	// reveals the real arch name to add to imageArchs.
	imgArch := effectiveImageArch(meta)
	if isImageArch(imgArch) {
		emitImageModel(b, s, row, ov, name, imgArch, meta.HasBakedEncoders, emitted)
		// Named variants are emitted as separate "<name>-<variant>" sd-server
		// entries. Each inherits the model's component paths/placement and overrides
		// only its own generation preset. Fleet-wide defaultVariants are llama-shaped
		// (game/judge) and intentionally skipped for image models.
		if ov != nil {
			for _, v := range ov.Variants {
				if strings.TrimSpace(v.Name) == "" {
					continue
				}
				vov := mergeImageVariant(*ov, v)
				emitImageModel(b, s, row, &vov, name+"-"+v.Name, imgArch, meta.HasBakedEncoders, emitted)
			}
		}
		return nil
	}

	// Text embedders (BERT-family or any model baking a pooling_type) go to
	// llama-server with --embeddings: no KV/ctx-tier/spec sizing, and the
	// embedding capability buckets them out of the chat-able LLM catalog.
	if IsEmbeddingModel(meta) {
		emitEmbeddingModel(b, s, row, ov, name, meta, emitted)
		return nil
	}

	// Speech GGUFs go to a TTS server (OpenAI /v1/audio/speech), not llama-server:
	// a Qwen3-TTS "talker" emits audio-codec tokens and loads with a paired codec
	// gguf (qwentts.cpp), a TTS.cpp export is self-contained. Detect ahead of the
	// LLM path — a talker is a small qwen3 LM that would otherwise route to chat.
	// emitTTSModel picks the engine from the registry (see ttsBackend).
	if IsTTSModel(meta, row.FileName) {
		emitTTSModel(b, s, row, ov, name, meta, emitted)
		// TTS.cpp runs on the CPU and is charged no VRAM, so it belongs in the
		// coexistence group rather than the exclusive one: speaking a reply must
		// not evict the chat model that produced it. qwentts is on the GPU and
		// stays exclusive.
		if kind, _ := ttsBackend(s, row, ov, meta); kind == ttsKindTTSCpp {
			coexist.TTS = append(coexist.TTS, name)
		}
		return nil
	}

	// Parakeet-family ASR GGUFs go to parakeet.cpp's parakeet-server (OpenAI
	// /v1/audio/transcriptions), not llama-server: transducer encoder+decoder, no
	// KV/ctx/spec sizing, and the audio->text capability buckets them out of the
	// chat-able LLM catalog.
	if IsASRModel(meta, row.FileName) {
		emitASRModel(b, s, row, ov, name, meta.Architecture, emitted)
		// Same reasoning as TTS.cpp above: parakeet runs on the CPU (20-36x
		// realtime) and is charged no VRAM, so dictating must not evict the chat
		// model the transcript is headed for. A GPU opt-in via extraArgs keeps
		// coexisting — the same accepted under-charge emitASRModel already makes
		// for estVramGB.
		coexist.ASR = append(coexist.ASR, name)
		return nil
	}

	// Resolve which backend serves this LLM. A "vllm" (or other non-llama) kind
	// emits through its own path — different engine, different args, no KV/-ngl
	// sizing. A chosen "llama" build just swaps the exe threaded through the llama
	// path below. No registry match => keep the legacy ServerExe (zero behaviour
	// change for single-backend setups).
	be := resolveBackend(s, ov, "llm")
	if strings.EqualFold(be.Kind, "vllm") {
		emitVllmModel(b, s, row, ov, name, be, meta, emitted)
		return nil
	}
	if be.Exe != "" {
		s.ServerExe = be.Exe // this model's chosen llama build wins (local copy)
	}

	// Charge the draft sidecar only when this spec chain actually attaches it as
	// -md (matchedDraftSizeGB mirrors the emitter's kind gate). A dir pairing a
	// DFlash drafter to a model that runs on its baked-in MTP head must fall back
	// to the flat baked-in charge, not the drafter's weights.
	modelSpec := effectiveSpec(meta, ov, row.DraftKind)
	specOh := draftOverheadGB(modelSpec, matchedDraftSizeGB(modelSpec, row.DraftKind, row.DraftSizeGB))

	// KV quant: f16 unless it can't buy denseMinCtx in this model's budget, in
	// which case q8_0 (see defaultKvQuant); settings.kvQuant pins it outright.
	// Per-model override still wins. Fast flash-attention needs matched K/V and
	// never iq4_nl.
	kvTarget := s.TargetVramGB
	if ov != nil && ov.VramTargetGB > 0 {
		kvTarget = ov.VramTargetGB
	}
	kvDef := defaultKvQuant(s, meta, kvTarget, s.VramOverheadGB+specOh)
	ovKvK, ovKvV := "", ""
	if ov != nil {
		ovKvK, ovKvV = ov.KvK, ov.KvV
	}
	kvK, kvV := resolveKvPair(ovKvK, ovKvV, kvDef, kvDef)

	kvModel := GetKvCostModel(meta, kvK, kvV)
	perTokGB := 0.0
	kvConstGB := 0.0
	if kvModel.OK {
		perTokGB = kvModel.SlopeGB
		kvConstGB = kvModel.ConstGB
	}
	modelMax := nativeCtx(meta)
	if ov != nil {
		// Rope scaling is the one knob that lifts the trained-length ceiling.
		modelMax = ropeCeiling(meta, ov.RopeScaling, ov.Ctx)
	}
	kvInRam := ov != nil && ov.KvInRam

	var ctxVariants []int
	override := Override{}
	if ov != nil {
		override = *ov
		ctxVariants = ov.CtxVariants
	}

	// Build the profile set: solo + optional ctx tiers.
	// Per-model VRAM budget: an override caps it below the fleet default.
	soloTarget := s.TargetVramGB
	if override.VramTargetGB > 0 {
		soloTarget = override.VramTargetGB
	}
	profiles := []profile{{
		Name:     name,
		Target:   soloTarget,
		Overhead: s.VramOverheadGB + specOh,
		Unlisted: override.Unlisted,
		Ctx:      override.Ctx,
		// A model pinned to a long ctx is a long profile like any tier — it earns
		// the same budget headroom (longCtxTarget) and the same -ub treatment. An
		// auto ctx (0) is not: the sizer picks the window against the full budget.
		IsLong:            override.Ctx >= longCtxThreshold,
		CpuOffload:        override.CpuOffload,
		CtxCheckpoints:    override.CtxCheckpoints,
		CheckpointMinStep: override.CheckpointMinStep,
	}}
	for _, cv := range ctxVariants {
		profiles = append(profiles, profile{
			Name: fmt.Sprintf("%s-%s", name, formatCtxTag(cv)),
			// Budget headroom for the long rungs is charged by longCtxTarget off
			// IsLong, so every long profile (and the editor preview) gets it.
			Target:            s.TargetVramGB,
			Overhead:          s.VramOverheadGB + specOh,
			Ctx:               cv,
			IsLong:            cv >= longCtxThreshold,
			CtxCheckpoints:    override.CtxCheckpoints,
			CheckpointMinStep: override.CheckpointMinStep,
		})
	}

	// Named custom variants: per-override ones plus the fleet-wide
	// settings.DefaultVariants, each emitting "<model>-<slug>" with its own
	// ctx/VRAM/kv/spec; zero fields inherit. Spec affects the VRAM overhead, so
	// bake the per-variant overhead here.
	variantSpecs := append(append([]VariantSpec{}, override.Variants...), s.DefaultVariants...)
	var visionSpec *VariantSpec
	for i := range variantSpecs {
		v := variantSpecs[i]
		if strings.TrimSpace(v.Name) == "" {
			continue
		}
		// "vision" is reserved: it tunes the auto-generated vision twin below in
		// place (ctx/vram/unlisted/kv/spec/…) instead of spawning its own profile.
		if strings.EqualFold(v.Name, "vision") {
			visionSpec = &variantSpecs[i]
			continue
		}
		vTarget := s.TargetVramGB
		if v.VramTargetGB > 0 {
			vTarget = v.VramTargetGB
		}
		// Variants inherit the model-wide spec chain unless they set their own,
		// so charge the draft overhead off the effective spec.
		vEffSpec := modelSpec
		if v.Spec != "" {
			vEffSpec = v.Spec
		}
		vSpecOh := draftOverheadGB(vEffSpec, matchedDraftSizeGB(vEffSpec, row.DraftKind, row.DraftSizeGB))
		// Standalone: a blank ctx-checkpoints uses the generator default, not the
		// model-wide override value.
		vCheckpoints := v.CtxCheckpoints
		// Spacing DOES inherit the model-wide value (matching the effOv merge at
		// emit): a blank variant -cms must not diverge from the emitted flag.
		vMinStep := v.CheckpointMinStep
		if vMinStep == 0 {
			vMinStep = override.CheckpointMinStep
		}
		profiles = append(profiles, profile{
			Name:              fmt.Sprintf("%s-%s", name, slugify(v.Name)),
			Target:            vTarget,
			Overhead:          s.VramOverheadGB + vSpecOh,
			Unlisted:          v.Unlisted,
			Ctx:               v.Ctx,
			IsLong:            v.Ctx >= longCtxThreshold,
			KvK:               v.KvK,
			KvV:               v.KvV,
			Spec:              v.Spec,
			ReasoningFmt:      v.ReasoningFmt,
			Ub:                v.Ub,
			CpuOffload:        v.CpuOffload,
			CtxCheckpoints:    vCheckpoints,
			CheckpointMinStep: vMinStep,
			Variant:           &variantSpecs[i],
		})
	}

	// Vision twin: a model shipping a sibling mmproj projector gets an extra
	// "-vision" profile that loads it (--mmproj) and declares image input. Listed
	// by default (a distinct served id, scopeable by API keys); the config editor's
	// reserved "vision" variant can still mark it unlisted. Charged the projector's
	// file size as flat VRAM overhead.
	// "none" drops the twin outright: a projector the user does not want wired
	// (a family-inherited one they judge wrong for this finetune, or vision they
	// simply never use) should cost no served id at all.
	if row.MmprojPath != "" && !strings.EqualFold(override.Mmproj, "none") {
		mmprojOh := MmprojVramGB(row.MmprojPath, row.MmprojSizeGB, s)
		vp := profile{
			Name:              fmt.Sprintf("%s-vision", name),
			Target:            soloTarget,
			Overhead:          s.VramOverheadGB + specOh + mmprojOh,
			MmprojGB:          mmprojOh,
			MmprojPin:         strings.ToLower(strings.TrimSpace(override.Mmproj)),
			Ctx:               override.Ctx,
			CheckpointMinStep: override.CheckpointMinStep,
			Vision:            true,
		}
		// Small text window only for dedicated VL models (Qwen-VL, InternVL, …):
		// their whole purpose is image chat, so the maxed 32k ctx (~2.5 GB KV) is
		// wasted. A plain LLM that merely ships an mmproj sidecar keeps full context
		// on its vision twin (ctx 0 → sizer picks the budget max, same as solo).
		// A model or vision-variant Ctx override still wins below.
		if vp.Ctx == 0 && isVLModel(name, meta.Architecture) {
			vp.Ctx = s.VisionCtx
		}
		// A reserved "vision" variant (from the config editor) tunes the twin in
		// place; blank fields keep the auto defaults, so an untouched seed emits
		// the same profile as before. Engine knobs layer over the model override
		// at emit via Variant.
		if v := visionSpec; v != nil {
			if v.VramTargetGB > 0 {
				vp.Target = v.VramTargetGB
			}
			if v.Ctx > 0 {
				vp.Ctx = v.Ctx
				vp.IsLong = v.Ctx >= longCtxThreshold
			}
			// Spec changes the draft overhead; recharge off the variant's spec.
			if v.Spec != "" {
				vp.Overhead = s.VramOverheadGB + draftOverheadGB(v.Spec, matchedDraftSizeGB(v.Spec, row.DraftKind, row.DraftSizeGB)) + mmprojOh
			}
			vp.Unlisted = v.Unlisted
			vp.KvK = v.KvK
			vp.KvV = v.KvV
			vp.Spec = v.Spec
			vp.ReasoningFmt = v.ReasoningFmt
			vp.Ub = v.Ub
			vp.CpuOffload = v.CpuOffload
			vp.CtxCheckpoints = v.CtxCheckpoints
			if v.CheckpointMinStep > 0 {
				vp.CheckpointMinStep = v.CheckpointMinStep
			}
			vp.Variant = v
			// The vision variant IS the twin, so its pin outranks the model-wide
			// one; blank keeps whatever Default set.
			if p := strings.ToLower(strings.TrimSpace(v.Mmproj)); p != "" {
				vp.MmprojPin = p
			}
		}
		// A "ram" pin is applied here rather than in the sizing loop so the
		// variant re-charge above (which rebuilds Overhead from scratch for a
		// vision variant with its own spec) can't hand the projector back.
		if vp.MmprojPin == "ram" {
			vp.Overhead -= mmprojOh
			vp.CpuMmproj = true
		}
		// "none" from the vision variant lands here rather than at the gate above,
		// which only sees the model-wide value; either way no twin is emitted.
		if vp.MmprojPin != "none" {
			profiles = append(profiles, vp)
		}
	}

	// Charge the GPU compute buffer (logits + activations + CUDA runtime) per
	// profile; it scales with the physical batch and lives on the GPU regardless
	// of CPU expert offload, so it's flat VRAM overhead. Replaces the old flat
	// 0.17 GB ubSoloOh fudge.
	for i := range profiles {
		profiles[i].Overhead += computeBufferGB(meta, effectiveUb(meta, profiles[i], ov, profiles[i].Ctx, s.TargetVramGB), s.ComputeBufFactor)
	}

	for _, prof := range profiles {
		// Per-variant kv quant changes the KV cost model, so resolve effective
		// kv and re-derive the cost slope/const for this profile (defaults to the
		// model-wide values when the variant doesn't override kv).
		// Variants inherit the model-wide kv quant; a variant's own kv wins.
		// A variant that pins only one side mirrors it; only a genuine mismatch
		// falls back to the model-wide quant.
		ekvK, ekvV := resolveKvPair(prof.KvK, prof.KvV, kvK, kvV)
		ptg, kcg := perTokGB, kvConstGB
		if ekvK != kvK || ekvV != kvV {
			if m := GetKvCostModel(meta, ekvK, ekvV); m.OK {
				ptg, kcg = m.SlopeGB, m.ConstGB
			}
		}

		// A variant may force KV in/out of RAM; otherwise inherit the model-wide
		// setting. Affects sizing, so resolve before sizeProfile.
		pkvInRam := kvInRam
		if prof.Variant != nil && prof.Variant.KvInRam {
			pkvInRam = true
		}

		// A variant can turn rope scaling on (or ask for a bigger ctx) by itself, so
		// the trained-length ceiling is resolved per profile, not once per model.
		pModelMax := modelMax
		if v := prof.Variant; v != nil {
			rs := v.RopeScaling
			if rs == "" {
				rs = override.RopeScaling
			}
			if c := ropeCeiling(meta, rs, prof.Ctx); c > pModelMax {
				pModelMax = c
			}
		}

		// Multi-slot: --kv-unified gives every slot one shared KV pool, so N slots
		// each holding a full-size conversation cost N x the KV. Charge that here —
		// sizing then yields a PER-SLOT ctx that N of can actually fit (buildCmdLines
		// multiplies it back out for -c), and every downstream number (kvReserve,
		// checkpoint reserve, estVramGB) describes the whole pool. Sizing against the
		// unscaled cost instead would hand slot 1 the whole card and let slots 2..N
		// evict it mid-conversation.
		pSlots := profileParallel(prof, override)
		if pSlots > 1 {
			ptg *= float64(pSlots)
			kcg *= float64(pSlots)
		}

		ctx, plan, kvReserve, planCkptGB, err := sizeProfile(meta, s, prof, ptg, kcg, pModelMax, pkvInRam)
		if err != nil {
			return err
		}
		// A "-vision" twin holds the CLIP projector in VRAM by default, and the
		// sizer pays for it out of the same budget as the text model: fewer GPU
		// layers, or a smaller window. llama-server's --no-mmproj-offload moves the
		// projector to the CPU, which costs image-encode latency ONCE per image but
		// nothing per token. So price the twin both ways and keep the projector on
		// the GPU only while it is affordable — the fallback is what lets any model
		// with a compatible projector carry a vision twin without the text side
		// paying for it.
		if prof.Vision && prof.MmprojGB > 0 && prof.MmprojPin == "" {
			cpuProf := prof
			cpuProf.Overhead -= prof.MmprojGB
			cpuProf.CpuMmproj = true
			cCtx, cPlan, cKvReserve, cCkptGB, cErr := sizeProfile(meta, s, cpuProf, ptg, kcg, pModelMax, pkvInRam)
			if cErr == nil && cpuMmprojWins(plan, ctx, cPlan, cCtx) {
				prof = cpuProf
				ctx, plan, kvReserve, planCkptGB = cCtx, cPlan, cKvReserve, cCkptGB
			}
		}
		ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
		if prof.CpuOffload > 0 {
			ngl, ncpuMoe = applyForcedOffload(meta, prof.CpuOffload)
		}
		// Re-price whenever the emitted placement is not the one GetLoadPlan
		// costed, so the baked estVramGB/estRamGB describe the flags we actually
		// write (forceLowActiveMoE rewrites them without touching the estimate).
		if ngl != plan.Ngl || ncpuMoe != plan.NCpuMoe {
			plan.EstVramGB, plan.EstRamGB = estForOffload(meta, prof, kvReserve, planCkptGB, ngl, ncpuMoe)
			plan.RamExceeded = s.MaxRamGB > 0 && plan.EstRamGB > s.MaxRamGB
		}

		// Per-variant spec/reasoning override the model-wide values for emit.
		// Variants layer their engine knobs OVER the model-wide override (inherit
		// at generate time: spec/draft chain, kv quant, reasoning budget, etc. flow
		// down); a variant's own non-blank field still wins, and sidecar edits keep
		// drifting per-variant freely.
		effOv := override
		if prof.Spec != "" {
			effOv.Spec = prof.Spec
		}
		if prof.ReasoningFmt != "" {
			effOv.ReasoningFmt = prof.ReasoningFmt
		}
		// A named variant carries the full launch shape: layer its engine knobs
		// over the model-wide override (zero/empty => inherit the override value).
		if v := prof.Variant; v != nil {
			// Inherit the model's preserve-thinking; an explicit variant value wins.
			// Both are *bool with the same nil = on meaning, so this is a pointer
			// assignment, not a deref: a nil variant value must leave the model's
			// own setting (which may be an explicit false) alone.
			if v.PreserveThinking != nil {
				effOv.PreserveThinking = v.PreserveThinking
			}
			if v.SlotCache != nil {
				effOv.SlotCache = v.SlotCache
			}
			if v.FlashAttn != "" {
				effOv.FlashAttn = v.FlashAttn
			}
			if v.Mmap != "" {
				effOv.Mmap = v.Mmap
			}
			if v.Mlock {
				effOv.Mlock = true
			}
			if v.Threads > 0 {
				effOv.Threads = v.Threads
			}
			if v.Parallel > 0 {
				effOv.Parallel = v.Parallel
			}
			if strings.TrimSpace(v.ExtraArgs) != "" {
				effOv.ExtraArgs = v.ExtraArgs
			}
			if strings.TrimSpace(v.ChatTemplateFile) != "" {
				effOv.ChatTemplateFile = v.ChatTemplateFile
			}
			// Sampler / speculative sub-knobs: non-zero/non-nil variant value wins.
			if v.Dry != nil {
				effOv.Dry = v.Dry
			}
			if v.DryMultiplier != 0 {
				effOv.DryMultiplier = v.DryMultiplier
			}
			if v.DryBase != 0 {
				effOv.DryBase = v.DryBase
			}
			if v.DryAllowedLength != 0 {
				effOv.DryAllowedLength = v.DryAllowedLength
			}
			// Sampler defaults: nil => inherit, non-nil wins INCLUDING an explicit
			// 0 (greedy temp / min-p off are real settings, not "unset").
			if v.Temp != nil {
				effOv.Temp = v.Temp
			}
			if v.TopK != nil {
				effOv.TopK = v.TopK
			}
			if v.TopP != nil {
				effOv.TopP = v.TopP
			}
			if v.MinP != nil {
				effOv.MinP = v.MinP
			}
			if v.PresencePenalty != nil {
				effOv.PresencePenalty = v.PresencePenalty
			}
			if v.SpecDraftNMax != 0 {
				effOv.SpecDraftNMax = v.SpecDraftNMax
			}
			if v.SpecDefault {
				effOv.SpecDefault = true
			}
			if v.SpecNgramSizeN != 0 {
				effOv.SpecNgramSizeN = v.SpecNgramSizeN
			}
			if v.SpecNgramSizeM != 0 {
				effOv.SpecNgramSizeM = v.SpecNgramSizeM
			}
			if v.SpecNgramMinHits != 0 {
				effOv.SpecNgramMinHits = v.SpecNgramMinHits
			}
			// Advanced knobs: variant's own non-zero/non-empty value wins.
			if v.ThreadsBatch != 0 {
				effOv.ThreadsBatch = v.ThreadsBatch
			}
			if v.Prio != 0 {
				effOv.Prio = v.Prio
			}
			if v.DirectIo {
				effOv.DirectIo = true
			}
			if v.NoOpOffload {
				effOv.NoOpOffload = true
			}
			if v.NoRepack {
				effOv.NoRepack = true
			}
			if v.KvKDraft != "" {
				effOv.KvKDraft = v.KvKDraft
			}
			if v.KvVDraft != "" {
				effOv.KvVDraft = v.KvVDraft
			}
			if v.CacheReuse != 0 {
				effOv.CacheReuse = v.CacheReuse
			}
			if v.CacheRamMB != 0 {
				effOv.CacheRamMB = v.CacheRamMB
			}
			if v.CacheIdleSlots != "" {
				effOv.CacheIdleSlots = v.CacheIdleSlots
			}
			if v.SwaFull {
				effOv.SwaFull = true
			}
			if v.CheckpointMinStep != 0 {
				effOv.CheckpointMinStep = v.CheckpointMinStep
			}
			if v.ContextShift != "" {
				effOv.ContextShift = v.ContextShift
			}
			if v.SpecDraftNMin != 0 {
				effOv.SpecDraftNMin = v.SpecDraftNMin
			}
			if v.SlotPromptSimilarity != 0 {
				effOv.SlotPromptSimilarity = v.SlotPromptSimilarity
			}
			if v.RopeScaling != "" {
				effOv.RopeScaling = v.RopeScaling
			}
			if v.RopeScale != 0 {
				effOv.RopeScale = v.RopeScale
			}
			if v.RopeFreqBase != 0 {
				effOv.RopeFreqBase = v.RopeFreqBase
			}
			if v.YarnOrigCtx != 0 {
				effOv.YarnOrigCtx = v.YarnOrigCtx
			}
			if v.SplitMode != "" {
				effOv.SplitMode = v.SplitMode
			}
			if v.TensorSplit != "" {
				effOv.TensorSplit = v.TensorSplit
			}
			if v.MainGpu != 0 {
				effOv.MainGpu = v.MainGpu
			}
			if v.OverrideTensor != "" {
				effOv.OverrideTensor = v.OverrideTensor
			}
		}
		emitProfile(b, s, meta, row, prof, ctx, ngl, ncpuMoe, plan, ekvK, ekvV, pkvInRam, &effOv)
		*emitted = append(*emitted, prof.Name)
	}
	return nil
}
