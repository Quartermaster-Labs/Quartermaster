package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
