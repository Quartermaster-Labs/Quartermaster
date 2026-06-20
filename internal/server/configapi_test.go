package server

import (
	"testing"

	"github.com/mostlygeek/llama-swap/internal/autogen"
)

// applyOverrideDTO copies curated editor fields onto an Override without touching
// Match; verify the new ExtraArgs passthrough is trimmed and the variants list is
// rebuilt from the body.
func TestApplyOverrideDTO(t *testing.T) {
	ov := autogen.Override{Match: "/m/foo.gguf"}
	body := overrideDTO{
		Ctx:       8192,
		FlashAttn: "off",
		ExtraArgs: "  --rope-freq-scale 0.5  ",
		Variants:  []variantDTO{{Name: "long", Ctx: 65536}},
	}
	applyOverrideDTO(&ov, body)

	if ov.Match != "/m/foo.gguf" {
		t.Errorf("Match clobbered: %q", ov.Match)
	}
	if ov.ExtraArgs != "--rope-freq-scale 0.5" {
		t.Errorf("ExtraArgs = %q, want trimmed", ov.ExtraArgs)
	}
	if ov.Ctx != 8192 || ov.FlashAttn != "off" {
		t.Errorf("curated fields not copied: %+v", ov)
	}
	if len(ov.Variants) != 1 || ov.Variants[0].Name != "long" {
		t.Errorf("variants = %+v, want [long]", ov.Variants)
	}
}

// The override PUT seeds from the hand-authored file override (ResolveFileOverride)
// so file-only fields the editor doesn't model — ctxVariants, quant — survive into
// the sidecar row. applyOverrideDTO must leave them untouched; the regression was
// the saved sidecar shadowing the file row and dropping its ctx tiers.
func TestApplyOverrideDTO_PreservesFileOnlyFields(t *testing.T) {
	ov := autogen.Override{
		Match:       "/m/foo.gguf",
		Quant:       "IQ4_NL",
		CtxVariants: []int{32768, 65536},
	}
	body := overrideDTO{Ctx: 8192, Variants: []variantDTO{{Name: "judge", Ctx: 4096}}}
	applyOverrideDTO(&ov, body)

	if ov.Quant != "IQ4_NL" {
		t.Errorf("Quant dropped: %q", ov.Quant)
	}
	if len(ov.CtxVariants) != 2 || ov.CtxVariants[0] != 32768 || ov.CtxVariants[1] != 65536 {
		t.Errorf("CtxVariants dropped: %v", ov.CtxVariants)
	}
	if len(ov.Variants) != 1 || ov.Variants[0].Name != "judge" {
		t.Errorf("variants = %+v, want [judge]", ov.Variants)
	}
}

// estimateInputFromCmd parses the placement-relevant flags out of a rendered
// llama-server command so the status-rail estimate matches the running variant
// (ctx, checkpoints disabled, spec, kv quant, no-kv-offload).
func TestEstimateInputFromCmd(t *testing.T) {
	cmd := "llama-server -m /m/foo.gguf --port 8080 -ngl 99 -c 4096 " +
		"-ub 1024 -b 1024 -fa on -ctk q8_0 -ctv q8_0 --spec-type draft-mtp " +
		"--ctx-checkpoints 0 --no-kv-offload -t 8"
	in := estimateInputFromCmd(cmd)
	if in.Ctx != 4096 {
		t.Errorf("Ctx=%d want 4096", in.Ctx)
	}
	if in.CtxCheckpoints == nil || *in.CtxCheckpoints != 0 {
		t.Errorf("CtxCheckpoints=%v want 0", in.CtxCheckpoints)
	}
	if in.Spec != "draft-mtp" {
		t.Errorf("Spec=%q want draft-mtp", in.Spec)
	}
	if in.KvK != "q8_0" || in.KvV != "q8_0" {
		t.Errorf("Kv=%q/%q want q8_0/q8_0", in.KvK, in.KvV)
	}
	if !in.KvInRam {
		t.Error("KvInRam=false want true (--no-kv-offload present)")
	}

	// Omitted --ctx-checkpoints => nil (llama default applies downstream).
	bare := estimateInputFromCmd("llama-server -c 32768 -ctk q8_0 -ctv q8_0")
	if bare.CtxCheckpoints != nil {
		t.Errorf("CtxCheckpoints=%v want nil when flag absent", bare.CtxCheckpoints)
	}
	if bare.KvInRam {
		t.Error("KvInRam=true want false when --no-kv-offload absent")
	}
}
