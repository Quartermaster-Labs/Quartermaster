package server

import (
	"encoding/json"
	"net/http"
)

// handleAPIKvCache serves the slot KV-cache monitoring snapshot for the
// Observe → KV Cache tab: lifetime counters, recent events, and the persisted
// session files on disk. Returns {enabled:false} when the cache is off.
func (s *Server) handleAPIKvCache(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.slotCache.stats())
}
