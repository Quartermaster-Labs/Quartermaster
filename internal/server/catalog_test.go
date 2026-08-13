package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// TestServer_APICatalog pins what /api/catalog gives that /v1/models cannot: the
// unlisted synthetic variants and a key-scope-free view. It is what the
// quartermaster_inspect chat tool reads, so a regression here makes the model
// answer "what models do I have?" with a filtered slice.
func TestServer_APICatalog(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	cfg := config.Config{
		RequiredAPIKeys: []string{"scopedkey"},
		APIKeyModels:    map[string][]string{"scopedkey": {"base"}},
		Models: map[string]config.ModelConfig{
			"base":         {Cmd: "llama-server -m base.gguf -c 32768"},
			"other":        {Cmd: "llama-server -m other.gguf -c 8192"},
			"base@ctx4096": {Cmd: "llama-server -m base.gguf -c 4096", Unlisted: true},
		},
	}
	if err := s.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/catalog", nil)
	r.Header.Set("Authorization", "Bearer scopedkey") // the loopback turn's key
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Models []apiModel `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]apiModel{}
	for _, m := range resp.Models {
		got[m.Id] = m
	}
	for _, id := range []string{"base", "other", "base@ctx4096"} {
		if _, ok := got[id]; !ok {
			t.Errorf("catalog missing %q (have %v)", id, got)
		}
	}
	if got["base"].Ctx != 32768 {
		t.Errorf("base ctx=%d want 32768", got["base"].Ctx)
	}
	if !got["base@ctx4096"].Unlisted {
		t.Error("variant should be flagged unlisted")
	}

	// Same request against the OpenAI discovery route: scoped to one model and
	// stripped of the variant — which is exactly why the tool cannot use it.
	r = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer scopedkey")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var listed struct {
		Data []modelRecord `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != "base" {
		t.Fatalf("/v1/models = %+v, want just [base] (test premise)", listed.Data)
	}
}
