package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mmprojFor is the one place that decides WHICH projector the vision twin
// loads, and at what size it gets priced. The size is the subtle half: it can
// never come from the row (that describes discovery's file), or an override
// pointing at a 2 GB projector gets budgeted against a 0.5 GB sibling.
func TestAutogen_mmprojFor(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "shared-mmproj-f16.gguf")
	// Sizes round to 2 decimals of a GB (same as discovery's), so the fixture has
	// to be big enough to survive that: 64 MiB = 0.06 GB.
	f, err := os.Create(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	row := GgufRow{MmprojPath: "C:/models/dir-local-mmproj.gguf", MmprojSizeGB: 7}

	if p, sz := mmprojFor(row, ""); p != row.MmprojPath || sz != row.MmprojSizeGB {
		t.Errorf("empty override: got %q/%.2f, want discovery's %q/%.2f", p, sz, row.MmprojPath, row.MmprojSizeGB)
	}
	if p, sz := mmprojFor(row, "  "); p != row.MmprojPath || sz != row.MmprojSizeGB {
		t.Errorf("blank override: got %q/%.2f, want discovery's values", p, sz)
	}
	p, sz := mmprojFor(row, explicit)
	if p != explicit {
		t.Errorf("explicit path: got %q, want %q", p, explicit)
	}
	if sz != 0.06 {
		t.Errorf("explicit path size: got %.2f GB, want the stat'd 0.06 (never the row's %.2f)", sz, row.MmprojSizeGB)
	}
	// A path that does not exist is passed through with size 0 rather than
	// dropped: save-time validation owns that message, and silently emitting no
	// twin for a path the user can see in the editor is the worse failure.
	if p, sz := mmprojFor(row, filepath.Join(dir, "gone.gguf")); p == row.MmprojPath || sz != 0 {
		t.Errorf("missing path: got %q/%.2f, want the override path at size 0", p, sz)
	}
	// A directory is not a file: same treatment as missing.
	if _, sz := mmprojFor(row, dir); sz != 0 {
		t.Errorf("directory: got size %.2f, want 0", sz)
	}
}

// The variant-level field is sentinel-aware exactly like ChatTemplateFile:
// empty inherits the model-wide path, "none" forces the twin back onto
// discovery, anything else pins.
func TestAutogen_MmprojFileVariantInherit(t *testing.T) {
	const modelWide = "C:/models/shared/mmproj-f16.gguf"
	tests := []struct {
		name    string
		variant string
		want    string
	}{
		{"empty inherits", "", modelWide},
		{"none clears", NoneSentinel, ""},
		{"explicit pins", "C:/models/other/mmproj-q8.gguf", "C:/models/other/mmproj-q8.gguf"},
	}
	for _, tc := range tests {
		eff := Override{MmprojFile: modelWide}
		v := VariantSpec{Name: "vision", MmprojFile: tc.variant}
		mergeInheritStrings(&eff, &v)
		if eff.MmprojFile != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, eff.MmprojFile, tc.want)
		}
	}
}

// End to end against the real tree: an explicit path must CREATE a vision twin
// for a model that paired with nothing, and "none" must still win over it.
// This is the crux of the feature - the twin used to exist only when discovery
// had already found a projector.
func TestAutogen_Generate_MmprojFileCreatesTwin(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	rows, err := DiscoverGgufModelsMulti([]string{realModelsRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// A projector to borrow, and a TEXT model that discovery gave none. The
	// target has to be an ordinary llama model: a diffusion or SAM row never
	// grows a vision twin, whatever the field says.
	var projector string
	var target GgufRow
	for _, r := range rows {
		if projector == "" && r.MmprojPath != "" {
			projector = r.MmprojPath
		}
		if target.FullPath != "" || r.MmprojPath != "" || r.IsSam {
			continue
		}
		meta, err := ReadGgufMetadataCached(r.FullPath)
		if err != nil || meta.BlockCount == 0 || meta.Architecture == "clip" || isImageArch(meta.Architecture) {
			continue
		}
		target = r
	}
	if projector == "" || target.FullPath == "" {
		t.Skip("need one model with a projector and one without in the tree")
	}

	gen := func(ov Override) string {
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
		return out
	}
	// Count twins rather than guess the model's served id: every other model in
	// the tree is untouched by the override, so the delta is this model's.
	twins := func(out string) int { return strings.Count(out, "-vision\":") }
	flag := "--mmproj " + strings.ReplaceAll(projector, "\\", "/")

	// The projector's own model emits the flag too, so count uses rather than
	// asking whether it appears at all.
	base := gen(Override{})

	set := gen(Override{MmprojFile: projector})
	if got, want := twins(set), twins(base)+1; got != want {
		t.Errorf("explicit mmprojFile: %d vision twins, want %d (the override must create one)", got, want)
	}
	if got, want := strings.Count(set, flag), strings.Count(base, flag)+1; got != want {
		t.Errorf("explicit mmprojFile: %d uses of %q, want %d", got, flag, want)
	}

	// Placement "none" is checked past the twin gate, so it drops a twin the
	// path would otherwise have created.
	none := gen(Override{MmprojFile: projector, Mmproj: "none"})
	if got, want := twins(none), twins(base); got != want {
		t.Errorf(`mmproj "none" over an explicit path: %d vision twins, want %d`, got, want)
	}
	if got, want := strings.Count(none, flag), strings.Count(base, flag); got != want {
		t.Errorf(`mmproj "none" over an explicit path: %d uses of %q, want %d`, got, flag, want)
	}
}

// An explicit path beats a projector sitting right next to the model.
func TestAutogen_Generate_MmprojFileBeatsDirLocal(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	rows, err := DiscoverGgufModelsMulti([]string{realModelsRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// Two DIFFERENT projectors: one paired with a model, one to point it at.
	var withProj GgufRow
	var other string
	for _, r := range rows {
		if r.MmprojPath == "" {
			continue
		}
		if withProj.FullPath == "" {
			withProj = r
			continue
		}
		if r.MmprojPath != withProj.MmprojPath {
			other = r.MmprojPath
			break
		}
	}
	if withProj.FullPath == "" || other == "" {
		t.Skip("need two models with different projectors in the tree")
	}

	gen := func(ovs ...Override) string {
		t.Helper()
		gf := GenerateFile{Settings: Settings{ModelsRoot: realModelsRoot}, Overrides: ovs}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		return out
	}
	// `other` is some other model's own projector, so it appears in the baseline
	// too. Count, don't Contains: what proves the override won is one MORE use of
	// it and one FEWER of the dir-local file.
	otherFlag := "--mmproj " + strings.ReplaceAll(other, "\\", "/")
	localFlag := "--mmproj " + strings.ReplaceAll(withProj.MmprojPath, "\\", "/")
	base := gen()
	out := gen(Override{Match: "*" + filepath.Base(withProj.FullPath), MmprojFile: other})

	if got, want := strings.Count(out, otherFlag), strings.Count(base, otherFlag)+1; got != want {
		t.Errorf("explicit %q: %d uses, want %d", other, got, want)
	}
	if got, want := strings.Count(out, localFlag), strings.Count(base, localFlag)-1; got != want {
		t.Errorf("dir-local %q: %d uses, want %d (the override must replace it)", withProj.MmprojPath, got, want)
	}
}
