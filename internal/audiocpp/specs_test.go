package audiocpp

import (
	"os"
	"path/filepath"
	"testing"
)

// write drops one spec file into dir. The fixtures are trimmed copies of the
// real shapes in upstream's model_specs/, keeping only the fields Load reads.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ggufSpec = `{
  "family": "qwen3_tts",
  "display_name": "Qwen3-TTS",
  "category": "tts",
  "status": "supported",
  "tasks": ["tts", "clone"],
  "package_defaults": {"download": {"kind": "huggingface_snapshot", "repo": "audio-cpp/audio.cpp-gguf", "revision": "main"}},
  "packages": [
    {"id": "big_bf16", "display_name": "Base BF16", "format": "gguf", "precision": "bf16",
     "target_directory": "X", "files": ["X/base-bf16.gguf"]},
    {"id": "small_q8", "display_name": "Base Q8_0", "default": true, "format": "gguf", "precision": "q8_0",
     "target_directory": "X", "files": ["X/base-q8_0.gguf"]},
    {"id": "hf_weights", "display_name": "HF Safetensors", "format": "safetensors", "precision": "bf16",
     "target_directory": "Y", "files": ["config.json", "model.safetensors"]}
  ]
}`

func TestLoad_GgufOnlyDefaultFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "qwen3_tts.json", ggufSpec)

	fams, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 {
		t.Fatalf("families = %d, want 1", len(fams))
	}
	f := fams[0]
	if f.Family != "qwen3_tts" || f.Category != "tts" || f.Status != "supported" {
		t.Errorf("family = %+v", f)
	}
	// The safetensors package is dropped: our discovery walks ggufs, so it would
	// download gigabytes that never become a model row.
	if len(f.Packages) != 2 {
		t.Fatalf("packages = %d, want 2 (safetensors dropped)", len(f.Packages))
	}
	// Upstream's default is the recommended pick and has to lead, even though it
	// is second in the file.
	if f.Packages[0].ID != "small_q8" || !f.Packages[0].Default {
		t.Errorf("packages[0] = %+v, want the default one first", f.Packages[0])
	}
	// Repo and revision are inherited from package_defaults.
	if got := f.Packages[0].Repo; got != "audio-cpp/audio.cpp-gguf" {
		t.Errorf("repo = %q", got)
	}
	if got := f.Packages[0].Revision; got != "main" {
		t.Errorf("revision = %q", got)
	}
}

// A per-package download block overrides the family default: ACE-Step's turbo
// packages live in a different publisher's repo than the rest of the spec.
func TestLoad_PerPackageRepoOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ace.json", `{
	  "family": "ace_step", "display_name": "ACE-Step", "category": "audio_generation",
	  "package_defaults": {"download": {"kind": "huggingface_snapshot", "repo": "audio-cpp/audio.cpp-gguf"}},
	  "packages": [
	    {"id": "turbo", "display_name": "Turbo", "format": "gguf", "files": ["a/turbo.gguf"],
	     "download": {"kind": "huggingface_snapshot", "repo": "CaptainArni/audio.cpp-gguf"}}
	  ]
	}`)

	fams, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || len(fams[0].Packages) != 1 {
		t.Fatalf("got %+v", fams)
	}
	if got := fams[0].Packages[0].Repo; got != "CaptainArni/audio.cpp-gguf" {
		t.Errorf("repo = %q, want the package's own", got)
	}
}

// Three ways a family offers nothing to download, all of which must leave it out
// of the catalog rather than render a name with no button.
func TestLoad_DropsUndownloadable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "unsupported.json", `{"family":"x1","display_name":"X1",
	  "package_defaults":{"download":{"kind":"unsupported"}},
	  "packages":[{"id":"p","format":"gguf","files":["p.gguf"]}]}`)
	write(t, dir, "norepo.json", `{"family":"x2","display_name":"X2",
	  "packages":[{"id":"p","format":"gguf","files":["p.gguf"]}]}`)
	write(t, dir, "nofiles.json", `{"family":"x3","display_name":"X3",
	  "package_defaults":{"download":{"kind":"huggingface_snapshot","repo":"a/b"}},
	  "packages":[{"id":"p","format":"gguf","files":[]}]}`)

	fams, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 0 {
		t.Errorf("families = %+v, want none", fams)
	}
}

// One spec upstream changed the shape of must not cost the user the other 71,
// and a non-spec file in the directory is not an error either.
func TestLoad_SkipsBadFilesKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "broken.json", `{not json at all`)
	write(t, dir, "README.md", "not a spec")
	if err := os.Mkdir(filepath.Join(dir, "nested.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "qwen3_tts.json", ggufSpec)

	fams, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || fams[0].Family != "qwen3_tts" {
		t.Fatalf("got %+v, want just the readable spec", fams)
	}
}

// No install, or an install too old to ship the catalog, is an empty catalog and
// not a failure: the browser hides the tab rather than showing an error.
func TestLoad_MissingDirIsEmpty(t *testing.T) {
	fams, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(fams) != 0 {
		t.Errorf("Load(missing) = %v, %v; want nil, nil", fams, err)
	}
	if fams, err := Load("  "); err != nil || fams != nil {
		t.Errorf("Load(blank) = %v, %v; want nil, nil", fams, err)
	}
}

func TestSpecsDir(t *testing.T) {
	exe := filepath.Join("C:", "qm", "bin", "audiocpp-server", "v0.8.0-vulkan", "audiocpp_server.exe")
	want := filepath.Join(filepath.Dir(exe), "model_specs")
	if got := SpecsDir(exe); got != want {
		t.Errorf("SpecsDir = %q, want %q", got, want)
	}
	if got := SpecsDir("   "); got != "" {
		t.Errorf("SpecsDir(blank) = %q, want empty", got)
	}
}
