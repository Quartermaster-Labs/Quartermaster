package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFileAt writes a stub file, creating parent directories.
func writeFileAt(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A TRELLIS.2 package is a DIRECTORY (pipeline.json plus ckpts/), discovered as
// a 3D model, emitted as a trellis2-server entry with the DINOv3 encoder and the
// BiRefNet remover found beside it in the same tree - and with nothing inside
// the package turned into a model of its own.
func TestAutogen_trellisDiscoverEmit(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "3D", "TRELLIS.2")
	pkg := filepath.Join(base, "TRELLIS.2-4B")
	dino := filepath.Join(base, "dinov3-vitl16-pretrain-lvd1689m")
	remover := filepath.Join(base, "BiRefNet", "BiRefNet-F16.gguf")
	// A copy vendored INSIDE the package wins over the sibling one below: the
	// package-local layout is the more specific answer.
	pkgRemover := filepath.Join(pkg, "BiRefNet", "BiRefNet-F16.gguf")

	writeFileAt(t, filepath.Join(pkg, "pipeline.json"), 128)
	writeFileAt(t, filepath.Join(pkg, "ckpts", "shape.safetensors"), 60_000_000) // ~0.06 GB, nonzero at 2dp
	// Inside the package: served by the trellis server, never as a llama model.
	writeFileAt(t, filepath.Join(pkg, "BiRefNet", "BiRefNet-F16.gguf"), 1024)
	writeFileAt(t, filepath.Join(dino, "model.safetensors"), 2048)
	writeFileAt(t, filepath.Join(dino, "config.json"), 64)
	writeFileAt(t, remover, 1024)
	writeFileAt(t, filepath.Join(root, "Qwen3-4B-Q4_K_M.gguf"), 4096)

	rows, err := DiscoverGgufModels(root)
	if err != nil {
		t.Fatal(err)
	}
	var trellis *GgufRow
	for i := range rows {
		if rows[i].IsTrellis {
			trellis = &rows[i]
		}
		if strings.Contains(strings.ToLower(rows[i].FileName), "birefnet") {
			t.Fatalf("a BiRefNet gguf must never be a model: %+v", rows[i])
		}
	}
	if trellis == nil {
		t.Fatalf("TRELLIS.2 package not discovered: %+v", rows)
	}
	if trellis.FullPath != pkg {
		t.Fatalf("FullPath = %q, want the package directory %q", trellis.FullPath, pkg)
	}
	if trellis.ID != "trellis-2-4b" {
		t.Fatalf("unexpected id %q", trellis.ID)
	}
	if trellis.SizeGB <= 0 {
		t.Fatalf("package size not measured: %v", trellis.SizeGB)
	}
	if len(rows) != 2 {
		t.Fatalf("want the llama gguf and the package, got %d rows: %+v", len(rows), rows)
	}

	out, err := Generate(GenerateFile{Settings: Settings{ModelsRoot: root}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"trellis-2-4b":`,
		"trellis2-server",
		"--model " + `"` + filepath.ToSlash(pkg) + `"`,
		"--dino " + `"` + filepath.ToSlash(dino) + `"`,
		"--birefnet " + `"` + filepath.ToSlash(pkgRemover) + `"`,
		"--model-cache-budget-mib 8192",
		"--port ${PORT}",
		// The full mesh is ~5.5M triangles / ~180 MB; a served model wants the
		// simplified one. Extra args land after this and win.
		"--mesh-postprocess-simplify",
		"checkEndpoint: /health",
		"in: [image]",
		"out: [3d]",
		"estVramGB: 11",
		// Not a coexist group: ~10 GB of VRAM has to swap like any other model.
		"  exclusive:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated config missing %q in:\n%s", want, out)
		}
	}
}

// The hub's own download layout is `<models root>/<owner>/<repo>`, so a browser
// install puts the package under one owner folder and the gated encoder under
// another. That must still emit a runnable command: the user opted in by
// downloading the files, and moving folders afterwards is not part of the deal.
func TestAutogen_trellisHubLayout(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "microsoft", "TRELLIS.2-4B")
	dino := filepath.Join(root, "facebook", "dinov3-vitl16-pretrain-lvd1689m")
	remover := filepath.Join(root, "Acly", "BiRefNet-GGUF", "BiRefNet-F16.gguf")
	writeFileAt(t, filepath.Join(pkg, "pipeline.json"), 128)
	writeFileAt(t, filepath.Join(pkg, "ckpts", "shape.safetensors"), 60_000_000)
	writeFileAt(t, filepath.Join(dino, "model.safetensors"), 2048)
	writeFileAt(t, filepath.Join(dino, "config.json"), 64)
	writeFileAt(t, remover, 1024)

	out, err := Generate(GenerateFile{Settings: Settings{ModelsRoot: root}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"trellis-2-4b":`,
		"--model " + `"` + filepath.ToSlash(pkg) + `"`,
		"--dino " + `"` + filepath.ToSlash(dino) + `"`,
		"--birefnet " + `"` + filepath.ToSlash(remover) + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated config missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SKIPPED") {
		t.Fatalf("a hub-layout package must not be skipped:\n%s", out)
	}
}

// A package whose DINOv3 encoder is absent is skipped with a reason rather than
// emitted as an entry that can only fail at request time: the shape stage cannot
// run without the encoder, so the server would refuse every generation.
func TestAutogen_trellisSkipsWithoutDino(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "TRELLIS.2-4B")
	writeFileAt(t, filepath.Join(pkg, "pipeline.json"), 128)
	writeFileAt(t, filepath.Join(pkg, "ckpts", "shape.safetensors"), 4096)

	out, err := Generate(GenerateFile{Settings: Settings{ModelsRoot: root}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `# SKIPPED "trellis-2-4b": no DINOv3 encoder`) {
		t.Fatalf("expected the package to be skipped with a reason, got:\n%s", out)
	}
	if strings.Contains(out, "trellis2-server") {
		t.Fatalf("a package without its encoder must not emit a command:\n%s", out)
	}
}
