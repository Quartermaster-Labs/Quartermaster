package autogen

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAutogen_promptEnhancerName(t *testing.T) {
	cases := []struct {
		name       string
		id         string
		wantOK     bool
		wantDir    string
		wantFamily string
	}{
		// The pair this feature was built for, as prithivMLmods ships them.
		{"qwen i2i", "qwen-image-2.1-pe-i2i-q5_k_m", true, PEDirEdit, "qwen-image-2.1"},
		{"qwen t2i", "Qwen-Image-2.1-PE-T2I-Q8_0", true, PEDirText, "qwen-image-2.1"},
		// A filename, which is what the encoder pool holds.
		{"full path", "/d/llm/models/Qwen-Image-2.1-PE-I2I-BF16.gguf", true, PEDirEdit, "qwen-image-2.1"},
		// Underscores must fold, or the pairing misses its own image model.
		{"underscores", "qwen_image_2.1_pe_t2i", true, PEDirText, "qwen-image-2.1"},
		// Spelled out, no direction: usable, just not direction-specific.
		{"spelled", "someones-prompt-enhancer-7b", true, "", "someones"},
		{"spelled joined", "flux-promptenhancer-v2", true, "", "flux"},

		// The false positives that would cost a user their text encoder.
		{"pegasus", "pegasus-7b-q4_k_m", false, "", ""},
		{"pe alone", "gpt-pe-7b", false, "", ""},
		{"direction alone", "flux-t2i-turbo", false, "", ""},
		{"ordinary chat model", "qwen3.6-27b-q4_k_m", false, "", ""},
		{"the image model itself", "qwen-image-2.1-q8_0", false, "", ""},
		{"empty", "", false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, family, ok := promptEnhancerName(c.id)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if dir != c.wantDir {
				t.Errorf("dir = %q, want %q", dir, c.wantDir)
			}
			if family != c.wantFamily {
				t.Errorf("family = %q, want %q", family, c.wantFamily)
			}
		})
	}
}

// The pairing has to survive the thing that makes ids differ in practice: the
// same model at another quant, on either side.
func TestAutogen_detectEnhancers_PairsAcrossQuants(t *testing.T) {
	rows := []GgufRow{
		{ID: "qwen-image-2.1-pe-t2i-q5_k_m"},
		{ID: "qwen-image-2.1-pe-i2i-q5_k_m"},
		{ID: "qwen3.6-27b-q4_k_m"},
	}
	auto := detectEnhancers(rows)

	got := auto.For("qwen-image-2.1-q8_0")
	if got == nil {
		t.Fatal("no enhancers paired to the image model")
	}
	if got.Text != "qwen-image-2.1-pe-t2i-q5_k_m" {
		t.Errorf("text = %q", got.Text)
	}
	if got.Edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("edit = %q", got.Edit)
	}
	if auto.For("flux-dev-q8_0") != nil {
		t.Error("paired an unrelated image model")
	}
}

// Enhancers used to get a "-vision" twin and an i2i one was paired to it. The
// twin is gone (the base profile holds the projector in VRAM), so a pin saved
// back then must land on the base id instead of naming a model that no longer
// exists. A non-enhancer "-vision" id is a real twin and is left alone.
func TestAutogen_resolveEnhancerIDs_LegacyVisionPin(t *testing.T) {
	text, edit := resolveEnhancerIDs(Settings{}, "qwen-image-2.1-q8_0", "qwen3-vl-8b-vision", "qwen-image-2.1-pe-i2i-q5_k_m-vision")
	if edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("edit = %q, want the base id", edit)
	}
	if text != "qwen3-vl-8b-vision" {
		t.Errorf("text = %q, a non-enhancer twin must be kept", text)
	}
}

// A PE model must never be offered as a text encoder: it outranks the real
// encoder on file size and the failure is a confidently unrelated image.
func TestAutogen_IsPromptEnhancerFile(t *testing.T) {
	if !IsPromptEnhancerFile("/d/models/Qwen-Image-2.1-PE-I2I-BF16.gguf") {
		t.Error("PE gguf not recognised")
	}
	if IsPromptEnhancerFile("/d/models/Qwen3-VL-8B-Instruct-Q8_0.gguf") {
		t.Error("a real text encoder was withheld from the pool")
	}
}

// Tier 2: what a model ends up carrying. The three cases that matter are the
// empty field (fill it), a configured one (never touch it) and the explicit
// refusal (which only exists because auto-fill gave "" a second meaning).
func TestAutogen_resolveEnhancerIDs(t *testing.T) {
	s := Settings{autoEnhancers: detectEnhancers([]GgufRow{
		{ID: "qwen-image-2.1-pe-t2i-q5_k_m"},
		{ID: "qwen-image-2.1-pe-i2i-q5_k_m"},
	})}

	text, edit := resolveEnhancerIDs(s, "qwen-image-2.1-q8_0", "", "")
	if text != "qwen-image-2.1-pe-t2i-q5_k_m" || edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("auto-fill: text=%q edit=%q", text, edit)
	}

	text, edit = resolveEnhancerIDs(s, "qwen-image-2.1-q8_0", "my-own-rewriter", "")
	if text != "my-own-rewriter" {
		t.Errorf("a configured id must win, got %q", text)
	}
	if edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("the other direction still fills, got %q", edit)
	}

	// PEDisabled survives resolution (writeEnhancerKey drops it), which is what
	// keeps a regeneration from re-adding an enhancer the user removed.
	text, _ = resolveEnhancerIDs(s, "qwen-image-2.1-q8_0", PEDisabled, "")
	if text != PEDisabled {
		t.Errorf("an explicit refusal must not be overwritten, got %q", text)
	}

	// Nothing detected beside it => unchanged, not invented.
	if text, edit = resolveEnhancerIDs(Settings{}, "flux-dev-q8_0", "", ""); text != "" || edit != "" {
		t.Errorf("no detection table should mean no enhancer, got %q/%q", text, edit)
	}
}

// Tier 3: the per-model system prompt, which is the one field a user pastes a
// multi-KB document into.
func TestAutogen_writeEnhancerPrompt(t *testing.T) {
	var b strings.Builder
	writeEnhancerPrompt(&b, "promptEnhancerPrompt", "pe-t2i", trickyPrompt)

	var parsed map[string]string
	if err := yaml.Unmarshal([]byte(b.String()), &parsed); err != nil {
		t.Fatalf("emitted prompt does not parse: %v\n%s", err, b.String())
	}
	if parsed["promptEnhancerPrompt"] != trickyPrompt {
		t.Errorf("prompt mangled:\n got %q\nwant %q", parsed["promptEnhancerPrompt"], trickyPrompt)
	}

	// A prompt with no enhancer to run under is not config, it is a leftover.
	b.Reset()
	writeEnhancerPrompt(&b, "promptEnhancerPrompt", "", trickyPrompt)
	writeEnhancerPrompt(&b, "promptEnhancerPrompt", PEDisabled, trickyPrompt)
	writeEnhancerPrompt(&b, "promptEnhancerPrompt", "pe-t2i", "")
	if b.String() != "" {
		t.Errorf("expected nothing, got %q", b.String())
	}
}

// An enhancer is emitted as ONE profile shaped for the job: no fleet tiers, no
// vision twin, a capped window, and the projector placed by direction (i2i in
// VRAM because every call carries an image, t2i none at all). Gated on the real
// models tree holding a Qwen PE pair.
func TestAutogen_Generate_PromptEnhancerSingleProfile(t *testing.T) {
	if realModelsRoot == "" {
		t.Skip("no real models tree")
	}
	gf := GenerateFile{Settings: Settings{ModelsRoot: realModelsRoot}}
	gf.Settings.applyDefaults()
	gf.Settings.DefaultVariants = []VariantSpec{{Name: "32k", Ctx: 32768}, {Name: "game", Ctx: 16384}}
	out, err := Generate(gf, "T")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	seen := map[string]bool{}
	for id, cmd := range modelBlocks(out) {
		dir, _, ok := promptEnhancerName(id)
		if !ok {
			continue
		}
		if !strings.Contains(cmd, "-c 32768") {
			t.Errorf("%s: want the enhancer window -c 32768", id)
		}
		if strings.HasSuffix(id, "-vision") || strings.HasSuffix(id, "-32k") || strings.HasSuffix(id, "-game") {
			t.Errorf("%s: enhancer must emit no extra profiles", id)
		}
		switch dir {
		case PEDirEdit:
			if strings.Contains(cmd, "--mmproj ") && strings.Contains(cmd, "--no-mmproj-offload") {
				t.Errorf("%s: i2i projector must be in VRAM", id)
			}
		case PEDirText:
			if strings.Contains(cmd, "--mmproj ") {
				t.Errorf("%s: t2i is never shown an image, want no projector", id)
			}
		}
		seen[dir] = true
	}
	if !seen[PEDirEdit] && !seen[PEDirText] {
		t.Skip("no prompt enhancer in the models tree")
	}
}
