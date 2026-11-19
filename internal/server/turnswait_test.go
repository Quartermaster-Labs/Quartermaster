package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The router narrates a pre-stream wait in SSE comment frames. streamSSE drops
// every one of them except the status frame, which is what tells the playground
// the difference between "your model is loading" and "another model has the
// GPU" -- two silences that are otherwise identical from here.
func TestStreamSSE_ReportsRouterWaitStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, ": quartermaster loading model: m\n\n")
		fmt.Fprint(w, ": qm-status: loading\n\n")
		fmt.Fprint(w, ": qm-status: waiting 2\n\n")
		fmt.Fprint(w, ": stretching its legs\n\n") // narration, not status
		fmt.Fprint(w, ": qm-status: loading\n\n")
		f.Flush()
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer upstream.Close()

	tm := newTurnTestManager(t)
	tm.pg.SelfBase = upstream.URL

	var states []string
	var positions []int
	var content string
	finish, err := tm.streamSSE(context.Background(), map[string]any{"model": "m"}, "c1", "",
		func(s string) { content += s }, nil, nil, nil,
		func(state string, pos int) {
			states = append(states, state)
			positions = append(positions, pos)
		}, nil)
	if err != nil {
		t.Fatalf("streamSSE: %v", err)
	}
	if finish != "stop" || content != "hi" {
		t.Fatalf("finish=%q content=%q", finish, content)
	}
	// The trailing "" is the first data frame: a token ends any wait, and
	// without it the label would stay up under the streaming answer.
	want := []string{"loading", "waiting", "loading", ""}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Errorf("states=%v want %v", states, want)
	}
	if positions[1] != 2 {
		t.Errorf("queue position=%d want 2", positions[1])
	}
}

func TestParseWaitStatus(t *testing.T) {
	cases := []struct {
		in    string
		state string
		pos   int
	}{
		{"waiting 2", "waiting", 2},
		{"loading", "loading", 0},
		{"waiting", "waiting", 0},
		{"waiting x", "waiting", 0}, // a malformed position is not a state error
		{"", "", 0},
	}
	for _, c := range cases {
		state, pos := parseWaitStatus(c.in)
		if state != c.state || pos != c.pos {
			t.Errorf("parseWaitStatus(%q) = %q,%d want %q,%d", c.in, state, pos, c.state, c.pos)
		}
	}
}

// A progress frame comes as prompt processing STARTS and carries no token: it
// must reach onPrefill with the cached prefix taken out, and must not count as
// the first token, or the label naming what is being prefilled would clear
// before any of it was.
func TestStreamSSE_PromptProgress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["return_progress"] != true {
			t.Errorf("request did not ask for return_progress: %v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, p := range []int{1000, 3048, 5000} {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"role":"assistant","content":null}}],"prompt_progress":{"total":5000,"cache":1000,"processed":%d,"time_ms":1}}`+"\n\n", p)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer upstream.Close()

	tm := newTurnTestManager(t)
	tm.pg.SelfBase = upstream.URL

	var events []string
	var content string
	_, err := tm.streamSSE(context.Background(), map[string]any{"model": "m"}, "c1", "",
		func(s string) { content += s }, nil, nil, nil,
		func(state string, _ int) { events = append(events, "status:"+state) },
		func(done, total int) { events = append(events, fmt.Sprintf("prefill:%d/%d", done, total)) })
	if err != nil {
		t.Fatalf("streamSSE: %v", err)
	}
	want := []string{"prefill:0/4000", "prefill:2048/4000", "prefill:4000/4000", "status:"}
	if fmt.Sprint(events) != fmt.Sprint(want) || content != "hi" {
		t.Errorf("events=%v content=%q, want %v and %q", events, content, want, "hi")
	}
}

// A round that llama-server has queued behind another request gets no frame at
// all until a slot takes it. With the slots full, the watch labels that wait;
// the first prompt_progress frame (the slot picking the request up) ends it.
// With a free slot the same silence is something else, and stays unlabelled.
func TestStreamSSE_QueuedInsideBackend(t *testing.T) {
	defer func(d, p time.Duration) { queueLabelDelay, queuePollEvery = d, p }(queueLabelDelay, queuePollEvery)
	queueLabelDelay, queuePollEvery = 20*time.Millisecond, 10*time.Millisecond

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond) // queued behind another request
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":null}}],"prompt_progress":{"total":3000,"cache":0,"processed":0,"time_ms":0}}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	for _, full := range []bool{true, false} {
		tm := newTurnTestManager(t)
		tm.pg.SelfBase = upstream.URL
		hook := func(model string) bool { return model == "m" && full }
		tm.slotsFull.Store(&hook)

		var mu sync.Mutex
		var events []string
		add := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
		_, err := tm.streamSSE(context.Background(), map[string]any{"model": "m"}, "c1", "",
			func(string) {}, nil, nil, nil,
			func(state string, _ int) { add("status:" + state) },
			func(done, total int) { add(fmt.Sprintf("prefill:%d/%d", done, total)) })
		if err != nil {
			t.Fatalf("streamSSE: %v", err)
		}
		want := []string{"prefill:0/3000", "status:"}
		if full {
			want = append([]string{"status:waiting"}, want...)
		}
		mu.Lock()
		got := fmt.Sprint(events)
		mu.Unlock()
		if got != fmt.Sprint(want) {
			t.Errorf("slots full=%v: events=%v, want %v", full, got, want)
		}
	}
}
