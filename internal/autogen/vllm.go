package autogen

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

// vllmDefaultGpuUtil is the fallback fraction of the GPU vllm may fill (weights
// + KV + activations) when the card's size is unknown, so a utilization can't be
// derived from the VRAM budget. vllm self-manages placement inside this budget —
// there is no -ngl / --n-cpu-moe layer offload like llama.cpp, so we don't place
// layers per-model; we hand vllm a cap and let its allocator fit inside it. Tune
// per-model via Override.VllmGpuUtil.
const vllmDefaultGpuUtil = 0.90

// vllm utilization bounds. The floor keeps a tiny budget on a big card from
// producing a utilization vllm can't even load weights into; the ceiling leaves
// the display/compositor room, since vllm measures utilization against TOTAL
// card memory and refuses to start if that much isn't actually free.
const (
	vllmMinGpuUtil = 0.10
	vllmMaxGpuUtil = 0.95
)

// vllmOverheadGB is the non-weights, non-KV VRAM vllm needs on top of the model:
// activations, CUDA graphs, and the profiling run's peak. A flat figure, unlike
// the llama path's modelled compute buffer — vllm's allocator is opaque to us, so
// this is a reserve, not an estimate.
const vllmOverheadGB = 1.5

// vllmFallbackCtx caps --max-model-len when the KV cost model can't be built
// from the gguf header (missing attention dims). Better a conservative window
// than the model's full trained context, which is what vllm would otherwise try
// to allocate KV for at startup.
const vllmFallbackCtx = 32768

// kindClass maps a backend kind to the model class it serves, so auto-pick can
// match a model to a compatible backend. llama and vllm both serve LLMs.
func kindClass(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "llama", "llama.cpp", "server", "vllm":
		return "llm"
	case "sd", "sd-server", "image":
		return "image"
	case "tts", "tts-server", "speech", "ttscpp", "tts.cpp", "kokoro":
		return "tts"
	case "asr", "parakeet", "parakeet-server", "transcribe":
		return "asr"
	case "sam", "sam3", "segment":
		return "segment"
	case "upscale", "realesrgan", "esrgan":
		return "upscale"
	}
	return ""
}

// KindClass exposes the kind→class mapping to callers outside this package (the
// managed-backend installer needs it to decide whether a class already has an
// entry before marking a freshly installed backend as the class default).
func KindClass(kind string) string { return kindClass(kind) }

// resolvedBackend is the backend a model resolves to: its kind (drives which
// emitter runs) and exe (the launcher). A zero value => no registry entry
// matched; the caller falls back to the legacy derived exe (ServerExe etc.).
type resolvedBackend struct {
	Kind string
	Exe  string
	ID   string
}

// resolveBackend picks the backend entry for a model of the given class.
// Precedence: an explicit Override.Backend (registry entry id) wins; else the
// class-default entry (Default=true); else the first entry of that class; else a
// zero value so the caller keeps the legacy behaviour. Auto-pick is thus:
// "the ★-default backend for this class, or the first one installed."
func resolveBackend(s Settings, ov *Override, class string) resolvedBackend {
	if ov != nil {
		if id := strings.TrimSpace(ov.Backend); id != "" {
			for _, e := range s.Backends {
				if e.ID == id {
					return resolvedBackend{Kind: e.Kind, Exe: strings.TrimSpace(e.Path), ID: e.ID}
				}
			}
			// A stale id (backend removed from the registry) falls through to
			// auto-pick rather than emitting a broken command.
		}
	}
	var first *BackendEntry
	for i := range s.Backends {
		e := &s.Backends[i]
		if kindClass(e.Kind) != class {
			continue
		}
		if e.Default {
			return resolvedBackend{Kind: e.Kind, Exe: strings.TrimSpace(e.Path), ID: e.ID}
		}
		if first == nil {
			first = e
		}
	}
	if first != nil {
		return resolvedBackend{Kind: first.Kind, Exe: strings.TrimSpace(first.Path), ID: first.ID}
	}
	return resolvedBackend{}
}

// resolveBackendPreferring is resolveBackend with a family hint. Some classes
// hold engines that are NOT interchangeable: llama and vllm both serve any LLM
// gguf, but qwentts.cpp and TTS.cpp each read their own weights format, so the
// ★-default of the "tts" class is the wrong pick for a model of the other
// family. preferKind therefore beats Default — an explicit per-model
// Override.Backend still wins over both, since that is the user overruling the
// auto-pick on purpose. Falls back to plain resolveBackend when the class holds
// no entry of the preferred kind.
func resolveBackendPreferring(s Settings, ov *Override, class, preferKind string) resolvedBackend {
	if ov != nil {
		if id := strings.TrimSpace(ov.Backend); id != "" {
			for _, e := range s.Backends {
				if e.ID == id {
					return resolvedBackend{Kind: e.Kind, Exe: strings.TrimSpace(e.Path), ID: e.ID}
				}
			}
		}
	}
	if preferKind != "" {
		var first *BackendEntry
		for i := range s.Backends {
			e := &s.Backends[i]
			if kindClass(e.Kind) != class || !strings.EqualFold(strings.TrimSpace(e.Kind), preferKind) {
				continue
			}
			if e.Default {
				return resolvedBackend{Kind: e.Kind, Exe: strings.TrimSpace(e.Path), ID: e.ID}
			}
			if first == nil {
				first = e
			}
		}
		if first != nil {
			return resolvedBackend{Kind: first.Kind, Exe: strings.TrimSpace(first.Path), ID: first.ID}
		}
	}
	return resolveBackend(s, ov, class)
}

// vllmCmdLines builds the vllm argv (exe first) for a gguf served through vllm.
// Shared by emitVllmModel (YAML emit) and RenderSoloCmd (editor preview) so the
// launch-parameters box matches a save. vllm loads the SAME gguf QM discovered
// (--quantization gguf); no per-model VRAM sizing — vllm's allocator fits inside
// --gpu-memory-utilization. Ctx maps to --max-model-len (caps the KV window).
func vllmCmdLines(s Settings, row GgufRow, ov *Override, name string, be resolvedBackend, meta Metadata) []string {
	modelPath := strings.ReplaceAll(row.FullPath, "\\", "/")
	exe := be.Exe
	if exe == "" {
		exe = "vllm"
	}

	ctx, _ := vllmMaxModelLen(s, ov, row, meta)
	util, _ := vllmGpuUtil(s, ov)

	lines := []string{
		exe,
		"serve " + imageArg(modelPath),
		"--host 127.0.0.1",
		"--port ${PORT}",
		"--served-model-name " + imageArg(name),
		"--quantization gguf",
		fmt.Sprintf("--gpu-memory-utilization %g", round2(util)),
	}
	if ctx > 0 {
		lines = append(lines, fmt.Sprintf("--max-model-len %d", ctx))
	}
	// Upstream: "We recommend using the tokenizer from base model instead of GGUF
	// model. Because the tokenizer conversion from GGUF is time-consuming and
	// unstable." It is not emitted automatically: GgufRow.Repo is the local folder
	// name, not a verified Hugging Face id, so deriving one would bake a guessed
	// remote reference into a launch command. Set it explicitly per model.
	if ov != nil {
		if tok := strings.TrimSpace(ov.VllmTokenizer); tok != "" {
			lines = append(lines, "--tokenizer "+imageArg(tok))
		}
	}
	if ov != nil && ov.VllmTensorParallel > 1 {
		lines = append(lines, fmt.Sprintf("--tensor-parallel-size %d", ov.VllmTensorParallel))
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitVllmModel writes a vllm YAML entry for a gguf LLM. vllm speaks the OpenAI
// API natively (/v1/chat/completions, /health) so it needs no capabilities block
// (default text chat) and no explicit checkEndpoint (llama parity — the config
// default /health probe fits vllm too). Named/ctx-tier variants are NOT emitted
// for vllm: the llama profile/KV sizing that produces them doesn't apply here.
func emitVllmModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, be resolvedBackend, meta Metadata, emitted *[]string) {
	if isSplitGguf(row) {
		// Discovery represents a split set by shard 1 alone, which is all
		// llama.cpp needs — it opens the sibling shards itself. vllm does not:
		// it would load a fifth of the weights and fail somewhere downstream.
		// Emit the reason as a comment rather than a command that cannot work,
		// and leave the model out of `emitted` so nothing routes to it.
		fmt.Fprintf(b, "\n  # %s: skipped (vllm cannot load split gguf shards; merge with llama-gguf-split --merge)\n", name)
		return
	}

	lines := vllmCmdLines(s, row, ov, name, be, meta)
	ctx, ctxNote := vllmMaxModelLen(s, ov, row, meta)
	util, utilNote := vllmGpuUtil(s, ov)
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (vllm, gguf, gpu-util=%g [%s], max-model-len=%d [%s])\n",
		meta.Architecture, row.SizeGB, round2(util), utilNote, ctx, ctxNote)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// vllm allocates its whole pool (weights + KV) up front from
	// --gpu-memory-utilization, so its footprint IS the budget it was sized
	// against, not a computed weights+KV sum.
	writeEstVram(b, vllmBudgetGB(s, ov))
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}

// isSplitGguf reports whether the row's file is one shard of a split set.
// Discovery collapses a set to its first shard, so this is the only trace left
// that the weights continue in sibling files.
func isSplitGguf(row GgufRow) bool {
	return shardRe.MatchString(strings.ReplaceAll(row.FullPath, "\\", "/"))
}

// vllmBudgetGB is the VRAM this model may occupy: the per-model cap when set,
// else the fleet budget. Same precedence the llama sizer uses.
func vllmBudgetGB(s Settings, ov *Override) float64 {
	if ov != nil && ov.VramTargetGB > 0 {
		return ov.VramTargetGB
	}
	return s.TargetVramGB
}

// vllmMaxModelLen sizes --max-model-len against the VRAM budget instead of
// handing vllm the model's full trained context. vllm allocates its KV pool up
// front from --max-model-len, so a 262144-token trained window on a card that
// can hold a fraction of it is not a large context — it is a refused or OOMing
// startup. An explicit Override.Ctx always wins: a pinned window is the user
// stating the requirement, and vllm failing loudly beats silently serving a
// smaller one.
//
// The KV cost model is llama's, evaluated at f16 (vllm's KV is fp16 and paged;
// paging changes fragmentation, not the per-token cost), so this is an
// approximation of a different engine's allocator — deliberately conservative
// rather than exact. note explains the choice for the YAML comment.
func vllmMaxModelLen(s Settings, ov *Override, row GgufRow, meta Metadata) (ctx int, note string) {
	trained := int(meta.ContextLength)
	if ov != nil && ov.Ctx > 0 {
		return ov.Ctx, "pinned"
	}

	avail := vllmBudgetGB(s, ov) - row.SizeGB - vllmOverheadGB
	if avail <= 0 {
		// Weights alone overrun the budget. vllm has no CPU offload to fall back
		// on, so this model needs a bigger budget or a smaller quant; emit the
		// floor and say so rather than emitting a window that implies it fits.
		return RoundedCtx(0), "weights exceed the VRAM budget"
	}

	m := GetKvCostModel(meta, "f16", "f16")
	if !m.OK {
		ctx = vllmFallbackCtx
		note = "no KV cost model; conservative default"
	} else {
		ctx = RoundedCtx(float64(MaxCtxForBudget(avail, m.SlopeGB, m.ConstGB)))
		note = fmt.Sprintf("sized to %.1fGB free of budget", avail)
	}
	if trained > 0 && ctx > trained {
		ctx, note = trained, "trained context"
	}
	return ctx, note
}

// vllmGpuUtil derives --gpu-memory-utilization from the VRAM budget and the
// card's real size. The old flat 0.90 was a fraction of TOTAL memory regardless
// of what the budget said or what the desktop already holds, so it both ignored
// a deliberately small budget and could exceed what is actually free. Falls back
// to the flat default when the card's size can't be read (headless, no GPU
// telemetry) — there is nothing to take a fraction of.
func vllmGpuUtil(s Settings, ov *Override) (util float64, note string) {
	if ov != nil && ov.VllmGpuUtil > 0 {
		return ov.VllmGpuUtil, "pinned"
	}
	total, ok := cachedTotalVramGB()
	if !ok || total <= 0 {
		return vllmDefaultGpuUtil, "no GPU reading"
	}
	util = vllmBudgetGB(s, ov) / total
	if util < vllmMinGpuUtil {
		util = vllmMinGpuUtil
	}
	if util > vllmMaxGpuUtil {
		util = vllmMaxGpuUtil
	}
	return util, fmt.Sprintf("%.1fGB budget of %.1fGB card", vllmBudgetGB(s, ov), total)
}

// cachedTotalVramGB probes the card once per process. Sizing runs per model and
// a GPU query is not free, and the card does not change size while we run. The
// function variable is the seam tests use to supply a card without a GPU.
var cachedTotalVramGB = sync.OnceValues(func() (float64, bool) {
	return SampleTotalVramGB(autoVramSampleTimeout)
})

// round2 trims float noise out of a derived utilization so the emitted flag
// reads 0.72, not 0.7200000000000001.
func round2(f float64) float64 { return math.Round(f*100) / 100 }
