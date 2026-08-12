package autogen

import "math"

// kvBytesPerElem is the approximate bytes-per-element of each KV cache quant
// (includes block-scale overhead).
var kvBytesPerElem = map[string]float64{
	"f32":    4.0,
	"f16":    2.0,
	"bf16":   2.0,
	"q8_0":   1.0625,
	"q5_1":   0.75,
	"q5_0":   0.6875,
	"q4_1":   0.625,
	"q4_0":   0.5625,
	"iq4_nl": 0.5625,
}

// kvQuantAllowed is the set of KV cache types quartermaster will emit for
// -ctk/-ctv. It is llama.cpp's own list (common/arg.cpp `kv_cache_types`) minus
// iq4_nl: every emitted command uses flash-attention, which has no iq4_nl KV
// kernel.
var kvQuantAllowed = map[string]bool{
	"f32": true, "f16": true, "bf16": true,
	"q8_0": true, "q5_1": true, "q5_0": true, "q4_1": true, "q4_0": true,
}

// ValidKvPair reports whether a K/V quant pair is emittable: both sides must be
// a known type and must match (flash-attention requires matched K/V).
func ValidKvPair(kvK, kvV string) bool {
	return kvK == kvV && kvQuantAllowed[kvK]
}

// defaultKvQuant picks the fleet-wide default KV cache type for an LLM when no
// per-model override is set. Quality-first: f16 is the baseline for EVERY arch,
// not just MoE — a quantized KV's damage shows up in long-context recall and
// multi-turn tool use long before it shows in perplexity, and quartermaster's
// whole point is that placement is computed, so KV precision shouldn't be the
// thing silently traded away. q8_0 is used only as a step-down, when f16 cannot
// reach DenseMinCtx inside the VRAM budget (there, a shrunken window is the worse
// loss). settings.kvQuant pins one type fleet-wide and skips the decision.
//
// bf16 is deliberately NOT the auto pick despite the same 2 bytes: it trades
// mantissa bits for exponent range that K/V activations don't need, and f16 is
// the native flash-attention path.
func defaultKvQuant(s Settings, meta Metadata, targetVramGB, overheadGB float64) string {
	if kvQuantAllowed[s.KvQuant] {
		return s.KvQuant
	}
	m := GetKvCostModel(meta, "f16", "f16")
	if !m.OK {
		return "f16"
	}
	minCtx := s.DenseMinCtx
	if minCtx <= 0 {
		minCtx = 32768
	}
	if meta.ContextLength > 0 && int(meta.ContextLength) < minCtx {
		minCtx = int(meta.ContextLength)
	}
	if MaxCtxForBudget(targetVramGB-meta.FileSizeGB-overheadGB, m.SlopeGB, m.ConstGB) >= minCtx {
		return "f16"
	}
	return "q8_0"
}

func kvByteWidth(quant string) float64 {
	if b, ok := kvBytesPerElem[quant]; ok {
		return b
	}
	return 1.0625
}

// KvCostModel expresses KV cache size as Slope*ctx + Const (GB). For non-SWA,
// non-hybrid archs Const is 0 (pure linear). GlobalLayers/LocalLayers/SsmLayers
// are diagnostics.
type KvCostModel struct {
	SlopeGB      float64 // GB per token
	ConstGB      float64 // GB, ctx-independent (SWA windows + SSM recurrent state)
	GlobalLayers int
	LocalLayers  int
	SsmLayers    int
	OK           bool // false when core dims are missing
}

// GetKvCostModel derives the KV cost from gguf attention dims and the chosen K/V
// quant. Port of Get-KvCostModel. Returns OK=false (caller falls back to a flat
// reserve) when core dims are missing.
func GetKvCostModel(meta Metadata, kvK, kvV string) KvCostModel {
	blocks := int(meta.BlockCount)
	kvHeads := int(meta.HeadCountKv)
	kLen := int(meta.KeyLength)
	vLen := int(meta.ValueLength)
	if blocks <= 0 || kvHeads <= 0 || kLen <= 0 || vLen <= 0 {
		return KvCostModel{OK: false}
	}
	bK := kvByteWidth(kvK)
	bV := kvByteWidth(kvV)

	win := int(meta.SlidingWindow)
	pattern := 6
	if meta.SlidingWinPattern > 0 {
		pattern = int(meta.SlidingWinPattern)
	}
	kLenL := kLen
	if meta.KeyLengthSwa > 0 {
		kLenL = int(meta.KeyLengthSwa)
	}
	vLenL := vLen
	if meta.ValueLengthSwa > 0 {
		vLenL = int(meta.ValueLengthSwa)
	}
	arr := meta.HeadCountKvArr // nil -> uniform kvHeads on every layer

	perTokGlobal := float64(kLen)*bK + float64(vLen)*bV
	perWinLocal := (float64(kLenL)*bK + float64(vLenL)*bV) * float64(win)

	// Hybrid SSM: full-attention (KV) layer only every Nth; the rest are linear
	// with a fixed recurrent state (ctx-independent constant).
	interval := 0
	if meta.FullAttnInterval > 0 {
		interval = int(meta.FullAttnInterval)
	}
	ssmInner := int(meta.SsmInnerSize)
	ssmConv := int(meta.SsmConvKernel)
	ssmState := int(meta.SsmStateSize)

	var slopeBytes, constBytes float64
	var globalLayers, localLayers, ssmLayers int
	for i := 0; i < blocks; i++ {
		if interval > 0 && ((i+1)%interval) != 0 {
			ssmLayers++
			continue
		}
		kvh := kvHeads
		if arr != nil && i < len(arr) {
			kvh = int(arr[i])
		}
		if kvh <= 0 {
			continue // conv / no-KV layer
		}
		isGlobal := win <= 0 || ((i+1)%pattern) == 0
		if isGlobal {
			slopeBytes += float64(kvh) * perTokGlobal
			globalLayers++
		} else {
			constBytes += float64(kvh) * perWinLocal
			localLayers++
		}
	}
	// Fixed recurrent state for SSM layers (stored f32, 4 bytes), ctx-independent.
	if ssmLayers > 0 && ssmInner > 0 {
		recElems := (ssmInner * ssmState) + (ssmInner * int(math.Max(0, float64(ssmConv-1))))
		constBytes += float64(ssmLayers) * float64(recElems) * 4.0
	}

	return KvCostModel{
		SlopeGB:      slopeBytes / gib,
		ConstGB:      constBytes / gib,
		GlobalLayers: globalLayers,
		LocalLayers:  localLayers,
		SsmLayers:    ssmLayers,
		OK:           true,
	}
}

// MaxCtxForBudget returns the largest ctx that fits a VRAM/RAM budget given the
// slope+const KV model. 0 when nothing fits.
func MaxCtxForBudget(budgetGB, slopeGB, constGB float64) int {
	avail := budgetGB - constGB
	if avail <= 0 || slopeGB <= 0 {
		return 0
	}
	return int(math.Floor(avail / slopeGB))
}

// KvReserveGB is the KV cache size (GB) for a chosen ctx.
func KvReserveGB(ctx int, slopeGB, constGB float64) float64 {
	return slopeGB*float64(ctx) + constGB
}

// RoundedCtx rounds ctx down to a multiple of 4096 (clean rope/cache
// boundaries), floored at 4096.
func RoundedCtx(ctx float64) int {
	r := int(math.Floor(ctx/4096)) * 4096
	if r < 4096 {
		return 4096
	}
	return r
}

// DenseCtxResult is the chosen dense context plus a human note.
type DenseCtxResult struct {
	Ctx  int
	Note string
}

// DenseCtxParams are inputs to GetDenseCtx.
type DenseCtxParams struct {
	ModelMax     int
	PerTokGB     float64
	KvConstGB    float64
	FileSizeGB   float64
	TargetVramGB float64
	Overhead     float64
	Ladder       []int
	MinCtx       int
	AllowOffload bool
}

// GetDenseCtx picks a dense model's context, speed-first: never offload layers
// just to grow context unless the model can't reach MinCtx on GPU (or the caller
// opts in via AllowOffload). Port of Get-DenseCtx.
func GetDenseCtx(p DenseCtxParams) DenseCtxResult {
	kvAt99 := p.TargetVramGB - p.FileSizeGB - p.Overhead
	if kvAt99 < 0.1 {
		kvAt99 = 0.1
	}
	ctxAt99 := MaxCtxForBudget(kvAt99, p.PerTokGB, p.KvConstGB)
	top := min(p.ModelMax, p.Ladder[0])

	// 1. Reaches the floor fully on GPU -> max ctx up to the ladder top. Fast.
	if ctxAt99 >= p.MinCtx {
		c := RoundedCtx(float64(min(min(p.ModelMax, top), ctxAt99)))
		note := "max-ctx (full gpu)"
		switch {
		case c >= p.ModelMax:
			note = "model-max"
		case c >= top:
			note = "ladder-top (full gpu)"
		}
		return DenseCtxResult{Ctx: c, Note: note}
	}

	weightsFitGpu := p.FileSizeGB <= (p.TargetVramGB - p.Overhead - 0.1)

	// 3 / manual opt-in: offload to reach the target window.
	if p.AllowOffload || !weightsFitGpu {
		c := RoundedCtx(float64(min(p.ModelMax, p.MinCtx)))
		return DenseCtxResult{Ctx: c, Note: "vram-limited (offload for ctx)"}
	}

	// 2. Weights fit but KV can't reach MinCtx on GPU -> stay full GPU, smaller ctx.
	c := RoundedCtx(float64(min(p.ModelMax, ctxAt99)))
	return DenseCtxResult{Ctx: c, Note: "max-ctx (full gpu, under min)"}
}
