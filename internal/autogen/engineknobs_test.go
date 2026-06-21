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
