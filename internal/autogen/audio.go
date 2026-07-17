package autogen

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ttsArchs are general.architecture values marking a Qwen3-TTS "talker" GGUF — a
// small autoregressive LM that emits audio-codec tokens, served by qwentts.cpp's
// tts-server (OpenAI /v1/audio/speech) instead of llama-server. The talker loads
// together with a companion codec GGUF (the 12Hz audio tokenizer), paired in
// discover.go.
//
// ponytail: only the Qwen3-TTS family exists on disk today, and the arch tag is
// unverified against a real header (HF reports "qwen3-tts"). The ttsTalkerFileRe
// name fallback catches the talker even if it reports a bare "qwen3" LM arch —
// extend this list when a header's "# arch=<x>" YAML comment names a new one.
var ttsArchs = map[string]bool{
	"qwen3-tts": true,
	"qwentts":   true,
}

// ttsTalkerFileRe matches the canonical talker filename (Serveurperso repo, e.g.
// qwen-talker-1.7b-base-Q8_0.gguf). Name fallback for a talker whose header arch
// is a bare LM arch that would otherwise route to llama-server.
var ttsTalkerFileRe = regexp.MustCompile(`(?i)talker`)

func isTTSArch(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		return false
	}
	if ttsArchs[a] {
		return true
	}
	for k := range ttsArchs {
		if strings.HasPrefix(a, k) {
			return true
		}
	}
	return false
}

// IsTTSModel reports whether a GGUF is a Qwen3-TTS talker: a known TTS arch, or a
// file named "*talker*" (the canonical repo naming) so a talker reporting a bare
// LM arch still routes to tts-server rather than the chat catalog.
func IsTTSModel(meta Metadata, fileName string) bool {
	return isTTSArch(meta.Architecture) || ttsTalkerFileRe.MatchString(fileName)
}

// ttsCmdLines builds the tts-server argv (exe first) for a talker GGUF plus its
// paired codec. Shared by emitTTSModel and RenderSoloCmd so the editor preview
// matches a save. Talker + codec are both tiny (<1GB Q8), fully resident — no
// KV/offload sizing. Voice is per-request (/v1/audio/speech "voice"), not a launch
// flag. `${PORT}` in the cmd is what makes config auto-derive the proxy URL.
func ttsCmdLines(s Settings, row GgufRow, ov *Override) []string {
	lines := []string{
		s.TtsServerExe,
		fmt.Sprintf("--model %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
	}
	if row.CodecPath != "" {
		lines = append(lines, fmt.Sprintf("--codec %s", strings.ReplaceAll(row.CodecPath, "\\", "/")))
	}
	// Persist cloned voices next to the talker gguf so they survive restarts
	// (tts-server keeps them in RAM otherwise). Per-file dir keeps a model's
	// latents from bleeding into another's.
	stem := strings.TrimSuffix(filepath.Base(row.FullPath), filepath.Ext(row.FullPath))
	voicesDir := filepath.Join(filepath.Dir(row.FullPath), "voices", stem)
	lines = append(lines, fmt.Sprintf("--voices-dir %s", strings.ReplaceAll(voicesDir, "\\", "/")))
	lines = append(lines, "--port ${PORT}")
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitTTSModel writes a tts-server YAML entry for a Qwen3-TTS talker GGUF. The
// capabilities in:[text] out:[audio] block is what makes /v1/models report
// audio_speech=true, so the playground's Speech tab lists the model.
//
// checkEndpoint is "/health": tts-server opens its listen socket only AFTER the
// talker + codec weights finish loading, and GET /health then returns 200
// {"status":"ok"}. Probing it gates readiness so the router holds the first
// request until load completes, instead of forwarding early and getting a 502.
func emitTTSModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name, arch string, emitted *[]string) {
	codec := "MISSING"
	if row.CodecPath != "" {
		codec = filepath.Base(row.CodecPath)
	}
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB (Qwen3-TTS talker, qwentts.cpp tts-server, codec=%s)\n", arch, row.SizeGB, codec)
	if row.CodecPath == "" {
		fmt.Fprintf(b, "  # WARNING: %s has no paired codec gguf (qwen-tokenizer-*hz) in its dir — tts-server won't start; place the codec alongside it\n", name)
	}
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range ttsCmdLines(s, row, ov) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// tts-server listens only after weights load; GET /health 200s once ready.
	b.WriteString("    checkEndpoint: /health\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [text]\n")
	b.WriteString("      out: [audio]\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
