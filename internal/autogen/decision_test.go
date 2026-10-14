package autogen

import (
	"path/filepath"
	"strings"
	"testing"
)

// The parser lifts <arch>.decision.type, and a decision model that also bakes
// a pooling_type classifies as a decision model, not an embedder: emitModel
// checks IsDecisionModel first.
func TestReadGgufMetadata_DecisionType(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Kev-4B-Q8_0.gguf")
	writeGgufKVs(t, p, [][2]any{
		{"general.architecture", "qwen3"},
		{"qwen3.decision.type", "kev"},
		{"qwen3.pooling_type", 3},
	})
	meta, err := ReadGgufMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.DecisionType != "kev" {
		t.Fatalf("DecisionType = %q, want kev", meta.DecisionType)
	}
	if !IsDecisionModel(meta) {
		t.Error("IsDecisionModel = false, want true")
	}
	if IsDecisionModel(Metadata{Architecture: "qwen3"}) {
		t.Error("a plain chat model must not classify as a decision model")
	}
}

func TestDecisionCmdLines(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 8}
	row := GgufRow{FullPath: `C:\models\kev\Kev-4B-Q8_0.gguf`}

	causal := strings.Join(decisionCmdLines(s, row, nil, Metadata{Architecture: "qwen3", DecisionType: "kev", ContextLength: 40960}), " ")
	for _, want := range []string{"-m C:/models/kev/Kev-4B-Q8_0.gguf", "-ngl 99", "-c 8192", "-t 8"} {
		if !strings.Contains(causal, want) {
			t.Errorf("decision cmd missing %q in: %s", want, causal)
		}
	}
	// llama-server enables embedding output itself; --embeddings, a pooling
	// mode or the chat machinery would change what it serves.
	for _, bad := range []string{"--embeddings", "--pooling", "--jinja", "-ub", "--slot-save-path"} {
		if strings.Contains(causal, bad) {
			t.Errorf("decision cmd must not carry %q: %s", bad, causal)
		}
	}

	// Whole-prompt types read the state in one ubatch.
	laya := strings.Join(decisionCmdLines(s, row, nil, Metadata{Architecture: "modernbert", DecisionType: "laya", ContextLength: 2048}), " ")
	if !strings.Contains(laya, "-b 2048 -ub 2048") {
		t.Errorf("laya cmd missing -b/-ub = ctx: %s", laya)
	}

	// An explicit ctx override wins over the cap.
	ov := strings.Join(decisionCmdLines(s, row, &Override{Ctx: 16384}, Metadata{DecisionType: "kev", ContextLength: 40960}), " ")
	if !strings.Contains(ov, "-c 16384") {
		t.Errorf("ctx override ignored: %s", ov)
	}

	// A pinned CPU offload reaches -ngl (it used to be dropped for -ngl 99).
	dense := Metadata{DecisionType: "kev", ContextLength: 40960, BlockCount: 32}
	all := strings.Join(decisionCmdLines(s, row, &Override{CpuOffload: 32}, dense), " ")
	if !strings.Contains(all, "-ngl 0 ") {
		t.Errorf("cpuOffload 32/32 should emit -ngl 0: %s", all)
	}
	half := strings.Join(decisionCmdLines(s, row, &Override{CpuOffload: 8}, dense), " ")
	if !strings.Contains(half, "-ngl 24 ") {
		t.Errorf("cpuOffload 8/32 should emit -ngl 24: %s", half)
	}
}

func TestDecisionEstVramGB_CpuOffload(t *testing.T) {
	s := Settings{}
	row := GgufRow{SizeGB: 4}
	meta := Metadata{DecisionType: "kev", ContextLength: 8192, BlockCount: 32}
	gpu := decisionEstVramGB(s, row, nil, meta)
	cpu := decisionEstVramGB(s, row, &Override{CpuOffload: 32}, meta)
	if gpu-cpu < 3.9 {
		t.Errorf("full CPU offload should drop the weights from VRAM: gpu=%.2f cpu=%.2f", gpu, cpu)
	}
}

func TestEmitDecisionModel_Capabilities(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{ServerExe: "llama-server", Threads: 8, TtlSec: 600}
	row := GgufRow{FullPath: "/models/kev/Kev-4B-Q8_0.gguf", SizeGB: 4.3}
	emitDecisionModel(&b, s, row, nil, "kev-4b-q8_0", Metadata{Architecture: "qwen3", DecisionType: "kev", ContextLength: 4096}, &emitted)
	out := b.String()
	for _, want := range []string{"decision: true", `decisionType: "kev"`, "context: 4096"} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted yaml missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "embedding: true") {
		t.Errorf("decision model must not advertise embedding:\n%s", out)
	}
	if len(emitted) != 1 || emitted[0] != "kev-4b-q8_0" {
		t.Errorf("emitted = %v", emitted)
	}
}

func TestDecisionEstVramGB_ScalesWithWholeBatch(t *testing.T) {
	s := Settings{ComputeBufFactor: 1}
	row := GgufRow{SizeGB: 0.3}
	enc := Metadata{Architecture: "modernbert", DecisionType: "laya", ContextLength: 8192, HeadCount: 12, EmbeddingLength: 768, VocabSize: 50368}
	small := decisionEstVramGB(s, row, &Override{Ctx: 1024}, enc)
	big := decisionEstVramGB(s, row, nil, enc)
	// 12 heads x 8192^2 x 4 bytes is 3GB of scores alone; a flat pad charged 0.5.
	if big-row.SizeGB < 3 {
		t.Errorf("laya at 8192 charged %.2fGB over weights, want >= 3 for the score matrix", big-row.SizeGB)
	}
	if big <= small*2 {
		t.Errorf("estimate should grow with -ub: 1024 -> %.2fGB, 8192 -> %.2fGB", small, big)
	}
	kev := Metadata{Architecture: "qwen3", DecisionType: "kev", ContextLength: 40960, HeadCount: 32, EmbeddingLength: 2560, VocabSize: 151936}
	if ub, whole := decisionUb(kev, nil); whole || ub != llamaDefaultUb {
		t.Errorf("kev ub = %d whole=%v, want %d false", ub, whole, llamaDefaultUb)
	}
}

// A ModernBERT sequence classifier converted without decision.type (von-1.2)
// has no LM head and no mode llama-server serves it in, so it is dropped with
// the reason instead of listed as a chat model. An encoder that bakes a
// pooling_type is still an embedder: that check runs first.
func TestEmitModel_SkipsNonCausalClassifier(t *testing.T) {
	dir := t.TempDir()
	von := filepath.Join(dir, "von-1.2-f16.gguf")
	writeGgufKVs(t, von, [][2]any{
		{"general.architecture", "modern-bert"},
		{"modern-bert.context_length", 2048},
		{"modern-bert.attention.causal", false},
		{"modern-bert.classifier.output_labels", []string{"a", "b", "c"}},
	})
	meta, err := ReadGgufMetadata(von)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.NonCausal || !meta.ClassifierHead {
		t.Fatalf("NonCausal=%v ClassifierHead=%v, want both", meta.NonCausal, meta.ClassifierHead)
	}

	var b strings.Builder
	var emitted []string
	var coexist coexistSets
	row := GgufRow{FullPath: von, FileName: "von-1.2-f16.gguf", SizeGB: 0.8}
	err = emitModel(&b, Settings{ServerExe: "llama-server"}, GenerateFile{}, row, nil, "von-1.2", &emitted, &coexist)
	if err == nil || !strings.Contains(err.Error(), "decision.type") {
		t.Fatalf("err = %v, want a non-causal classifier skip", err)
	}
	if len(emitted) != 0 || b.Len() != 0 {
		t.Errorf("classifier was emitted: %v\n%s", emitted, b.String())
	}

	emb := filepath.Join(dir, "bge-small-f16.gguf")
	writeGgufKVs(t, emb, [][2]any{
		{"general.architecture", "bert"},
		{"bert.attention.causal", false},
		{"bert.pooling_type", 2},
	})
	b.Reset()
	row = GgufRow{FullPath: emb, FileName: "bge-small-f16.gguf", SizeGB: 0.1}
	if err := emitModel(&b, Settings{ServerExe: "llama-server"}, GenerateFile{}, row, nil, "bge-small", &emitted, &coexist); err != nil {
		t.Fatalf("embedder skipped: %v", err)
	}
	if !strings.Contains(b.String(), "--embeddings") {
		t.Errorf("embedder not routed to --embeddings:\n%s", b.String())
	}
}
