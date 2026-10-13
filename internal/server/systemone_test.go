package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// /v1/systemone is a plain model-dispatched route: the body goes to
// llama-server untouched and llama-server does the validation, because what
// it accepts varies per decision model (lfm2-d1 takes a null state, vllm-d3 a
// question without instructions). These bodies must all reach the router.
func TestServer_SystemoneRoute(t *testing.T) {
	s := newTestServer(newStubRouter([]string{"kev-0.8b-q8_0"}, "upstream-ok"), nil)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}

	for _, body := range []string{
		`{"state":"x","model":"kev-0.8b-q8_0","questions":{"q":{"type":"choice","instructions":"i","criteria":{"a":null,"b":"B"}}}}`,
		`{"state":"x","model":"kev-0.8b-q8_0","questions":{"q":{"type":"noul","instructions":"urgent?"}}}`,
		`{"state":"x","model":"kev-0.8b-q8_0","questions":{"q":{"type":"score","instructions":"rate","criteria":["a","b"]}}}`,
		`{"state":null,"images":["data:image/png;base64,AA=="],"model":"kev-0.8b-q8_0","questions":{"q":{"type":"noul"}}}`,
	} {
		if w := post(body); w.Code != http.StatusOK || w.Body.String() != "upstream-ok" {
			t.Errorf("status = %d body = %q, want 200 upstream-ok for %s", w.Code, w.Body.String(), body)
		}
	}

	t.Run("missing model rejected by context middleware", func(t *testing.T) {
		w := post(`{"state":"x","questions":{"q":{"type":"choice","instructions":"i","criteria":{"a":"b"}}}}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})
}

// A decision model is advertised the way llama-server advertises it, with
// "decisions" as its output modality, and the config's Out slice is not
// mutated by the rendering (it aliases the live config).
func TestRenderCapabilities_Decision(t *testing.T) {
	caps := config.ModelCapConfig{In: []string{"text", "image"}, Decision: true, DecisionType: "lfm2-d1", Context: 4096}
	arch, capsMap, _, ctx := renderCapabilities(caps)
	if out, _ := arch["output_modalities"].([]string); !slices.Equal(out, []string{"decisions"}) {
		t.Errorf("output_modalities = %v, want [decisions]", arch["output_modalities"])
	}
	if arch["modality"] != "text+image->decisions" {
		t.Errorf("modality = %v", arch["modality"])
	}
	if capsMap["decisions"] != true || capsMap["decision_type"] != "lfm2-d1" || capsMap["vision"] != true {
		t.Errorf("capabilities = %v", capsMap)
	}
	if ctx != 4096 {
		t.Errorf("context_length = %d", ctx)
	}
	if len(caps.Out) != 0 {
		t.Errorf("config Out mutated: %v", caps.Out)
	}

	// No declared inputs: text is implied, and no vision flag.
	arch, capsMap, _, _ = renderCapabilities(config.ModelCapConfig{Decision: true})
	if in, _ := arch["input_modalities"].([]string); !slices.Equal(in, []string{"text"}) || capsMap["vision"] != nil {
		t.Errorf("arch = %v caps = %v", arch, capsMap)
	}
}
