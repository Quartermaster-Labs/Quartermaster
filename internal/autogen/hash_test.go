package autogen

import (
	"os"
	"path/filepath"
	"testing"
)

const generateFilePath = `E:\Apps\LLM\llama-quartermaster\quartermaster-generate.yaml`

// TestEnsureConfig_HashGate exercises the full startup path: first call
// generates the config, second call (inputs unchanged) skips regeneration.
func TestEnsureConfig_HashGate(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	if _, err := os.Stat(generateFilePath); err != nil {
		t.Skipf("generate file %s absent", generateFilePath)
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "config.yaml")

	regen, err := EnsureConfig(generateFilePath, out, "", nil)
	if err != nil {
		t.Fatalf("first EnsureConfig: %v", err)
	}
	if !regen {
		t.Fatal("first call should regenerate")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if _, err := os.Stat(out + hashCacheSuffix); err != nil {
		t.Fatalf("hash cache not written: %v", err)
	}

	regen, err = EnsureConfig(generateFilePath, out, "", nil)
	if err != nil {
		t.Fatalf("second EnsureConfig: %v", err)
	}
	if regen {
		t.Error("second call should skip regeneration (inputs unchanged)")
	}
}

func TestInputsHash_Stable(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	a, err := InputsHash(realModelsRoot, []byte("control"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := InputsHash(realModelsRoot, []byte("control"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("hash not stable: %s vs %s", a, b)
	}
	c, err := InputsHash(realModelsRoot, []byte("different"))
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Error("hash should change when control bytes change")
	}
}
