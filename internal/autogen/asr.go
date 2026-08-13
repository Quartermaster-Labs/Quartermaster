package autogen

import (
	"fmt"
	"regexp"
	"strings"
)

// asrArchs are general.architecture values marking a Parakeet / FastConformer ASR
// GGUF — a speech-recognition encoder+decoder served by parakeet.cpp's
// parakeet-server (OpenAI /v1/audio/transcriptions) instead of llama-server.
//
// ponytail: the arch tags are taken from parakeet.cpp's GGUF exports and are
// unverified against a real header here. The asrFileRe name fallback catches the
// canonical filenames even if the header reports something else — extend this
// list when a model's "# arch=<x>" YAML comment names a new one.
var asrArchs = map[string]bool{
	"parakeet":      true,
	"fastconformer": true,
	"conformer":     true,
	"nemo-asr":      true,
}

// asrFileRe matches the canonical parakeet.cpp gguf filenames (mudler/parakeet-cpp-gguf,
// e.g. parakeet-tdt-0.6b-v3-q8_0.gguf, nemotron-3.5-asr-streaming-0.6b-q8_0.gguf).
//
// Deliberately NARROW on the nemotron side: bare "nemotron" also names NVIDIA's
// text LLMs, which must keep routing to llama-server. Only nemotron builds that
// spell out asr/speech are claimed here.
var asrFileRe = regexp.MustCompile(`(?i)(parakeet|nemotron[-_. \d]*(asr|speech))`)

func isASRArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		return false
	}
	if asrArchs[a] {
		return true
	}
	for k := range asrArchs {
		if strings.HasPrefix(a, k) {
			return true
		}
	}
	return false
}

// IsASRModel reports whether a GGUF is a Parakeet-family speech-to-text model: a
// known ASR arch, or a canonical parakeet.cpp filename so a model whose header
// reports an unmapped arch still routes to parakeet-server rather than the chat
// catalog.
func IsASRModel(meta Metadata, fileName string) bool {
	return isASRArch(meta.Architecture) || asrFileRe.MatchString(fileName)
}

// asrCmdLines builds the parakeet-server argv (exe first) for an ASR GGUF. Shared
// by emitASRModel and RenderSoloCmd so the editor preview matches a save.
//
// No KV/offload sizing applies: parakeet is an encoder-decoder transducer with no
// growing KV cache, and the 0.6B weights are fully resident (<1GB at q8_0).
// parakeet.cpp runs 20-36x faster than realtime on CPU alone, so the GPU is
// optional here — pass `--n-gpu-layers` (or whatever the build exposes) via
// Override.ExtraArgs to move it onto the card. `${PORT}` in the cmd is what makes
// config auto-derive the proxy URL.
func asrCmdLines(s Settings, row GgufRow, ov *Override) []string {
	lines := []string{
		s.AsrServerExe,
		fmt.Sprintf("--model %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
		"--port ${PORT}",
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitASRModel writes a parakeet-server YAML entry for a speech-to-text GGUF. The
// capabilities in:[audio] out:[text] block is what makes /v1/models report
// audio_transcriptions=true, so the playground's Transcription tab lists the model.
//
// checkEndpoint is "none": parakeet-server documents no health route, and probing
// an unknown path would 404 forever and never mark the model ready. Readiness
// falls back to the listen socket opening.
func emitASRModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, emitted *[]string) {
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (Parakeet ASR, parakeet.cpp parakeet-server)\n", arch, row.SizeGB)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range asrCmdLines(s, row, ov) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// No estVramGB: parakeet runs on the CPU unless a user opts into GPU via
	// extraArgs, so it occupies no VRAM budget and must never cost a chat model
	// its residency. A GPU opt-in under-charges — an accepted trade for not
	// evicting on every dictation.
	// parakeet-server exposes no health route; readiness = socket open.
	b.WriteString("    checkEndpoint: none\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [audio]\n")
	b.WriteString("      out: [text]\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
