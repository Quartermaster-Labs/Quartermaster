package autogen

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// profile is one emitted llama-quartermaster entry for a model: the solo variant or an
// optional ctx-tier variant. Target/Overhead are the VRAM budget the sizing math
// uses; the flags drive ub/spec/reasoning.
type profile struct {
	Name     string
	Target   float64
	Overhead float64
	Unlisted bool
	Ctx      int  // 0 = auto-size; >0 forces that ctx via the manual-cap path
	IsLong   bool // ctx-tier rung >= 64k (drops -ub to 512)
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
	// Variant, when non-nil, is the named-variant source this profile was built
	// from. Its engine knobs (kvInRam/flash/mmap/mlock/threads/parallel/extraArgs)
	// layer over the model-wide override at emit so a variant carries the full
	// launch shape. Solo/ctx-tier profiles leave this nil and use the override.
	Variant *VariantSpec
	// Vision marks the auto-generated "-vision" twin: emits --mmproj <projector>
	// and an image-input capabilities block so the playground can attach images.
	Vision bool
}

// Generate discovers models under gf.Settings.ModelsRoot and returns a complete
// llama-quartermaster config YAML. Port of Generate-Config.ps1. nowRFC is stamped into
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
	fmt.Fprintf(&b, "# llama-quartermaster config - generated %s\n", nowRFC)
	fmt.Fprintf(&b, "# TargetVramGB=%g  MaxRamGB=%g  Threads=%d\n", s.TargetVramGB, s.MaxRamGB, s.Threads)
	b.WriteString("# Regen: quartermaster startup (hash-gated)\n\n")
	fmt.Fprintf(&b, "healthCheckTimeout: %d\n\n", s.HealthCheckTimeout)
	emitSlotCache(&b, s.SlotCache)
	emitAPIKeys(&b, s.APIKeys)
	b.WriteString("models:\n")

	var emitted []string
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
		if err := emitModel(&b, s, gf, row, ov, name, &emitted); err != nil {
			fmt.Fprintf(&b, "\n  # SKIPPED %q: %v\n", name, err)
			continue
		}
	}

	emitGroupsAndListeners(&b, s, emitted)
	return b.String(), nil
}

// slotKvPath resolves the slot-cache snapshot dir as forward-slash text shared
// by the --slot-save-path flag and the emitted slotCache.path. Blank Path falls
// back to a ".cache" folder next to the quartermaster binary (kept in sync with
// config.DefaultSlotCachePath; duplicated here so autogen stays free of an
// internal/config import).
func slotKvPath(sc SlotCacheSettings) string {
	p := sc.Path
	if p == "" {
		if exe, err := os.Executable(); err == nil {
			p = filepath.Join(filepath.Dir(exe), ".cache", "slotkv")
		} else {
			p = filepath.Join(os.TempDir(), "llama-quartermaster", "slotkv")
		}
	}
	return strings.ReplaceAll(p, "\\", "/")
}

// emitSlotCache writes the slotCache config block (consumed by the server) when
// the feature is enabled. Unset knobs are omitted so the server applies its
// defaults. The path is always emitted so it matches the --slot-save-path flag.
func emitSlotCache(b *strings.Builder, sc SlotCacheSettings) {
	if !sc.Enable {
		return
	}
	b.WriteString("slotCache:\n")
	b.WriteString("  enable: true\n")
	fmt.Fprintf(b, "  path: %q\n", slotKvPath(sc))
	if sc.MinSaveTokens > 0 {
		fmt.Fprintf(b, "  minSaveTokens: %d\n", sc.MinSaveTokens)
	}
	if sc.MaxDiskGB > 0 {
		fmt.Fprintf(b, "  maxDiskGB: %g\n", sc.MaxDiskGB)
	}
	if sc.MaxSessions > 0 {
		fmt.Fprintf(b, "  maxSessions: %d\n", sc.MaxSessions)
	}
	b.WriteString("\n")
}

// emitAPIKeys writes the apiKeys list and, for any key scoped to a model
// subset, the apiKeyModels map (key => allowed model IDs). Keys with no Models
// are unrestricted and appear only in apiKeys. No keys => nothing emitted.
func emitAPIKeys(b *strings.Builder, keys []APIKeyEntry) {
	if len(keys) == 0 {
		return
	}
	b.WriteString("apiKeys:\n")
	for _, k := range keys {
		fmt.Fprintf(b, "  - %q\n", k.Key)
	}
	scoped := false
	for _, k := range keys {
		if len(k.Models) > 0 {
			scoped = true
			break
		}
	}
	if scoped {
		b.WriteString("apiKeyModels:\n")
		for _, k := range keys {
			if len(k.Models) == 0 {
				continue
			}
			fmt.Fprintf(b, "  %q:\n", k.Key)
			for _, m := range k.Models {
				fmt.Fprintf(b, "    - %q\n", m)
			}
		}
	}
	b.WriteString("\n")
}

// emitGroupsAndListeners writes the groups block and, when settings.groups
// define listen addresses, a listeners block. The mechanism is use-case
// agnostic: each configured group has name-glob patterns (PowerShell -like);
// every emitted model is assigned to the FIRST group whose any pattern matches
// its name, so put specific groups before a "*" catch-all. Models matching no
// group fall into an implicit "default" group with no listener. All groups are
// exclusive swap groups, so loading on any listener still evicts whatever the
// others were running (one GPU, VRAM-exclusive). With no settings.groups the
// output is a single "exclusive" group over every model (upstream default).
func emitGroupsAndListeners(b *strings.Builder, s Settings, emitted []string) {
	if len(s.Groups) == 0 {
		b.WriteString("\ngroups:\n")
		writeGroup(b, "exclusive", emitted)
		return
	}

	// Assign each model to the first matching group (config order preserved).
	members := make(map[string][]string, len(s.Groups)+1)
	order := make([]string, 0, len(s.Groups)+1)
	seenGroup := map[string]bool{}
	addGroup := func(name string) {
		if !seenGroup[name] {
			seenGroup[name] = true
			order = append(order, name)
		}
	}
	for _, g := range s.Groups {
		addGroup(g.Name)
	}
	const defaultGroup = "default"
	for _, name := range emitted {
		assigned := ""
		for _, g := range s.Groups {
			for _, pat := range g.Match {
				if globLike(pat, name) {
					assigned = g.Name
					break
				}
			}
			if assigned != "" {
				break
			}
		}
		if assigned == "" {
			assigned = defaultGroup
			addGroup(defaultGroup)
		}
		members[assigned] = append(members[assigned], name)
	}

	b.WriteString("\ngroups:\n")
	for _, name := range order {
		writeGroup(b, name, members[name])
	}

	// listeners: address -> the groups it exposes (a group with no Listen binds
	// no dedicated port but still groups for eviction).
	byAddr := map[string][]string{}
	var addrOrder []string
	for _, g := range s.Groups {
		if g.Listen == "" {
			continue
		}
		if _, ok := byAddr[g.Listen]; !ok {
			addrOrder = append(addrOrder, g.Listen)
		}
		byAddr[g.Listen] = append(byAddr[g.Listen], g.Name)
	}
	if len(addrOrder) == 0 {
		return
	}
	b.WriteString("\nlisteners:\n")
	for _, addr := range addrOrder {
		fmt.Fprintf(b, "  %q:\n    groups: [%s]\n", addr, strings.Join(byAddr[addr], ", "))
	}
}

// writeGroup emits one exclusive swap group with the given members.
func writeGroup(b *strings.Builder, name string, members []string) {
	fmt.Fprintf(b, "  %s:\n", name)
	b.WriteString("    swap: true\n")
	b.WriteString("    exclusive: true\n")
	b.WriteString("    members:\n")
	for _, n := range members {
		fmt.Fprintf(b, "      - %q\n", n)
	}
}

// emitModel reads metadata once and emits every profile (solo, ctx tiers, game)
// for one discovered gguf.
func emitModel(b *strings.Builder, s Settings, gf GenerateFile, row GgufRow, ov *Override, name string, emitted *[]string) error {
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

	// KV quant: dense archs default to q8_0; MoE defaults to f16. A low number of
	// active params means each token's KV carries more of the model's signal, so
	// MoE tends to degrade more under a quantized cache — keep it full precision.
	// Per-model override still wins. Fast flash-attention needs matched K/V and
	// never iq4_nl.
	kvK, kvV := "q8_0", "q8_0"
	if meta.IsMoE {
		kvK, kvV = "f16", "f16"
	}
	kvDefK, kvDefV := kvK, kvV
	if ov != nil && ov.KvK != "" {
		kvK = ov.KvK
	}
	if ov != nil && ov.KvV != "" {
		kvV = ov.KvV
	}
	if kvK != kvV || kvK == "iq4_nl" || kvV == "iq4_nl" {
		kvK, kvV = kvDefK, kvDefV
	}

	kvModel := GetKvCostModel(meta, kvK, kvV)
	perTokGB := 0.0
	kvConstGB := 0.0
	if kvModel.OK {
		perTokGB = kvModel.SlopeGB
		kvConstGB = kvModel.ConstGB
	}
	modelMax := 32768
	if meta.ContextLength > 0 {
		modelMax = int(meta.ContextLength)
	}
	kvInRam := ov != nil && ov.KvInRam

	// Pre-placement (ngl not chosen yet): assume GPU-bound so a dflash sidecar
	// charges its real draft VRAM. If the model turns out CPU-bound and emit
	// downgrades to mtp, we've over-reserved by the draft size — conservative,
	// never an under-count that could OOM.
	modelSpec := effectiveSpec(meta, ov, row.DraftKind)
	specOh := draftOverheadGB(modelSpec, row.DraftSizeGB)
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
		Name:           name,
		Target:         soloTarget,
		Overhead:       s.VramOverheadGB + specOh,
		Unlisted:       override.Unlisted,
		Ctx:            override.Ctx,
		CpuOffload:     override.CpuOffload,
		CtxCheckpoints: override.CtxCheckpoints,
	}}
	for _, cv := range ctxVariants {
		cvTarget := s.TargetVramGB
		if cv >= 65536 {
			cvTarget = s.TargetVramGB - 0.5
		}
		profiles = append(profiles, profile{
			Name:           fmt.Sprintf("%s-%s", name, formatCtxTag(cv)),
			Target:         cvTarget,
			Overhead:       s.VramOverheadGB + specOh,
			Ctx:            cv,
			IsLong:         cv >= 65536,
			CtxCheckpoints: override.CtxCheckpoints,
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
		vSpecOh := draftOverheadGB(vEffSpec, row.DraftSizeGB)
		// Standalone: a blank ctx-checkpoints uses the generator default, not the
		// model-wide override value.
		vCheckpoints := v.CtxCheckpoints
		profiles = append(profiles, profile{
			Name:           fmt.Sprintf("%s-%s", name, slugify(v.Name)),
			Target:         vTarget,
			Overhead:       s.VramOverheadGB + vSpecOh,
			Unlisted:       v.Unlisted,
			Ctx:            v.Ctx,
			IsLong:         v.Ctx >= 65536,
			KvK:            v.KvK,
			KvV:            v.KvV,
			Spec:           v.Spec,
			ReasoningFmt:   v.ReasoningFmt,
			Ub:             v.Ub,
			CpuOffload:     v.CpuOffload,
			CtxCheckpoints: vCheckpoints,
			Variant:        &variantSpecs[i],
		})
	}

	// Vision twin: a model shipping a sibling mmproj projector gets an extra
	// "-vision" profile that loads it (--mmproj) and declares image input. Listed
	// by default (a distinct served id, scopeable by API keys); the config editor's
	// reserved "vision" variant can still mark it unlisted. Charged the projector's
	// file size as flat VRAM overhead.
	if row.MmprojPath != "" {
		vp := profile{
			Name:     fmt.Sprintf("%s-vision", name),
			Target:   soloTarget,
			Overhead: s.VramOverheadGB + specOh + mmprojVramGB(row.MmprojSizeGB, s),
			Ctx:      override.Ctx,
			Vision:   true,
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
				vp.IsLong = v.Ctx >= 65536
			}
			// Spec changes the draft overhead; recharge off the variant's spec.
			if v.Spec != "" {
				vp.Overhead = s.VramOverheadGB + draftOverheadGB(v.Spec, row.DraftSizeGB) + mmprojVramGB(row.MmprojSizeGB, s)
			}
			vp.Unlisted = v.Unlisted
			vp.KvK = v.KvK
			vp.KvV = v.KvV
			vp.Spec = v.Spec
			vp.ReasoningFmt = v.ReasoningFmt
			vp.Ub = v.Ub
			vp.CpuOffload = v.CpuOffload
			vp.CtxCheckpoints = v.CtxCheckpoints
			vp.Variant = v
		}
		profiles = append(profiles, vp)
	}

	// Charge the GPU compute buffer (logits + activations + CUDA runtime) per
	// profile; it scales with the physical batch and lives on the GPU regardless
	// of CPU expert offload, so it's flat VRAM overhead. Replaces the old flat
	// 0.17 GB ubSoloOh fudge.
	for i := range profiles {
		profiles[i].Overhead += computeBufferGB(meta, effectiveUb(profiles[i], ov, profiles[i].Ctx), s.ComputeBufFactor)
	}

	for _, prof := range profiles {
		// Per-variant kv quant changes the KV cost model, so resolve effective
		// kv and re-derive the cost slope/const for this profile (defaults to the
		// model-wide values when the variant doesn't override kv).
		// Variants inherit the model-wide kv quant; a variant's own kv wins.
		ekvK, ekvV := kvK, kvV
		if prof.KvK != "" {
			ekvK = prof.KvK
		}
		if prof.KvV != "" {
			ekvV = prof.KvV
		}
		if ekvK != ekvV || ekvK == "iq4_nl" || ekvV == "iq4_nl" {
			ekvK, ekvV = kvK, kvV // fall back to the model-wide quant (f16 for MoE)
		}
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

		ctx, plan, kvReserve, err := sizeProfile(meta, s, prof, ptg, kcg, modelMax, pkvInRam)
		if err != nil {
			return err
		}
		ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
		if prof.CpuOffload > 0 {
			ngl, ncpuMoe = applyForcedOffload(meta, prof.CpuOffload)
			plan.EstVramGB, plan.EstRamGB = estForOffload(meta, prof, kvReserve, ngl, ncpuMoe)
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
			if v.PreserveThinking != nil {
				effOv.PreserveThinking = *v.PreserveThinking
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
		}
		emitProfile(b, s, meta, row, prof, ctx, ngl, ncpuMoe, plan, ekvK, ekvV, pkvInRam, &effOv)
		*emitted = append(*emitted, prof.Name)
	}
	return nil
}

// sizeProfile computes the context window and load plan for one profile,
// mirroring the dense / MoE / kv-in-ram / no-attn branches of Generate-Config.
func sizeProfile(meta Metadata, s Settings, prof profile, perTokGB, kvConstGB float64, modelMax int, kvInRam bool) (ctx int, plan LoadPlan, kvReserve float64, err error) {
	target := prof.Target
	overhead := prof.Overhead

	switch {
	case kvInRam && perTokGB > 0:
		kvReserve = 0.1
		plan, err = GetLoadPlan(meta, planOpt(target, s.MaxRamGB, kvReserve, overhead))
		if err != nil {
			return
		}
		ctxBudgetRam := s.MaxRamGB - plan.EstRamGB
		if ctxBudgetRam < 0.5 {
			ctxBudgetRam = 0.5
		}
		maxCtxRam := MaxCtxForBudget(ctxBudgetRam, perTokGB, kvConstGB)
		ctx = RoundedCtx(float64(min(modelMax, maxCtxRam)))
		if prof.Ctx != 0 {
			ctx = min(ctx, prof.Ctx)
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)

	case perTokGB > 0:
		ckptCtxCeil := modelMax
		if prof.Ctx != 0 {
			ckptCtxCeil = min(ckptCtxCeil, prof.Ctx)
		}
		ckpt := checkpointReserveGB(prof, perTokGB, kvConstGB, ckptCtxCeil, meta.FullAttnInterval > 0)
		// Checkpoints live wherever the KV cache does. MoE keeps KV (and thus its
		// checkpoints) VRAM-resident even when expert weights spill to CPU via
		// --n-cpu-moe, so they're a flat VRAM overhead. Dense models keep the KV
		// of any CPU-offloaded layer in RAM, so the checkpoint cost is folded into
		// the per-layer KV reserve (placementCkpt) and split across the GPU/CPU
		// layers by densePlacement instead of charged whole to VRAM up front
		// (which drove -ngl toward 0 at partial offload).
		placementCkpt := 0.0
		if meta.IsMoE {
			overhead += ckpt
			if prof.Ctx != 0 {
				// Explicit ctx (a custom ctx tier / variant) is HARD: honor it and
				// let GetLoadPlan below trade expert layers (--n-cpu-moe) for the
				// larger KV reserve, instead of shrinking ctx to whatever VRAM is
				// free. "64k variant" means 64k context, capped only by modelMax.
				ctx = RoundedCtx(float64(min(modelMax, prof.Ctx)))
			} else {
				share := effectiveShare(meta, genMoeShareFor)
				nonExpert := meta.FileSizeGB * (1.0 - share)
				usableBase := target - 0.25 - overhead
				if meta.FileSizeGB <= usableBase {
					kvBudget := target - meta.FileSizeGB - overhead
					if kvBudget < 0.1 {
						kvBudget = 0.1
					}
					ctx = RoundedCtx(float64(min(modelMax, MaxCtxForBudget(kvBudget, perTokGB, kvConstGB))))
				} else {
					maxKvVram := target - nonExpert - overhead
					if maxKvVram < 0.1 {
						maxKvVram = 0.1
					}
					maxCtxVram := MaxCtxForBudget(maxKvVram, perTokGB, kvConstGB)
					ctx = RoundedCtx(float64(min(min(modelMax, s.MoeCtxTarget), maxCtxVram)))
				}
			}
		} else {
			ladder := s.DenseCtxLadder
			minCtx := s.DenseMinCtx
			if prof.Ctx != 0 {
				ladder = []int{prof.Ctx}
				minCtx = prof.Ctx
			}
			// Size ctx conservatively against the checkpoint cost (overhead+ckpt),
			// but keep ckpt out of overhead so placement can split it per-layer.
			d := GetDenseCtx(DenseCtxParams{
				ModelMax: modelMax, PerTokGB: perTokGB, KvConstGB: kvConstGB,
				FileSizeGB: meta.FileSizeGB, TargetVramGB: target, Overhead: overhead + ckpt,
				Ladder: ladder, MinCtx: minCtx, AllowOffload: prof.Ctx != 0,
			})
			ctx = d.Ctx
			if prof.Ctx != 0 {
				ctx = min(ctx, prof.Ctx)
			}
			placementCkpt = ckpt
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)
		plan, err = GetLoadPlan(meta, planOpt(target, s.MaxRamGB, kvReserve+placementCkpt, overhead))
		if err != nil {
			return
		}

	default:
		ctx = RoundedCtx(float64(min(modelMax, 32768)))
		if prof.Ctx != 0 {
			ctx = min(ctx, prof.Ctx)
		}
		kvReserve = 0
		// No attention dims: planner uses its flat 1.0GB KV reserve default.
		plan, err = GetLoadPlan(meta, PlanOptions{TargetVramGB: target, MaxRamGB: s.MaxRamGB, CudaOverheadGB: overhead, cudaSet: true})
		if err != nil {
			return
		}
	}
	return
}

// llamaDefaultCtxCheckpoints is llama-server's --ctx-checkpoints default when
// the flag is omitted (PR #15293). Tuned for a multi-slot server; overkill for
// local single-user serving, so we override it with defaultCtxCheckpoints.
const llamaDefaultCtxCheckpoints = 32

// defaultCtxCheckpoints picks a sane checkpoint count for a model that doesn't
// set ctxCheckpoints itself.
//
// recurrent (FullAttnInterval>0: GatedDeltaNet/SSM hybrids) => 0: their recurrent
// state can only be restored at its exact saved length, so a checkpoint restore
// lands it at the wrong position and llama-server spams "non-consecutive token
// position" + reprocesses the whole prompt (0 reuse, upstream llama.cpp #21831).
// Checkpoints cost VRAM and buy nothing on this arch, so disable them.
//
// Otherwise kvConstGB > 0 means SWA: the KV window rolls, so prefix-cache reuse
// breaks on a context shift and checkpoints (which DO restore on SWA) are the
// only reuse path — worth a few, though each is pricey. Plain full-attention
// models keep a persistent KV that already covers linear chat, so they need only
// a couple for the occasional edit/branch.
func defaultCtxCheckpoints(kvConstGB float64, recurrent bool) int {
	if recurrent {
		return 0
	}
	if kvConstGB > 0 {
		return 6
	}
	return 3
}

// checkpointMinStep mirrors llama-server's --checkpoint-min-step default â€” the
// minimum prompt-token spacing between context checkpoints. We don't model the
// flag, so the default is assumed for the per-checkpoint global-KV term.
const checkpointMinStep = 256

// effectiveCtxCheckpoints resolves the checkpoint count a profile will actually
// run with: an explicit value (incl. 0 = disabled) when set, else the
// llama-server default.
func effectiveCtxCheckpoints(prof profile, def int) int {
	if prof.CtxCheckpoints != nil {
		return *prof.CtxCheckpoints
	}
	return def
}

// checkpointReserveGB estimates the extra VRAM a profile's context checkpoints
// consume. llama-server keeps up to --ctx-checkpoints KV snapshots so a diverging
// prompt can be restored instead of reprocessed. Each snapshot holds the
// ctx-independent window/recurrent state (kvConstGB) plus roughly one
// checkpoint-min-step worth of global KV. Left unaccounted, the default 32
// snapshots silently overflow VRAM into sysmem and tank decode speed.
//
// The count is capped by how many checkpoints can actually exist: at min-step
// spacing a context of ctxCeil tokens holds at most ctxCeil/checkpointMinStep
// snapshots, so a small pinned ctx (e.g. a 4k judge) reserves far fewer than the
// 32 default. Returns 0 when checkpoints are disabled or the model has no
// VRAM-resident KV.
func checkpointReserveGB(prof profile, perTokGB, kvConstGB float64, ctxCeil int, recurrent bool) float64 {
	n := effectiveCtxCheckpoints(prof, defaultCtxCheckpoints(kvConstGB, recurrent))
	if n <= 0 || (perTokGB <= 0 && kvConstGB <= 0) {
		return 0
	}
	if ctxCeil > 0 {
		if maxN := ctxCeil / checkpointMinStep; maxN < n {
			n = maxN
		}
	}
	if n <= 0 {
		return 0
	}
	perCheckpoint := kvConstGB + perTokGB*float64(checkpointMinStep)
	return float64(n) * perCheckpoint
}

// planOpt builds PlanOptions with explicit reserve + overhead set.
func planOpt(target, maxRam, kvReserve, overhead float64) PlanOptions {
	return PlanOptions{
		TargetVramGB:     target,
		MaxRamGB:         maxRam,
		KvCacheReserveGB: kvReserve,
		CudaOverheadGB:   overhead,
		kvReserveSet:     true,
		cudaSet:          true,
	}
}

// forceLowActiveMoE recomputes the expert split for low-active MoE models that
// the planner's PCIe-thrash crossover wrongly fell back to naive -ngl on. Keeps
// dense+attention on GPU (-ngl 99) with experts on CPU.
func forceLowActiveMoE(meta Metadata, plan LoadPlan, prof profile, kvReserve float64) (ngl, ncpuMoe int) {
	ngl, ncpuMoe = plan.Ngl, plan.NCpuMoe
	if !(meta.IsMoE && ncpuMoe == 0 && ngl < 99) {
		return
	}
	share := effectiveShare(meta, genMoeShareFor)
	reserve := 1.0
	if kvReserve > 0 {
		reserve = kvReserve
	}
	usable := prof.Target - reserve - prof.Overhead
	nonExpert := meta.FileSizeGB * (1.0 - share)
	perMoeLayer := (meta.FileSizeGB * share) / float64(meta.BlockCount)
	moeOnGpu := math.Floor((usable - nonExpert) / perMoeLayer)
	if moeOnGpu > float64(meta.BlockCount) {
		moeOnGpu = float64(meta.BlockCount)
	}
	if moeOnGpu < 0 {
		moeOnGpu = 0
	}
	ncpuMoe = int(math.Max(0, float64(meta.BlockCount)-moeOnGpu))
	ngl = 99
	return
}

// applyForcedOffload overrides the auto placement with a user-pinned number of
// layers pushed to CPU (Override.CpuOffload). MoE models offload expert layers
// (--n-cpu-moe n, GPU stays -ngl 99); dense models drop GPU layers
// (-ngl = blocks-n). n is clamped to [0, blockCount].
func applyForcedOffload(meta Metadata, n int) (ngl, ncpuMoe int) {
	blocks := int(meta.BlockCount)
	if n < 0 {
		n = 0
	}
	if blocks > 0 && n > blocks {
		n = blocks
	}
	if meta.IsMoE {
		return 99, n
	}
	ngl = blocks - n
	if ngl < 0 {
		ngl = 0
	}
	return ngl, 0
}

// estForOffload recomputes the VRAM/RAM estimate for a forced placement so the
// generated header comment (and the editor preview) reflect the pinned offload
// rather than the auto sizer's numbers. Mirrors the cost model in plan.go.
func estForOffload(meta Metadata, prof profile, kvReserve float64, ngl, ncpuMoe int) (estVram, estRam float64) {
	size := meta.FileSizeGB
	blocks := float64(meta.BlockCount)
	overhead := prof.Overhead
	if blocks <= 0 {
		return prof.Target, 0
	}
	if meta.IsMoE {
		share := effectiveShare(meta, genMoeShareFor)
		nonExpert := size * (1.0 - share)
		expertGpuFrac := (blocks - float64(ncpuMoe)) / blocks
		estVram = nonExpert + size*share*expertGpuFrac + kvReserve + overhead
		estRam = size * share * (float64(ncpuMoe) / blocks)
		return round(estVram, 2), round(estRam, 2)
	}
	gpuFrac := float64(ngl) / blocks
	if gpuFrac > 1 {
		gpuFrac = 1
	}
	estVram = gpuFrac*(size+kvReserve) + overhead
	estRam = (1 - gpuFrac) * (size + kvReserve)
	return round(estVram, 2), round(estRam, 2)
}

// Empirical compute-graph constants. With flash attention on (the default), the
// CUDA compute buffer is dominated by the logits/output tensor plus a handful of
// n_ubatch*n_embd activation copies; computeCudaCtxGB covers the fixed CUDA
// runtime + cuBLAS workspace. The activation-copy count is a coarse fit, so
// Settings.ComputeBufFactor scales the whole analytic term for per-build/arch
// calibration against the "compute buffer size" llama logs.
//
// computeLogitsTokens caps the vocab-scaled term at n_vocab*THIS*4. The output
// TENSOR is sized by n_outputs (~1 in prefill) — but empirically the CUDA compute
// buffer for a large-vocab model still grows with the physical batch (output-
// projection / cuBLAS workspace tiled over the ubatch). Measured on Qwen3.6-35B-A3B
// (vocab 248320, embd 2048) on an 8GB card: at ub=512 the model fits its budget, at
// ub=1024 it spills ~0.5GB into shared memory. A 256 cap made the estimate nearly
// ub-blind (+31MB from 512->1024) so the sizer never charged the extra ub cost and
// over-committed VRAM. Cap at 1024 so the term scales with ub across the useful
// range (vocab*1024*4 ~1.0GB, giving the observed +0.5GB from 512->1024) and stops
// the overfill. ponytail: ceiling is 1024 (== the common max ub); revisit if ub>1024
// is used, and dial Settings.ComputeBufFactor down if it over-offloads other models.
const (
	computeActCopies    = 8.0
	computeCudaCtxGB    = 0.3
	computeLogitsTokens = 1024.0
	computeFallbackGB   = 0.17 // vocab/embd dims missing => prior flat estimate
)

// computeBufferGB estimates the GPU compute buffer (logits + activations + CUDA
// runtime) for a given physical batch (ub). This lives on the GPU regardless of
// CPU expert offload, so it is charged as flat VRAM overhead.
func computeBufferGB(meta Metadata, ub int, factor float64) float64 {
	if factor <= 0 {
		factor = 1.0
	}
	embd := float64(meta.EmbeddingLength)
	vocab := float64(meta.VocabSize)
	if embd <= 0 || vocab <= 0 || ub <= 0 {
		return computeFallbackGB
	}
	logits := vocab * math.Min(float64(ub), computeLogitsTokens) * 4.0
	acts := float64(ub) * embd * computeActCopies * 4.0
	return computeCudaCtxGB + factor*(logits+acts)/gib
}

// mmprojVramGB is a "-vision" twin's total projector VRAM footprint: the
// projector gguf's own weights (fileSizeGB — resident on GPU by default) plus
// s.VisionOverheadGB for the CLIP compute buffer that image processing allocates.
// Charged at BOTH generate time (baked plan) and spawn time (LiveOffloadArgs via
// EstimateInput.MmprojGB), so the live guard sizes the twin against the same
// footprint the config assumed. Without the reserve, the projector's compute
// buffer is invisible to the sizer and the vision load leaves too little free VRAM.
func mmprojVramGB(fileSizeGB float64, s Settings) float64 {
	return fileSizeGB + s.VisionOverheadGB
}

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

// qwenFixedChatTemplateFile is the server-cwd-relative path to froggeric's
// community chat-template fix for Qwen 3.5/3.6 (Apache-2.0, inherited from
// Qwen; see templates/CREDITS.md). The official Qwen 3.5/3.6 templates
// mutate already-rendered history on every turn, so llama.cpp's prefix
// cache never matches and the whole prompt reprocesses each request; this
// drop-in template renders history deterministically instead.
const qwenFixedChatTemplateFile = "templates/qwen-fixed-chat-template.jinja"

// needsQwenFixedChatTemplate reports whether a model's gguf arch identifies it
// as a Qwen 3.5 or 3.6 variant, which should get qwenFixedChatTemplateFile
// instead of its baked-in gguf template. llama.cpp has not split out a
// separate arch for 3.6: both "Qwen3.5" and "Qwen3.6"-branded ggufs report
// "qwen35" (dense) or "qwen35moe" (MoE) — verified against real local ggufs,
// since neither is exercised by real_models_test.go's fixture set.
func needsQwenFixedChatTemplate(arch string) bool {
	switch strings.ToLower(arch) {
	case "qwen35", "qwen35moe":
		return true
	default:
		return false
	}
}

// effectiveUb resolves the physical batch (-ub/-b) for a profile, matching the
// flag emitted by buildCmdLines: 1024 default, 512 for long ctx, overridden by
// ov.Ub then the profile's own Ub. ctx is the resolved context length; a >=64k
// ctx drops ub to 512 even when prof.IsLong wasn't set upfront — the auto-sized
// solo profile (Ctx=0) only learns its real ctx after sizing, and at 64k+ the
// ub=1024 compute buffer won't fit an 8GB card even fully expert-offloaded.
// Pass 0 pre-sizing to charge the conservative (larger) ub=1024 buffer.
func effectiveUb(prof profile, ov *Override, ctx int) int {
	ub := 1024
	if prof.IsLong || ctx >= 65536 {
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

	// mmap on by default: lazy demand-paging → snappy load, OS caches naturally.
	// --no-mmap only on explicit Mmap:"off" (forces fully-resident anon RAM,
	// resists eviction under pressure at the cost of a full upfront read).
	noMmapFlag := ""
	if ov != nil && ov.Mmap == "off" {
		noMmapFlag = "--no-mmap "
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

	ub := effectiveUb(prof, ov, ctx)
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
	ckpts := effectiveCtxCheckpoints(prof, defaultCtxCheckpoints(GetKvCostModel(meta, kvK, kvV).ConstGB, meta.FullAttnInterval > 0))
	lines = append(lines, fmt.Sprintf("--ctx-checkpoints %d", ckpts))
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
	if needsQwenFixedChatTemplate(meta.Architecture) {
		lines = append(lines, fmt.Sprintf("--chat-template-file %s", qwenFixedChatTemplateFile))
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
	// Diffusion models render an sd-server command, not a llama-server one.
	if imgArch := effectiveImageArch(meta); isImageArch(imgArch) {
		lines, _, _, _ := imageCmdLines(s, row, &ov, imgArch, row.FullPath)
		return strings.Join(lines, " "), nil
	}
	// Embedders render a minimal --embeddings command (no KV/spec sizing).
	if IsEmbeddingModel(meta) {
		return strings.Join(embeddingCmdLines(s, row, &ov, meta), " "), nil
	}
	kvK, kvV := "q8_0", "q8_0"
	if ov.KvK != "" {
		kvK = ov.KvK
	}
	if ov.KvV != "" {
		kvV = ov.KvV
	}
	if kvK != kvV || kvK == "iq4_nl" || kvV == "iq4_nl" {
		kvK, kvV = "q8_0", "q8_0"
	}
	perTokGB, kvConstGB := 0.0, 0.0
	if m := GetKvCostModel(meta, kvK, kvV); m.OK {
		perTokGB, kvConstGB = m.SlopeGB, m.ConstGB
	}
	modelMax := 32768
	if meta.ContextLength > 0 {
		modelMax = int(meta.ContextLength)
	}
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
		IsLong:         ov.Ctx >= 65536,
		CtxCheckpoints: ov.CtxCheckpoints,
	}
	prof.Overhead += computeBufferGB(meta, effectiveUb(prof, &ov, prof.Ctx), s.ComputeBufFactor)
	ctx, plan, kvReserve, err := sizeProfile(meta, s, prof, perTokGB, kvConstGB, modelMax, ov.KvInRam)
	if err != nil {
		return "", err
	}
	ngl, ncpuMoe := forceLowActiveMoE(meta, plan, prof, kvReserve)
	if ov.CpuOffload > 0 {
		ngl, ncpuMoe = applyForcedOffload(meta, ov.CpuOffload)
	}
	return strings.Join(buildCmdLines(s, meta, row, prof, ctx, ngl, ncpuMoe, kvK, kvV, ov.KvInRam, &ov), " "), nil
}

// emitProfile writes one model entry's YAML block.
func emitProfile(b *strings.Builder, s Settings, meta Metadata, row GgufRow, prof profile, ctx, ngl, ncpuMoe int, plan LoadPlan, kvK, kvV string, kvInRam bool, ov *Override) {
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB blocks=%d moe=%v\n", meta.Architecture, meta.FileSizeGB, meta.BlockCount, meta.IsMoE)
	fmt.Fprintf(b, "  # est vram=%gGB ram=%gGB\n", plan.EstVramGB, plan.EstRamGB)
	fmt.Fprintf(b, "  %q:\n", prof.Name)
	b.WriteString("    cmd: >\n")
	for _, line := range buildCmdLines(s, meta, row, prof, ctx, ngl, ncpuMoe, kvK, kvV, kvInRam, ov) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	if prof.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	if prof.Vision {
		b.WriteString("    capabilities:\n")
		b.WriteString("      in: [text, image]\n")
		b.WriteString("      out: [text]\n")
	}
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
// layer or a paired mtp-*.gguf sidecar) default to draft-mtp, everything else
// to ngram-mod. A paired DFlash drafter is never auto-selected — see below.
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
		return "draft-mtp"
	}
	return "ngram-mod"
}

// draftOverheadGB returns the VRAM overhead to charge for the active spec
// chain's draft model. A baked-in MTP nextn layer with no separate weights
// file is a flat ~0.34 GB (KV+compute). A separate draft gguf — an MTP
// sidecar (Gemma-4) or any DFlash block-diffusion drafter, which is always a
// separate file — charges its real on-disk weight size plus a small
// KV/compute pad instead, so large drafts scale up rather than under-counting.
func draftOverheadGB(spec string, draftSizeGB float64) float64 {
	switch {
	case specHas(spec, "draft-dflash"):
		return draftSizeGB + 0.1
	case specHas(spec, "draft-mtp"):
		if draftSizeGB > 0 {
			return draftSizeGB + 0.1
		}
		return 0.34
	default:
		return 0
	}
}

func formatCtxTag(ctx int) string {
	if ctx >= 1048576 && ctx%1048576 == 0 {
		return fmt.Sprintf("%dm", ctx/1048576)
	}
	if ctx >= 1024 && ctx%1024 == 0 {
		return fmt.Sprintf("%dk", ctx/1024)
	}
	return fmt.Sprintf("%d", ctx)
}

// slugify lowercases and collapses non-alphanumerics to single dashes.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// DefaultNow returns an RFC3339 timestamp for the generation header. Kept
// separate so Generate stays deterministic in tests.
func DefaultNow() string {
	return time.Now().Format(time.RFC3339)
}
