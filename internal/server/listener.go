package server

import (
	"context"
	"net/http"
)

// listenerScopeKey carries the set of model IDs a request's listen address is
// allowed to reach. A nil value (key absent) means unrestricted — every model
// is reachable, matching legacy single --listen behaviour.
type listenerCtxKey struct{}

// ServeListener dispatches a request that arrived on listen address addr. When
// addr maps to a restricted model set, that set is stored in the request
// context so handleListModels can filter the catalog and localPeerHandler can
// reject models the address does not expose. Addresses without an entry are
// unrestricted.
//
// All listeners share this single Server (and therefore one router/scheduler),
// which is the invariant that makes cross-listener VRAM accounting and eviction
// correct.
func (s *Server) ServeListener(addr string, w http.ResponseWriter, r *http.Request) {
	if lm := s.listenerModels.Load(); lm != nil {
		if models, ok := (*lm)[addr]; ok {
			ctx := context.WithValue(r.Context(), listenerCtxKey{}, models)
			r = r.WithContext(ctx)
		}
	}
	r = s.markPlayground(addr, r)
	(*s.handler.Load()).ServeHTTP(w, r)
}

// listenerModelSet returns the restricted model set for the current request and
// whether the request is scoped to a listener at all. ok=false means
// unrestricted (no listeners configured, or the address is not restricted).
func listenerModelSet(r *http.Request) (models map[string]bool, ok bool) {
	models, ok = r.Context().Value(listenerCtxKey{}).(map[string]bool)
	return models, ok
}
