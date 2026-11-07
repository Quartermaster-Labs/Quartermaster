package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/hub"
)

// companionFakeSource lists a fixed file set for every repo.
type companionFakeSource struct{ files []hub.File }

func (companionFakeSource) ID() string   { return "hf" }
func (companionFakeSource) Name() string { return "fake" }
func (companionFakeSource) Search(context.Context, hub.Query) (hub.Page, error) {
	return hub.Page{}, errors.New("unused")
}
func (f companionFakeSource) Detail(_ context.Context, repo string) (hub.ModelDetail, error) {
	return hub.ModelDetail{Files: f.files}, nil
}
func (companionFakeSource) FileURL(repo, path string) (string, error) {
	return "", errors.New("unused")
}
func (companionFakeSource) CheckURL(string) error   { return nil }
func (companionFakeSource) Authorize(*http.Request) {}

func TestHubCompanionState(t *testing.T) {
	root := t.TempDir()
	src := companionFakeSource{files: []hub.File{
		{Path: "ae.safetensors", SizeBytes: 4},
		{Path: "enc.gguf", SizeBytes: 100},
		{Path: "busy.gguf", SizeBytes: 50},
	}}
	s := newTestServer(newStubRouter(nil, ""), nil)
	s.autogen = &AutogenAdmin{ModelsDir: root}
	s.hub = hub.NewManager(s.hubModelsRoot, nil, src)

	dir := filepath.Join(root, hub.RepoDirName("o/r"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Full size = local; a short copy is a truncated download, still needed.
	if err := os.WriteFile(filepath.Join(dir, "ae.safetensors"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "enc.gguf"), []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs := []hub.Job{
		{Repo: "o/r", Phase: hub.PhaseDownloading, Files: []hub.JobFile{{Path: "busy.gguf"}}},
		// A finished job is history, not a live download.
		{Repo: "o/r", Phase: hub.PhaseDone, Files: []hub.JobFile{{Path: "enc.gguf"}}},
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range []struct {
		path, want string
		size       int64
	}{
		{"ae.safetensors", companionLocal, 4},
		{"enc.gguf", companionNeeded, 100},
		{"busy.gguf", companionDownloading, 50},
		{"gone.gguf", companionUnavailable, 0},
	} {
		state, size := s.companionState(r, src, "o/r", c.path, jobs)
		if state != c.want || size != c.size {
			t.Errorf("%s: state=%s size=%d, want %s %d", c.path, state, size, c.want, c.size)
		}
	}
}
