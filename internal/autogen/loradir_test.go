package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// The resolution ladder: per-model override > per-category folder > fleet-wide
// folder > the model gguf's own directory.
func TestResolveLoraDir_ladder(t *testing.T) {
	const modelPath = `D:/LLM/Models/flux1/flux1-dev-Q8_0.gguf`
	full := Settings{
		LoraDir:  `D:/loras/all`,
		LoraDirs: map[string]string{"image": `D:/loras/image`, "video": `D:/loras/video`},
	}
	tests := []struct {
		name     string
		s        Settings
		ov       string
		category string
		want     string
	}{
		{"override wins over everything", full, `D:/loras/mine`, "image", `D:/loras/mine`},
		{"image takes the image folder", full, "", "image", `D:/loras/image`},
		{"video takes the video folder", full, "", "video", `D:/loras/video`},
		{
			"a category with no entry falls back to the fleet-wide folder",
			Settings{LoraDir: `D:/loras/all`, LoraDirs: map[string]string{"image": `D:/loras/image`}},
			"", "video", `D:/loras/all`,
		},
		{
			"a blank entry is not a pin",
			Settings{LoraDir: `D:/loras/all`, LoraDirs: map[string]string{"video": "  "}},
			"", "video", `D:/loras/all`,
		},
		{"nothing set falls back to the model's own dir", Settings{}, "", "image", `D:/LLM/Models/flux1`},
		{
			"a per-category folder alone still beats the model's own dir",
			Settings{LoraDirs: map[string]string{"video": `D:/loras/video`}},
			"", "video", `D:/loras/video`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveLoraDir(tt.s, tt.ov, tt.category, modelPath); got != tt.want {
				t.Fatalf("resolveLoraDir = %q, want %q", got, tt.want)
			}
		})
	}
}

// A nil map must not panic on lookup, which is the shape every pre-existing
// generate file has.
func TestResolveLoraDir_nilMap(t *testing.T) {
	got := resolveLoraDir(Settings{LoraDir: `D:/loras`}, "", "video", `D:/m/x.gguf`)
	if got != `D:/loras` {
		t.Fatalf("resolveLoraDir = %q, want D:/loras", got)
	}
}

func TestLoraCategory(t *testing.T) {
	if got := loraCategory(true); got != "video" {
		t.Errorf("loraCategory(true) = %q, want video", got)
	}
	if got := loraCategory(false); got != "image" {
		t.Errorf("loraCategory(false) = %q, want image", got)
	}
	// Both must be keys the settings API will accept.
	for _, c := range []string{loraCategory(true), loraCategory(false)} {
		if !IsCategory(c) {
			t.Errorf("loraCategory produced %q, which IsCategory rejects", c)
		}
	}
}

// The settings patch is how the dashboard writes these, so a patched map must
// reach Settings intact.
func TestSettingsPatch_loraDirs(t *testing.T) {
	s := Settings{LoraDir: `D:/old`}
	dirs := map[string]string{"video": `D:/loras/video`}
	(&SettingsPatch{LoraDirs: &dirs}).apply(&s)
	if s.LoraDirs["video"] != `D:/loras/video` {
		t.Fatalf("after patch: LoraDirs = %v", s.LoraDirs)
	}
	if s.LoraDir != `D:/old` {
		t.Fatalf("patch clobbered the fleet-wide LoraDir: %q", s.LoraDir)
	}
}

// Saving an unrelated settings section must not drop the per-category folders.
// They are a map, not a pointer, so this exercises MergeSettingsPatch's nil
// carry-forward for non-pointer fields.
func TestMergeSettingsPatch_carriesForwardLoraDirs(t *testing.T) {
	prevDirs := map[string]string{"video": `D:/loras/video`}
	prev := SettingsPatch{LoraDirs: &prevDirs}
	vram := 24.0
	got := MergeSettingsPatch(prev, SettingsPatch{TargetVramGB: &vram})
	if got.LoraDirs == nil || (*got.LoraDirs)["video"] != `D:/loras/video` {
		t.Fatalf("loraDirs dropped by an unrelated save: %v", got.LoraDirs)
	}
	// A non-nil map is still an explicit overwrite (the advanced section
	// rewrites itself wholesale on every save).
	empty := map[string]string{}
	cleared := MergeSettingsPatch(prev, SettingsPatch{LoraDirs: &empty})
	if cleared.LoraDirs == nil || len(*cleared.LoraDirs) != 0 {
		t.Fatalf("explicit clear ignored: %v", cleared.LoraDirs)
	}
}

// The dashboard writes one category at a time, so the helper must be a
// read-modify-write over the single LoraDirs map rather than a plain overwrite.
func TestUpsertSidecarLoraDir_setClearAndPreserve(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generate.yaml")
	if err := os.WriteFile(gen, []byte("settings:\n  modelsRoot: D:/Models\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertSidecarLoraDir(gen, "image", `D:/loras/image`); err != nil {
		t.Fatal(err)
	}
	// Setting a SECOND category must not drop the first.
	if err := UpsertSidecarLoraDir(gen, "video", `D:/loras/video`); err != nil {
		t.Fatal(err)
	}
	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if gf.Settings.LoraDirs["image"] != `D:/loras/image` || gf.Settings.LoraDirs["video"] != `D:/loras/video` {
		t.Fatalf("after two sets: %v", gf.Settings.LoraDirs)
	}

	// Clearing one leaves the other alone.
	if err := UpsertSidecarLoraDir(gen, "image", ""); err != nil {
		t.Fatal(err)
	}
	gf, err = LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gf.Settings.LoraDirs["image"]; ok {
		t.Fatalf("image not cleared: %v", gf.Settings.LoraDirs)
	}
	if gf.Settings.LoraDirs["video"] != `D:/loras/video` {
		t.Fatalf("clearing image dropped video: %v", gf.Settings.LoraDirs)
	}
}

// An unrelated settings section saving afterwards must not wipe the folders.
func TestUpsertSidecarLoraDir_survivesAnotherSectionSave(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "generate.yaml")
	if err := os.WriteFile(gen, []byte("settings:\n  modelsRoot: D:/Models\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpsertSidecarLoraDir(gen, "video", `D:/loras/video`); err != nil {
		t.Fatal(err)
	}
	vram := 24.0
	if err := UpsertSidecarSettings(gen, SettingsPatch{TargetVramGB: &vram}); err != nil {
		t.Fatal(err)
	}
	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if gf.Settings.LoraDirs["video"] != `D:/loras/video` {
		t.Fatalf("a memory-section save dropped the LoRA folder: %v", gf.Settings.LoraDirs)
	}
}
