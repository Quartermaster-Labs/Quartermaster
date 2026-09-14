package autogen

import (
	"strings"
	"testing"
)

func TestIsEmbeddingModel(t *testing.T) {
	cases := []struct {
		name string
		meta Metadata
		want bool
	}{
		{"bert", Metadata{Architecture: "bert"}, true},
		{"nomic-bert-moe", Metadata{Architecture: "nomic-bert-moe"}, true},
		{"xlm-roberta", Metadata{Architecture: "xlm-roberta"}, true},
		{"gte versioned prefix", Metadata{Architecture: "gte-v1.5"}, true},
		{"llm-arch embedder via pooling", Metadata{Architecture: "qwen3", PoolingType: 3}, true},
		{"chat model qwen3", Metadata{Architecture: "qwen3"}, false},
		{"chat model llama", Metadata{Architecture: "llama"}, false},
		{"pooling none is not embedder", Metadata{Architecture: "qwen3", PoolingType: 0}, false},
		{"empty arch", Metadata{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsEmbeddingModel(c.meta); got != c.want {
				t.Errorf("IsEmbeddingModel(%+v) = %v, want %v", c.meta, got, c.want)
			}
		})
	}
}

func TestEmbeddingCmdLines_BackendPin(t *testing.T) {
	rows := []BackendEntry{
		{ID: "managed-lemonade", Kind: "llama", Path: "C:/rocm/llama-server.exe"},
		{ID: "managed-llama-server", Kind: "llama", Path: "C:/vulkan/llama-server.exe"},
		{ID: "managed-vllm", Kind: "vllm", Path: "C:/vllm/vllm.exe"},
	}
	withDefault := func(id string) []BackendEntry {
		out := append([]BackendEntry(nil), rows...)
		for i := range out {
			out[i].Default = out[i].ID == id
		}
		return out
	}
	row := GgufRow{FullPath: `C:\models\bge\bge-m3.gguf`}
	meta := Metadata{Architecture: "bert"}

	cases := []struct {
		name string
		def  string
		pin  string
		want string
	}{
		// The ★ moving is what must not touch a pinned model: the embedding
		// emitter runs for class "llm", so it has to obey the same precedence
		// the chat emitter does.
		{"pin wins over the default", "managed-lemonade", "managed-llama-server", "C:/vulkan/llama-server.exe"},
		{"pin wins after the default switched", "managed-llama-server", "managed-lemonade", "C:/rocm/llama-server.exe"},
		{"auto follows the default", "managed-llama-server", "", "C:/vulkan/llama-server.exe"},
		{"stale pin falls back to the default", "managed-lemonade", "gone", "C:/rocm/llama-server.exe"},
		// No vllm embedding emitter exists, so a vllm pin keeps the class
		// default rather than handing llama flags to a vllm binary.
		{"vllm pin keeps the default exe", "managed-lemonade", "managed-vllm", "C:/rocm/llama-server.exe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Settings{ServerExe: "C:/rocm/llama-server.exe", Backends: withDefault(tc.def)}
			got := embeddingCmdLines(s, row, &Override{Backend: tc.pin}, meta)[0]
			if got != tc.want {
				t.Errorf("exe = %q, want %q", got, tc.want)
			}
		})
	}

	// Single-backend setup: nothing in the registry, legacy exe unchanged.
	s := Settings{ServerExe: "llama-server"}
	if got := embeddingCmdLines(s, row, nil, meta)[0]; got != "llama-server" {
		t.Errorf("legacy exe = %q, want llama-server", got)
	}
}

func TestEmbeddingCmdLines(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 8}
	row := GgufRow{FullPath: `C:\models\bge\bge-m3.gguf`}
	joined := strings.Join(embeddingCmdLines(s, row, nil, Metadata{Architecture: "bert", ContextLength: 8192}), " ")
	for _, want := range []string{"--embeddings", "--pooling auto", "-ngl 99", "-c 8192", "-m C:/models/bge/bge-m3.gguf"} {
		if !strings.Contains(joined, want) {
			t.Errorf("embedding cmd missing %q in: %s", want, joined)
		}
	}
}
