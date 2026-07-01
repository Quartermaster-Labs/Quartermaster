package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/shared"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// promptCanon canonicalizes volatile spans in a client's chat prompt so the
// stable prefix stays byte-identical turn-to-turn — maximizing llama-server's
// native prefix-cache reuse (and our slot KV cache seeding) for ANY client, not
// just slotcache-participating models. It is the second feature under the
// "Context Management" umbrella; the slot KV cache (slotcache.go) is the first.
//
// v1 canonicalizer: strip sub-day timestamps from the system prompt. An agent
// that stamps the wall clock into its system prompt (e.g. pi's per-session memory
// snapshot "session_start at 2026-06-29 12:35:44") otherwise changes its prefix
// every request, so the whole prompt reprefills each turn. normalizeTimestamps
// reduces it to date granularity — non-lossy for caching (the model still sees
// the date) and idempotent, so it runs unconditionally. slotcache applies the
// same normalization for participating models; a second pass here is a no-op.
//
// ponytail: always-on, non-lossy, idempotent — no config gate, one hardcoded
// rule. Add a config gate + a canonicalizer registry only if a future rule is
// lossy or a client needs raw timestamps.
type promptCanon struct {
	mu       sync.Mutex
	counters canonCounters
	events   []canonEvent // newest last, capped at canonEventRing
}

const canonEventRing = 100

// canonCounters tallies lifetime canonicalization activity for the monitoring tab.
type canonCounters struct {
	Seen         int64 `json:"seen"`         // chat requests inspected
	Rewritten    int64 `json:"rewritten"`    // requests whose prefix was canonicalized
	BytesRemoved int64 `json:"bytesRemoved"` // total bytes trimmed from prompts
}

// canonEvent is one recent rewrite shown in the live event log.
type canonEvent struct {
	Time  time.Time `json:"time"`
	Model string    `json:"model"`
	Rule  string    `json:"rule"`  // which canonicalizer fired (e.g. "timestamp")
	Bytes int64     `json:"bytes"` // bytes removed
}

func newPromptCanon() *promptCanon { return &promptCanon{} }

// middleware canonicalizes the system prompt of chat-style JSON requests before
// they reach the slot cache and upstream. A no-op for non-JSON (multipart form,
// etc.) and for bodies with no normalizable timestamp.
func (pc *promptCanon) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pc == nil || r.Body == nil ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
			return
		}

		out, rule, removed := canonicalizeBody(body)
		if rule != "" && removed > 0 {
			// Forward the shorter, canonicalized body — fix Content-Length too, or the
			// reverse proxy advertises the stale length and the upstream stalls.
			r.Body = io.NopCloser(bytes.NewReader(out))
			r.Header.Del("Transfer-Encoding")
			r.Header.Set("Content-Length", strconv.Itoa(len(out)))
			r.ContentLength = int64(len(out))
			model := ""
			if data, ok := shared.ReadContext(r.Context()); ok {
				model = data.ModelID
			}
			pc.record(canonEvent{Model: model, Rule: rule, Bytes: int64(removed)})
		} else {
			r.Body = io.NopCloser(bytes.NewReader(body)) // restore untouched
			if isChatBody(body) {
				pc.bumpSeen()
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isChatBody reports whether a request carries a chat prompt we canonicalize
// (OpenAI messages array or an Anthropic top-level system field).
func isChatBody(body []byte) bool {
	return gjson.GetBytes(body, "messages").IsArray() || gjson.GetBytes(body, "system").Exists()
}

// canonicalizeBody applies the v1 timestamp canonicalizer to a chat body's
// system prompt (OpenAI system/developer message, or Anthropic top-level system).
// Returns the possibly-rewritten body, the rule name ("" if nothing changed), and
// the number of bytes removed. Pure so it can be unit-tested.
func canonicalizeBody(body []byte) (out []byte, rule string, removed int) {
	// First system/developer message in an OpenAI-style messages array.
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		for i, m := range msgs.Array() {
			role := m.Get("role").String()
			if role != "system" && role != "developer" {
				continue
			}
			raw := m.Get("content").Raw
			if norm := normalizeTimestamps(raw); norm != raw {
				if nb, err := sjson.SetRawBytes(body, "messages."+strconv.Itoa(i)+".content", []byte(norm)); err == nil {
					return nb, "timestamp", len(raw) - len(norm)
				}
			}
			return body, "", 0 // only the first system message carries the preamble
		}
	}
	// Anthropic /v1/messages carries the system prompt in a top-level field.
	if top := gjson.GetBytes(body, "system"); top.Exists() {
		raw := top.Raw
		if norm := normalizeTimestamps(raw); norm != raw {
			if nb, err := sjson.SetRawBytes(body, "system", []byte(norm)); err == nil {
				return nb, "timestamp", len(raw) - len(norm)
			}
		}
	}
	return body, "", 0
}

func (pc *promptCanon) bumpSeen() {
	pc.mu.Lock()
	pc.counters.Seen++
	pc.mu.Unlock()
}

// record bumps the rewrite counters and appends to the bounded event ring.
func (pc *promptCanon) record(ev canonEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	pc.mu.Lock()
	pc.counters.Seen++
	pc.counters.Rewritten++
	pc.counters.BytesRemoved += ev.Bytes
	pc.events = append(pc.events, ev)
	if len(pc.events) > canonEventRing {
		pc.events = pc.events[len(pc.events)-canonEventRing:]
	}
	pc.mu.Unlock()
}

// CanonStats is the /api/canon snapshot powering the Context Management →
// Prompt Canonicalization tab.
type CanonStats struct {
	Counters canonCounters `json:"counters"`
	Events   []canonEvent  `json:"events"` // newest first
}

func (pc *promptCanon) stats() CanonStats {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	out := CanonStats{Counters: pc.counters}
	out.Events = make([]canonEvent, len(pc.events))
	for i, e := range pc.events { // reverse: newest first for the UI
		out.Events[len(pc.events)-1-i] = e
	}
	return out
}

// handleAPICanon serves the prompt-canonicalization monitoring snapshot.
func (s *Server) handleAPICanon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.promptCanon.stats())
}
