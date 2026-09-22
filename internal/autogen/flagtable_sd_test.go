package autogen

import (
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// sdKitchenSink turns on every optional sd-server flag the emitter can write,
// including the video-only ones, so the two tests below see the widest command
// imageCmdLines can produce.
func sdKitchenSink(t *testing.T) []string {
	t.Helper()
	s := Settings{SdServerExe: "sd-server", Threads: 8}
	row := GgufRow{FullPath: "/models/ltx-2.5-22b-q4_k_m.gguf", SizeGB: 12}
	ov := &Override{
		VaePath: "/models/ltx-vae.gguf", AudioVaePath: "/models/ltx-audio-vae.gguf",
		ClipLPath: "/models/clip_l.gguf", ClipGPath: "/models/clip_g.gguf",
		T5Path: "/models/t5xxl.gguf", TextEncoderPath: "/models/gemma-4-12b.gguf",
		LlmVision: "on", LlmVisionPath: "/models/mmproj.gguf",
		LoraDir:      "/models/loras",
		OffloadToCpu: "on", TeOnCpu: "on", VaeOnCpu: "on", StreamLayers: "on",
		VaeTiling: "on", TemporalTiling: "on", DiffusionFa: "on",
		DefaultSteps: 30, DefaultCfg: 1, DefaultSampler: "euler",
		DefaultWidth: 1280, DefaultHeight: 704, DefaultFrames: 49, DefaultFps: 24,
		Threads: 8,
	}
	lines, _, _, _, _ := imageCmdLines(s, row, ov, "ltxv", row.FullPath, 3840, videoInfo{Kind: VideoFamilyLtxAV, AudioOut: true})
	argv, err := config.SanitizeCommand(lines.Effective)
	if err != nil {
		t.Fatalf("generated command does not split: %v", err)
	}
	return argv
}

// Every flag the diffusion emitter can produce must be in the sd table. Without
// this, a new emitter flag silently falls through ComposeCmd as unknown: it
// owns no knob, so a user pinning it gets the generated copy AND theirs, which
// is the duplication this table was added to stop.
func TestSdFlagTable_CoversEveryEmittedFlag(t *testing.T) {
	argv := sdKitchenSink(t)
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		name, _, inline := SplitFlagToken(tok)
		def, ok := SdFlags.Lookup(name)
		if !ok {
			t.Errorf("emitted flag %q is missing from sdFlagTable (add it there and to the knob-clearing switch)", tok)
			continue
		}
		if def.Value && !inline {
			i++
		}
	}
}

func TestSdFlagTable_NoDuplicateSpellings(t *testing.T) {
	seen := map[string]string{}
	for _, d := range sdFlagTable {
		for _, name := range append([]string{d.Name}, d.Aliases...) {
			if prev, dup := seen[name]; dup {
				t.Errorf("flag %q appears in both %q and %q", name, prev, d.Name)
			}
			seen[name] = d.Name
		}
		if d.Knob == "" {
			t.Errorf("flag %q has no knob; sd-server flags all control something", d.Name)
		}
	}
}

// The reported bug, as a test. A user pinning --video-frames and --max-vram in
// custom launch arguments must end up with exactly ONE of each, carrying their
// values, no matter how many times the config is regenerated. The old code
// appended the text beside the generated flags, so the backend saw both and
// used whichever it parsed last, and every save added another copy.
func TestImageCmdLines_CustomArgsReplaceRatherThanAccumulate(t *testing.T) {
	s := Settings{SdServerExe: "sd-server", Threads: 8}
	row := GgufRow{FullPath: "/models/ltx-2.5-22b-q4_k_m.gguf", SizeGB: 12}
	ov := &Override{
		DefaultFrames: 49, DefaultFps: 24, DefaultWidth: 1280, DefaultHeight: 704,
		CustomArgs: "--video-frames 97\n--max-vram 7\n--params-backend te=disk,vae=ROCm0,diffusion=ROCm0",
	}
	lines, _, _, _, _ := imageCmdLines(s, row, ov, "ltxv", row.FullPath, 3840, videoInfo{Kind: VideoFamilyLtxAV})
	argv, err := config.SanitizeCommand(lines.Effective)
	if err != nil {
		t.Fatalf("composed command does not split: %v", err)
	}
	count := func(flag string) int {
		n := 0
		for _, tok := range argv {
			if tok == flag {
				n++
			}
		}
		return n
	}
	for _, flag := range []string{"--video-frames", "--max-vram", "--params-backend"} {
		if got := count(flag); got != 1 {
			t.Errorf("%s appears %d times, want exactly 1: %v", flag, got, argv)
		}
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--video-frames 97") {
		t.Errorf("custom --video-frames did not win: %v", argv)
	}
	if strings.Contains(joined, "--video-frames 49") {
		t.Errorf("generated --video-frames survived beside the custom one: %v", argv)
	}
	// A flag the user did NOT pin must still be emitted, or suppression has
	// turned into "custom args replace the whole command".
	if !strings.Contains(joined, "--fps 24") {
		t.Errorf("unpinned generated flag was dropped: %v", argv)
	}
}
