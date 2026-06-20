package autogen

import (
	"fmt"
	"math"
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
	Ctx      int // 0 = auto-size; >0 forces that ctx via the manual-cap path
	Aliases  []string
	IsLong   bool // ctx-tier rung >= 64k (drops -ub to 512)
	// Per-variant overrides. Empty/zero => inherit the model-wide override. Set
	// only by named custom variants (Override.Variants); emitProfile and the
	// kv-cost sizing prefer these over the model-wide values.
	KvK, KvV     string
	Spec         string
	ReasoningFmt string
	Ub           int   // physical batch size override (0 => default)
	Dry          *bool // nil => DRY on; non-nil false => omit DRY sampler
	CpuOffload   int   // >0 pins layers offloaded to CPU, overriding the sizer
	// CtxCheckpoints, when non-nil, emits --ctx-checkpoints N (0 disables the KV
	// prompt-prefix checkpoint cache). nil => inherit the model-wide value, else
	// the llama-server default (32). See effectiveCtxCheckpoints.
	CtxCheckpoints *int
	// Variant, when non-nil, is the named-variant source this profile was built
	// from. Its engine knobs (kvInRam/flash/mmap/mlock/threads/parallel/extraArgs)
	// layer over the model-wide override at emit so a variant carries the full
	// launch shape. Solo/ctx-tier profiles leave this nil and use the override.
	Variant *VariantSpec
}

// Generate discovers models under gf.Settings.ModelsRoot and returns a complete
// llama-quartermaster config YAML. Port of Generate-Config.ps1. nowRFC is stamped into
// the header comment (passed in so the function stays deterministic/testable).
func Generate(gf GenerateFile, nowRFC string) (string, error) {
	s := gf.Settings
	rows, err := DiscoverGgufModels(s.ModelsRoot)
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

		if err := emitModel(&b, s, gf, row, ov, name, &emitted); err != nil {
			return "", err
		}
	}

	emitGroupsAndListeners(&b, s, emitted)
	return b.String(), nil
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

	// KV quant: forced q8_0 for all archs (per-model override still wins). Fast
	// flash-attention needs matched K/V and never iq4_nl.
	kvK, kvV := "q8_0", "q8_0"
	if ov != nil && ov.KvK != "" {
		kvK = ov.KvK
	}
	if ov != nil && ov.KvV != "" {
		kvV = ov.KvV
	}
	if kvK != kvV || kvK == "iq4_nl" || kvV == "iq4_nl" {
		kvK, kvV = "q8_0", "q8_0"
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

	modelSpec := effectiveSpec(meta, ov)

	specOh := 0.0
	if modelSpec == "draft-mtp" {
		specOh = 0.34
	}
	const ubSoloOh = 0.17

	var aliases []string
	var ctxVariants []int
	override := Override{}
	if ov != nil {
		override = *ov
		aliases = ov.Aliases
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
		Overhead:       s.VramOverheadGB + ubSoloOh + specOh,
		Unlisted:       override.Unlisted,
		Ctx:            override.Ctx,
		Aliases:        aliases,
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
			Overhead:       s.VramOverheadGB + ubSoloOh + specOh,
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
	for i := range variantSpecs {
		v := variantSpecs[i]
		if strings.TrimSpace(v.Name) == "" {
			continue
		}
		vTarget := s.TargetVramGB
		if v.VramTargetGB > 0 {
			vTarget = v.VramTargetGB
		}
		vSpecOh := 0.0
		if v.Spec == "draft-mtp" {
			vSpecOh = 0.34
		}
		vCheckpoints := v.CtxCheckpoints
		if vCheckpoints == nil {
			vCheckpoints = override.CtxCheckpoints
		}
		profiles = append(profiles, profile{
			Name:           fmt.Sprintf("%s-%s", name, slugify(v.Name)),
			Target:         vTarget,
			Overhead:       s.VramOverheadGB + ubSoloOh + vSpecOh,
			Unlisted:       v.Unlisted,
			Ctx:            v.Ctx,
			Aliases:        v.Aliases,
			IsLong:         v.Ctx >= 65536,
			KvK:            v.KvK,
			KvV:            v.KvV,
			Spec:           v.Spec,
			ReasoningFmt:   v.ReasoningFmt,
			Ub:             v.Ub,
			Dry:            v.Dry,
			CpuOffload:     v.CpuOffload,
			CtxCheckpoints: vCheckpoints,
			Variant:        &variantSpecs[i],
		})
	}

	for _, prof := range profiles {
		// Per-variant kv quant changes the KV cost model, so resolve effective
		// kv and re-derive the cost slope/const for this profile (defaults to the
		// model-wide values when the variant doesn't override kv).
		ekvK, ekvV := kvK, kvV
		if prof.KvK != "" {
			ekvK = prof.KvK
		}
		if prof.KvV != "" {
			ekvV = prof.KvV
		}
		if ekvK != ekvV || ekvK == "iq4_nl" || ekvV == "iq4_nl" {
			ekvK, ekvV = "q8_0", "q8_0"
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
		if prof.Variant != nil {
			pkvInRam = prof.Variant.KvInRam
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
		ctx = RoundedCtx(float64(minInt(modelMax, maxCtxRam)))
		if prof.Ctx != 0 {
			ctx = minInt(ctx, prof.Ctx)
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)

	case perTokGB > 0:
		// Context checkpoints live in the KV (VRAM) buffer alongside the cache.
		// Reserve their VRAM up front so ctx sizing + layer placement don't
		// overcommit and spill into sysmem (which tanks decode speed).
		ckptCtxCeil := modelMax
		if prof.Ctx != 0 {
			ckptCtxCeil = minInt(ckptCtxCeil, prof.Ctx)
		}
		overhead += checkpointReserveGB(prof, perTokGB, kvConstGB, ckptCtxCeil)
		if meta.IsMoE {
			share := effectiveShare(meta, genMoeShareFor)
			nonExpert := meta.FileSizeGB * (1.0 - share)
			usableBase := target - 0.25 - overhead
			if meta.FileSizeGB <= usableBase {
				kvBudget := target - meta.FileSizeGB - overhead
				if kvBudget < 0.1 {
					kvBudget = 0.1
				}
				ctx = RoundedCtx(float64(minInt(modelMax, MaxCtxForBudget(kvBudget, perTokGB, kvConstGB))))
			} else {
				desiredCtx := s.MoeCtxTarget
				if prof.Ctx != 0 {
					desiredCtx = prof.Ctx
				}
				maxKvVram := target - nonExpert - overhead
				if maxKvVram < 0.1 {
					maxKvVram = 0.1
				}
				maxCtxVram := MaxCtxForBudget(maxKvVram, perTokGB, kvConstGB)
				ctx = RoundedCtx(float64(minInt(minInt(modelMax, desiredCtx), maxCtxVram)))
			}
			if prof.Ctx != 0 {
				ctx = minInt(ctx, prof.Ctx)
			}
		} else {
			ladder := s.DenseCtxLadder
			minCtx := s.DenseMinCtx
			if prof.Ctx != 0 {
				ladder = []int{prof.Ctx}
				minCtx = prof.Ctx
			}
			d := GetDenseCtx(DenseCtxParams{
				ModelMax: modelMax, PerTokGB: perTokGB, KvConstGB: kvConstGB,
				FileSizeGB: meta.FileSizeGB, TargetVramGB: target, Overhead: overhead,
				Ladder: ladder, MinCtx: minCtx, AllowOffload: prof.Ctx != 0,
			})
			ctx = d.Ctx
			if prof.Ctx != 0 {
				ctx = minInt(ctx, prof.Ctx)
			}
		}
		kvReserve = KvReserveGB(ctx, perTokGB, kvConstGB)
		plan, err = GetLoadPlan(meta, planOpt(target, s.MaxRamGB, kvReserve, overhead))
		if err != nil {
			return
		}

	default:
		ctx = RoundedCtx(float64(minInt(modelMax, 32768)))
		if prof.Ctx != 0 {
			ctx = minInt(ctx, prof.Ctx)
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
// the flag is omitted (PR #15293).
const llamaDefaultCtxCheckpoints = 32

// checkpointMinStep mirrors llama-server's --checkpoint-min-step default — the
// minimum prompt-token spacing between context checkpoints. We don't model the
// flag, so the default is assumed for the per-checkpoint global-KV term.
const checkpointMinStep = 256

// effectiveCtxCheckpoints resolves the checkpoint count a profile will actually
// run with: an explicit value (incl. 0 = disabled) when set, else the
// llama-server default.
func effectiveCtxCheckpoints(prof profile) int {
	if prof.CtxCheckpoints != nil {
		return *prof.CtxCheckpoints
	}
	return llamaDefaultCtxCheckpoints
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
func checkpointReserveGB(prof profile, perTokGB, kvConstGB float64, ctxCeil int) float64 {
	n := effectiveCtxCheckpoints(prof)
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

// buildCmdLines returns the launch command as a list of flag lines (no leading
// indentation), shared by emitProfile (which writes them as a YAML `cmd: >`
// block) and RenderSoloCmd (which joins them for the editor preview). Any
// Override.ExtraArgs are appended verbatim as a final line.
func buildCmdLines(s Settings, meta Metadata, row GgufRow, prof profile, ctx, ngl, ncpuMoe int, kvK, kvV string, kvInRam bool, ov *Override) []string {
	cpuMoeFlag := ""
	if ncpuMoe > 0 {
		cpuMoeFlag = fmt.Sprintf(" --n-cpu-moe %d", ncpuMoe)
	}
	cpuOverride := ncpuMoe > 0
	fullGpu := ncpuMoe == 0 && ngl >= int(meta.BlockCount)

	mmap := ""
	if ov != nil {
		mmap = ov.Mmap
	}
	noMmapFlag := ""
	if mmap == "off" || (mmap != "on" && (fullGpu || cpuOverride)) {
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
	parallel := 1
	if ov != nil && ov.Parallel > 0 {
		parallel = ov.Parallel
	}
	threads := s.Threads
	if ov != nil && ov.Threads > 0 {
		threads = ov.Threads
	}

	ub := 1024
	if prof.IsLong {
		ub = 512
	}
	if ov != nil && ov.Ub > 0 {
		ub = ov.Ub
	}
	if prof.Ub > 0 {
		ub = prof.Ub
	}

	spec := effectiveSpec(meta, ov)
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
		fmt.Sprintf("-ub %d -b %d", ub, ub),
		fmt.Sprintf("-fa %s -ctk %s -ctv %s", fa, kvK, kvV),
		fmt.Sprintf("--parallel %d %s%s%s--kv-unified --no-warmup --no-webui", parallel, noMmapFlag, mlockFlag, kvoFlag),
		fmt.Sprintf("--spec-type %s", spec),
	}
	if spec == "draft-mtp" {
		lines = append(lines, "--spec-draft-n-max 2")
	}
	lines = append(lines, fmt.Sprintf("--jinja --reasoning-format %s%s", rfmt, reasoningFlag))
	if prof.CtxCheckpoints != nil {
		lines = append(lines, fmt.Sprintf("--ctx-checkpoints %d", *prof.CtxCheckpoints))
	}
	if prof.Dry == nil || *prof.Dry {
		lines = append(lines, "--dry-multiplier 0.8 --dry-base 1.75 --dry-allowed-length 3")
	}
	lines = append(lines, fmt.Sprintf("-t %d%s", threads, cpuMoeFlag))
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
	specOh := 0.0
	if effectiveSpec(meta, &ov) == "draft-mtp" {
		specOh = 0.34
	}
	const ubSoloOh = 0.17
	prof := profile{
		Name:       "preview",
		Target:     target,
		Overhead:   s.VramOverheadGB + ubSoloOh + specOh,
		Ctx:        ov.Ctx,
		CpuOffload: ov.CpuOffload,
	}
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
	if len(prof.Aliases) > 0 {
		b.WriteString("    aliases:\n")
		for _, al := range prof.Aliases {
			fmt.Fprintf(b, "      - %q\n", al)
		}
	}
}

// formatCtxTag renders a short ctx tag: 8192->"8k", 131072->"128k", 1048576->"1m".
// effectiveSpec resolves the spec-type: MTP-capable models default to draft-mtp, others to
// ngram-mod. An explicit override spec wins (set spec: "ngram-mod" to force ngram on an MTP model).
func effectiveSpec(meta Metadata, ov *Override) string {
	spec := "ngram-mod"
	if meta.IsMTP {
		spec = "draft-mtp"
	}
	if ov != nil && ov.Spec != "" {
		spec = ov.Spec
	}
	return spec
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
