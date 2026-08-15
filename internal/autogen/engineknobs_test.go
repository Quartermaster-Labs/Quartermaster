package autogen

import (
	"strings"
	"testing"
)

// emitProfile should surface the per-model engine knobs (flash attention, mmap,
// mlock, threads, parallel, batch) as the matching llama-server flags. Pure unit
// test on emit — no real gguf needed.
func TestEmitProfile_EngineKnobs(t *testing.T) {
	var b strings.Builder
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	prof := profile{Name: "foo"}
	ov := &Override{FlashAttn: "off", Mmap: "off", Mlock: true, Threads: 12, Parallel: 4, Ub: 256}

	emitProfile(&b, s, meta, row, prof, 8192, 10 /*ngl*/, 0 /*ncpuMoe*/, LoadPlan{}, "q8_0", "q8_0", false, ov)
	out := b.String()

	// -b is decoupled from -ub now: clamps up to 2048 (>=ub, <=ctx=8192).
	for _, want := range []string{"-fa off", "--load-mode mlock", "-t 12", "--parallel 4", "-ub 256 -b 2048"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in emitted cmd:\n%s", want, out)
		}
	}
}

// computeBufferGB models logits (vocab*min(ub,1024)) + activations (ub*embd) +
// CUDA ctx, scaled by factor; falls back to a flat estimate when dims are
// missing. The vocab term scales with ub up to a 1024 ceiling — empirically the
// compute buffer grows with the physical batch on large-vocab models, so a small
// cap made the estimate ub-blind and the sizer overfilled VRAM at ub=1024.
func TestComputeBufferGB(t *testing.T) {
	meta := Metadata{EmbeddingLength: 4096, VocabSize: 151936}

	// ub=1024: 0.3 CUDA + logits(vocab*1024*4 ~0.58) + acts(ub*embd*8*4 ~0.125).
	got := computeBufferGB(meta, 1024, 1.0)
	if got < 0.95 || got > 1.06 {
		t.Errorf("ub=1024 factor=1: got %.3f, want ~1.0 GB", got)
	}

	// Halving ub halves the (now dominant) vocab term too, so the buffer shrinks
	// substantially — the whole point of un-flattening the ub scaling.
	half := computeBufferGB(meta, 512, 1.0)
	if half >= got-0.25 || half < 0.55 {
		t.Errorf("ub=512 should be well below ub=1024: got %.3f (ub1024=%.3f)", half, got)
	}

	// factor scales the analytic term (~0.7 GB here) above the fixed CUDA ctx.
	if d := computeBufferGB(meta, 1024, 2.0) - got; d < 0.5 {
		t.Errorf("factor=2 should add ~one analytic term (~0.7 GB), added %.3f", d)
	}

	// Missing dims => flat fallback.
	if fb := computeBufferGB(Metadata{}, 1024, 1.0); fb != computeFallbackGB {
		t.Errorf("missing dims: got %.3f, want fallback %.3f", fb, computeFallbackGB)
	}

	// Non-CUDA GPU (Vulkan/ROCm) drops the fixed CUDA-context constant.
	cudaGPU.Store(false)
	defer cudaGPU.Store(true)
	if d := got - computeBufferGB(meta, 1024, 1.0); d < computeCudaCtxGB-0.001 || d > computeCudaCtxGB+0.001 {
		t.Errorf("non-CUDA should drop the %.2f CUDA-ctx constant, dropped %.3f", computeCudaCtxGB, d)
	}
}

// mmap is placement-gated: on when CPU offload happens (--n-cpu-moe or partial
// layer offload), --load-mode none when fully GPU-resident. Explicit Mmap:on/off
// wins. mmap-on is llama-server's own default, so it emits no flag at all.
func TestEmitProfile_MmapDefaultOn(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600, MaxRamGB: 32}
	meta := Metadata{Architecture: "qwen3moe", BlockCount: 48, IsMoE: true}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	prof := profile{Name: "foo"}

	// CPU offload, no override: mmap stays on, so no load-mode flag at all.
	var def strings.Builder
	emitProfile(&def, s, meta, row, prof, 8192, 99, 20, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, nil)
	if strings.Contains(def.String(), "--load-mode") {
		t.Errorf("mmap should default on (no --load-mode) even with CPU offload:\n%s", def.String())
	}

	// Explicit Mmap:"off" emits --load-mode none.
	var off strings.Builder
	emitProfile(&off, s, meta, row, prof, 8192, 99, 20, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, &Override{Mmap: "off"})
	if !strings.Contains(off.String(), "--load-mode none") {
		t.Errorf("explicit Mmap:off should emit --load-mode none:\n%s", off.String())
	}

	// Fully GPU-offloaded (ngl > blocks, no --n-cpu-moe): default --load-mode none.
	var full strings.Builder
	emitProfile(&full, s, meta, row, prof, 8192, 99, 0, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, nil)
	if !strings.Contains(full.String(), "--load-mode none") {
		t.Errorf("full offload should default --load-mode none:\n%s", full.String())
	}

	// Explicit Mmap:"on" overrides the full-offload default.
	var on strings.Builder
	emitProfile(&on, s, meta, row, prof, 8192, 99, 0, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, &Override{Mmap: "on"})
	if strings.Contains(on.String(), "--load-mode") {
		t.Errorf("explicit Mmap:on should suppress the load-mode flag on full offload:\n%s", on.String())
	}
}

// ExtraArgs are appended verbatim to the emitted command (passthrough for flags
// autogen doesn't model), after the computed flags.
func TestEmitProfile_ExtraArgs(t *testing.T) {
	var b strings.Builder
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	ov := &Override{ExtraArgs: "--rope-freq-scale 0.5 --override-kv x=int:1"}

	emitProfile(&b, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, ov)
	out := b.String()

	if !strings.Contains(out, "--rope-freq-scale 0.5 --override-kv x=int:1") {
		t.Errorf("missing extra args in emit:\n%s", out)
	}
}

// Advanced / power-user knobs each emit their flag when set, and emit NOTHING
// when unset (zero/empty => existing configs unchanged).
func TestEmitProfile_AdvancedKnobs(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}

	// Unset: none of the advanced flags appear.
	var def strings.Builder
	emitProfile(&def, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, &Override{})
	for _, unwanted := range []string{"-tb ", "--prio", "-dio", "--no-op-offload", "--no-repack", "--cache-reuse", "-cram", "--cache-idle-slots", "--swa-full", "-cms", "--context-shift", "--spec-draft-n-min", "-sps", "--rope-scaling", "-sm ", "-ts ", "-mg ", "-ot "} {
		if strings.Contains(def.String(), unwanted) {
			t.Errorf("unexpected %q emitted for a blank override:\n%s", unwanted, def.String())
		}
	}

	// Set: each flag renders.
	ov := &Override{
		ThreadsBatch: 12, Prio: 2, DirectIo: true, NoOpOffload: true, NoRepack: true,
		CacheReuse: 256, CacheRamMB: 4096, CacheIdleSlots: "off", SwaFull: true,
		CheckpointMinStep: 2048, ContextShift: "on", SpecDraftNMin: 1, SlotPromptSimilarity: 0.5,
		RopeScaling: "yarn", RopeScale: 2, RopeFreqBase: 1000000, YarnOrigCtx: 4096,
		SplitMode: "row", TensorSplit: "3,1", MainGpu: 1, OverrideTensor: "exps=CPU",
	}
	var on strings.Builder
	emitProfile(&on, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, ov)
	out := on.String()
	for _, want := range []string{"-tb 12", "--prio 2", "--no-op-offload", "--no-repack", "--cache-reuse 256", "--load-mode dio", "-cram 4096", "--no-cache-idle-slots", "--swa-full", "-cms 2048", "--context-shift", "--spec-draft-n-min 1", "-sps 0.5", "--rope-scaling yarn", "--rope-scale 2", "--rope-freq-base 1e+06", "--yarn-orig-ctx 4096", "-sm row", "-ts 3,1", "-mg 1", "-ot exps=CPU"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in emit:\n%s", want, out)
		}
	}
}

// Every spawned llama-server is pinned to localhost CORS origins. The default
// ('*' with credentials) lets any page the user visits read /props and /slots
// off the loopback upstream, which binding to 127.0.0.1 does not prevent.
func TestEmitProfile_CorsOriginsLockedDown(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "qwen3", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}

	var b strings.Builder
	emitProfile(&b, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, nil)
	if !strings.Contains(b.String(), "--cors-origins localhost") {
		t.Errorf("missing --cors-origins localhost:\n%s", b.String())
	}

	emb := strings.Join(embeddingCmdLines(s, row, nil, meta), " ")
	if !strings.Contains(emb, "--cors-origins localhost") {
		t.Errorf("embedding server missing --cors-origins localhost:\n%s", emb)
	}
}

// PreserveThinking emits --reasoning-preserve when thinking is on, and
// suppresses it when reasoning is off (pointless without thinking).
func TestEmitProfile_PreserveThinking(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "qwen3", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}

	var on strings.Builder
	emitProfile(&on, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, &Override{PreserveThinking: true})
	if !strings.Contains(on.String(), "--reasoning-preserve") {
		t.Errorf("missing --reasoning-preserve:\n%s", on.String())
	}
	// The old chat-template-kwargs form is gone: llama.cpp owns the knob now.
	if strings.Contains(on.String(), "preserve_thinking") {
		t.Errorf("legacy chat-template-kwargs form still emitted:\n%s", on.String())
	}

	// reasoning off => nothing to preserve.
	var off strings.Builder
	emitProfile(&off, s, meta, row, profile{Name: "judge", ReasoningFmt: "off"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, &Override{PreserveThinking: true})
	if strings.Contains(off.String(), "reasoning-preserve") {
		t.Errorf("--reasoning-preserve emitted despite reasoning off:\n%s", off.String())
	}
}

// A variant with ReasoningFmt "off" and CtxCheckpoints 0 emits both the reasoning
// disable flags and --ctx-checkpoints 0; nil CtxCheckpoints omits the flag.
func TestEmitProfile_ReasoningOffAndCtxCheckpoints(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	zero := 0

	var on strings.Builder
	emitProfile(&on, s, meta, row, profile{Name: "judge", ReasoningFmt: "off", CtxCheckpoints: &zero}, 4096, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, nil)
	out := on.String()
	for _, want := range []string{"--reasoning-format none", "--reasoning off", "--ctx-checkpoints 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// nil CtxCheckpoints => emit the arch-aware default (3 for plain attention)
	// rather than letting llama-server fall back to its 32.
	var off strings.Builder
	emitProfile(&off, s, meta, row, profile{Name: "foo"}, 4096, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, nil)
	if !strings.Contains(off.String(), "--ctx-checkpoints 3") {
		t.Errorf("nil CtxCheckpoints should emit the plain-attn default 3:\n%s", off.String())
	}
}

// With no override, emit keeps the defaults: flash attention on, global threads,
// one parallel slot, and no mlock. (ngl < blocks so mmap stays on, no flag.)
func TestEmitProfile_EngineDefaults(t *testing.T) {
	var b strings.Builder
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "llama", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}

	emitProfile(&b, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, nil)
	out := b.String()

	for _, want := range []string{"-fa on", "--parallel 1", "-t 7"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing default %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"--mlock", "--load-mode"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in default emit:\n%s", unwanted, out)
		}
	}
}
