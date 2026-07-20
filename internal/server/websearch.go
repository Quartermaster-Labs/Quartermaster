package server

import (
	"net/http"
	"strings"
	"time"
)

var webSearchClient = &http.Client{Timeout: 15 * time.Second}

// handleAPIWebSearch proxies a SearXNG JSON query for the chat playground so the
// browser reaches it same-origin and dodges CORS (SearXNG ships no CORS headers).
// Takes ?url=<searxng base>&q=<query>, forwards through the shared rate-limited
// gate (searxng.go) and returns the JSON.
//
// ponytail: open forwarder — it fetches whatever ?url= points at (SSRF). Fine for
// a local single-user tool; restrict to a configured allowlist if ever exposed.
func (s *Server) handleAPIWebSearch(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSpace(r.URL.Query().Get("url"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if base == "" || q == "" {
		http.Error(w, "missing url or q", http.StatusBadRequest)
		return
	}

	body, err := searxngJSON(r.Context(), base, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
