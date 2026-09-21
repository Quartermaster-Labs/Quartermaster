package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
)

// stubSource is the minimum a sizing batch touches: it only ever calls Detail.
//
// The counter is ATOMIC because a batch sizes its rows concurrently — which is
// the contract hub.Source states for real adapters, and a plain int here was a
// race in the test rather than in the code under test.
type stubSource struct {
	detail hub.ModelDetail
	err    error
	calls  atomic.Int64
}

func (s *stubSource) ID() string   { return "stub" }
func (s *stubSource) Name() string { return "Stub" }
func (s *stubSource) Search(context.Context, hub.Query) (hub.Page, error) {
	return hub.Page{}, errors.New("not used")
}
func (s *stubSource) Detail(context.Context, string) (hub.ModelDetail, error) {
	s.calls.Add(1)
	return s.detail, s.err
}
func (s *stubSource) FileURL(repo, path string) (string, error) {
	return "https://huggingface.co/" + repo + "/resolve/main/" + path, nil
}
func (s *stubSource) CheckURL(string) error     { return nil }
func (s *stubSource) Authorize(r *http.Request) {}

// A batch must answer every path it was given, once each, whatever order they
// finish in — and a row that cannot be sized is a row with Err set, not an HTTP
// error that would sink the rest of the table.
func TestServer_StreamHubEstimates_OneRowPerPath(t *testing.T) {
	s := &Server{}
	src := &stubSource{err: errors.New("hub is down")}
	paths := []string{"a-Q4_K_M.gguf", "b-Q5_K_M.gguf", "c-Q6_K.gguf", "d-Q8_0.gguf", "e-F16.gguf"}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/hub/estimate", nil)
	s.streamHubEstimates(rec, req, src, "o/r", paths, autogen.Settings{TargetVramGB: 24})

	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}
	got := map[string]hubEstimateResp{}
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var row hubEstimateResp
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		if _, dup := got[row.Path]; dup {
			t.Errorf("path %q answered twice", row.Path)
		}
		got[row.Path] = row
	}
	if len(got) != len(paths) {
		t.Fatalf("got %d rows, want %d", len(got), len(paths))
	}
	for _, p := range paths {
		row, ok := got[p]
		if !ok {
			t.Fatalf("no row for %q", p)
		}
		if row.Repo != "o/r" || row.Err == "" {
			t.Errorf("row %q = %+v, want the repo echoed and the failure reported per row", p, row)
		}
	}
}

// No VRAM target is not an error either: the picker keeps showing sizes, and
// nothing should be fetched to find that out.
func TestServer_HubEstimateCached_NoTarget(t *testing.T) {
	s := &Server{}
	src := &stubSource{}
	req := httptest.NewRequest(http.MethodGet, "/api/hub/estimate", nil)
	row := s.hubEstimateCached(req, src, "o/r", "m.gguf", autogen.Settings{})
	if row.Err == "" {
		t.Errorf("row = %+v, want an explanatory err", row)
	}
	if n := src.calls.Load(); n != 0 {
		t.Errorf("Detail called %d times, want none without a budget to size against", n)
	}
}
