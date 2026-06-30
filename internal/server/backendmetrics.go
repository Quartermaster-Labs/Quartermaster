package server

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/radu0120/llama-quartermaster/internal/event"
	"github.com/radu0120/llama-quartermaster/internal/logmon"
	"github.com/radu0120/llama-quartermaster/internal/shared"
	"github.com/tidwall/gjson"
)

// BackendMetrics is one running llama-server's live state, scraped from its
// own /metrics (Prometheus) and /props endpoints. Quartermaster's per-request
// metrics (metrics.go) can't see these — they are backend-internal gauges:
// KV-cache fill, slot/queue saturation, and process-lifetime throughput totals.
type BackendMetrics struct {
	Model     string    `json:"model"`
	Timestamp time.Time `json:"timestamp"`
	OK        bool      `json:"ok"` // /metrics scrape succeeded

	// Live gauges.
	KVCacheUsageRatio  float64 `json:"kv_cache_usage_ratio"`
	KVCacheTokens      int64   `json:"kv_cache_tokens"`
	RequestsProcessing int64   `json:"requests_processing"`
	RequestsDeferred   int64   `json:"requests_deferred"`

	// Live per-request snapshot from /slots: the active (or last) prompt size and
	// llama-server's rolling throughput gauges. These surface "In"/"Prompt" while
	// a request is still streaming, before the final per-request timings land.
	PromptTokens           int64   `json:"prompt_tokens"`
	PromptTokensSeconds    float64 `json:"prompt_tokens_seconds"`
	PredictedTokensSeconds float64 `json:"predicted_tokens_seconds"`

	// Cumulative counters since the process started.
	PromptTokensTotal     int64   `json:"prompt_tokens_total"`
	TokensPredictedTotal  int64   `json:"tokens_predicted_total"`
	NDecodeTotal          int64   `json:"n_decode_total"`
	PromptSecondsTotal    float64 `json:"prompt_seconds_total"`
	PredictedSecondsTotal float64 `json:"predicted_seconds_total"`

	// Static config from /props (constant per load).
	NCtx       int64 `json:"n_ctx"`
	TotalSlots int64 `json:"total_slots"`
}

// BackendMetricsEvent carries a full snapshot (all currently-running backends)
// to the SSE stream. Defined here (not in shared) so it can hold server types,
// mirroring ActivityLogEvent; its Type() borrows a shared ID.
type BackendMetricsEvent struct {
	Metrics []BackendMetrics
}

func (e BackendMetricsEvent) Type() uint32 { return shared.BackendMetricsEventID }

// backendMetricsMonitor polls each running backend's /metrics + /props on a
// ticker, caches the latest snapshot, and emits a BackendMetricsEvent so the
// dashboard gets live KV/slot/throughput gauges over SSE.
type backendMetricsMonitor struct {
	mu     sync.RWMutex
	latest map[string]BackendMetrics

	// running returns model-id -> resolved upstream base URL for every running
	// local process (${PORT} already substituted in cfg.Models[id].Proxy).
	running  func() map[string]string
	client   *http.Client
	log      *logmon.Monitor
	interval time.Duration
}

func newBackendMetricsMonitor(running func() map[string]string, log *logmon.Monitor) *backendMetricsMonitor {
	return &backendMetricsMonitor{
		latest:  map[string]BackendMetrics{},
		running: running,
		// Timeout MUST exceed interval: a busy llama-server services /slots and
		// /metrics through the same task queue as token generation, so a poll can
		// wait several seconds under load. If timeout == interval the client cancels
		// mid-decode every tick -> upstream "stop: cancel task" spam + wasted loop
		// iterations. Give the poll room to wait its turn instead.
		client:   &http.Client{Timeout: 10 * time.Second},
		log:      log,
		interval: 2 * time.Second,
	}
}

// run polls until ctx is cancelled. Intended as a goroutine started at boot.
func (m *backendMetricsMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// poll scrapes every running backend in parallel, replaces the cache, and emits
// a snapshot. Backends that stopped since the last tick drop out of the cache.
func (m *backendMetricsMonitor) poll(ctx context.Context) {
	targets := m.running()
	if len(targets) == 0 {
		m.mu.Lock()
		empty := len(m.latest) == 0
		m.latest = map[string]BackendMetrics{}
		m.mu.Unlock()
		if !empty {
			event.Emit(BackendMetricsEvent{Metrics: []BackendMetrics{}})
		}
		return
	}

	var wg sync.WaitGroup
	out := make(map[string]BackendMetrics, len(targets))
	var mu sync.Mutex
	for model, base := range targets {
		wg.Add(1)
		go func(model, base string) {
			defer wg.Done()
			bm := m.scrapeOne(ctx, model, base)
			mu.Lock()
			out[model] = bm
			mu.Unlock()
		}(model, base)
	}
	wg.Wait()

	m.mu.Lock()
	m.latest = out
	m.mu.Unlock()
	event.Emit(BackendMetricsEvent{Metrics: m.snapshot()})
}

// snapshot returns the cached metrics sorted by model id (stable UI ordering).
func (m *backendMetricsMonitor) snapshot() []BackendMetrics {
	if m == nil { // Server literals in tests skip New(); treat as no backends.
		return []BackendMetrics{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BackendMetrics, 0, len(m.latest))
	for _, bm := range m.latest {
		out = append(out, bm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func (m *backendMetricsMonitor) scrapeOne(ctx context.Context, model, base string) BackendMetrics {
	base = strings.TrimRight(base, "/")
	bm := BackendMetrics{Model: model, Timestamp: time.Now()}

	if body, err := m.get(ctx, base+"/metrics"); err == nil {
		parsePrometheusInto(&bm, body)
		bm.OK = true
	} else {
		m.log.Debugf("backend metrics: %s /metrics: %v", model, err)
	}
	// ponytail: /props is static per load but re-scraped each tick — localhost,
	// 1-2 backends, negligible. Cache by model if backend count ever grows large.
	if body, err := m.get(ctx, base+"/props"); err == nil {
		j := gjson.ParseBytes(body)
		bm.NCtx = j.Get("default_generation_settings.n_ctx").Int()
		bm.TotalSlots = j.Get("total_slots").Int()
	}
	// KV-cache fill moved out of /metrics in recent llama.cpp (b9620+ no longer
	// exports llamacpp:kv_cache_tokens / _usage_ratio), so derive it from /slots:
	// per-slot prompt size + tokens decoded so far = current context occupancy.
	if body, err := m.get(ctx, base+"/slots"); err == nil {
		parseSlotsInto(&bm, body)
	}
	return bm
}

// parseSlotsInto reads llama-server's /slots array and fills the KV-cache and
// live prompt-size fields. KV occupancy = max over slots of (prompt tokens +
// tokens decoded so far); n_ctx falls back to the slot value if /props missed.
func parseSlotsInto(bm *BackendMetrics, body []byte) {
	arr := gjson.ParseBytes(body)
	if !arr.IsArray() {
		return
	}
	for _, s := range arr.Array() {
		np := s.Get("n_prompt_tokens").Int()
		used := np
		if s.Get("is_processing").Bool() {
			used += s.Get("next_token.0.n_decoded").Int()
		}
		if bm.NCtx == 0 {
			bm.NCtx = s.Get("n_ctx").Int()
		}
		if used > bm.KVCacheTokens {
			bm.KVCacheTokens = used
			bm.PromptTokens = np
		}
	}
	if bm.NCtx > 0 {
		bm.KVCacheUsageRatio = float64(bm.KVCacheTokens) / float64(bm.NCtx)
	}
}

func (m *backendMetricsMonitor) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// parsePrometheusInto reads the llama-server Prometheus exposition text and
// fills the gauges/counters we surface. Lines are "name value"; comments (#)
// and unknown metrics are ignored. llama-server emits no labels on these.
func parsePrometheusInto(bm *BackendMetrics, body []byte) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		name, valStr, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(valStr), 64)
		if err != nil {
			continue
		}
		switch name {
		case "llamacpp:kv_cache_usage_ratio":
			bm.KVCacheUsageRatio = val
		case "llamacpp:kv_cache_tokens":
			bm.KVCacheTokens = int64(val)
		case "llamacpp:requests_processing":
			bm.RequestsProcessing = int64(val)
		case "llamacpp:requests_deferred":
			bm.RequestsDeferred = int64(val)
		case "llamacpp:prompt_tokens_total":
			bm.PromptTokensTotal = int64(val)
		case "llamacpp:tokens_predicted_total":
			bm.TokensPredictedTotal = int64(val)
		case "llamacpp:n_decode_total":
			bm.NDecodeTotal = int64(val)
		case "llamacpp:prompt_seconds_total":
			bm.PromptSecondsTotal = val
		case "llamacpp:tokens_predicted_seconds_total":
			bm.PredictedSecondsTotal = val
		case "llamacpp:prompt_tokens_seconds":
			bm.PromptTokensSeconds = val
		case "llamacpp:predicted_tokens_seconds":
			bm.PredictedTokensSeconds = val
		}
	}
}
