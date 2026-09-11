package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

const generateFilePath = `E:\Apps\LLM\quartermaster\quartermaster-generate.yaml`

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

// TestEnsureConfig_Concurrent keeps the regen serialized: several goroutines
// (the download-completion hook, the watch-models poll, a UI save) can reach
// EnsureConfig in the same instant, and two of them interleaving their writes
// tore config.yaml. Every goroutine must come back clean and the config on
// disk must be a whole YAML document — not a splice of two writes.
func TestEnsureConfig_Concurrent(t *testing.T) {
	dir := t.TempDir()
	modelsDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	generateFilePath := filepath.Join(dir, "quartermaster-generate.yaml")
	if err := os.WriteFile(generateFilePath, []byte(fmt.Sprintf("settings:\n  modelsRoot: %s\n", filepath.ToSlash(modelsDir))), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "config.yaml")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := EnsureConfig(generateFilePath, out, "", nil); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent EnsureConfig %d: %v", i, err)
		}
	}

	bytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(bytes, &doc); err != nil {
		t.Fatalf("config.yaml is not a whole YAML document (torn write?): %v\n%s", err, string(bytes))
	}
	if _, err := os.Stat(out + hashCacheSuffix); err != nil {
		t.Fatalf("hash cache not written: %v", err)
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
