package autogen

import (
	"strings"
	"testing"
)

func TestIsImageArch(t *testing.T) {
	for _, a := range []string{"flux", "FLUX", "flux.1", "sd3", "sd3.5", "qwen_image", "z_image", " stable-diffusion "} {
		if !isImageArch(a) {
			t.Errorf("isImageArch(%q) = false, want true", a)
		}
	}
	for _, a := range []string{"", "qwen3moe", "llama", "gemma4"} {
		if isImageArch(a) {
			t.Errorf("isImageArch(%q) = true, want false", a)
		}
	}
}

func TestEmitImageModel(t *testing.T) {
	var b strings.Builder
	var emitted []string
	pool := EncoderSet{FluxVae: "ae.safetensors", ClipL: "clip_l.safetensors", T5: "t5xxl.gguf", ZimageVae: "zae.safetensors", QwenLlm: "qwen3.gguf"}
	s := Settings{SdServerExe: "sd-server", TtlSec: 600, TargetVramGB: 7, VramOverheadGB: 0.5, Threads: 7, Encoders: pool}

	// Big model (6.5GB + 1.5 compute > 6.5 budget) → offload path. No per-model
	// override: VAE + CLIP-L + T5 must be auto-attached from the arch (flux) pool.
	big := GgufRow{FullPath: `C:\models\flux.gguf`, SizeGB: 6.5}
	emitImageModel(&b, s, big, &Override{}, "flux-q4", "flux", false, 0, &emitted)
	out := b.String()
	for _, want := range []string{"sd-server", "--diffusion-model C:/models/flux.gguf", "--listen-port ${PORT}", "--max-vram 6.5", "--vae ae.safetensors", "--clip_l clip_l.safetensors", "--t5xxl t5xxl.gguf", "--diffusion-fa", "--vae-tiling", "--offload-to-cpu", "--vae-on-cpu", "--backend te=cpu", "offload=true", "checkEndpoint: /", "out: [image]", "in: [text]", "ttl: 600"} {
		if !strings.Contains(out, want) {
			t.Errorf("emit missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("complete pool should not warn:\n%s", out)
	}
	if len(emitted) != 1 || emitted[0] != "flux-q4" {
		t.Errorf("emitted = %v, want [flux-q4]", emitted)
	}

	// Override knobs: component paths + gen defaults emit, toggled-off savers omit,
	// offload pinned off wins over the auto "fits" decision.
	var b3 strings.Builder
	var em3 []string
	ov := &Override{
		VaePath: `C:\models\ae.safetensors`, ClipLPath: "clip_l.gguf", T5Path: "t5xxl.gguf",
		TextEncoderPath: "qwen3.gguf", VaeTiling: "off", DiffusionFa: "off", TeOnCpu: "off",
		OffloadToCpu: "off", DefaultSteps: 8, DefaultCfg: 1.0, DefaultSampler: "euler",
		DefaultWidth: 768, DefaultHeight: 512, ExtraArgs: "--clip-on-cpu",
	}
	emitImageModel(&b3, s, big, ov, "flux-tuned", "flux", false, 0, &em3)
	out3 := b3.String()
	for _, want := range []string{"--vae C:/models/ae.safetensors", "--clip_l clip_l.gguf", "--t5xxl t5xxl.gguf", "--llm qwen3.gguf", "--steps 8", "--cfg-scale 1", "--sampling-method euler", "--width 768", "--height 512", "--clip-on-cpu", "offload=false"} {
		if !strings.Contains(out3, want) {
			t.Errorf("tuned emit missing %q:\n%s", want, out3)
		}
	}
	for _, unwant := range []string{"--vae-tiling", "--diffusion-fa", "--backend te=cpu", "--offload-to-cpu"} {
		if strings.Contains(out3, unwant) {
			t.Errorf("tuned emit should omit %q:\n%s", unwant, out3)
		}
	}

	// Small model (2GB + 1.5 < 6.5 budget) → fits resident, no offload flags.
	var b2 strings.Builder
	var em2 []string
	small := GgufRow{FullPath: `C:\models\sd15.gguf`, SizeGB: 2.0}
	emitImageModel(&b2, s, small, &Override{}, "sd15-q4", "sd1", true, 0, &em2)
	out2 := b2.String()
	// SD1 is a full-checkpoint arch → -m, no external components.
	if !strings.Contains(out2, "-m C:/models/sd15.gguf") || strings.Contains(out2, "--diffusion-model") {
		t.Errorf("sd1 should load via -m, not --diffusion-model:\n%s", out2)
	}
	if strings.Contains(out2, "--offload-to-cpu") || strings.Contains(out2, "offload=true") {
		t.Errorf("small model should not offload:\n%s", out2)
	}
	// --vae-on-cpu rides with offload only; a resident model keeps VAE on GPU.
	if strings.Contains(out2, "--vae-on-cpu") {
		t.Errorf("resident model should not force vae-on-cpu:\n%s", out2)
	}
	// vae-tiling is always on (caps the VAE decode VRAM spike), even when resident.
	if !strings.Contains(out2, "--vae-tiling") {
		t.Errorf("small model should still vae-tile:\n%s", out2)
	}
	// te=cpu is unconditional (external encoder always parked on CPU), even when the
	// diffusion weights fit resident.
	if !strings.Contains(out2, "--backend te=cpu") {
		t.Errorf("small model should still set te=cpu:\n%s", out2)
	}
}

func TestFluxGuidance(t *testing.T) {
	cases := map[string]struct {
		val float64
		ok  bool
	}{
		"flux1-kontext-dev-Q4_K_M": {2.5, true},
		"flux1-fill-dev-Q4_K_S":    {30, true},
		"flux1-schnell-Q4_K_S":     {0, false},
		"flux1-dev":                {0, false},
	}
	for name, want := range cases {
		if v, ok := fluxGuidance(name); v != want.val || ok != want.ok {
			t.Errorf("fluxGuidance(%q) = (%g,%t), want (%g,%t)", name, v, ok, want.val, want.ok)
		}
	}
}

func TestResolveComponents(t *testing.T) {
	pool := EncoderSet{FluxVae: "ae", ClipL: "cl", ClipG: "cg", T5: "t5", ZimageVae: "zae", QwenLlm: "qwen"}

	// flux: vae + clip_l + t5, nothing missing.
	if c, m := resolveComponents(pool, nil, "flux", "flux1-schnell", nil, 0); c.vae != "ae" || c.clipL != "cl" || c.t5 != "t5" || c.clipG != "" || c.llm != "" || len(m) != 0 {
		t.Errorf("flux: got %+v missing=%v", c, m)
	}
	// chroma (arch flux, name-detected): vae + t5, NO clip_l.
	if c, m := resolveComponents(pool, nil, "flux", "Chroma1-HD-Q5_K_M", nil, 0); c.vae != "ae" || c.t5 != "t5" || c.clipL != "" || len(m) != 0 {
		t.Errorf("chroma should be vae+t5 only: got %+v missing=%v", c, m)
	}
	// z-image (arch lumina2): vae + llm.
	if c, m := resolveComponents(pool, nil, "lumina2", "Z-Image-Turbo", nil, 0); c.vae != "zae" || c.llm != "qwen" || c.clipL != "" || len(m) != 0 {
		t.Errorf("z-image should be vae+llm: got %+v missing=%v", c, m)
	}
	// sdxl: full-checkpoint arch → never resolves external components (loads via -m).
	// resolveComponents isn't called for it in emit, and falls through to attach nothing.
	if c, m := resolveComponents(pool, nil, "sdxl", "animagineXLV31", nil, 0); c.vae != "" || c.clipL != "" || c.clipG != "" || len(m) != 0 {
		t.Errorf("sdxl should attach nothing (served via -m): got %+v missing=%v", c, m)
	}
	// empty pool: flux reports every required role missing.
	if _, m := resolveComponents(EncoderSet{}, nil, "flux", "flux1-fill-dev", nil, 0); len(m) != 3 {
		t.Errorf("empty-pool flux should miss 3 roles, got %v", m)
	}
	// override supplies what the pool lacks → wins and clears the missing role.
	ov := &Override{VaePath: "ov-ae", ClipLPath: "ov-cl", T5Path: "ov-t5"}
	if c, m := resolveComponents(EncoderSet{}, ov, "flux", "flux1-fill-dev", nil, 0); c.vae != "ov-ae" || c.clipL != "ov-cl" || c.t5 != "ov-t5" || len(m) != 0 {
		t.Errorf("override should win and clear missing: got %+v missing=%v", c, m)
	}
}

// LongCat ships its DiT in BFL (flux) tensor layout, so it reports
// general.architecture "flux" and would otherwise be wired like flux.1:
// clip_l + t5xxl and no --llm. It actually wants flux.1's AE plus a Qwen2.5-VL
// text encoder, and the edit variant additionally wants that encoder's vision
// projector. Nothing here is declared: the components come from the scanned
// pool, matched on the width the DiT itself asks for.
func TestResolveComponents_LongCatFromPool(t *testing.T) {
	pool := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/ae.safetensors", Role: RoleVae, Family: VaeFamilyFlux},
		{Path: "/m/clip_l.safetensors", Role: RoleClip, Width: 768},
		{Path: "/m/t5xxl.gguf", Role: RoleT5, Width: 4096},
		{Path: "/m/qwen25vl/model-Q8_0.gguf", Role: RoleLlm, Width: 3584, SizeGB: 8,
			Mmproj: "/m/qwen25vl/mmproj-F16.gguf", Vision: true},
		{Path: "/m/qwen3-4b.gguf", Role: RoleLlm, Width: 2560, SizeGB: 3},
	}}

	// Edit variant: vae + llm + the paired projector, and NO clip_l/t5.
	c, m := resolveComponents(EncoderSet{}, nil, "flux", "LongCat-Image-Edit-Turbo-Q8_0", pool, 3584)
	if len(m) != 0 {
		t.Fatalf("missing = %v, want none", m)
	}
	if c.vae != "/m/ae.safetensors" {
		t.Errorf("vae = %q", c.vae)
	}
	if c.llm != "/m/qwen25vl/model-Q8_0.gguf" {
		t.Errorf("llm = %q", c.llm)
	}
	if c.llmVision != "/m/qwen25vl/mmproj-F16.gguf" {
		t.Errorf("llm_vision = %q", c.llmVision)
	}
	if c.clipL != "" || c.t5 != "" || c.clipG != "" {
		t.Errorf("longcat must not take clip/t5: %+v", c)
	}

	// Base (non-edit) LongCat: same encoder, no projector.
	c2, _ := resolveComponents(EncoderSet{}, nil, "flux", "LongCat-Image-Q8_0", pool, 3584)
	if c2.llm != "/m/qwen25vl/model-Q8_0.gguf" || c2.llmVision != "" {
		t.Errorf("base longcat: %+v", c2)
	}

	// A different caption width picks a different encoder with no name table.
	c3, _ := resolveComponents(EncoderSet{}, nil, "lumina2", "Z-Image-Turbo", pool, 2560)
	if c3.llm != "/m/qwen3-4b.gguf" {
		t.Errorf("z-image llm = %q", c3.llm)
	}

	// A declared encoder still wins, and drags its own neighbour projector.
	ov := &Override{TextEncoderPath: "/m/qwen25vl/model-Q8_0.gguf"}
	c4, _ := resolveComponents(EncoderSet{}, ov, "flux", "LongCat-Image-Edit", pool, 3584)
	if c4.llm != ov.TextEncoderPath || c4.llmVision != "/m/qwen25vl/mmproj-F16.gguf" {
		t.Errorf("declared encoder: %+v", c4)
	}

	// llmVision "off" drops the projector without touching the encoder pick.
	c5, _ := resolveComponents(EncoderSet{}, &Override{LlmVision: "off"}, "flux", "LongCat-Image-Edit", pool, 3584)
	if c5.llm == "" || c5.llmVision != "" {
		t.Errorf("llmVision off: %+v", c5)
	}
}

func TestMergeImageVariant(t *testing.T) {
	base := Override{
		VaePath: "ae.safetensors", TextEncoderPath: "qwen3.gguf",
		DefaultSteps: 30, DefaultCfg: 7, VramTargetGB: 6,
	}
	// A "fast" preset overrides only steps/cfg.
	v := VariantSpec{Name: "fast", DefaultSteps: 8, DefaultCfg: 1}
	got := mergeImageVariant(base, v)
	if got.VaePath != "ae.safetensors" || got.TextEncoderPath != "qwen3.gguf" {
		t.Errorf("preset should inherit component paths, got %+v", got)
	}
	if got.DefaultSteps != 8 || got.DefaultCfg != 1 {
		t.Errorf("preset should override steps/cfg, got steps=%d cfg=%g", got.DefaultSteps, got.DefaultCfg)
	}
	if got.VramTargetGB != 6 {
		t.Errorf("preset should inherit vram budget, got %g", got.VramTargetGB)
	}
	if got.Variants != nil {
		t.Errorf("merged override must clear Variants to avoid re-emit")
	}
}
