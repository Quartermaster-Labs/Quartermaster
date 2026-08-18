package server

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
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
// so file-only fields the editor still doesn't model — quant — survive into the
// sidecar row; applyOverrideDTO must leave Quant untouched. CtxVariants is now
// editor-modeled, so the body is authoritative for it (the GET returns the tiers,
// the editor round-trips them) and applyOverrideDTO rebuilds it from the body.
func TestApplyOverrideDTO_PreservesFileOnlyFields(t *testing.T) {
	ov := autogen.Override{
		Match:       "/m/foo.gguf",
		Quant:       "IQ4_NL",
		CtxVariants: []int{131072}, // stale file value; the body should replace it
	}
	body := overrideDTO{
		Ctx:         8192,
		CtxVariants: []int{32768, 65536},
		Variants:    []variantDTO{{Name: "judge", Ctx: 4096}},
	}
	applyOverrideDTO(&ov, body)

	if ov.Quant != "IQ4_NL" {
		t.Errorf("Quant dropped: %q", ov.Quant)
	}
	if len(ov.CtxVariants) != 2 || ov.CtxVariants[0] != 32768 || ov.CtxVariants[1] != 65536 {
		t.Errorf("CtxVariants = %v, want [32768 65536] from body", ov.CtxVariants)
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

	// Omitted checkpoint flags => llama-server's OWN defaults, not ours: a cmd
	// without them really does run 32 snapshots at 8192 spacing, and charging our
	// arch defaults (3 at 256) under-reserved the preview against the launch.
	bare := estimateInputFromCmd("llama-server -c 32768 -ctk q8_0 -ctv q8_0")
	if bare.CtxCheckpoints == nil || *bare.CtxCheckpoints != autogen.LlamaDefaultCtxCheckpoints {
		t.Errorf("CtxCheckpoints=%v want %d when flag absent", bare.CtxCheckpoints, autogen.LlamaDefaultCtxCheckpoints)
	}
	if bare.CheckpointMinStep != autogen.LlamaDefaultCheckpointMinStep {
		t.Errorf("CheckpointMinStep=%d want %d when -cms absent", bare.CheckpointMinStep, autogen.LlamaDefaultCheckpointMinStep)
	}
	if withCms := estimateInputFromCmd("llama-server -c 32768 -cms 512"); withCms.CheckpointMinStep != 512 {
		t.Errorf("CheckpointMinStep=%d want 512 when -cms pinned", withCms.CheckpointMinStep)
	}
	if bare.KvInRam {
		t.Error("KvInRam=true want false when --no-kv-offload absent")
	}
}

// Regression: these parsers used to whitespace-split the rendered command, so a
// quoted model path containing a space (the norm on Windows — "C:\Program
// Files\...", "D:\LLM\My Models\...") shredded into two tokens and every flag
// read after it landed on the wrong argument. They now share the process
// layer's own splitter (cmdArgv -> config.SanitizeCommand).
func TestCmdParsers_SpacedPaths(t *testing.T) {
	// Forward slashes so the assertion holds under both shlex dialects (POSIX
	// treats a backslash as an escape); the space is what is being tested.
	cmd := `"C:/Program Files/llama/llama-server.exe" -m "D:/LLM/My Models/foo.gguf" ` +
		`--mmproj "D:/LLM/My Models/mmproj f16.gguf" -ngl 60 -c 4096 --port 9099`

	if in := estimateInputFromCmd(cmd); in.Ctx != 4096 {
		t.Errorf("Ctx=%d want 4096 (flag after a spaced path)", in.Ctx)
	}
	if n, ok := forcedOffloadFromCmd(cmd, autogen.Metadata{BlockCount: 65}); !ok || n != 5 {
		t.Errorf("forcedOffload = (%d,%v), want (5,true)", n, ok)
	}
	if got, want := mmprojPathFromCmd(cmd), "D:/LLM/My Models/mmproj f16.gguf"; got != want {
		t.Errorf("mmproj=%q want %q", got, want)
	}
	if got := portFromCmd(cmd); got != "9099" {
		t.Errorf("port=%q want 9099", got)
	}
}

// forcedOffloadFromCmd maps a running argv's layer split to EstimateInput.CpuOffload
// so the settings preview reproduces the loaded placement (post spawn-time guard).
func TestForcedOffloadFromCmd(t *testing.T) {
	// Dense: 60 of 65 layers on GPU => 5 pinned to CPU.
	dense := autogen.Metadata{BlockCount: 65}
	if n, ok := forcedOffloadFromCmd("llama-server -m x.gguf -ngl 60 -c 8192", dense); !ok || n != 5 {
		t.Errorf("dense -ngl 60/65: got (%d,%v), want (5,true)", n, ok)
	}
	// Dense fully on GPU (-ngl 99 clamps to blocks) => 0 offloaded.
	if n, ok := forcedOffloadFromCmd("llama-server -ngl 99", dense); !ok || n != 0 {
		t.Errorf("dense -ngl 99: got (%d,%v), want (0,true)", n, ok)
	}
	// MoE: --n-cpu-moe is the offload count directly.
	moe := autogen.Metadata{BlockCount: 48, IsMoE: true}
	if n, ok := forcedOffloadFromCmd("llama-server -ngl 99 --n-cpu-moe 7", moe); !ok || n != 7 {
		t.Errorf("moe --n-cpu-moe 7: got (%d,%v), want (7,true)", n, ok)
	}
	// No placement flag => not forced.
	if _, ok := forcedOffloadFromCmd("llama-server -m x.gguf -c 8192", dense); ok {
		t.Error("no -ngl/--n-cpu-moe: want ok=false")
	}
}
