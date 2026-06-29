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
