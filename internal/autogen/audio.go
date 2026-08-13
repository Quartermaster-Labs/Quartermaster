package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The "tts" backend class holds two engines that are NOT interchangeable — each
// reads its own GGUF format, so a model is routed to the engine its weights were
// converted for:
//
//	ttsKindQwen   qwentts.cpp tts-server — Qwen3-TTS talker + paired codec gguf
//	ttsKindTTSCpp mmwillet/TTS.cpp tts-server — Kokoro / Parler / Dia / Orpheus
//
// Both speak the same OpenAI surface (/v1/audio/speech, /v1/audio/voices,
// /health), which is why they share a class and why the playground's Speech tab
// and read-aloud voice picker work against either without changes.
const (
	ttsKindQwen   = "tts"
	ttsKindTTSCpp = "ttscpp"
)

// ttsOverheadGB pads a qwentts talker's weights into a VRAM admission estimate
// (its short KV plus the codec's decode buffers). Talkers are sub-GB and take no
// KV/offload sizing, so a flat pad is the right resolution here.
const ttsOverheadGB = 0.3

// codecSizeGB is the paired audio-tokenizer gguf's on-disk size, loaded into the
// same process as the talker. 0 when unpaired (the emit already warns) or
// unreadable — an under-estimate, not a spawn failure.
func codecSizeGB(row GgufRow) float64 {
	if row.CodecPath == "" {
		return 0
	}
	fi, err := os.Stat(row.CodecPath)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / (1 << 30)
}

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

// ttscppArchs are the architectures TTS.cpp writes into its own GGUF exports.
// Same ponytail as ttsArchs: these are the upstream model names, not verified
// against a real header, so ttscppFileRe carries the detection in practice —
// check a generated config's "# arch=<x>" comment and add the real tag here.
var ttscppArchs = map[string]bool{
	"kokoro":     true,
	"parler-tts": true,
	"parler_tts": true,
	"parler":     true,
	"dia":        true,
	"orpheus":    true,
}

// ttsTalkerFileRe matches the canonical talker filename (Serveurperso repo, e.g.
// qwen-talker-1.7b-base-Q8_0.gguf). Name fallback for a talker whose header arch
// is a bare LM arch that would otherwise route to llama-server.
var ttsTalkerFileRe = regexp.MustCompile(`(?i)talker`)

// ttscppFileRe matches the TTS.cpp release filenames (e.g.
// Kokoro_no_espeak_Q8_0.gguf, Parler_TTS_Mini_Q5_0.gguf). "dia" is deliberately
// absent — three letters that appear inside ordinary words would misroute chat
// models to a TTS server; a Dia gguf needs its arch tag or an explicit backend.
var ttscppFileRe = regexp.MustCompile(`(?i)(kokoro|parler[-_ ]?tts|orpheus)`)

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

// isTTSCppModel reports whether a GGUF is a TTS.cpp export (Kokoro & friends)
// rather than a Qwen3-TTS talker. These carry no codec sidecar — the vocoder is
// baked into the same file — so the qwentts --codec pairing does not apply.
func isTTSCppModel(meta Metadata, fileName string) bool {
	if a := strings.ToLower(strings.TrimSpace(meta.Architecture)); a != "" && ttscppArchs[a] {
		return true
	}
	return ttscppFileRe.MatchString(fileName)
}

// IsTTSModel reports whether a GGUF is served by a TTS engine rather than
// llama-server: a Qwen3-TTS talker (known arch, or a "*talker*" filename so a
// talker reporting a bare LM arch still routes here) or a TTS.cpp export.
func IsTTSModel(meta Metadata, fileName string) bool {
	return isTTSArch(meta.Architecture) || ttsTalkerFileRe.MatchString(fileName) || isTTSCppModel(meta, fileName)
}

// ttsBackend resolves which speech engine serves this model. The model's own
// format is the preferred kind (the two engines cannot read each other's
// weights), so a fleet holding both registry entries still routes each model
// correctly; an explicit per-model Override.Backend overrules that. Returns the
// normalized kind plus the exe to launch.
func ttsBackend(s Settings, row GgufRow, ov *Override, meta Metadata) (kind, exe string) {
	prefer := ttsKindQwen
	if isTTSCppModel(meta, row.FileName) {
		prefer = ttsKindTTSCpp
	}
	be := resolveBackendPreferring(s, ov, "tts", prefer)
	kind = strings.ToLower(strings.TrimSpace(be.Kind))
	if kindClass(kind) != "tts" {
		// No registry entry matched (or a stale id resolved to nothing): keep the
		// model's own family and the legacy derived exe, so single-backend setups
		// behave exactly as before the registry existed.
		kind = prefer
	} else if kind == "tts.cpp" || kind == "kokoro" {
		kind = ttsKindTTSCpp
	} else if kind == "tts-server" || kind == "speech" {
		kind = ttsKindQwen
	}
	exe = be.Exe
	if exe == "" {
		// TtsServerExe is the qwentts exe; for TTS.cpp it is at best a same-named
		// binary from a different project. emitTTSModel warns about that case
		// rather than silently emitting a command that will fail at spawn.
		exe = s.TtsServerExe
	}
	return kind, exe
}

// ttsCmdLines builds the speech-server argv (exe first) for a TTS GGUF. Shared
// by emitTTSModel and RenderSoloCmd so the editor preview matches a save. Both
// engines' models are small and fully resident — no KV/offload sizing — and the
// voice is per-request (/v1/audio/speech "voice"), not a launch flag. `${PORT}`
// in the cmd is what makes config auto-derive the proxy URL.
func ttsCmdLines(s Settings, row GgufRow, ov *Override, meta Metadata) []string {
	kind, exe := ttsBackend(s, row, ov, meta)
	modelPath := strings.ReplaceAll(row.FullPath, "\\", "/")

	var lines []string
	if kind == ttsKindTTSCpp {
		// TTS.cpp: one self-contained gguf (vocoder baked in), --model-path, and
		// long flags only — its parser binds -t to BOTH --temperature and
		// --timeout, so short forms are ambiguous. No GPU flag is emitted:
		// --use-metal is macOS-only and there is no CUDA/ROCm path upstream, so
		// this runs on CPU. Kokoro is 87M params, which CPU handles fine.
		lines = []string{
			exe,
			fmt.Sprintf("--model-path %s", modelPath),
			"--host 127.0.0.1",
			"--port ${PORT}",
		}
		if ov != nil && ov.Threads > 0 {
			lines = append(lines, fmt.Sprintf("--n-threads %d", ov.Threads))
		}
		if ov != nil {
			if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
				lines = append(lines, extra)
			}
		}
		return lines
	}

	lines = []string{
		exe,
		fmt.Sprintf("--model %s", modelPath),
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

// emitTTSModel writes a speech-server YAML entry for a TTS GGUF. The
// capabilities in:[text] out:[audio] block is what makes /v1/models report
// audio_speech=true, so the playground's Speech tab lists the model.
//
// checkEndpoint is "/health": both engines open the listen socket only AFTER
// weights finish loading, and GET /health then returns 200. Probing it gates
// readiness so the router holds the first request until load completes, instead
// of forwarding early and getting a 502.
func emitTTSModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, meta Metadata, emitted *[]string) {
	kind, exe := ttsBackend(s, row, ov, meta)
	arch := meta.Architecture

	if kind == ttsKindTTSCpp {
		fmt.Fprintf(b, "\n  # arch=%s size=%gGB (TTS.cpp tts-server, self-contained)\n", arch, row.SizeGB)
		if exe == s.TtsServerExe && !hasBackendKind(s, ttsKindTTSCpp) {
			// Both projects ship a binary called tts-server. Falling back to the
			// qwentts exe here would spawn the wrong engine against weights it
			// cannot parse, which reads as a mystery load failure.
			fmt.Fprintf(b, "  # WARNING: no TTS.cpp backend registered (Settings -> Backends, engine \"TTS.cpp\"); using %s, which is the qwentts.cpp binary and will not load this model\n", exe)
		}
	} else {
		codec := "MISSING"
		if row.CodecPath != "" {
			codec = filepath.Base(row.CodecPath)
		}
		fmt.Fprintf(b, "\n  # arch=%s size=%gGB (Qwen3-TTS talker, qwentts.cpp tts-server, codec=%s)\n", arch, row.SizeGB, codec)
		if row.CodecPath == "" {
			fmt.Fprintf(b, "  # WARNING: %s has no paired codec gguf (qwen-tokenizer-*hz) in its dir — tts-server won't start; place the codec alongside it\n", name)
		}
	}

	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range ttsCmdLines(s, row, ov, meta) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	if kind == ttsKindTTSCpp {
		// TTS.cpp VALIDATES the request's "model" field against its own model map,
		// whose keys are gguf file stems (server.cpp builds it from --model-path),
		// and 400s "Invalid Model: <id>" on anything else. Our model id is a
		// slugified name, so rewrite it on the way through — same mechanism as
		// upstream issue #69. qwentts ignores the field, hence ttscpp-only.
		fmt.Fprintf(b, "    useModelName: %q\n", ttscppModelName(row))
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// VRAM admission: qwentts runs on the GPU, so charge its talker + codec
	// weights plus a small pad. TTS.cpp is CPU-only here (no CUDA/ROCm path
	// upstream, --use-metal is macOS), so it costs no VRAM and gets no estimate
	// — charging it would evict a chat model to load an 87M CPU voice.
	if kind != ttsKindTTSCpp {
		writeEstVram(b, row.SizeGB+codecSizeGB(row)+ttsOverheadGB)
	}
	// Both servers listen only after weights load; GET /health 200s once ready.
	b.WriteString("    checkEndpoint: /health\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [text]\n")
	b.WriteString("      out: [audio]\n")
	if kind != ttsKindTTSCpp {
		// qwentts.cpp registers new voices from a reference clip (POST
		// /v1/audio/voices). TTS.cpp has no such route — Kokoro's speakers are a
		// baked-in voice pack — so the playground must not offer cloning there.
		b.WriteString("      voiceClone: true\n")
	}
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}

// ttscppModelName is the key TTS.cpp registers this gguf under: the file stem,
// extension dropped, exactly as std::filesystem::path::stem() computes it.
func ttscppModelName(row GgufRow) string {
	base := filepath.Base(strings.ReplaceAll(row.FullPath, "\\", "/"))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// hasBackendKind reports whether the registry holds an entry of the given kind,
// used to tell "the user registered this engine" apart from "we fell back".
func hasBackendKind(s Settings, kind string) bool {
	for _, e := range s.Backends {
		k := strings.ToLower(strings.TrimSpace(e.Kind))
		if k == kind || (kind == ttsKindTTSCpp && (k == "tts.cpp" || k == "kokoro")) {
			return true
		}
	}
	return false
}
