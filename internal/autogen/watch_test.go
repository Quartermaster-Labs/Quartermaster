package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCurrentInputsHash_DetectsModelChanges exercises the -watch-models gate:
// the hash is stable across calls, matches what CachedConfigHash returns, and
// changes when a GGUF is added to the models folder. It only stats files (no
// metadata parsing), so empty placeholder .gguf files are enough.
func TestCurrentInputsHash_DetectsModelChanges(t *testing.T) {
	dir := t.TempDir()
	modelsRoot := filepath.Join(dir, "models")
	if err := os.MkdirAll(modelsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelsRoot, "a.gguf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	genPath := filepath.Join(dir, "generate.yaml")
	writeGen := func(root string) {
		body := "settings:\n  modelsRoot: " + filepath.ToSlash(root) + "\n"
		if err := os.WriteFile(genPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeGen(modelsRoot)

	h1, err := CurrentInputsHash(genPath, "")
	if err != nil {
		t.Fatalf("CurrentInputsHash: %v", err)
	}
	if h1 == "" {
		t.Fatal("hash is empty")
	}

	// Stable across calls with unchanged inputs.
	again, err := CurrentInputsHash(genPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if again != h1 {
		t.Errorf("hash not stable: %s vs %s", h1, again)
	}

	// CachedConfigHash reads the <config>.modelhash sidecar that EnsureConfig
	// writes; simulate it and confirm the getter returns the stored value.
	out := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(out+hashCacheSuffix, []byte(h1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CachedConfigHash(out); got != h1 {
		t.Errorf("CachedConfigHash = %q, want %q", got, h1)
	}

	// Adding a model changes the hash (the signal the poller reloads on).
	if err := os.WriteFile(filepath.Join(modelsRoot, "b.gguf"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := CurrentInputsHash(genPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if h2 == h1 {
		t.Error("hash should change when a GGUF is added")
	}
}
