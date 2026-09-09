package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clipComputeBufferGB models the CLIP vision compute buffer from mmproj hparams.
// Verified against the real Qwen3.6-27B mmproj-F16 (image 768 / patch 16 / embd
// 1152 / ffn 4304 / heads 16): base-tile n_patches = (768/16)^2 = 2304, KQ =
// 16*2304^2*4 ~0.34 GB dominates, total ~0.5 GB — well under the old flat 1.0 pad.
func TestClipComputeBufferGB(t *testing.T) {
	qwen := Metadata{VisionImageSize: 768, VisionPatchSize: 16, VisionEmbd: 1152, VisionFFN: 4304, VisionHeads: 16}
	got := clipComputeBufferGB(qwen)
	if got < 0.4 || got > 0.65 {
		t.Errorf("Qwen3-VL mmproj: got %.3f GB, want ~0.5 GB", got)
	}

	// Quadratic in patch count: doubling the grid (halving patch_size) ~4x's the
	// dominant KQ term, so the buffer grows well past linear.
	dense := qwen
	dense.VisionPatchSize = 8 // grid 96 -> 9216 patches vs 2304
	if d := clipComputeBufferGB(dense); d < 3*got {
		t.Errorf("halving patch_size should >3x the buffer: got %.3f vs base %.3f", d, got)
	}

	// Missing vision dims => 0 (caller falls back to the flat VisionOverheadGB).
	if z := clipComputeBufferGB(Metadata{}); z != 0 {
		t.Errorf("no vision dims: got %.3f, want 0", z)
	}
}

// A served id that cannot take an image is a trap: llama-server loads the CLIP
// projector at spawn and nowhere else, so a process launched without --mmproj
// answers every image with "image input is not supported" until it is evicted.
// Every profile of a model that HAS a projector therefore loads one. Placement
// is what differs: the default and its tiers keep it in RAM, where it costs no
// VRAM and so cannot shrink the window or push layers off the GPU, while the
// "-vision" twin pays for GPU residency to encode fast.
func TestAutogen_Generate_ProjectorOnEveryProfile(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	target := firstModelWithProjector(t)

	gen := func(ov Override) map[string]string {
		t.Helper()
		ov.Match = "*" + filepath.Base(target.FullPath)
		gf := GenerateFile{
			Settings:  Settings{ModelsRoot: realModelsRoot},
			Overrides: []Override{ov},
		}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		// Keep only the blocks launching THIS gguf; every other model in the
		// tree is untouched by the override.
		got := blocksFor(out, target.FullPath)
		if len(got) == 0 {
			t.Fatalf("no emitted block loads %s", target.FullPath)
		}
		return got
	}

	proj := "--mmproj " + strings.ReplaceAll(target.MmprojPath, "\\", "/")
	blocks := gen(Override{})
	twins := 0
	for id, cmd := range blocks {
		if !strings.Contains(cmd, proj) {
			t.Errorf("%s: no %q - this id 500s on every image", id, proj)
			continue
		}
		onCPU := strings.Contains(cmd, "--no-mmproj-offload")
		if isTwin := strings.HasSuffix(id, "-vision"); isTwin {
			twins++
			if onCPU {
				t.Errorf("%s: the vision twin exists to hold the projector in VRAM, got --no-mmproj-offload", id)
			}
		} else if !onCPU {
			t.Errorf("%s: default/tier profiles must keep the projector in RAM, got no --no-mmproj-offload", id)
		}
	}
	if twins != 1 {
		t.Errorf("got %d vision twins, want exactly 1", twins)
	}

	// The Default tab's pin buys VRAM residency for the non-twin profiles; it
	// never reaches the twin, which is already GPU-resident.
	for id, cmd := range gen(Override{Mmproj: "gpu"}) {
		if strings.Contains(cmd, "--no-mmproj-offload") {
			t.Errorf("%s: mmproj=gpu must pin the projector in VRAM everywhere", id)
		}
	}
	// "none" opts the model out of vision entirely - no projector, no twin.
	for id, cmd := range gen(Override{Mmproj: "none"}) {
		if strings.Contains(cmd, "--mmproj") {
			t.Errorf("%s: mmproj=none must emit no projector", id)
		}
		if strings.HasSuffix(id, "-vision") {
			t.Errorf("mmproj=none still emitted the twin %s", id)
		}
	}
}

// blocksFor keeps the emitted blocks that launch one gguf. The path is its own
// folded cmd line, so the match ends at the newline: "-m <path>" is a prefix of
// nothing else, but a bare Contains would also hit a longer sibling path.
func blocksFor(out, ggufPath string) map[string]string {
	want := "-m " + strings.ReplaceAll(ggufPath, "\\", "/") + "\n"
	got := map[string]string{}
	for id, cmd := range modelBlocks(out) {
		if strings.Contains(cmd, want) {
			got[id] = cmd
		}
	}
	return got
}

// modelBlocks splits a generated config into served id -> cmd text.
func modelBlocks(out string) map[string]string {
	blocks := map[string]string{}
	for _, chunk := range strings.Split(out, "\n  \"")[1:] {
		id, rest, ok := strings.Cut(chunk, "\":\n")
		if !ok {
			continue
		}
		blocks[id] = rest
	}
	return blocks
}

// The spawn-time guard must agree with the baked plan: an argv carrying
// --no-mmproj-offload has no projector in VRAM, so re-estimating it must not
// charge one (which would offload text layers the config meant to keep on GPU).
func TestLiveOffload_NoMmprojOffloadUncharged(t *testing.T) {
	args := []string{"llama-server", "--mmproj", "C:/models/mmproj.gguf", "--no-mmproj-offload"}
	if !hasFlag(args, "--no-mmproj-offload") {
		t.Fatal("hasFlag missed --no-mmproj-offload")
	}
	if _, i := argVal(args, "--mmproj"); i < 0 {
		t.Fatal("argVal missed --mmproj")
	}
}

// The per-model projector dropdown (Override.Mmproj) pins what the auto
// fallback would otherwise decide: "ram" always emits --no-mmproj-offload,
// "gpu" never does, and "none" removes the vision twin entirely. Gated on the
// real models tree — it needs a model that actually ships a projector.
func TestAutogen_Generate_MmprojPin(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	gen := func(mode string) string {
		t.Helper()
		gf := GenerateFile{
			Settings:  Settings{ModelsRoot: realModelsRoot},
			Overrides: []Override{{Match: "*", Mmproj: mode}},
		}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate(%q): %v", mode, err)
		}
		return out
	}

	if auto := gen(""); !strings.Contains(auto, "-vision\":") {
		t.Skip("no model in the tree ships an mmproj; nothing to pin")
	}
	if ram := gen("ram"); !strings.Contains(ram, "--no-mmproj-offload") {
		t.Error(`mmproj "ram": expected --no-mmproj-offload on the vision twin`)
	}
	if gpu := gen("gpu"); strings.Contains(gpu, "--no-mmproj-offload") {
		t.Error(`mmproj "gpu": projector pinned to VRAM must not emit --no-mmproj-offload`)
	}
	none := gen("none")
	if strings.Contains(none, "-vision\":") {
		t.Error(`mmproj "none": expected no vision twin`)
	}
	if strings.Contains(none, "--mmproj ") {
		t.Error(`mmproj "none": expected no --mmproj flag anywhere`)
	}
}

// Two pins, two audiences: the model-wide one (the config modal's Default tab)
// places the projector on the default profile and its tiers, the reserved
// "vision" variant places it on the twin. Neither reaches the other's profiles,
// so a variant "none" drops the twin while the text ids keep image input.
func TestAutogen_Generate_MmprojPin_VisionVariant(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	target := firstModelWithProjector(t)
	gen := func(model, variant string) (twin string, others map[string]string) {
		t.Helper()
		gf := GenerateFile{
			Settings: Settings{ModelsRoot: realModelsRoot},
			Overrides: []Override{{
				Match:    "*" + filepath.Base(target.FullPath),
				Mmproj:   model,
				Variants: []VariantSpec{{Name: "vision", Mmproj: variant}},
			}},
		}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate(%q/%q): %v", model, variant, err)
		}
		others = map[string]string{}
		for id, cmd := range blocksFor(out, target.FullPath) {
			if strings.HasSuffix(id, "-vision") {
				twin = cmd
				continue
			}
			others[id] = cmd
		}
		if len(others) == 0 {
			t.Fatalf("generate(%q/%q): no non-twin block for %s", model, variant, target.FullPath)
		}
		return twin, others
	}

	// Model "gpu" + variant "ram": exactly inverted from the defaults.
	twin, others := gen("gpu", "ram")
	if !strings.Contains(twin, "--no-mmproj-offload") {
		t.Error(`variant "ram": expected the twin's projector in RAM`)
	}
	for id, cmd := range others {
		if strings.Contains(cmd, "--no-mmproj-offload") {
			t.Errorf(`%s: model pin "gpu" must hold the projector in VRAM`, id)
		}
	}

	twin, others = gen("ram", "gpu")
	if strings.Contains(twin, "--no-mmproj-offload") {
		t.Error(`variant "gpu": expected the twin's projector back in VRAM`)
	}
	for id, cmd := range others {
		if !strings.Contains(cmd, "--no-mmproj-offload") {
			t.Errorf(`%s: model pin "ram" must keep the projector off the GPU`, id)
		}
	}

	// A variant "none" is checked past the twin-construction gate, so it drops
	// the twin - and only the twin. The text ids still take images.
	twin, others = gen("", "none")
	if twin != "" {
		t.Error(`variant "none": expected no vision twin`)
	}
	for id, cmd := range others {
		if !strings.Contains(cmd, "--mmproj ") {
			t.Errorf(`%s: variant "none" must not disarm the other profiles`, id)
		}
	}
}

// firstModelWithProjector picks an ordinary text LLM that discovery paired with
// a CLIP projector, skipping the image and SAM classes that never grow a twin.
func firstModelWithProjector(t *testing.T) GgufRow {
	t.Helper()
	rows, err := DiscoverGgufModelsMulti([]string{realModelsRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, r := range rows {
		if r.MmprojPath == "" || r.IsSam {
			continue
		}
		meta, err := ReadGgufMetadataCached(r.FullPath)
		if err != nil || meta.BlockCount == 0 || meta.Architecture == "clip" || isImageArch(meta.Architecture) {
			continue
		}
		return r
	}
	t.Skip("no text model with a paired projector in the tree")
	return GgufRow{}
}
