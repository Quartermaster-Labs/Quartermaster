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
	auto := detectEnhancers(rows, Settings{}, nil)

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

// The i2i half must pair to the VISION twin when the model ships a projector.
// The base profile carries its mmproj in RAM (--no-mmproj-offload), and an i2i
// rewriter is handed an image on every single call, so pairing it there pays a
// host-side encode every time: measured in minutes, not seconds.
func TestAutogen_detectEnhancers_PrefersVisionTwin(t *testing.T) {
	rows := []GgufRow{
		{ID: "qwen-image-2.1-pe-i2i-q5_k_m", FullPath: "/d/models/qwen-image-2.1-pe-i2i-q5_k_m.gguf", MmprojPath: "/d/models/pe-i2i.mmproj-bf16.gguf", MmprojSizeGB: 1.3},
		// The t2i half ships a projector too (same base model), but it is never
		// shown an image, so it must NOT be moved onto the twin and made to pay
		// VRAM for a projector it will not use.
		{ID: "qwen-image-2.1-pe-t2i-q5_k_m", FullPath: "/d/models/qwen-image-2.1-pe-t2i-q5_k_m.gguf", MmprojPath: "/d/models/pe-t2i.mmproj-bf16.gguf", MmprojSizeGB: 1.3},
	}
	got := detectEnhancers(rows, Settings{}, nil).For("qwen-image-2.1-q8_0")
	if got == nil {
		t.Fatal("no enhancers paired to the image model")
	}
	if got.Edit != "qwen-image-2.1-pe-i2i-q5_k_m-vision" {
		t.Errorf("edit = %q, want the vision twin", got.Edit)
	}
	if got.Text != "qwen-image-2.1-pe-t2i-q5_k_m" {
		t.Errorf("text = %q, want the base profile", got.Text)
	}

	// No projector => no twin is emitted, so pairing to one would be a 404.
	bare := []GgufRow{{ID: "qwen-image-2.1-pe-i2i-q5_k_m"}}
	if got := detectEnhancers(bare, Settings{}, nil).For("qwen-image-2.1-q8_0"); got.Edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("edit = %q, want the base id when there is no twin", got.Edit)
	}

	// "mmproj: none" drops the twin as well, same rule the emit loop uses.
	pinned := []Override{{Match: "*pe-i2i*", Mmproj: "none"}}
	if got := detectEnhancers(rows, Settings{}, pinned).For("qwen-image-2.1-q8_0"); got.Edit != "qwen-image-2.1-pe-i2i-q5_k_m" {
		t.Errorf("edit = %q, want the base id when the twin is pinned off", got.Edit)
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
	}, Settings{}, nil)}

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
