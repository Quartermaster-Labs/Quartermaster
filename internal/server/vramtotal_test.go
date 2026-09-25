package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// No GPU monitor means no telemetry: the playground gets 0 (and falls back to
// its default) rather than an error it would have to handle.
func TestServer_VramTotalNoPerf(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleAPIVramTotal(rec, httptest.NewRequest(http.MethodGet, "/api/vram-total", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if v, ok := got["total_mb"]; !ok || v != 0 {
		t.Fatalf("got %v, want total_mb=0", got)
	}
}
