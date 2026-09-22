package autogen

import (
	"strings"
	"testing"
)

// A video model can name a prompt enhancer, and it rides the model entry exactly
// as it does on the image path: the rewrite happens client-side before the job is
// posted, so the only thing the config carries is WHICH model and under WHAT
// system prompt. LTX is why this matters more here than for images - it was
// trained on single-paragraph audio-visual captions and degrades on a short
// prompt, so an un-rewritten prompt is out of distribution, not merely vague.
func TestEmitVideoModel_PromptEnhancer(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{
		SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1,
		PromptEnhancers: []PromptEnhancer{{
			Model:        "Gemma-4-E2B-it",
			Name:         "LTX enhancer",
			SystemPrompt: "shared fallback prompt",
			Vision:       true,
		}},
	}
	row := GgufRow{FullPath: "D:/models/ltx-2.5-22b-distilled-transformer-Q4_K_M.gguf", SizeGB: 15.7}
	vid := videoInfo{Kind: VideoFamilyLtxAV, AudioOut: true}
	ov := &Override{
		VaePath:         "D:/m/ltx-video-vae.safetensors",
		AudioVaePath:    "D:/m/ltx-audio-vae.safetensors",
		TextEncoderPath: "D:/m/gemma4-12b-with-proj.gguf",
		// Cased differently from the settings row on purpose: the row is what
		// fixes an id up to its canonical spelling.
		PromptEnhancer:     "gemma-4-e2b-it",
		PromptEnhancerEdit: "gemma-4-e2b-it",
		// One rewriter, two system prompts. LTX ships exactly this pair
		// (gemma4_t2v_system_prompt.txt / gemma4_i2v_system_prompt.txt) and they
		// are not interchangeable: the i2v one has to describe a first frame that
		// already exists.
		PromptEnhancerPrompt:     "t2v: write one paragraph.",
		PromptEnhancerEditPrompt: "i2v: the first frame is given.",
	}

	emitVideoModel(&b, s, row, ov, "ltx-2.5-22b-distilled-Q4_K_M", "ltxv", vid, 3840, &emitted)
	out := b.String()
	for _, want := range []string{
		`promptEnhancer: "Gemma-4-E2B-it"`,
		`promptEnhancerEdit: "Gemma-4-E2B-it"`,
		"t2v: write one paragraph.",
		"i2v: the first frame is given.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("video emit missing %q:\n%s", want, out)
		}
	}
	// The two keys must stay distinct: a second promptEnhancer line would be
	// silently collapsed by the YAML loader and the i2v prompt would be lost.
	if n := strings.Count(out, "promptEnhancer:"); n != 1 {
		t.Errorf("want exactly one promptEnhancer key, got %d:\n%s", n, out)
	}
}

// The enhancer is opt-in. A video model that names none must emit no enhancer
// keys at all, or the playground shows an Enhance button wired to nothing.
func TestEmitVideoModel_NoEnhancerByDefault(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1}
	row := GgufRow{FullPath: "D:/models/wan2.2-t2v-14b-Q4_K_M.gguf", SizeGB: 9}

	emitVideoModel(&b, s, row, &Override{}, "wan2.2-t2v-14b-Q4_K_M", "wan", videoInfo{Kind: VideoFamilyWan}, 0, &emitted)
	if strings.Contains(b.String(), "promptEnhancer") {
		t.Errorf("unconfigured video model must not emit an enhancer:\n%s", b.String())
	}
}

// A disabled enhancer ("none") is a refusal that has to survive emit, the same
// way it does for an image model: auto-detection gave the empty string a second
// meaning, so clearing the field would otherwise be undone by the next regen.
func TestEmitVideoModel_EnhancerDisabledSentinel(t *testing.T) {
	var b strings.Builder
	var emitted []string
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 24, VramOverheadGB: 1}
	row := GgufRow{FullPath: "D:/models/ltx-2.5-22b-distilled-transformer-Q4_K_M.gguf", SizeGB: 15.7}
	ov := &Override{PromptEnhancer: PEDisabled, PromptEnhancerPrompt: "never used"}

	emitVideoModel(&b, s, row, ov, "ltx-2.5-22b-distilled-Q4_K_M", "ltxv", videoInfo{Kind: VideoFamilyLtxAV}, 3840, &emitted)
	out := b.String()
	if strings.Contains(out, "promptEnhancer") {
		t.Errorf("disabled enhancer must emit no key:\n%s", out)
	}
	if strings.Contains(out, "never used") {
		t.Errorf("a prompt with no enhancer to run under must be dropped:\n%s", out)
	}
}
