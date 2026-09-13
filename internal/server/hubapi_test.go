package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/hub"
)

// StartHubDownloads is the boot hook main calls once the autogen admin (and
// with it the models root) is attached. This is the seam that is easy to miss:
// run from server.New() there is no models root yet, and the restore silently
// finds nothing.
func TestStartHubDownloads_RestoresJournaledDownload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m.gguf.part"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hub package owns the journal writer; this is the shape it consumes.
	const journal = `{"jobs":[{"id":"dl-x","source":"hf","repo":"o/r","label":"m.gguf",` +
		`"started":"2026-01-01T00:00:00Z","files":[{"path":"m.gguf","size":4096}]}]}`
	if err := os.WriteFile(filepath.Join(dir, ".quartermaster-download.json"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(newStubRouter(nil, ""), nil)
	s.autogen = &AutogenAdmin{ModelsDir: root}
	s.hub = hub.NewManager(s.hubModelsRoot, nil)
	s.StartHubDownloads()

	jobs := s.hub.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want the journaled download restored", len(jobs))
	}
	j := jobs[0]
	if j.Phase != hub.PhasePaused {
		t.Errorf("phase = %q, want %q", j.Phase, hub.PhasePaused)
	}
	if j.Downloaded != 2048 || j.Total != 4096 {
		t.Errorf("progress = %d/%d, want 2048/4096 read off the disk", j.Downloaded, j.Total)
	}
}
