package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/tools"
)

// guardSearchCtx decides whether the search this request is about to run may
// reach a private address, and records the answer on the context that every
// dial underneath it will consult (internal/tools/dialguard.go).
//
// A search endpoint takes its target FROM the caller, which is the point (a
// SearXNG instance is personal and normally lives on loopback or the LAN) and
// also a forgery primitive. The split is by origin: the person at this machine
// keeps the feature, and a caller arriving over the network -- the listener
// binds 0.0.0.0 and this API takes no credential -- is held to public
// addresses so it cannot use the server as a probe for the network around it.
//
// RemoteAddr on purpose, never X-Forwarded-For: that header is written by the
// client on a direct connection. The corollary is that a reverse proxy in
// front of this process makes every caller look local, so a deployment that
// adds one has to re-establish the distinction itself.
func guardSearchCtx(ctx context.Context, r *http.Request) context.Context {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsLoopback() {
		return ctx
	}
	return tools.PublicOnly(ctx)
}

// handleAPIWebSearch runs a web search on the browser's behalf. Two shapes:
//
//   - POST {providers:[…], q, limit} — the provider chain (internal/tools). Used by
//     the playground's per-provider Test button. POST, not GET, because the
//     body carries API keys and a query string lands in the access log.
//   - GET ?url=<searxng base>&q=… — the original SearXNG-only proxy, kept for
//     older clients. Returns SearXNG's raw JSON.
//
// Either way it exists because SearXNG ships no CORS headers, so the browser
// cannot reach it directly.
//
// Both forms take their target from the caller, so both go through
// guardSearchCtx: a loopback caller may still point them at a private SearXNG,
// and a caller from the network may not.
func (s *Server) handleAPIWebSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleWebSearchChain(w, r)
		return
	}

	base := strings.TrimSpace(r.URL.Query().Get("url"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if base == "" || q == "" {
		http.Error(w, "missing url or q", http.StatusBadRequest)
		return
	}

	body, err := tools.SearxngJSON(guardSearchCtx(r.Context(), r), base, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *Server) handleWebSearchChain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Providers []searchProviderCfg `json:"providers"`
		Query     string              `json:"q"`
		Limit     int                 `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	results, provider, err := searchChain(guardSearchCtx(r.Context(), r), req.Providers, req.Query, req.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Same field names as SearXNG's own JSON so the browser has one result shape
	// whichever provider answered.
	rows := make([]map[string]string, 0, len(results))
	for _, x := range results {
		rows = append(rows, map[string]string{"title": x.Title, "url": x.URL, "content": x.Content})
	}
	writeJSON(w, map[string]any{"provider": provider, "results": rows})
}
