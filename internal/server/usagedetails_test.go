package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// serveThrough runs body through the usage-details middleware with the given
// response headers and status, and returns the recorder.
func serveThrough(t *testing.T, status int, header map[string]string, writes ...string) *httptest.ResponseRecorder {
	t.Helper()
	h := CreateUsageDetailsMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		for _, chunk := range writes {
			if _, err := w.Write([]byte(chunk)); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	return rec
}

func TestUsageDetails_SingleShotFillsCachedAndReasoning(t *testing.T) {
	body := `{"choices":[{"message":{"reasoning_content":"aaaa","content":"bbbb"}}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120},` +
		`"timings":{"cache_n":80,"prompt_n":100,"predicted_n":20}}`

	rec := serveThrough(t, http.StatusOK, map[string]string{"Content-Type": "application/json"}, body)

	got := gjson.ParseBytes(rec.Body.Bytes())
	if v := got.Get("usage.prompt_tokens_details.cached_tokens"); v.Int() != 80 {
		t.Errorf("cached_tokens: want 80, got %v", v.Raw)
	}
	// Equal halves of reasoning and content text split the 20 output tokens.
	if v := got.Get("usage.completion_tokens_details.reasoning_tokens"); v.Int() != 10 {
		t.Errorf("reasoning_tokens: want 10, got %v", v.Raw)
	}
	if !got.Get("usage.completion_tokens_details.reasoning_tokens_estimated").Bool() {
		t.Error("estimated reasoning_tokens must carry the reasoning_tokens_estimated label")
	}
	if got.Get("usage.prompt_tokens").Int() != 100 || got.Get("usage.completion_tokens").Int() != 20 {
		t.Error("existing usage fields must survive the rewrite")
	}

	cl := rec.Result().Header.Get("Content-Length")
	if want := strconv.Itoa(rec.Body.Len()); cl != want {
		t.Errorf("Content-Length: want %s, got %s", want, cl)
	}
}

func TestUsageDetails_StreamingEnrichesFinalChunkOnly(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"reasoning_content":"think"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"answer"}}]}` + "\n\n",
		`data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10},` +
			`"timings":{"cache_n":40,"predicted_n":10}}` + "\n\n",
		"data: [DONE]\n\n",
	}

	rec := serveThrough(t, http.StatusOK, map[string]string{"Content-Type": "text/event-stream"}, chunks...)
	out := rec.Body.String()

	// Frames without a usage object pass through byte for byte.
	for _, c := range []string{chunks[0], chunks[1], chunks[3]} {
		if !strings.Contains(out, c) {
			t.Errorf("frame altered in flight: %q\nstream: %s", c, out)
		}
	}

	var usage gjson.Result
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"usage"`) {
			usage = gjson.Parse(strings.TrimPrefix(line, "data: ")).Get("usage")
		}
	}
	if !usage.Exists() {
		t.Fatalf("no usage frame in stream: %s", out)
	}
	if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Int() != 40 {
		t.Errorf("cached_tokens: want 40, got %v", v.Raw)
	}
	// "think" (5) vs "answer" (6) runes over 10 output tokens.
	if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Int() != 5 {
		t.Errorf("reasoning_tokens: want 5, got %v", v.Raw)
	}
	if !usage.Get("completion_tokens_details.reasoning_tokens_estimated").Bool() {
		t.Error("missing reasoning_tokens_estimated label")
	}
}

func TestUsageDetails_LeavesUpstreamValuesAlone(t *testing.T) {
	body := `{"choices":[{"message":{"reasoning_content":"aaaa","content":"bbbb"}}],` +
		`"usage":{"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":7},` +
		`"completion_tokens_details":{"reasoning_tokens":3}},` +
		`"timings":{"cache_n":80,"predicted_n":20}}`

	rec := serveThrough(t, http.StatusOK, map[string]string{"Content-Type": "application/json"}, body)

	got := gjson.ParseBytes(rec.Body.Bytes())
	if v := got.Get("usage.prompt_tokens_details.cached_tokens"); v.Int() != 7 {
		t.Errorf("upstream cached_tokens overwritten: %v", v.Raw)
	}
	if v := got.Get("usage.completion_tokens_details.reasoning_tokens"); v.Int() != 3 {
		t.Errorf("upstream reasoning_tokens overwritten: %v", v.Raw)
	}
	if got.Get("usage.completion_tokens_details.reasoning_tokens_estimated").Exists() {
		t.Error("upstream-reported reasoning tokens must not be labelled an estimate")
	}
}

func TestUsageDetails_NoUsageObjectIsByteIdentical(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hi"}}],"timings":{"cache_n":40,"predicted_n":10}}`
	rec := serveThrough(t, http.StatusOK, map[string]string{"Content-Type": "application/json"}, body)
	if rec.Body.String() != body {
		t.Errorf("body altered: %s", rec.Body.String())
	}
}

func TestUsageDetails_PassthroughCases(t *testing.T) {
	body := `{"usage":{"completion_tokens":9},"timings":{"cache_n":40}}`

	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
	}{
		{"error status", http.StatusInternalServerError, map[string]string{"Content-Type": "application/json"}, body},
		{"compressed", http.StatusOK, map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip"}, body},
		{"not json", http.StatusOK, map[string]string{"Content-Type": "text/plain"}, body},
		{"invalid json", http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"usage":`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveThrough(t, tc.status, tc.header, tc.body)
			if rec.Body.String() != tc.body {
				t.Errorf("body altered: %s", rec.Body.String())
			}
			if rec.Code != tc.status {
				t.Errorf("status: want %d, got %d", tc.status, rec.Code)
			}
		})
	}
}

func TestSplitReasoningTokens(t *testing.T) {
	cases := []struct {
		total                     int64
		reasonChars, contentChars int
		want                      int64
	}{
		{20, 10, 10, 10},
		{20, 0, 10, 0},    // no reasoning text, no estimate
		{0, 10, 10, 0},    // no output tokens to split
		{10, 100, 0, 10},  // all reasoning, capped at the total
		{10, 1, 10000, 1}, // rounds to nothing, floored to 1: reasoning happened
	}
	for _, tc := range cases {
		if got := splitReasoningTokens(tc.total, tc.reasonChars, tc.contentChars); got != tc.want {
			t.Errorf("splitReasoningTokens(%d, %d, %d) = %d, want %d",
				tc.total, tc.reasonChars, tc.contentChars, got, tc.want)
		}
	}
}
