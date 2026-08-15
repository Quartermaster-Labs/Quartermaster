package autogen

import (
	"fmt"
	"strings"
)

// embeddingOverheadGB pads an embedder's weights into a VRAM admission estimate
// (compute buffer + the single short KV sequence it keeps). These models are
// sub-GB and fully offloaded, so a flat pad beats running the LLM sizer here.
const embeddingOverheadGB = 0.5

// embeddingArchs are general.architecture values that mark a text-embedding GGUF
// (BERT-family encoders served with --embedding, no generation head). The
// pooling_type metadata key catches the rest (incl. LLM-arch embedders like
// Qwen3-Embedding that share "qwen3" with chat models), so this list only needs
// the encoder archs that may omit it.
//
// ponytail: list grounded in llama.cpp arch names; extend it when an embedder
// slips through as a (chat-broken) llm — its YAML "# arch=<x>" comment names the
// arch to add here.
var embeddingArchs = map[string]bool{
	"bert":           true,
	"nomic-bert":     true,
	"nomic-bert-moe": true,
	"jina-bert-v2":   true,
	"xlm-roberta":    true,
	"roberta":        true,
	"gte":            true,
}

// isEmbeddingArch reports whether arch identifies a BERT-family embedder. Exact
// match plus a prefix fallback so versioned archs still hit.
func isEmbeddingArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		return false
	}
	if embeddingArchs[a] {
		return true
	}
	for k := range embeddingArchs {
		if strings.HasPrefix(a, k) {
			return true
		}
	}
	return false
}

// IsEmbeddingModel reports whether a GGUF is a text embedder: either a known
// encoder arch, or any model that bakes a pooling_type (the authoritative
// signal — generative models leave it unset, embedders set mean/cls/last).
func IsEmbeddingModel(meta Metadata) bool {
	return isEmbeddingArch(meta.Architecture) || meta.PoolingType > 0
}

// embeddingCtx caps the served context for an embedder: its trained max (small —
// 512/2048/8192), bounded so a model advertising a huge window doesn't reserve
// pointless KV. Embedders process one pass, no autoregression.
func embeddingCtx(meta Metadata, ov *Override) int {
	if ov != nil && ov.Ctx > 0 {
		return ov.Ctx
	}
	ctx := 512
	if meta.ContextLength > 0 {
		ctx = int(meta.ContextLength)
	}
	if ctx > 8192 {
		ctx = 8192
	}
	return ctx
}

// embeddingCmdLines builds the llama-server argv (exe first) for an embedding
// gguf. Embedders are small and fully offloaded (-ngl 99); no KV-cost sizing,
// spec-decode, or chat/jinja flags apply. --pooling auto lets llama-server read
// the model's baked pooling_type. Shared by emitEmbeddingModel and RenderSoloCmd
// so the editor preview matches a save.
func embeddingCmdLines(s Settings, row GgufRow, ov *Override, meta Metadata) []string {
	threads := s.Threads
	if ov != nil && ov.Threads > 0 {
		threads = ov.Threads
	}
	lines := []string{
		s.ServerExe,
		fmt.Sprintf("-m %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
		"--port ${PORT}",
		"--host 127.0.0.1",
		corsOriginsFlag,
		"--embeddings",
		"--pooling auto",
		"-ngl 99",
		fmt.Sprintf("-c %d", embeddingCtx(meta, ov)),
		fmt.Sprintf("-t %d", threads),
		"--no-warmup --no-ui --metrics --props",
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitEmbeddingModel writes a llama-server YAML entry for an embedding GGUF. The
// capabilities.embedding flag is what makes /v1/models report embeddings=true,
// so the UI buckets it under Embed instead of chat-able LLMs.
func emitEmbeddingModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, meta Metadata, emitted *[]string) {
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (text embedder, llama-server --embeddings, pooling_type=%d)\n", meta.Architecture, row.SizeGB, meta.PoolingType)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range embeddingCmdLines(s, row, ov, meta) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// Embedders are small, fully offloaded and their KV is one short sequence:
	// weights plus a flat pad is close enough for admission.
	writeEstVram(b, row.SizeGB+embeddingOverheadGB)
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      embedding: true\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
