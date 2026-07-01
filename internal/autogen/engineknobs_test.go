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

	for _, want := range []string{"-fa off", "--no-mmap", "--mlock", "-t 12", "--parallel 4", "-ub 256 -b 256"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in emitted cmd:\n%s", want, out)
		}
	}
}

// computeBufferGB models logits (vocab*min(ub,256)) + activations (ub*embd) +
// CUDA ctx, scaled by factor; falls back to a flat estimate when dims are
// missing. The logits token count is capped (llama sizes the output buffer by
// n_outputs, not the physical batch) so large-vocab models aren't over-charged.
func TestComputeBufferGB(t *testing.T) {
	meta := Metadata{EmbeddingLength: 4096, VocabSize: 151936}

	// ub=1024: 0.3 CUDA + logits(vocab*256*4 ~0.145) + acts(ub*embd*8*4 ~0.125).
	got := computeBufferGB(meta, 1024, 1.0)
	if got < 0.52 || got > 0.63 {
		t.Errorf("ub=1024 factor=1: got %.3f, want ~0.57 GB", got)
	}

	// Halving ub shrinks only the activation term now (logits is capped), so the
	// buffer is smaller but by less than half.
	half := computeBufferGB(meta, 512, 1.0)
	if half >= got || half < 0.45 {
		t.Errorf("ub=512 should be a bit smaller: got %.3f (ub1024=%.3f)", half, got)
	}

	// factor scales the analytic term (~0.27 GB here) above the fixed CUDA ctx.
	if d := computeBufferGB(meta, 1024, 2.0) - got; d < 0.2 {
		t.Errorf("factor=2 should add ~one analytic term (~0.27 GB), added %.3f", d)
	}

	// Missing dims => flat fallback.
	if fb := computeBufferGB(Metadata{}, 1024, 1.0); fb != computeFallbackGB {
		t.Errorf("missing dims: got %.3f, want fallback %.3f", fb, computeFallbackGB)
	}
}

// mmap is on by default — even on the CPU-offload path (--n-cpu-moe). Only an
// explicit Mmap:"off" override emits --no-mmap.
func TestEmitProfile_MmapDefaultOn(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600, MaxRamGB: 32}
	meta := Metadata{Architecture: "qwen3moe", BlockCount: 48, IsMoE: true}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	prof := profile{Name: "foo"}

	// CPU offload, no override: mmap stays on (no --no-mmap).
	var def strings.Builder
	emitProfile(&def, s, meta, row, prof, 8192, 99, 20, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, nil)
	if strings.Contains(def.String(), "--no-mmap") {
		t.Errorf("mmap should default on even with CPU offload:\n%s", def.String())
	}

	// Explicit Mmap:"off" emits --no-mmap.
	var off strings.Builder
	emitProfile(&off, s, meta, row, prof, 8192, 99, 20, LoadPlan{EstRamGB: 18}, "q8_0", "q8_0", false, &Override{Mmap: "off"})
	if !strings.Contains(off.String(), "--no-mmap") {
		t.Errorf("explicit Mmap:off should emit --no-mmap:\n%s", off.String())
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

// PreserveThinking emits the chat-template-kwargs flag when thinking is on, and
// suppresses it when reasoning is off (pointless without thinking).
func TestEmitProfile_PreserveThinking(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 7, TtlSec: 600}
	meta := Metadata{Architecture: "qwen3", BlockCount: 32}
	row := GgufRow{FullPath: "/models/foo.gguf"}
	want := `--chat-template-kwargs "{\"preserve_thinking\":true}"`

	var on strings.Builder
	emitProfile(&on, s, meta, row, profile{Name: "foo"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, &Override{PreserveThinking: true})
	if !strings.Contains(on.String(), want) {
		t.Errorf("missing preserve_thinking flag:\n%s", on.String())
	}

	// reasoning off => no preserve_thinking (nothing to preserve).
	var off strings.Builder
	emitProfile(&off, s, meta, row, profile{Name: "judge", ReasoningFmt: "off"}, 8192, 10, 0, LoadPlan{}, "q8_0", "q8_0", false, &Override{PreserveThinking: true})
	if strings.Contains(off.String(), "preserve_thinking") {
		t.Errorf("preserve_thinking emitted despite reasoning off:\n%s", off.String())
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
// one parallel slot, and no --mlock. (ngl < blocks so --no-mmap stays off too.)
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
	for _, unwanted := range []string{"--mlock", "--no-mmap"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in default emit:\n%s", unwanted, out)
		}
	}
}
