package autogen

import (
	"strings"
	"testing"
)

func TestResolveBackend_AutoPickAndExplicit(t *testing.T) {
	s := Settings{Backends: []BackendEntry{
		{ID: "llama-vk", Kind: "llama", Path: "C:/llama/llama-server.exe"},
		{ID: "vllm-1", Kind: "vllm", Path: "vllm", Default: true},
		{ID: "sd-1", Kind: "sd", Path: "sd-server"},
	}}

	// Auto-pick for the LLM class honours the ★ default (vllm here, not the
	// first-listed llama).
	if be := resolveBackend(s, nil, "llm"); be.ID != "vllm-1" {
		t.Errorf("auto-pick llm = %q, want vllm-1 (the class default)", be.ID)
	}
	// Explicit Override.Backend wins over the default.
	if be := resolveBackend(s, &Override{Backend: "llama-vk"}, "llm"); be.ID != "llama-vk" {
		t.Errorf("explicit backend = %q, want llama-vk", be.ID)
	}
	// A stale id falls back to auto-pick, not a broken zero value.
	if be := resolveBackend(s, &Override{Backend: "gone"}, "llm"); be.ID != "vllm-1" {
		t.Errorf("stale id = %q, want auto-pick vllm-1", be.ID)
	}
	// No entry of a class => zero value (caller uses the legacy exe).
	if be := resolveBackend(s, nil, "tts"); be.ID != "" {
		t.Errorf("no tts backend => %q, want empty", be.ID)
	}
	// First-of-class when none is marked default.
	s2 := Settings{Backends: []BackendEntry{
		{ID: "a", Kind: "llama", Path: "a"},
		{ID: "b", Kind: "llama", Path: "b"},
	}}
	if be := resolveBackend(s2, nil, "llm"); be.ID != "a" {
		t.Errorf("first-of-class = %q, want a", be.ID)
	}
}

func TestVllmCmdLines_Shape(t *testing.T) {
	s := Settings{Backends: []BackendEntry{{ID: "v", Kind: "vllm", Path: "C:/py/vllm.exe", Default: true}}}
	row := GgufRow{FullPath: `E:\Models\qwen\q.gguf`, SizeGB: 4}
	meta := Metadata{Architecture: "qwen3", ContextLength: 32768}
	be := resolveBackend(s, &Override{Ctx: 8192, VllmGpuUtil: 0.8, VllmTensorParallel: 2}, "llm")

	cmd := strings.Join(vllmCmdLines(s, row, &Override{Ctx: 8192, VllmGpuUtil: 0.8, VllmTensorParallel: 2}, "qwen3-q4", be, meta), " ")
	for _, want := range []string{
		"C:/py/vllm.exe", "serve E:/Models/qwen/q.gguf", "--port ${PORT}",
		"--served-model-name qwen3-q4", "--quantization gguf",
		"--gpu-memory-utilization 0.8", "--max-model-len 8192", "--tensor-parallel-size 2",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("vllm cmd missing %q\ngot: %s", want, cmd)
		}
	}
	// A tokenizer is only emitted when set — never guessed from the model's folder.
	if strings.Contains(cmd, "--tokenizer") {
		t.Errorf("tokenizer emitted without an override: %s", cmd)
	}
	tok := strings.Join(vllmCmdLines(s, row, &Override{VllmTokenizer: "Qwen/Qwen3-8B"}, "m", be, meta), " ")
	if !strings.Contains(tok, "--tokenizer Qwen/Qwen3-8B") {
		t.Errorf("tokenizer override not emitted: %s", tok)
	}

	// With no GPU reading there is nothing to take a fraction of, so utilization
	// falls back to the flat default rather than to a number derived from half
	// the picture.
	withCard(t, 0, false)
	s.TargetVramGB = 24
	def := strings.Join(vllmCmdLines(s, row, nil, "m", be, meta), " ")
	if !strings.Contains(def, "--gpu-memory-utilization 0.9") {
		t.Errorf("no-GPU util should be the flat default: %s", def)
	}
}

// withCard stubs the one-shot card probe for the duration of a test, so sizing
// is exercised on machines with no GPU telemetry (CI) and with a card size we
// choose rather than whatever the host happens to have.
func withCard(t *testing.T, totalGB float64, ok bool) {
	t.Helper()
	prev := cachedTotalVramGB
	cachedTotalVramGB = func() (float64, bool) { return totalGB, ok }
	t.Cleanup(func() { cachedTotalVramGB = prev })
}

func TestVllmGpuUtil_DerivedFromBudget(t *testing.T) {
	withCard(t, 24, true)

	// The budget is the fraction of the card we hand vllm — not a flat 0.90 that
	// ignores the operator asking for a smaller share.
	if got, _ := vllmGpuUtil(Settings{TargetVramGB: 12}, nil); round2(got) != 0.5 {
		t.Errorf("12GB of a 24GB card = %g, want 0.5", got)
	}
	// A per-model cap wins over the fleet budget, same precedence as llama sizing.
	if got, _ := vllmGpuUtil(Settings{TargetVramGB: 12}, &Override{VramTargetGB: 6}); round2(got) != 0.25 {
		t.Errorf("per-model 6GB cap = %g, want 0.25", got)
	}
	// Clamped at both ends: a tiny budget still needs room for weights, and a
	// budget at/over the card size must leave the desktop something.
	if got, _ := vllmGpuUtil(Settings{TargetVramGB: 0.5}, nil); got != vllmMinGpuUtil {
		t.Errorf("tiny budget = %g, want the floor %g", got, vllmMinGpuUtil)
	}
	if got, _ := vllmGpuUtil(Settings{TargetVramGB: 32}, nil); got != vllmMaxGpuUtil {
		t.Errorf("oversized budget = %g, want the ceiling %g", got, vllmMaxGpuUtil)
	}
	// An explicit knob is passed through untouched, clamps included.
	if got, _ := vllmGpuUtil(Settings{TargetVramGB: 12}, &Override{VllmGpuUtil: 0.99}); got != 0.99 {
		t.Errorf("pinned util = %g, want 0.99", got)
	}
}

func TestVllmMaxModelLen_SizedToBudget(t *testing.T) {
	// A model whose trained window is far larger than the budget can hold: vllm
	// allocates its KV pool from --max-model-len up front, so emitting the
	// trained length is a refused startup, not a large context.
	meta := Metadata{
		Architecture: "qwen3", ContextLength: 262144, BlockCount: 48,
		HeadCount: 32, HeadCountKv: 8, EmbeddingLength: 4096,
		KeyLength: 128, ValueLength: 128,
	}
	row := GgufRow{FullPath: "/m/q.gguf", SizeGB: 8}
	s := Settings{TargetVramGB: 16}

	ctx, _ := vllmMaxModelLen(s, nil, row, meta)
	if ctx <= 0 || ctx >= int(meta.ContextLength) {
		t.Fatalf("ctx = %d, want a budget-sized window below the trained %d", ctx, meta.ContextLength)
	}
	if ctx%4096 != 0 {
		t.Errorf("ctx = %d, want a 4096 multiple", ctx)
	}
	// The same model on a bigger budget must not shrink.
	big, _ := vllmMaxModelLen(Settings{TargetVramGB: 48}, nil, row, meta)
	if big < ctx {
		t.Errorf("bigger budget gave a smaller window: %d < %d", big, ctx)
	}
	// Never exceed what the model was trained for, however much VRAM there is.
	if huge, _ := vllmMaxModelLen(Settings{TargetVramGB: 4096}, nil, row, meta); huge != int(meta.ContextLength) {
		t.Errorf("huge budget = %d, want the trained %d", huge, meta.ContextLength)
	}
	// A pinned ctx is the user stating a requirement; sizing must not override it.
	if pinned, _ := vllmMaxModelLen(s, &Override{Ctx: 200000}, row, meta); pinned != 200000 {
		t.Errorf("pinned ctx = %d, want 200000", pinned)
	}
	// Weights alone over budget: vllm has no CPU offload, so say so rather than
	// emitting a window that implies the model fits.
	tiny, note := vllmMaxModelLen(Settings{TargetVramGB: 4}, nil, row, meta)
	if tiny != 4096 || !strings.Contains(note, "exceed") {
		t.Errorf("over-budget model = (%d, %q), want (4096, an exceeds-budget note)", tiny, note)
	}
}

func TestEmitVllmModel_SkipsSplitShards(t *testing.T) {
	withCard(t, 24, true)
	s := Settings{TargetVramGB: 16, Backends: []BackendEntry{{ID: "v", Kind: "vllm", Path: "vllm", Default: true}}}
	be := resolveBackend(s, nil, "llm")
	meta := Metadata{Architecture: "qwen3", ContextLength: 32768}

	// Discovery represents a split set by shard 1 alone. llama.cpp opens the
	// siblings itself; vllm would load a fifth of the weights.
	var b strings.Builder
	var emitted []string
	split := GgufRow{FullPath: `E:\Models\big\big-00001-of-00005.gguf`, FileName: "big.gguf", SizeGB: 8}
	emitVllmModel(&b, s, split, nil, "big", be, meta, &emitted)
	if len(emitted) != 0 {
		t.Errorf("split model was emitted as %v, want skipped", emitted)
	}
	if out := b.String(); !strings.Contains(out, "skipped") || strings.Contains(out, "cmd:") {
		t.Errorf("want a skip comment and no command, got:\n%s", out)
	}

	// A single-file model still emits normally.
	b.Reset()
	emitVllmModel(&b, s, GgufRow{FullPath: `E:\Models\q\q.gguf`, SizeGB: 4}, nil, "q", be, meta, &emitted)
	if len(emitted) != 1 || !strings.Contains(b.String(), "cmd:") {
		t.Errorf("single-file model should emit, got %v:\n%s", emitted, b.String())
	}
}
