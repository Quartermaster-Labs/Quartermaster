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
	// Default util when unset; ctx falls back to the model's trained length.
	def := strings.Join(vllmCmdLines(s, row, nil, "m", be, meta), " ")
	if !strings.Contains(def, "--gpu-memory-utilization 0.9") || !strings.Contains(def, "--max-model-len 32768") {
		t.Errorf("default vllm cmd wrong: %s", def)
	}
}
