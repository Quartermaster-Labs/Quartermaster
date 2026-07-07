package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/radu0120/llama-quartermaster/internal/logmon"
)

func TestParsePrometheusInto(t *testing.T) {
	body := `# HELP llamacpp:kv_cache_usage_ratio KV-cache usage
# TYPE llamacpp:kv_cache_usage_ratio gauge
llamacpp:kv_cache_usage_ratio 0.42
llamacpp:kv_cache_tokens 512
llamacpp:requests_processing 1
llamacpp:requests_deferred 3
llamacpp:prompt_tokens_total 1000
llamacpp:tokens_predicted_total 2500
llamacpp:n_decode_total 2500
llamacpp:tokens_predicted_seconds_total 50.5
llamacpp:unknown_metric 999
`
	var bm BackendMetrics
	parsePrometheusInto(&bm, []byte(body))

	if bm.KVCacheUsageRatio != 0.42 {
		t.Errorf("kv ratio = %v, want 0.42", bm.KVCacheUsageRatio)
	}
	if bm.KVCacheTokens != 512 {
		t.Errorf("kv tokens = %d, want 512", bm.KVCacheTokens)
	}
	if bm.RequestsProcessing != 1 || bm.RequestsDeferred != 3 {
		t.Errorf("requests = %d/%d, want 1/3", bm.RequestsProcessing, bm.RequestsDeferred)
	}
	if bm.TokensPredictedTotal != 2500 || bm.PredictedSecondsTotal != 50.5 {
		t.Errorf("predicted = %d/%v, want 2500/50.5", bm.TokensPredictedTotal, bm.PredictedSecondsTotal)
	}
}

func TestParseSlotsInto(t *testing.T) {
	// Two slots: an idle one holding cached context and a processing one mid-decode.
	// KV occupancy = max(prompt+decoded) = 1536+12; PromptTokens tracks that slot.
	body := `[
	  {"id":0,"n_ctx":102400,"is_processing":false,"n_prompt_tokens":800,"next_token":[{"n_decoded":0}]},
	  {"id":1,"n_ctx":102400,"is_processing":true,"n_prompt_tokens":1536,"next_token":[{"n_decoded":12}]}
	]`
	var bm BackendMetrics
	parseSlotsInto(&bm, []byte(body))

	if bm.KVCacheTokens != 1548 {
		t.Errorf("kv tokens = %d, want 1548", bm.KVCacheTokens)
	}
	if bm.PromptTokens != 1536 {
		t.Errorf("prompt tokens = %d, want 1536", bm.PromptTokens)
	}
	if bm.NCtx != 102400 {
		t.Errorf("n_ctx = %d, want 102400", bm.NCtx)
	}
	want := 1548.0 / 102400.0
	if bm.KVCacheUsageRatio != want {
		t.Errorf("kv ratio = %v, want %v", bm.KVCacheUsageRatio, want)
	}
}

// TestScrapeOne_SkipsQueuedEndpointsWhenBusy confirms /metrics and /slots
// (both queue-contending, per server-context.cpp) are skipped while a request
// is in flight, and that /props is fetched only once per (model, base) --
// never re-hit on later ticks unless the base URL changes (restart/reload).
func TestScrapeOne_SkipsQueuedEndpointsWhenBusy(t *testing.T) {
	var hits struct{ metrics, slots, props atomic.Int64 }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			hits.metrics.Add(1)
			w.Write([]byte("llamacpp:requests_processing 1\n"))
		case "/slots":
			hits.slots.Add(1)
			w.Write([]byte(`[]`))
		case "/props":
			hits.props.Add(1)
			w.Write([]byte(`{"total_slots":1,"default_generation_settings":{"n_ctx":4096}}`))
		}
	}))
	defer srv.Close()

	m := newBackendMetricsMonitor(nil, nil, func(model string) (int64, bool) { return 3, true }, logmon.New())
	prev := BackendMetrics{RequestsProcessing: 7}

	busy := m.scrapeOne(context.Background(), "m", srv.URL, true, prev, true)
	if hits.metrics.Load() != 0 || hits.slots.Load() != 0 {
		t.Errorf("busy scrape hit metrics=%d slots=%d, want 0/0", hits.metrics.Load(), hits.slots.Load())
	}
	if hits.props.Load() != 1 {
		t.Errorf("busy scrape props hits = %d, want 1 (first-ever fetch)", hits.props.Load())
	}
	if busy.RequestsProcessing != 3 {
		t.Errorf("busy scrape RequestsProcessing = %d, want live modelInflight 3 (not carried-forward 7)", busy.RequestsProcessing)
	}
	if busy.NCtx != 4096 {
		t.Errorf("busy scrape NCtx = %d, want fresh 4096 from /props", busy.NCtx)
	}

	idle := m.scrapeOne(context.Background(), "m", srv.URL, false, prev, true)
	if hits.metrics.Load() != 1 || hits.slots.Load() != 1 {
		t.Errorf("idle scrape hit metrics=%d slots=%d, want 1/1", hits.metrics.Load(), hits.slots.Load())
	}
	if hits.props.Load() != 1 {
		t.Errorf("idle scrape props hits = %d, want still 1 (cached, same base)", hits.props.Load())
	}
	if idle.RequestsProcessing != 1 {
		t.Errorf("idle scrape RequestsProcessing = %d, want fresh 1", idle.RequestsProcessing)
	}
	if idle.NCtx != 4096 {
		t.Errorf("idle scrape NCtx = %d, want cached 4096", idle.NCtx)
	}

	// Base URL change (process restart on a new port) must invalidate the cache.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			hits.props.Add(1)
			w.Write([]byte(`{"total_slots":1,"default_generation_settings":{"n_ctx":4096}}`))
		}
	}))
	defer srv2.Close()
	m.scrapeOne(context.Background(), "m", srv2.URL, false, prev, true)
	if hits.props.Load() != 2 {
		t.Errorf("scrape after base change props hits = %d, want 2 (cache invalidated)", hits.props.Load())
	}
}
