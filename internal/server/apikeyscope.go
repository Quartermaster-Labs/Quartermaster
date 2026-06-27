package server

import (
	"context"
	"net/http"

	"github.com/radu0120/llama-quartermaster/internal/config"
)

// apiKeyScopeKey carries the set of model IDs the request's API key is allowed
// to reach. Absent => the key is unrestricted (full access), matching the
// behaviour of a config with no apiKeyModels entry for that key.
type apiKeyScopeKey struct{}

// buildKeyScopes turns cfg.APIKeyModels into key => allowed-model-set, dropping
// keys mapped to an empty list (those are unrestricted). nil when no key is
// scoped, so the auth middleware can skip the context write entirely.
func buildKeyScopes(cfg config.Config) map[string]map[string]bool {
	if len(cfg.APIKeyModels) == 0 {
		return nil
	}
	scopes := make(map[string]map[string]bool, len(cfg.APIKeyModels))
	for key, models := range cfg.APIKeyModels {
		if len(models) == 0 {
			continue
		}
		set := make(map[string]bool, len(models))
		for _, m := range models {
			set[m] = true
		}
		scopes[key] = set
	}
	if len(scopes) == 0 {
		return nil
	}
	return scopes
}

// withKeyScope returns r carrying the key's allowed-model set, or r unchanged
// when the set is nil (unrestricted key).
func withKeyScope(r *http.Request, models map[string]bool) *http.Request {
	if models == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), apiKeyScopeKey{}, models))
}

// apiKeyModelSet returns the restricted model set for the current request's API
// key and whether the key is scoped at all. ok=false => unrestricted.
func apiKeyModelSet(r *http.Request) (models map[string]bool, ok bool) {
	models, ok = r.Context().Value(apiKeyScopeKey{}).(map[string]bool)
	return models, ok
}
