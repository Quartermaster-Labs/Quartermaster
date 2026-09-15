package autogen

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSafetensors lays down a real .safetensors container carrying only the
// header (8-byte LE length + JSON table). Classification never reads past it, so
// a synthetic file with no payload exercises the same path as a 10 GB encoder.
func writeSafetensors(t *testing.T, path string, shapes map[string][]int64) {
	t.Helper()
	tbl := map[string]stTensor{}
	for k, v := range shapes {
		tbl[k] = stTensor{DType: "F16", Shape: v}
	}
	j, err := json.Marshal(tbl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := binary.Write(f, binary.LittleEndian, uint64(len(j))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(j); err != nil {
		t.Fatal(err)
	}
}

func TestAutogen_classifySafetensors(t *testing.T) {
	cases := []struct {
		name       string
		shapes     map[string][]int64
		wantRole   ComponentRole
		wantFamily string
		wantWidth  int64
		wantVision bool
	}{
		{
			name:       "sd vae",
			shapes:     map[string][]int64{"decoder.conv_in.weight": {512, 4, 3, 3}},
			wantRole:   RoleVae,
			wantFamily: VaeFamilySD,
		},
		{
			name:       "flux ae",
			shapes:     map[string][]int64{"decoder.conv_in.weight": {512, 16, 3, 3}},
			wantRole:   RoleVae,
			wantFamily: VaeFamilyFlux,
		},
		{
			name:       "flux2 ae",
			shapes:     map[string][]int64{"decoder.conv_in.weight": {512, 32, 3, 3}},
			wantRole:   RoleVae,
			wantFamily: VaeFamilyFlux2,
		},
		{
			name:       "wan 3d causal vae",
			shapes:     map[string][]int64{"conv1.weight": {96, 3, 3, 3, 3}},
			wantRole:   RoleVae,
			wantFamily: VaeFamilyWan3D,
		},
		{
			name:      "clip-l",
			shapes:    map[string][]int64{"text_model.embeddings.token_embedding.weight": {49408, 768}},
			wantRole:  RoleClip,
			wantWidth: 768,
		},
		{
			name:      "clip-g",
			shapes:    map[string][]int64{"text_model.embeddings.token_embedding.weight": {49408, 1280}},
			wantRole:  RoleClip,
			wantWidth: 1280,
		},
		{
			name:      "t5xxl",
			shapes:    map[string][]int64{"encoder.block.0.layer.0.SelfAttention.q.weight": {4096, 4096}},
			wantRole:  RoleT5,
			wantWidth: 4096,
		},
		{
			name:      "plain decoder llm",
			shapes:    map[string][]int64{"model.embed_tokens.weight": {131072, 3072}},
			wantRole:  RoleLlm,
			wantWidth: 3072,
		},
		{
			name: "vision llm",
			shapes: map[string][]int64{
				"model.embed_tokens.weight":      {152064, 3584},
				"visual.patch_embed.proj.weight": {1280, 3, 2, 14, 14},
			},
			wantRole:   RoleLlm,
			wantWidth:  3584,
			wantVision: true,
		},
		{
			// A DiT is not a component: it must not land in the pool, or the
			// diffusion model would be offered as its own text encoder.
			name:     "dit is not a component",
			shapes:   map[string][]int64{"double_blocks.0.img_attn.qkv.weight": {9216, 3072}},
			wantRole: RoleNone,
		},
		{
			// An unknown latent width is a VAE we cannot vouch for; wiring it
			// would produce a broken decode, so it is dropped entirely.
			name:     "unknown latent width dropped",
			shapes:   map[string][]int64{"decoder.conv_in.weight": {512, 7, 3, 3}},
			wantRole: RoleNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl := map[string]stTensor{}
			for k, v := range tc.shapes {
				tbl[k] = stTensor{Shape: v}
			}
			got := classifySafetensors(tbl, 1.0, "x.safetensors")
			if got.Role != tc.wantRole {
				t.Fatalf("role = %q, want %q", got.Role, tc.wantRole)
			}
			if got.Family != tc.wantFamily {
				t.Errorf("family = %q, want %q", got.Family, tc.wantFamily)
			}
			if got.Width != tc.wantWidth {
				t.Errorf("width = %d, want %d", got.Width, tc.wantWidth)
			}
			if got.Vision != tc.wantVision {
				t.Errorf("vision = %v, want %v", got.Vision, tc.wantVision)
			}
		})
	}
}

func TestAutogen_ScanEncoderPool(t *testing.T) {
	root := t.TempDir()
	writeSafetensors(t, filepath.Join(root, "vae", "ae.safetensors"),
		map[string][]int64{"decoder.conv_in.weight": {512, 16, 3, 3}})
	writeSafetensors(t, filepath.Join(root, "vae", "sdxl_vae.safetensors"),
		map[string][]int64{"decoder.conv_in.weight": {512, 4, 3, 3}})
	writeSafetensors(t, filepath.Join(root, "clip", "clip_l.safetensors"),
		map[string][]int64{"text_model.embeddings.token_embedding.weight": {49408, 768}})
	writeSafetensors(t, filepath.Join(root, "clip", "clip_g.safetensors"),
		map[string][]int64{"text_model.embeddings.token_embedding.weight": {49408, 1280}})
	writeSafetensors(t, filepath.Join(root, "junk", "readme.safetensors"),
		map[string][]int64{"nothing.weight": {1, 1}})
	// Not a safetensors container at all: must be skipped, not fatal.
	if err := os.WriteFile(filepath.Join(root, "junk", "half.safetensors"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := ScanEncoderPool([]string{root})
	if len(p.Files) != 4 {
		t.Fatalf("files = %d, want 4: %+v", len(p.Files), p.Files)
	}
	if got := p.Vae(VaeFamilyFlux); !strings.HasSuffix(got, "ae.safetensors") {
		t.Errorf("flux vae = %q", got)
	}
	if got := p.Vae(VaeFamilySD, "sdxl"); !strings.Contains(got, "sdxl") {
		t.Errorf("sdxl vae = %q", got)
	}
	if got := p.Clip(768); !strings.Contains(got, "clip_l") {
		t.Errorf("clip-l = %q", got)
	}
	if got := p.Clip(1280); !strings.Contains(got, "clip_g") {
		t.Errorf("clip-g = %q", got)
	}
	if got := p.T5(); got != "" {
		t.Errorf("t5 = %q, want none", got)
	}
	// A hint that matches nothing still yields a stable pick, never a blank.
	if got := p.Vae(VaeFamilyFlux, "no-such-thing"); got == "" {
		t.Error("hint miss should fall back to the sorted-first candidate")
	}
}

func TestAutogen_EncoderPoolLlm(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/qwen25vl-q4.gguf", Role: RoleLlm, Width: 3584, SizeGB: 4, Mmproj: "/m/mmproj.gguf", Vision: true},
		{Path: "/m/qwen25vl-q8.gguf", Role: RoleLlm, Width: 3584, SizeGB: 8, Mmproj: "/m/mmproj.gguf", Vision: true},
		{Path: "/m/qwen25-7b.gguf", Role: RoleLlm, Width: 3584, SizeGB: 9},
		{Path: "/m/qwen3-4b.gguf", Role: RoleLlm, Width: 2560, SizeGB: 3},
		{Path: "/m/t5.gguf", Role: RoleT5, Width: 4096, SizeGB: 5},
	}}
	// Widest file wins at equal width...
	if got, _ := p.Llm(3584, false, ""); got != "/m/qwen25-7b.gguf" {
		t.Errorf("llm(3584) = %q, want the largest", got)
	}
	// ...unless a vision tower is required, which excludes the bigger text-only
	// file and drags the paired projector along.
	got, proj := p.Llm(3584, true, "")
	if got != "/m/qwen25vl-q8.gguf" || proj != "/m/mmproj.gguf" {
		t.Errorf("llm(3584, vision) = %q/%q", got, proj)
	}
	if got, _ := p.Llm(2560, false, ""); got != "/m/qwen3-4b.gguf" {
		t.Errorf("llm(2560) = %q", got)
	}
	// An unmatched width picks nothing rather than the closest: a mismatched
	// encoder does not degrade, it fails to load.
	if got, _ := p.Llm(4096, false, ""); got != "" {
		t.Errorf("llm(4096) = %q, want none (t5 is not an llm)", got)
	}
	if got, _ := p.Llm(0, false, ""); got != "" {
		t.Errorf("llm(0) = %q, want none", got)
	}
	var nilPool *EncoderPool
	if got, _ := nilPool.Llm(3584, true, ""); got != "" {
		t.Error("nil pool must be inert")
	}
	if nilPool.Vae(VaeFamilyFlux) != "" || nilPool.Clip(768) != "" || nilPool.T5() != "" {
		t.Error("nil pool must be inert for every getter")
	}
}

// The real-world tie this pool has to break: Qwen3-4B-Instruct-2507 and
// Qwen3-VL-4B-Instruct are both 2560 wide, and the Q8 quants of each round to
// the same size, so the scan's own tiebreak (path) lands on the VL file. Only
// the declared pin says which of them a given DiT was trained against.
func TestAutogen_EncoderPoolLlmPrefer(t *testing.T) {
	const (
		vl   = "/m/QwenVL/Qwen3VL-4B-Instruct-Q8_0.gguf"
		q3q8 = "/m/lmstudio-community/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q8_0.gguf"
		q3q4 = "/m/lmstudio-community/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf"
	)
	p := &EncoderPool{Files: []ComponentFile{
		{Path: vl, Role: RoleLlm, Width: 2560, SizeGB: 3.99, Vision: true,
			Mmproj: "/m/QwenVL/mmproj-Qwen3VL-4B-Instruct-F16.gguf"},
		{Path: q3q8, Role: RoleLlm, Width: 2560, SizeGB: 3.99},
		{Path: q3q4, Role: RoleLlm, Width: 2560, SizeGB: 2.32},
	}}
	// No declaration: the path tiebreak takes the VL file, which is how
	// Z-Image ended up conditioned on Qwen3-VL.
	if got, _ := p.Llm(2560, false, ""); got != vl {
		t.Errorf("unpinned llm = %q, want %q", got, vl)
	}
	// A declared file of the matching width wins outright, even when the scan
	// finds a larger candidate.
	if got, _ := p.Llm(2560, false, q3q4); got != q3q4 {
		t.Errorf("pinned llm = %q, want %q", got, q3q4)
	}
	// A pin without a vision tower cannot serve a vision model (the caller then
	// retries text-only and gets it), and an unseen pin changes nothing.
	if got, _ := p.Llm(2560, true, q3q4); got != vl {
		t.Errorf("vision call took a text-only pin: %q", got)
	}
	if got, proj := p.Llm(2560, true, vl); got != vl || proj == "" {
		t.Errorf("vision pin = %q/%q", got, proj)
	}
	if got, _ := p.Llm(2560, false, "/m/elsewhere/qwen3-4b.gguf"); got != vl {
		t.Errorf("unseen pin = %q, want %q", got, vl)
	}
}

func TestAutogen_fillEncoderSet(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/ae.safetensors", Role: RoleVae, Family: VaeFamilyFlux},
		{Path: "/m/flux2-ae.safetensors", Role: RoleVae, Family: VaeFamilyFlux2},
		{Path: "/m/clip_l.safetensors", Role: RoleClip, Width: 768},
		{Path: "/m/t5xxl.gguf", Role: RoleT5, Width: 4096},
	}}
	got := fillEncoderSet(EncoderSet{ClipL: "/hand/clip_l.safetensors"}, p)
	if got.ClipL != "/hand/clip_l.safetensors" {
		t.Errorf("declared ClipL was overwritten: %q", got.ClipL)
	}
	if got.FluxVae != "/m/ae.safetensors" || got.Flux2Vae != "/m/flux2-ae.safetensors" {
		t.Errorf("vae fill = %q / %q", got.FluxVae, got.Flux2Vae)
	}
	// Z-Image ships an AE byte-identical to flux.1's, so it falls back to it.
	if got.ZimageVae != "/m/ae.safetensors" {
		t.Errorf("zimage vae = %q", got.ZimageVae)
	}
	if got.T5 != "/m/t5xxl.gguf" {
		t.Errorf("t5 = %q", got.T5)
	}
	if got.SdxlVae != "" {
		t.Errorf("sdxl vae = %q, want blank (none on disk)", got.SdxlVae)
	}
	// A nil pool must leave a hand-written set exactly as it was.
	in := EncoderSet{FluxVae: "a", ClipL: "b"}
	if fillEncoderSet(in, nil) != in {
		t.Error("nil pool must not alter the declared set")
	}
}

func TestAutogen_wantsVisionEncoder(t *testing.T) {
	cases := []struct {
		name string
		ov   *Override
		want bool
	}{
		{name: "LongCat-Image-Edit-Turbo-Q8_0", want: true},
		{name: "Qwen-Rapid-NSFW", want: true},
		{name: "flux1-kontext-dev", want: true},
		{name: "sd15-inpaint", want: true},
		{name: "LongCat-Image-Q8_0", want: false},
		{name: "Z-Image-Turbo", want: false},
		// "edit" only counts as its own token: "credit" is not an edit model.
		{name: "credit-model", want: false},
		{name: "LongCat-Image-Edit", ov: &Override{LlmVision: "off"}, want: false},
		{name: "LongCat-Image", ov: &Override{LlmVision: "on"}, want: true},
	}
	for _, tc := range cases {
		if got := wantsVisionEncoder("flux", tc.name, tc.ov); got != tc.want {
			t.Errorf("wantsVisionEncoder(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAutogen_condHiddenFrom(t *testing.T) {
	if got := condHiddenFrom(nil); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	if got := condHiddenFrom(map[string]int64{"txt_in.weight": 3584}); got != 3584 {
		t.Errorf("txt_in = %d", got)
	}
	// Highest-ranked present tensor wins when a model carries several.
	dims := map[string]int64{
		"context_embedder.weight": 4096,
		"txt_in.weight":           3584,
	}
	if got := condHiddenFrom(dims); got != 3584 {
		t.Errorf("ranked pick = %d, want 3584", got)
	}
	if got := condHiddenFrom(map[string]int64{"unrelated.weight": 99}); got != 0 {
		t.Errorf("unrelated = %d, want 0", got)
	}
}

func TestAutogen_IsReferenceEditModel(t *testing.T) {
	cases := []struct {
		name string
		ov   *Override
		want bool
	}{
		{name: "LongCat-Image-Edit-Turbo-Q8_0", want: true},
		{name: "Qwen-Rapid-NSFW", want: true},
		{name: "flux1-kontext-dev", want: true},
		{name: "LongCat-Image-Q8_0", want: false},
		{name: "Z-Image-Turbo", want: false},
		{name: "credit-model", want: false},
		// Narrower than wantsVisionEncoder on purpose: an inpaint model is an
		// edit, but it wants the MASKED img2img route. Sending its source as a
		// reference would redraw the whole frame and ignore the mask.
		{name: "sd15-inpaint", want: false},
		{name: "flux1-fill-dev", want: false},
		{name: "LongCat-Image-Edit", ov: &Override{LlmVision: "off"}, want: false},
		{name: "LongCat-Image", ov: &Override{LlmVision: "on"}, want: true},
	}
	for _, tc := range cases {
		if got := IsReferenceEditModel(tc.name, tc.ov); got != tc.want {
			t.Errorf("IsReferenceEditModel(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Flux.2 states a caption width that is a MULTIPLE of its encoder's hidden size
// (klein 9B: txt_in 12288 against a 4096-wide Qwen3-8B), so the pick has to
// divide rather than compare. Exactness still outranks concatenation, and the
// factor cap keeps the rule from matching arbitrarily narrow encoders.
func TestAutogen_EncoderPoolLlmConcat(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/qwen3-8b.gguf", Role: RoleLlm, Family: "qwen3", Width: 4096, SizeGB: 8},
		{Path: "/m/qwen3-4b.gguf", Role: RoleLlm, Family: "qwen3", Width: 2560, SizeGB: 3},
		{Path: "/m/tiny-1024.gguf", Role: RoleLlm, Family: "qwen3", Width: 1024, SizeGB: 1},
	}}
	// klein 9B: 12288 = 3 x 4096.
	if got, _ := p.Llm(12288, false, ""); got != "/m/qwen3-8b.gguf" {
		t.Errorf("llm(12288) = %q, want the 4096-wide encoder", got)
	}
	// The global pin is 2560 wide: 12288 is not a multiple of it, so it must not
	// be dragged in. This is the exact shape of the klein-9B mis-wire.
	if got, _ := p.Llm(12288, false, "/m/qwen3-4b.gguf"); got != "/m/qwen3-8b.gguf" {
		t.Errorf("llm(12288, pin=4b) = %q, want the pin ignored", got)
	}
	// 12288/1024 = 12, past maxCaptionConcat: a narrow encoder is not a candidate
	// just because the arithmetic works.
	narrow := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/tiny-1024.gguf", Role: RoleLlm, Family: "qwen3", Width: 1024, SizeGB: 1},
	}}
	if got, _ := narrow.Llm(12288, false, ""); got != "" {
		t.Errorf("llm(12288) over a 1024-wide pool = %q, want none", got)
	}
	// An exact match beats a concatenated one even when the concat candidate is
	// bigger and would win the size tiebreak within a tier.
	exact := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/wide-12288.gguf", Role: RoleLlm, Family: "qwen3", Width: 12288, SizeGB: 2},
		{Path: "/m/qwen3-8b.gguf", Role: RoleLlm, Family: "qwen3", Width: 4096, SizeGB: 8},
	}}
	if got, _ := exact.Llm(12288, false, ""); got != "/m/wide-12288.gguf" {
		t.Errorf("llm(12288) = %q, want the exact-width file", got)
	}
	// A pin only reachable by concat does not outrank an exact-width scan hit.
	if got, _ := exact.Llm(12288, false, "/m/qwen3-8b.gguf"); got != "/m/wide-12288.gguf" {
		t.Errorf("llm(12288, pin=concat) = %q, want the exact-width file", got)
	}
	// ...but a pin at the same tier still wins outright.
	if got, _ := p.Llm(12288, false, "/m/qwen3-8b.gguf"); got != "/m/qwen3-8b.gguf" {
		t.Errorf("llm(12288, pin=8b) = %q, want the pin", got)
	}
}

func TestAutogen_captionFactor(t *testing.T) {
	cases := []struct {
		hidden, width, want int64
	}{
		{2560, 2560, 1},  // the ordinary case: one encoder hidden state
		{12288, 4096, 3}, // flux.2 klein 9B
		{7680, 2560, 3},  // flux.2 klein 4B
		{15360, 5120, 3}, // flux.2 dev (Mistral-Small-3)
		{12288, 2560, 0}, // not divisible
		{12288, 1024, 0}, // divisible but past the concat cap
		{4096, 0, 0},     // unmeasured encoder
		{0, 4096, 0},     // unmeasured DiT
		{-1, 4096, 0},    // defensive
	}
	for _, c := range cases {
		if got := captionFactor(c.hidden, c.width); got != c.want {
			t.Errorf("captionFactor(%d, %d) = %d, want %d", c.hidden, c.width, got, c.want)
		}
	}
}

func TestAutogen_knowsLlm(t *testing.T) {
	p := &EncoderPool{Files: []ComponentFile{
		{Path: "/m/qwen3-4b.gguf", Role: RoleLlm, Width: 2560},
		{Path: "/m/t5.gguf", Role: RoleT5, Width: 4096},
	}}
	// Classified as an LLM: known at ANY width, which is what separates "the scan
	// rejected this pin" from "the scan never saw it".
	if !p.knowsLlm("/m/qwen3-4b.gguf") {
		t.Error("a scanned llm should be known")
	}
	if p.knowsLlm("/m/t5.gguf") {
		t.Error("a t5 is not an llm")
	}
	if p.knowsLlm("/m/never-scanned.gguf") || p.knowsLlm("") {
		t.Error("an unscanned or empty path is not known")
	}
	var nilPool *EncoderPool
	if nilPool.knowsLlm("/m/qwen3-4b.gguf") {
		t.Error("nil pool knows nothing")
	}
}
