package server

import "testing"

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
