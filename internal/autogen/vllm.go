package autogen

import (
	"fmt"
	"strings"
)

// vllmDefaultGpuUtil is the fraction of each GPU vllm may fill (weights + KV +
// activations). vllm self-manages placement inside this budget — there is no
// -ngl / --n-cpu-moe layer offload like llama.cpp, so we don't size per-model;
// we hand vllm a utilization cap and let its allocator fit. Tune per-model via
// Override.VllmGpuUtil.
const vllmDefaultGpuUtil = 0.90

// kindClass maps a backend kind to the model class it serves, so auto-pick can
// match a model to a compatible backend. llama and vllm both serve LLMs.
func kindClass(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "llama", "llama.cpp", "server", "vllm":
		return "llm"
	case "sd", "sd-server", "image":
		return "image"
	case "tts", "tts-server", "speech":
		return "tts"
	}
	return ""
}

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

	// --max-model-len: a per-model ctx override, else the model's trained ctx.
	ctx := 0
	if ov != nil && ov.Ctx > 0 {
		ctx = ov.Ctx
	} else if meta.ContextLength > 0 {
		ctx = int(meta.ContextLength)
	}

	util := vllmDefaultGpuUtil
	if ov != nil && ov.VllmGpuUtil > 0 {
		util = ov.VllmGpuUtil
	}

	lines := []string{
		exe,
		"serve " + imageArg(modelPath),
		"--host 127.0.0.1",
		"--port ${PORT}",
		"--served-model-name " + imageArg(name),
		"--quantization gguf",
		fmt.Sprintf("--gpu-memory-utilization %g", util),
	}
	if ctx > 0 {
		lines = append(lines, fmt.Sprintf("--max-model-len %d", ctx))
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
	lines := vllmCmdLines(s, row, ov, name, be, meta)
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (vllm, gguf, gpu-util=%g)\n", meta.Architecture, row.SizeGB, vllmUtil(ov))
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	*emitted = append(*emitted, name)
}

// vllmUtil is the effective gpu-memory-utilization, for the YAML comment.
func vllmUtil(ov *Override) float64 {
	if ov != nil && ov.VllmGpuUtil > 0 {
		return ov.VllmGpuUtil
	}
	return vllmDefaultGpuUtil
}
