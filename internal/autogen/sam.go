package autogen

import (
	"fmt"
	"path/filepath"
	"strings"
)

// samDefaultExe is the sam3_server binary name. SAM has no legacy Settings exe
// (unlike llama/sd/tts), so when no backend registry entry of class "segment" is
// installed it derives as a sibling of ServerExe (the same fallback sd/tts use):
// sam3_server.exe lives in the shared backends dir beside its ggml*.dll runtime,
// not on PATH. Bare name only when ServerExe itself is bare (no dir to borrow).
const samDefaultExe = "sam3_server"

// samFallbackExe returns the sibling-of-ServerExe sam3_server path, or the bare
// name when ServerExe carries no directory (mirrors applyDefaults' sd/tts derive).
func samFallbackExe(s Settings) string {
	if strings.ContainsAny(s.ServerExe, `/\`) {
		return filepath.Join(filepath.Dir(s.ServerExe), samDefaultExe)
	}
	return samDefaultExe
}

// samCmdLines builds the sam3_server argv (exe first) for a SAM *.ggml model.
// Shared by emitSamModel and RenderSoloCmd so the editor preview matches a save.
// SAM models are tiny (15-22 MB); no KV/offload sizing. `${PORT}` is what makes
// the config auto-derive the proxy URL.
//
// Placement: always CPU. The spawn guard (LiveOffloadArgs) appends --no-gpu for
// every .ggml because the Vulkan SAM backend is numerically broken on this
// hardware (RX 7900 XTX) — both text (PCS) and box/point (PVS) inference return
// garbage, while CPU is correct. Not baked here (the guard owns placement); an
// explicit extraArgs "--no-gpu" is a harmless no-op.
func samCmdLines(s Settings, row GgufRow, ov *Override) []string {
	exe := resolveBackend(s, ov, "segment").Exe
	if exe == "" {
		exe = samFallbackExe(s)
	}
	lines := []string{
		exe,
		fmt.Sprintf("--model %s", strings.ReplaceAll(row.FullPath, "\\", "/")),
		"--host 127.0.0.1",
		"--port ${PORT}",
	}
	if s.Threads > 0 {
		lines = append(lines, fmt.Sprintf("--threads %d", s.Threads))
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitSamModel writes a sam3_server YAML entry for a SAM *.ggml model. Served via
// POST /v1/segment (image + box/point prompts -> mask PNGs). checkEndpoint
// /health gates readiness until the model finishes loading. The capabilities
// in:[image] out:[image] block buckets it out of the chat catalog and lets a UI
// know it consumes/produces images.
func emitSamModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, emitted *[]string) {
	fmt.Fprintf(b, "\n  # size=%gGB (SAM segmentation, sam3_server, %s)\n", row.SizeGB, row.FileName)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range samCmdLines(s, row, ov) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	b.WriteString("    checkEndpoint: /health\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      segmentation: true\n")
	b.WriteString("      in: [image]\n")
	b.WriteString("      out: [image]\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
