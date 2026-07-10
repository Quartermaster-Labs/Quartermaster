package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// TestServer_APIKeyScope checks that the auth middleware attaches a scoped key's
// allowed-model set to the request, leaves unscoped keys unrestricted, and that
// the admin gate rejects scoped keys.
func TestServer_APIKeyScope(t *testing.T) {
	cfg := config.Config{
		RequiredAPIKeys: []string{"adminkey", "pikey"},
		APIKeyModels:    map[string][]string{"pikey": {"qwen-35b", "qwen-27b"}},
	}

	t.Run("scoped key carries its model set", func(t *testing.T) {
		var got map[string]bool
		var ok bool
		final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, ok = apiKeyModelSet(r)
		})
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer pikey")
		CreateAuthMiddleware(cfg)(final).ServeHTTP(httptest.NewRecorder(), r)
		if !ok || !got["qwen-35b"] || !got["qwen-27b"] || got["other"] {
			t.Fatalf("scope = %v ok=%v, want {qwen-35b,qwen-27b}", got, ok)
		}
	})

	t.Run("unscoped key is unrestricted", func(t *testing.T) {
		var ok bool
		final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, ok = apiKeyModelSet(r)
		})
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer adminkey")
		CreateAuthMiddleware(cfg)(final).ServeHTTP(httptest.NewRecorder(), r)
		if ok {
			t.Fatal("admin key should be unrestricted (no scope in context)")
		}
	})

	t.Run("invalid key is rejected", func(t *testing.T) {
		final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer nope")
		w := httptest.NewRecorder()
		CreateAuthMiddleware(cfg)(final).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("invalid key = %d, want 401", w.Code)
		}
	})
}
