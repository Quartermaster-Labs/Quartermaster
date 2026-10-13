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

// llamaDefaultUb is llama-server's -ub when none is passed: the physical batch a
// causal decision model (kev, openjev) runs its compute graph at.
const llamaDefaultUb = 512

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

// decisionUb is the physical batch the model runs at: the whole window for the
// types that must read the prompt in one batch, llama-server's default
// otherwise. The launch flag and the VRAM estimate both read it, so they cannot
// disagree about how big the compute graph is.
func decisionUb(meta Metadata, ov *Override) (ub int, wholeBatch bool) {
	if decisionWholeBatchTypes[strings.ToLower(strings.TrimSpace(meta.DecisionType))] {
		return decisionCtx(meta, ov), true
	}
	return llamaDefaultUb, false
}

// decisionAttnScoresGB is the attention-score matrix (n_head x ub x ub, f32) a
// whole-batch model materializes when flash attention does not run. llama.cpp
// picks FA per backend at load (-fa auto) and computeGraphGB assumes it ran, which
// holds for the 512-token batches of a causal model but not for an encoder
// reading 8192 tokens at once: 12 heads at 8192 is 3.2GB. Charged as the peak
// because an under-charge here is a spill into shared memory, and an over-charge
// only keeps a second model from co-residing with a small encoder.
func decisionAttnScoresGB(meta Metadata, ub int) float64 {
	heads := float64(meta.HeadCount)
	if heads <= 0 {
		heads = 12 // ModernBERT-base, the smallest common decision encoder
	}
	return heads * float64(ub) * float64(ub) * 4.0 / gib
}

// decisionPlacement is the -ngl / --n-cpu-moe pair: every layer on the GPU
// unless the user pinned a CPU offload, which goes through the same
// applyForcedOffload an LLM's pin does.
func decisionPlacement(meta Metadata, ov *Override) (ngl, ncpuMoe int) {
	if ov.CpuDevice() {
		return 0, 0
	}
	if ov != nil && ov.CpuOffload > 0 {
		return applyForcedOffload(meta, ov.CpuOffload)
	}
	return 99, 0
}

// decisionGpuShare is the fraction of the weights and KV a dense placement
// keeps in VRAM (a CPU layer holds its own KV). MoE expert offload is left at
// 1: the experts are most of the file, but a decision model is never one.
func decisionGpuShare(meta Metadata, ov *Override) float64 {
	ngl, _ := decisionPlacement(meta, ov)
	if meta.IsMoE || meta.BlockCount <= 0 || int64(ngl) >= meta.BlockCount {
		return 1
	}
	return float64(ngl) / float64(meta.BlockCount)
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
	ngl, ncpuMoe := decisionPlacement(meta, ov)
	lines := []string{
		embeddingExe(s, ov),
		fmt.Sprintf("-m %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
		"--port ${PORT}",
		"--host 127.0.0.1",
		corsOriginsFlag,
		fmt.Sprintf("-ngl %d", ngl),
	}
	if ncpuMoe > 0 {
		lines = append(lines, fmt.Sprintf("--n-cpu-moe %d", ncpuMoe))
	}
	if ov.CpuDevice() {
		lines = append(lines, "--device none")
	}
	lines = append(lines, fmt.Sprintf("-c %d", ctx))
	if ub, whole := decisionUb(meta, ov); whole {
		lines = append(lines, fmt.Sprintf("-b %d -ub %d", ub, ub))
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

// decisionEstVramGB is weights + the window's KV at f16 + the compute buffer at
// the model's real -ub, plus the projector when one loads. Unlike an embedder, a
// causal decision model (kev, openjev) keeps a real KV cache over the whole
// state. The compute buffer is the same model every llama profile is charged
// with (computeBufferGB), because a whole-batch type runs at -ub = ctx and a flat
// pad cannot follow that; those also pay decisionAttnScoresGB.
func decisionEstVramGB(s Settings, row GgufRow, ov *Override, meta Metadata) float64 {
	ub, whole := decisionUb(meta, ov)
	share := decisionGpuShare(meta, ov)
	est := row.SizeGB*share + computeBufferGB(meta, ub, s.ComputeBufFactor)
	if whole {
		est += decisionAttnScoresGB(meta, ub)
	}
	if kv := GetKvCostModel(meta, "f16", "f16"); kv.OK {
		est += (kv.SlopeGB*float64(decisionCtx(meta, ov)) + kv.ConstGB) * share
	}
	if p, gb := decisionMmproj(row, ov); p != "" {
		est += MmprojVramGB(p, gb, s)
	}
	return est
}

// decisionEstGB splits the footprint between VRAM and RAM. A CPU-device pin
// moves all of it (compute buffer included) to RAM; a partial offload leaves
// the offloaded share of the weights and KV there.
func decisionEstGB(s Settings, row GgufRow, ov *Override, meta Metadata) (vram, ram float64) {
	if ov == nil || (!ov.CpuDevice() && ov.CpuOffload <= 0) {
		return decisionEstVramGB(s, row, ov, meta), 0
	}
	gpu := *ov
	gpu.Device, gpu.CpuOffload = "", 0
	full := decisionEstVramGB(s, row, &gpu, meta)
	if ov.CpuDevice() {
		return 0, full
	}
	vram = decisionEstVramGB(s, row, ov, meta)
	return vram, full - vram
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
	vram, ram := decisionEstGB(s, row, ov, meta)
	writeEstVram(b, vram)
	writeEstRam(b, ram)
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
