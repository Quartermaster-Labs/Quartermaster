package autogen

import (
	"math"
	"strings"
	"testing"
)

// qwen38Meta is Qwen3.8-27B-UD-Q2_K_XL as its gguf reports it: a hybrid
// GatedDeltaNet with one full-attention layer in four, and ONE baked-in nextn
// (MTP) layer. It is the model that exposed the flat draft charge.
func qwen38Meta() Metadata {
	return Metadata{
		Architecture: "qwen35", BlockCount: 65, HeadCountKv: 4,
		KeyLength: 256, ValueLength: 256, FullAttnInterval: 4,
		FileSizeGB: 9.154, ContextLength: 262144,
		IsMTP: true, NextnLayers: 1,
	}
}

// The baked-in MTP drafter is a second context over the same model at the same
// n_ctx, so its KV is a per-token cost over its nextn layers, not a constant.
func TestMtpDraftSlopeGB(t *testing.T) {
	meta := qwen38Meta()

	// 1 nextn layer x 4 kv heads x (256 K + 256 V) x 2 bytes = 4096 B/token.
	wantF16 := 4096.0 / gib
	if got := mtpDraftSlopeGB(meta, "f16", "f16"); math.Abs(got-wantF16) > 1e-12 {
		t.Fatalf("f16 slope=%g want %g", got, wantF16)
	}
	// Matching the draft cache to a q8_0 main cache is what -ctkd buys: at the
	// 159744-token window this model sizes to, 0.61 GB becomes 0.32 GB.
	q8 := mtpDraftSlopeGB(meta, "q8_0", "q8_0")
	if ratio := q8 / wantF16; math.Abs(ratio-1.0625/2) > 1e-9 {
		t.Fatalf("q8_0/f16 slope ratio=%g want %g", ratio, 1.0625/2)
	}
	if gb := wantF16 * 159744; math.Abs(gb-0.6094) > 1e-3 {
		t.Fatalf("f16 draft KV at 159744 ctx = %.4f GB, want ~0.61", gb)
	}

	// No nextn layers (or no dims): nothing to charge.
	plain := meta
	plain.NextnLayers, plain.IsMTP = 0, false
	if got := mtpDraftSlopeGB(plain, "f16", "f16"); got != 0 {
		t.Fatalf("non-MTP model charged %g", got)
	}

	// The gate follows the emitter: a baked-in head scales, a separate draft
	// gguf does not (it is charged its own weights by draftOverheadGB), and a
	// spec chain without draft-mtp charges nothing at all.
	if got := mtpDraftSlopeFor(meta, "draft-mtp+ngram-mod", "f16", "f16", 0); got != mtpDraftSlopeGB(meta, "f16", "f16") {
		t.Fatalf("baked-in MTP not charged: %g", got)
	}
	if got := mtpDraftSlopeFor(meta, "draft-mtp+ngram-mod", "f16", "f16", 0.46); got != 0 {
		t.Fatalf("sidecar draft charged the baked-in slope: %g", got)
	}
	if got := mtpDraftSlopeFor(meta, "ngram-mod", "f16", "f16", 0); got != 0 {
		t.Fatalf("non-draft spec charged %g", got)
	}
}

// The drafter's KV must scale with the window in the estimate, and must be
// reported under Draft rather than inflating the main KV segment.
func TestEstimatePlan_mtpDraftIsCtxScaled(t *testing.T) {
	meta := qwen38Meta()
	s := Settings{TargetVramGB: 40, VramOverheadGB: 1, ComputeBufFactor: 1}
	s.applyDefaults()
	slope := mtpDraftSlopeGB(meta, "q8_0", "q8_0") // -ctkd defaults to the main quant

	small, err := EstimatePlan(s, meta, EstimateInput{Ctx: 32768, KvK: "q8_0", KvV: "q8_0"})
	if err != nil {
		t.Fatal(err)
	}
	big, err := EstimatePlan(s, meta, EstimateInput{Ctx: 131072, KvK: "q8_0", KvV: "q8_0"})
	if err != nil {
		t.Fatal(err)
	}
	if small.Ctx != 32768 || big.Ctx != 131072 {
		t.Fatalf("ctx not honored: %d / %d", small.Ctx, big.Ctx)
	}
	wantDelta := slope * float64(big.Ctx-small.Ctx)
	if got := big.DraftGB - small.DraftGB; math.Abs(got-wantDelta) > 1e-9 {
		t.Fatalf("draft charge delta=%.4f want %.4f (flat charge would be 0)", got, wantDelta)
	}
	// Pad + KV, not one or the other.
	wantSmall := mtpDraftPadGB + slope*float64(small.Ctx)
	if math.Abs(small.DraftGB-wantSmall) > 1e-9 {
		t.Fatalf("draft charge=%.4f want %.4f", small.DraftGB, wantSmall)
	}
	// The KV segment stays the MAIN cache: the drafter's share is split back out.
	m := GetKvCostModel(meta, "q8_0", "q8_0")
	if want := KvReserveGB(big.Ctx, m.SlopeGB, m.ConstGB); math.Abs(big.KvReserveGB-want) > 1e-9 {
		t.Fatalf("kv reserve=%.4f want %.4f (drafter must not land in the KV segment)", big.KvReserveGB, want)
	}
}

// -ctkd/-ctvd are emitted for ANY active draft spec, defaulted to the model's
// own -ctk/-ctv. A baked-in MTP head attaches no -md but still gets a draft
// context, and llama defaults that context's cache to f16 whatever -ctk says.
func TestBuildCmdLines_draftKvFollowsMain(t *testing.T) {
	s := Settings{}
	s.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	join := func(meta Metadata, ov *Override) string {
		return strings.Join(buildCmdLines(s, meta, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, ov), " ")
	}

	got := join(qwen38Meta(), &Override{})
	if !strings.Contains(got, "--spec-type draft-mtp") {
		t.Fatalf("test assumption broken, no MTP spec: %s", got)
	}
	if !strings.Contains(got, "-ctkd q8_0 -ctvd q8_0") {
		t.Fatalf("baked-in MTP draft KV not matched to the main quant: %s", got)
	}
	// An explicit override still wins on either side.
	got = join(qwen38Meta(), &Override{KvKDraft: "f16", KvVDraft: "f16"})
	if !strings.Contains(got, "-ctkd f16 -ctvd f16") {
		t.Fatalf("draft KV override ignored: %s", got)
	}
	// No draft backend in the chain: no draft context, no flags.
	if got := join(Metadata{Architecture: "qwen35"}, &Override{}); strings.Contains(got, "-ctkd") {
		t.Fatalf("draft KV emitted without a draft spec: %s", got)
	}
}
