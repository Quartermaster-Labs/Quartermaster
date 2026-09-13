package server

import (
	"io"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
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

// A custom flag owns its knob: the save path zeroes the structured field it
// shadows, so deleting the flag later cannot resurrect a stale pin (the mmap
// reset half of issue #38).
func TestApplyOverrideDTO_CustomArgsZeroShadows(t *testing.T) {
	var ov autogen.Override
	applyOverrideDTO(&ov, overrideDTO{
		Ctx:        8192,
		Mmap:       "off",
		Mlock:      true,
		CacheRamMB: 4096,
		CustomArgs: "--ctx-size 32768 --load-mode mmap",
	})
	if ov.Ctx != 0 {
		t.Errorf("ctx shadowed by --ctx-size was not zeroed: %d", ov.Ctx)
	}
	if ov.Mmap != "" || ov.Mlock {
		t.Errorf("load mode shadowed by --load-mode was not zeroed: %q/%v", ov.Mmap, ov.Mlock)
	}
	if ov.CacheRamMB != 4096 {
		t.Errorf("unshadowed cacheRamMB was cleared: %d", ov.CacheRamMB)
	}
	if ov.CustomArgs != "--ctx-size 32768 --load-mode mmap" {
		t.Errorf("CustomArgs was rewritten: %q", ov.CustomArgs)
	}
}

// The legacy extraArgs bucket is an effective custom text too, so a save from
// the old editor still zeroes what it shadows (-cram was the accumulation bug).
func TestApplyOverrideDTO_LegacyExtraArgsZeroShadows(t *testing.T) {
	var ov autogen.Override
	applyOverrideDTO(&ov, overrideDTO{CacheRamMB: 4096, ExtraArgs: "-cram 2048"})
	if ov.CacheRamMB != 0 {
		t.Errorf("cacheRamMB shadowed by legacy extraArgs was not zeroed: %d", ov.CacheRamMB)
	}
}

// The editor's off toggle keeps the text but applies and clears nothing.
func TestApplyOverrideDTO_CustomArgsOffChangesNothing(t *testing.T) {
	var ov autogen.Override
	applyOverrideDTO(&ov, overrideDTO{Ctx: 8192, CustomArgs: "--ctx-size 32768", CustomArgsOff: true})
	if ov.Ctx != 8192 {
		t.Errorf("disabled custom text zeroed a field: %d", ov.Ctx)
	}
	if !ov.CustomArgsOff || ov.CustomArgs == "" {
		t.Errorf("disabled text must be kept: %q off=%v", ov.CustomArgs, ov.CustomArgsOff)
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

// Regression: /estimate?actual=true seeds from the LOADED command, whose -c is
// the total pool and which the process really runs. Decoding it as a per-slot
// sizer input and re-rounding it through the 4096 ladder reported "4k" for a
// live `-c 5000` while the configured command said 126976: the panel disagreed
// with both the running process and the final-args pane.
func TestEstimateInputFromCmd_seededCtxIsExact(t *testing.T) {
	in := estimateInputFromCmd("llama-server -m x.gguf -ngl 99 -c 5000 --parallel 1 --kv-unified")
	if in.Ctx != 5000 {
		t.Fatalf("Ctx=%d want 5000", in.Ctx)
	}
	if !in.CtxExact {
		t.Fatal("CtxExact=false, want true for a window read off a real command")
	}

	// -c is the pool, so N slots divide it: the emitter writes ctx*parallel.
	multi := estimateInputFromCmd("llama-server -m x.gguf -c 8192 --parallel 2")
	if multi.Ctx != 4096 || multi.Parallel != 2 || !multi.CtxExact {
		t.Fatalf("multi: Ctx=%d Parallel=%d exact=%v, want 4096/2/true", multi.Ctx, multi.Parallel, multi.CtxExact)
	}
	if alias := estimateInputFromCmd("llama-server -m x.gguf -np 4 -c 16384"); alias.Ctx != 4096 || alias.Parallel != 4 {
		t.Fatalf("alias: Ctx=%d Parallel=%d, want 4096/4", alias.Ctx, alias.Parallel)
	}
	// No -c => the sizer is still free to pick.
	if bare := estimateInputFromCmd("llama-server -m x.gguf -ngl 99"); bare.Ctx != 0 || bare.CtxExact {
		t.Fatalf("bare: Ctx=%d exact=%v, want 0/false", bare.Ctx, bare.CtxExact)
	}

	// The number the panel shows: the same settings+metadata that rounded 5000 to
	// 4096 (TestPinnedCtxIsExact) must report 5000 for the seeded input.
	meta := autogen.Metadata{
		Architecture: "llama", BlockCount: 32, HeadCountKv: 8,
		KeyLength: 128, ValueLength: 128,
		FileSizeGB: 4.0, ContextLength: 32768,
	}
	s := autogen.Settings{TargetVramGB: 40, VramOverheadGB: 1, ComputeBufFactor: 1}
	plan, err := autogen.EstimatePlan(s, meta, in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ctx != 5000 {
		t.Fatalf("seeded plan ctx = %d, want 5000 (unrounded)", plan.Ctx)
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

// A key's scope must lose model ids that no longer exist in the catalog (a
// deleted or renamed gguf), or the key advertises ghosts forever. The guards
// matter more than the prune: an empty catalog, or a key whose every id went
// missing, is left alone — emptying a scope reads as "full access".
func TestPruneDeadKeyScopes(t *testing.T) {
	newSrv := func(models ...string) *Server {
		s := &Server{proxylog: logmon.NewWriter(io.Discard)}
		mc := map[string]config.ModelConfig{}
		for _, m := range models {
			mc[m] = config.ModelConfig{}
		}
		s.cfg.Store(&config.Config{Models: mc})
		return s
	}
	keys := func(models ...string) []autogen.APIKeyEntry {
		return []autogen.APIKeyEntry{{Name: "pi", Key: "qm-1", Models: models}}
	}

	t.Run("drops ids missing from the catalog", func(t *testing.T) {
		got, changed := newSrv("live", "other").pruneDeadKeyScopes(keys("live", "dead"))
		if !changed || len(got[0].Models) != 1 || got[0].Models[0] != "live" {
			t.Fatalf("models = %v changed=%v, want [live] true", got[0].Models, changed)
		}
	})

	t.Run("empty catalog prunes nothing", func(t *testing.T) {
		got, changed := newSrv().pruneDeadKeyScopes(keys("live", "dead"))
		if changed || len(got[0].Models) != 2 {
			t.Fatalf("models = %v changed=%v, want both kept", got[0].Models, changed)
		}
	})

	t.Run("an all-dead scope is kept rather than emptied", func(t *testing.T) {
		got, changed := newSrv("other").pruneDeadKeyScopes(keys("gone1", "gone2"))
		if changed || len(got[0].Models) != 2 {
			t.Fatalf("models = %v changed=%v, want both kept (empty = full access)", got[0].Models, changed)
		}
	})

	t.Run("unscoped key untouched", func(t *testing.T) {
		got, changed := newSrv("live").pruneDeadKeyScopes(keys())
		if changed || len(got[0].Models) != 0 {
			t.Fatalf("models = %v changed=%v, want unscoped", got[0].Models, changed)
		}
	})
}
