package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/chain"
	"github.com/quartermaster-labs/quartermaster/internal/event"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// inflightCounter tracks the in-flight model-dispatched requests: a count for
// the SSE feed and the busy gate, and a registry of the requests themselves so
// the status rail can list what is running and open one.
//
// The count and the registry move together (both under the middleware below),
// so the rail never shows "2 in flight" over a list of one.
type inflightCounter struct {
	total atomic.Int64

	mu     sync.Mutex
	nextID atomic.Uint64
	byID   map[uint64]*inflightEntry
}

// inflightEntry is one running request. Method/Path/Started are set on entry;
// Model and the request body arrive later, from the metrics middleware, which
// is where the model gets resolved and the body buffered - so a request that
// has not reached it yet (or never does: a GET) lists without them.
type inflightEntry struct {
	ID      uint64
	Method  string
	Path    string
	Started time.Time

	owner   *inflightCounter // whose mu guards the fields below
	model   string
	headers map[string]string
	body    []byte
}

func (c *inflightCounter) Increment() int64 { return c.total.Add(1) }
func (c *inflightCounter) Decrement() int64 { return c.total.Add(-1) }
func (c *inflightCounter) Current() int64   { return c.total.Load() }

func (c *inflightCounter) add(r *http.Request) *inflightEntry {
	e := &inflightEntry{
		ID:      c.nextID.Add(1),
		Method:  r.Method,
		Path:    r.URL.Path,
		Started: time.Now(),
		owner:   c,
	}
	c.mu.Lock()
	if c.byID == nil {
		c.byID = make(map[uint64]*inflightEntry)
	}
	c.byID[e.ID] = e
	c.mu.Unlock()
	return e
}

func (c *inflightCounter) remove(id uint64) {
	c.mu.Lock()
	delete(c.byID, id)
	c.mu.Unlock()
}

// attach records what the metrics middleware learned about a request. The
// body is the capture buffer it already holds (captures on), not a second
// copy; with captures off there is none and the entry lists without it.
func (e *inflightEntry) attach(model string, headers map[string]string, body []byte) {
	if e == nil {
		return
	}
	e.owner.mu.Lock()
	e.model = model
	e.headers = headers
	e.body = body
	e.owner.mu.Unlock()
}

// inflightSummary is a list row: everything but the body, which can be MBs
// (a base64 image) and is fetched per request only when one is opened.
type inflightSummary struct {
	ID      uint64    `json:"id"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Model   string    `json:"model,omitempty"`
	Started time.Time `json:"started"`
	HasBody bool      `json:"has_body"`
}

func (c *inflightCounter) list() []inflightSummary {
	c.mu.Lock()
	out := make([]inflightSummary, 0, len(c.byID))
	for _, e := range c.byID {
		out = append(out, inflightSummary{
			ID: e.ID, Method: e.Method, Path: e.Path, Model: e.model,
			Started: e.Started, HasBody: len(e.body) > 0,
		})
	}
	c.mu.Unlock()
	// Oldest first: the one that has been running longest is the one worth
	// looking at.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// detail returns a running request in the capture shape, so the dashboard can
// show it in the same viewer as a finished one - response half empty.
func (c *inflightCounter) detail(id uint64) (*ReqRespCapture, inflightSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok {
		return nil, inflightSummary{}, false
	}
	return &ReqRespCapture{
			ID:         int(e.ID),
			ReqPath:    e.Path,
			ReqHeaders: e.headers,
			ReqBody:    e.body,
		}, inflightSummary{
			ID: e.ID, Method: e.Method, Path: e.Path, Model: e.model,
			Started: e.Started, HasBody: len(e.body) > 0,
		}, true
}

type inflightCtxKey struct{}

func inflightEntryFrom(ctx context.Context) *inflightEntry {
	e, _ := ctx.Value(inflightCtxKey{}).(*inflightEntry)
	return e
}

// CreateInflightMiddleware returns middleware that registers the request on
// entry and drops it on exit, emitting an InFlightRequestsEvent for each.
func CreateInflightMiddleware(c *inflightCounter) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e := c.add(r)
			event.Emit(shared.InFlightRequestsEvent{Total: int(c.Increment())})
			defer func() {
				c.remove(e.ID)
				event.Emit(shared.InFlightRequestsEvent{Total: int(c.Decrement())})
			}()
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), inflightCtxKey{}, e)))
		})
	}
}

// handleAPIInflight lists the running requests, oldest first. No bodies.
func (s *Server) handleAPIInflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.inflight.list())
}

// handleAPIInflightRequest returns one running request in the capture shape.
// 404 once it has finished - the caller treats that as "look in Activity".
func (s *Server) handleAPIInflightRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid request ID")
		return
	}
	capture, summary, ok := s.inflight.detail(id)
	if !ok {
		shared.SendResponse(w, r, http.StatusNotFound, "request finished")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		inflightSummary
		Capture *ReqRespCapture `json:"capture"`
	}{summary, capture})
}
