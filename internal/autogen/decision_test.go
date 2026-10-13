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
