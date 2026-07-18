package autogen

import (
	"strings"
	"testing"
)

// A *.ggml SAM file is discovered by name, emitted as a sam3_server model, and
// placed in a persistent non-exclusive coexist group; a plain .ggml is ignored.
func TestAutogen_samDiscoverEmitAndGroup(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "sam2.1_hiera_tiny_f16.ggml", 22_000_000)
	writeStub(t, dir, "not-a-model.ggml", 1024) // no sam/hiera token -> ignored

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sam *GgufRow
	for i := range rows {
		if rows[i].IsSam {
			sam = &rows[i]
		}
		if strings.Contains(rows[i].FileName, "not-a-model") {
			t.Fatalf("plain .ggml must not be discovered: %+v", rows[i])
		}
	}
	if sam == nil {
		t.Fatal("SAM model not discovered")
	}
	if sam.ID != "sam2.1-hiera-tiny-f16" {
		t.Fatalf("unexpected SAM id %q", sam.ID)
	}

	out, err := Generate(GenerateFile{Settings: Settings{ModelsRoot: dir}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"sam2.1-hiera-tiny-f16":`,
		"sam3_server",
		"--model",
		"checkEndpoint: /health",
		"segmentation: true",
		"in: [image]",
		"  sam:\n",
		"    exclusive: false\n",
		"    persistent: true\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated config missing %q in:\n%s", want, out)
		}
	}
}
