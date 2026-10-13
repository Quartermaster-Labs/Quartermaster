package autogen

import (
	"fmt"
	"strings"
)

// Decision models are llama.cpp classifiers served on POST /v1/systemone (the
// TypeSafe-compatible System One API): they answer typed questions (choice,
// score, noul) about a state with probabilities, and never generate text.
// llama-server recognises one from "<arch>.decision.type" and switches itself
// into decision mode, enabling embedding output on its own, so the launch needs
// no mode flag. What it must NOT get is the chat machinery: --embeddings, slot
// saving, spec decoding, chat templates and samplers all do nothing here or,
// worse, change how the prompt is batched.

// decisionOverheadGB pads a decision model's estimate for the compute buffer
// and, for vision models, nothing else: the projector is charged separately.
const decisionOverheadGB = 0.5

// decisionMaxCtx bounds the default window. The state is a ticket, a message or
// a document excerpt, not a conversation, and a decision model with a long
// trained window would otherwise reserve KV nobody uses.
const decisionMaxCtx = 8192

// decisionWholeBatchTypes are the decision types whose whole prompt has to be
// read in ONE batch (llama.cpp tools/server/README.md, /v1/systemone): the
// non-causal encoders (laya, clef, vllm-d3) and lfm2-d1-omni. llama-server's
// default -ub of 512 would reject any prompt longer than that, so these get
// -b/-ub pinned to the window.
var decisionWholeBatchTypes = map[string]bool{
	"laya":         true,
	"clef":         true,
	"vllm-d3":      true,
	"lfm2-d1-omni": true,
}

// IsDecisionModel reports whether a GGUF is a llama.cpp decision model.
// Checked before IsEmbeddingModel: a decision model built on an encoder can
// bake a pooling_type too, and launching it with --embeddings would serve the
// wrong API.
func IsDecisionModel(meta Metadata) bool {
	return strings.TrimSpace(meta.DecisionType) != ""
}

// decisionCtx is the served window: the override, else the trained length
// capped at decisionMaxCtx.
func decisionCtx(meta Metadata, ov *Override) int {
	if ov != nil && ov.Ctx > 0 {
		return ov.Ctx
	}
	ctx := 4096
	if meta.ContextLength > 0 {
		ctx = int(meta.ContextLength)
	}
	if ctx > decisionMaxCtx {
		ctx = decisionMaxCtx
	}
	return ctx
}

// decisionMmproj resolves the vision projector a decision model loads, the
// same way an LLM does (own folder, family sibling, or an explicit
// mmprojFile), honouring `mmproj: none`. openjev, clef, pplx-decider, vllm-d3
// and the lfm2-d1 pair take images on /v1/systemone through it.
func decisionMmproj(row GgufRow, ov *Override) (path string, sizeGB float64) {
	file := ""
	if ov != nil {
		if strings.EqualFold(strings.TrimSpace(ov.Mmproj), NoneSentinel) {
			return "", 0
		}
		file = ov.MmprojFile
	}
	return mmprojFor(row, file)
}

// decisionCmdLines builds the llama-server argv (exe first). Shared by
// emitDecisionModel and RenderSoloCmdLayers so the editor preview matches a
// save. The exe is the class "llm" pick, with the same vllm exception
// embedders make: there is no vllm decision emitter.
func decisionCmdLines(s Settings, row GgufRow, ov *Override, meta Metadata) []string {
	threads := s.Threads
	if ov != nil && ov.Threads > 0 {
		threads = ov.Threads
	}
	ctx := decisionCtx(meta, ov)
	lines := []string{
		embeddingExe(s, ov),
		fmt.Sprintf("-m %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
		"--port ${PORT}",
		"--host 127.0.0.1",
		corsOriginsFlag,
		"-ngl 99",
		fmt.Sprintf("-c %d", ctx),
	}
	if decisionWholeBatchTypes[strings.ToLower(strings.TrimSpace(meta.DecisionType))] {
		lines = append(lines, fmt.Sprintf("-b %d -ub %d", ctx, ctx))
	}
	if p, _ := decisionMmproj(row, ov); p != "" {
		lines = append(lines, fmt.Sprintf("--mmproj %s", strings.ReplaceAll(p, "\\", "/")))
	}
	lines = append(lines,
		fmt.Sprintf("-t %d", threads),
		"--no-warmup --no-ui --metrics --props",
	)
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// decisionEstVramGB is weights + the window's KV at f16 + a flat pad, plus the
// projector when one loads. Unlike an embedder, a causal decision model (kev,
// openjev) keeps a real KV cache over the whole state, so the flat pad alone
// would under-charge it.
func decisionEstVramGB(s Settings, row GgufRow, ov *Override, meta Metadata) float64 {
	est := row.SizeGB + decisionOverheadGB
	if kv := GetKvCostModel(meta, "f16", "f16"); kv.OK {
		est += kv.SlopeGB*float64(decisionCtx(meta, ov)) + kv.ConstGB
	}
	if p, gb := decisionMmproj(row, ov); p != "" {
		est += MmprojVramGB(p, gb, s)
	}
	return est
}

// emitDecisionModel writes the YAML entry. capabilities.decision is what
// /v1/models turns into output_modalities ["decisions"] (llama-server's own
// marker), which puts the model in the UI's Decision category and keeps it out
// of every chat picker.
func emitDecisionModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, meta Metadata, emitted *[]string) {
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (decision model, llama-server /v1/systemone, decision.type=%s)\n", meta.Architecture, row.SizeGB, meta.DecisionType)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range decisionCmdLines(s, row, ov, meta) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	writeSingleDeviceEnv(b, s, embeddingExe(s, ov))
	writeEstVram(b, decisionEstVramGB(s, row, ov, meta))
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	if p, _ := decisionMmproj(row, ov); p != "" {
		b.WriteString("      in: [text, image]\n")
	}
	b.WriteString("      decision: true\n")
	fmt.Fprintf(b, "      decisionType: %q\n", meta.DecisionType)
	fmt.Fprintf(b, "      context: %d\n", decisionCtx(meta, ov))
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
