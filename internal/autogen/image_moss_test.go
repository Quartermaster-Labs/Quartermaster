package autogen

import (
	"path/filepath"
	"strings"
	"testing"
)

// FLUX.3 klein states txt_in 20480 = 8 x 2560. Under the old "any k <= 4" cap
// it matched nothing; a plain raised cap would hand it a 4 x 5120 or a 5 x 4096
// encoder first, because the caller prefers the lowest factor.
func TestAutogen_CaptionLayouts(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/qwen3-32b.gguf", Role: RoleLlm, Family: "qwen3", Width: 5120, SizeGB: 30},
		{Path: "/m/qwen3-8b.gguf", Role: RoleLlm, Family: "qwen3", Width: 4096, SizeGB: 8},
		{Path: "/m/qwen3-vl-4b.gguf", Role: RoleLlm, Family: "qwen3vl", Width: 2560, SizeGB: 4},
	}}
	if got, _ := p.Llm(20480, false, ""); got != "/m/qwen3-vl-4b.gguf" {
		t.Errorf("llm(20480) = %q, want the 2560-wide encoder (8 layers)", got)
	}
	if got, _ := p.Llm(12288, false, ""); got != "/m/qwen3-8b.gguf" {
		t.Errorf("llm(12288) = %q, want the 4096-wide encoder (3 layers)", got)
	}
	for _, c := range []struct{ hidden, width, want int64 }{
		{4096, 4096, 1}, {12288, 4096, 3}, {20480, 2560, 8},
		{20480, 5120, 0}, {20480, 4096, 0}, {8192, 4096, 0},
	} {
		if got := captionFactor(c.hidden, c.width); got != c.want {
			t.Errorf("captionFactor(%d, %d) = %d, want %d", c.hidden, c.width, got, c.want)
		}
	}
}

func TestAutogen_EncoderPoolRecurrentLlm(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/MiMo-V2.6-Distill-Qwen-9B-BF16.gguf", Role: RoleRecurrentLlm, Family: "qwen35", Width: 4096, SizeGB: 18},
		{Path: "/m/Qwen3.5-9B-Q8_0.gguf", Role: RoleRecurrentLlm, Family: "qwen35", Width: 4096, SizeGB: 9.5},
		{Path: "/m/Qwen3.5-4B-Q6_K.gguf", Role: RoleRecurrentLlm, Family: "qwen35", Width: 2560, SizeGB: 3},
		// A plain transformer of the same width is never a recurrent pick.
		{Path: "/m/qwen3-8b.gguf", Role: RoleLlm, Family: "qwen3", Width: 4096, SizeGB: 8},
	}}
	if got := p.RecurrentLlm(4096, "qwen3.5-9b"); got != "/m/Qwen3.5-9B-Q8_0.gguf" {
		t.Errorf("hinted = %q, want the hint to beat the bigger same-width distill", got)
	}
	if got := p.RecurrentLlm(2560); got != "/m/Qwen3.5-4B-Q6_K.gguf" {
		t.Errorf("unhinted 2560 = %q", got)
	}
	if got := p.RecurrentLlm(12288); got != "" {
		t.Errorf("no concatenation for recurrent encoders, got %q", got)
	}
	// The recurrent role stays invisible to every plain-LLM caller.
	if got, _ := p.Llm(4096, false, ""); got != "/m/qwen3-8b.gguf" {
		t.Errorf("Llm(4096) = %q, want the plain transformer only", got)
	}
}

func TestAutogen_EncoderPoolTokenizer(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/von-1.0/tokenizer.json", Role: RoleTokenizer},
		{Path: "/m/unsloth/MOSS-Image-SFT-FP8/tokenizer/tokenizer.json", Role: RoleTokenizer},
	}}
	if got := p.Tokenizer("moss-image"); got != "/m/unsloth/MOSS-Image-SFT-FP8/tokenizer/tokenizer.json" {
		t.Errorf("Tokenizer(moss-image) = %q", got)
	}
	// No fallback: an unrelated vocab is worse than a missing-component warning.
	if got := p.Tokenizer("qwen3.5"); got != "" {
		t.Errorf("Tokenizer(qwen3.5) = %q, want none", got)
	}
}

// The scan collects tokenizer.json by name and keeps the FLUX.2 small decoder
// (same 32-ch latent, 384-wide decoder input) out of the VAE family.
func TestAutogen_ScanEncoderPoolTokenizerAndSmallDecoder(t *testing.T) {
	dir := t.TempDir()
	writeSafetensors(t, filepath.Join(dir, "flux2", "ae.safetensors"),
		map[string][]int64{"decoder.conv_in.weight": {512, 32, 3, 3}})
	writeSafetensors(t, filepath.Join(dir, "MOSS-Image-SFT-FP8", "vae", "diffusion_pytorch_model.safetensors"),
		map[string][]int64{"decoder.conv_in.weight": {384, 32, 3, 3}})
	writeSafetensors(t, filepath.Join(dir, "MOSS-Image-SFT-FP8", "tokenizer", "tokenizer.json"), nil)

	p := ScanEncoderPool([]string{dir})
	var vaes []string
	tok := ""
	for _, f := range p.Files {
		switch f.Role {
		case RoleVae:
			vaes = append(vaes, filepath.ToSlash(f.Path))
		case RoleTokenizer:
			tok = filepath.ToSlash(f.Path)
		}
	}
	if len(vaes) != 1 || !strings.HasSuffix(vaes[0], "flux2/ae.safetensors") {
		t.Errorf("vaes = %v, want only the full FLUX.2 ae", vaes)
	}
	if !strings.HasSuffix(tok, "MOSS-Image-SFT-FP8/tokenizer/tokenizer.json") {
		t.Errorf("tokenizer = %q", tok)
	}
}

func TestAutogen_resolveComponentsMossAndFlux3(t *testing.T) {
	pool := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/flux2/ae.safetensors", Role: RoleVae, Family: VaeFamilyFlux2},
		{Path: "/m/Qwen3.5-9B-Q8_0.gguf", Role: RoleRecurrentLlm, Family: "qwen35", Width: 4096, SizeGB: 9.5},
		{Path: "/m/MOSS-Image-SFT-FP8/tokenizer/tokenizer.json", Role: RoleTokenizer},
		{Path: "/m/Qwen3VL-4B-Instruct-Q8_0.gguf", Role: RoleLlm, Family: "qwen3vl", Width: 2560, SizeGB: 4.3},
	}}
	c, missing := resolveComponents(EncoderSet{}, nil, "moss_image", "MOSS-Image-SFT-Q8_0", pool, 4096, videoInfo{})
	if c.vae != "/m/flux2/ae.safetensors" || c.llm != "/m/Qwen3.5-9B-Q8_0.gguf" ||
		c.tokenizer != "/m/MOSS-Image-SFT-FP8/tokenizer/tokenizer.json" || len(missing) != 0 {
		t.Errorf("moss: %+v missing=%v", c, missing)
	}
	// Nothing recurrent or tokenizer on disk: both roles surface as missing,
	// which is what the model browser's companion download keys on.
	bare := &EncoderPool{Files: pool.Files[:1]}
	if _, missing := resolveComponents(EncoderSet{}, nil, "moss_image", "MOSS-Image-SFT-Q8_0", bare, 4096, videoInfo{}); strings.Join(missing, ",") != "llm,tokenizer" {
		t.Errorf("moss bare missing = %v, want [llm tokenizer]", missing)
	}

	c, missing = resolveComponents(EncoderSet{}, nil, "flux", "flux-3-image-klein-Q8_0", pool, 20480, videoInfo{})
	if c.vae != "/m/flux2/ae.safetensors" || c.llm != "/m/Qwen3VL-4B-Instruct-Q8_0.gguf" || c.t5 != "" || c.clipL != "" || len(missing) != 0 {
		t.Errorf("flux3: %+v missing=%v", c, missing)
	}

	if d := imageDefaultsFor("moss_image", "MOSS-Image-SFT-Q8_0", 4096); d.steps != 50 || d.cfg != 3 || d.flowShift != 5 {
		t.Errorf("moss defaults = %+v", d)
	}
	if d := imageDefaultsFor("flux", "flux-3-image-klein-Q8_0", 20480); d.steps != 16 || d.cfg != 1 {
		t.Errorf("flux3 defaults = %+v", d)
	}
}

// Qwen3-4B and Qwen3-VL-4B are both 2560 wide, so width cannot say which a DiT
// wants. The real box that found this: a Qwen3-4B qwenLlm pin (for Z-Image)
// won FLUX.3's 8 x 2560 match, and with no pin the larger VL file won Z-Image's.
func TestAutogen_ImageEncoderArch(t *testing.T) {
	const q3, vl = "/m/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", "/m/Qwen3VL-4B-Instruct-Q8_0.gguf"
	both := &EncoderPool{Files: []ComponentFile{
		{Path: q3, Role: RoleLlm, Family: "qwen3", Width: 2560, SizeGB: 2.3},
		{Path: vl, Role: RoleLlm, Family: "qwen3vl", Width: 2560, SizeGB: 4.0},
	}}
	cases := []struct {
		label, arch, name string
		cond              int64
		pin, want         string
		pool              *EncoderPool
	}{
		{"flux3, q3 pin", "flux", "flux-3-image-klein-q8_0", 20480, q3, vl, both},
		{"flux3, no pin", "flux", "flux-3-image-klein-q8_0", 20480, "", vl, both},
		{"z-image, no pin", "lumina2", "z-image-turbo-q8_0", 2560, "", q3, both},
		{"z-image, vl pin", "lumina2", "z-image-turbo-q8_0", 2560, vl, q3, both},
		{"klein 4b, no pin", "flux", "flux-2-klein-4b-q8_0", 7680, "", q3, both},
		// Only the impostor on disk: report the encoder missing rather than
		// wire it (that is what makes the model browser offer the right one).
		{"flux3, only q3", "flux", "flux-3-image-klein-q8_0", 20480, q3, "",
			&EncoderPool{Files: both.Files[:1]}},
	}
	for _, c := range cases {
		got, missing := resolveComponents(EncoderSet{QwenLlm: c.pin}, nil, c.arch, c.name, c.pool, c.cond, videoInfo{})
		if got.llm != c.want {
			t.Errorf("%s: llm = %q, want %q", c.label, got.llm, c.want)
		}
		if c.want == "" && !strings.Contains(strings.Join(missing, ","), "llm") {
			t.Errorf("%s: missing = %v, want llm reported", c.label, missing)
		}
	}
}
