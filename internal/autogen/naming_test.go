package autogen

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// modelKeyRe matches one served id at the top of a model block. modelBlocks
// folds its result into a map, which is exactly what hides a duplicate key, so
// the uniqueness tests read the raw lines instead.
var modelKeyRe = regexp.MustCompile(`(?m)^  "([^"]+)":$`)

func modelKeys(out string) []string {
	var keys []string
	for _, m := range modelKeyRe.FindAllStringSubmatch(out, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

func assertUniqueKeys(t *testing.T, out string) {
	t.Helper()
	seen := map[string]bool{}
	for _, k := range modelKeys(out) {
		if seen[k] {
			t.Fatalf("duplicate model key %q in:\n%s", k, out)
		}
		seen[k] = true
	}
}

// seededGguf writes a stub gguf at root/rel and pins plain llama metadata for it,
// so Generate emits a real block instead of a "# SKIPPED" parse failure.
func seededGguf(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	seedMetaCache(t, p, pinsTestMeta())
	return p
}

// Three copies of one quant under one publisher derive the same id AND the same
// publisher tag. The clash fallback used to append the publisher once and never
// re-check, so the third copy reused the second's key: a duplicate YAML key, and
// whichever block the loader kept silently shadowed the other.
func TestGenerate_ThreeWayNameClash(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for _, repo := range []string{"a", "b", "c"} {
		paths = append(paths, seededGguf(t, root, "pub/"+repo+"/Model-7B-Q4_K_M.gguf"))
	}

	out, err := Generate(GenerateFile{Settings: Settings{ModelsRoot: root}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	assertUniqueKeys(t, out)
	for _, p := range paths {
		if len(blocksFor(out, p)) == 0 {
			t.Errorf("no block launches %s in:\n%s", p, out)
		}
	}
}

// settings.extraImageModels is the only way to serve a model the gguf scan
// cannot see, so it has to survive the whole trip: hand-written YAML ->
// LoadGenerateFile -> Generate -> an sd-server block.
func TestGenerate_ExtraImageModelFromFile(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	dit := filepath.ToSlash(filepath.Join(dir, "hidream-o1.safetensors"))
	vae := filepath.ToSlash(filepath.Join(dir, "ae.safetensors"))
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	yml := "settings:\n" +
		"  modelsRoot: " + filepath.ToSlash(models) + "\n" +
		"  extraImageModels:\n" +
		"    - name: hidream-o1\n" +
		"      modelPath: " + dit + "\n" +
		"      modelFlag: --diffusion-model\n" +
		"      vaePath: " + vae + "\n" +
		"      defaultSteps: 28\n"
	if err := os.WriteFile(gen, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(gf, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"hidream-o1":`,
		"--diffusion-model",
		dit,
		"--vae",
		vae,
		"out: [image]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated config missing %q in:\n%s", want, out)
		}
	}
}

// A hand-declared model whose name is already served must not vanish without a
// word: the user wrote it on purpose, and a silent drop reads as "the feature
// does not work". The discovered model keeps the name (a regen must not flip
// which process answers an id), and the extra leaves an in-band reason.
func TestGenerate_ExtraImageModelNameClash(t *testing.T) {
	root := t.TempDir()
	seededGguf(t, root, "pub/repo/Model-7B-Q4_K_M.gguf")

	variants := []VariantSpec{{Name: "game", Ctx: 2048}}

	// Clash with the row id AND with a profile emitted for it: a discovered
	// model serves more ids than its row name (ctx tiers, variants, the vision
	// twin), and every one of them is taken.
	for _, taken := range []string{"model-7b-q4_k_m", "model-7b-q4_k_m-game"} {
		t.Run(taken, func(t *testing.T) {
			s := Settings{ModelsRoot: root, DefaultVariants: variants, ExtraImageModels: []ExtraImageModel{
				{Name: taken, ModelPath: "C:/models/dit.safetensors"},
			}}
			out, err := Generate(GenerateFile{Settings: s}, "test")
			if err != nil {
				t.Fatal(err)
			}
			assertUniqueKeys(t, out)
			if !slices.Contains(modelKeys(out), taken) {
				t.Fatalf("fixture no longer emits %q, so nothing clashes:\n%s", taken, out)
			}
			if strings.Contains(out, "C:/models/dit.safetensors") && !strings.Contains(out, "# SKIPPED") {
				t.Fatalf("clashing extra model was emitted:\n%s", out)
			}
			if !strings.Contains(out, "# SKIPPED extra image model "+`"`+taken+`"`) {
				t.Fatalf("clash left no in-band reason:\n%s", out)
			}
		})
	}
}
