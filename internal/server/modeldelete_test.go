package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModelDelete_planCoversShardsVariantsAndKept(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "org", "Big-GGUF")
	s1 := filepath.Join(dir, "Big-Q4_K_M-00001-of-00002.gguf")
	s2 := filepath.Join(dir, "Big-Q4_K_M-00002-of-00002.gguf")
	proj := filepath.Join(dir, "mmproj-F16.gguf")
	other := filepath.Join(dir, "Big-Q8_0.gguf")
	writeFile(t, s1, 10)
	writeFile(t, s2, 20)
	writeFile(t, proj, 5)
	writeFile(t, other, 7)
	writeFile(t, filepath.Join(dir, ".quartermaster-hub.json"), 2)

	m := filepath.ToSlash(s1)
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"big":        {Cmd: "llama-server -m " + m + " -c 8192"},
		"big-vision": {Cmd: "llama-server -m " + m + " --mmproj " + filepath.ToSlash(proj)},
		"big-q8":     {Cmd: "llama-server -m " + filepath.ToSlash(other)},
		"host":       {Cmd: "llama-server -m " + filepath.ToSlash(other) + " --model-draft=" + m},
	}}
	plan, err := planModelDelete(cfg, "big", []string{root}, map[string]bool{"big-vision?ctx=65536": true, "big-q8": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Bytes != 30 {
		t.Fatalf("files = %+v bytes = %d, want both shards / 30", plan.Files, plan.Bytes)
	}
	if want := []string{"big", "big-vision"}; len(plan.Removes) != 2 || plan.Removes[0] != want[0] || plan.Removes[1] != want[1] {
		t.Fatalf("removes = %v, want %v", plan.Removes, want)
	}
	if len(plan.Running) != 1 || plan.Running[0] != "big-vision?ctx=65536" {
		t.Fatalf("running = %v, want the ctx variant only", plan.Running)
	}
	if len(plan.UsedBy) != 1 || plan.UsedBy[0] != "host" {
		t.Fatalf("usedBy = %v, want [host]", plan.UsedBy)
	}
	if len(plan.Kept) != 2 {
		t.Fatalf("kept = %+v, want projector + other quant, no dotfile", plan.Kept)
	}
}

func TestModelDelete_refusesOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.gguf")
	writeFile(t, outside, 1)
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"x": {Cmd: "llama-server -m " + filepath.ToSlash(outside)},
	}}
	if _, err := planModelDelete(cfg, "x", []string{root}, nil); err == nil {
		t.Fatal("a file outside every models root must be refused")
	}
	if _, err := planModelDelete(cfg, "x", nil, nil); err == nil {
		t.Fatal("no roots must refuse everything")
	}
}

func TestModelDelete_removeEmptyDirSparesRootsAndJournals(t *testing.T) {
	root := t.TempDir()
	if removeEmptyDir(root, []string{root + string(filepath.Separator)}) {
		t.Fatal("a models root must never be removed")
	}
	withJournal := filepath.Join(root, "a")
	writeFile(t, filepath.Join(withJournal, ".quartermaster-download.json"), 1)
	if removeEmptyDir(withJournal, []string{root}) {
		t.Fatal("a folder with a download journal belongs to a paused download")
	}
	manifestOnly := filepath.Join(root, "b")
	writeFile(t, filepath.Join(manifestOnly, ".quartermaster-hub.json"), 1)
	if !removeEmptyDir(manifestOnly, []string{root}) {
		t.Fatal("a folder holding only the hub manifest should go")
	}
}
