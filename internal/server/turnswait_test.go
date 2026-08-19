package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
		})
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
