package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// loopUpstream is a model stuck in a rut: every round it makes a tool call
// (the same one, or a new one each round when vary is set) until a request
// arrives with tool_choice "none", which it answers, or ignores when stubborn.
func loopUpstream(t *testing.T, vary, stubborn bool) (*httptest.Server, func() (rounds int, choices []string)) {
	var mu sync.Mutex
	rounds := 0
	var choices []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		choice, _ := body["tool_choice"].(string)
		mu.Lock()
		rounds++
		n := rounds
		choices = append(choices, choice)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if choice == "none" && !stubborn {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"camp 17"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		expr := "143+17"
		if vary {
			expr = fmt.Sprintf("%d+1", n)
		}
		args, _ := json.Marshal(map[string]string{"expression": expr})
		call, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("c%d", n), "type": "function",
				"function": map[string]any{"name": "calculate", "arguments": string(args)}}},
		}, "finish_reason": "tool_calls"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", call)
	}))
	t.Cleanup(srv.Close)
	return srv, func() (int, []string) { mu.Lock(); defer mu.Unlock(); return rounds, append([]string(nil), choices...) }
}

func runLoopAgainst(t *testing.T, url string) (*activeTurn, error) {
	t.Helper()
	tm := newTurnTestManager(t)
	tm.pg.SelfBase = url
	at := &activeTurn{user: "u", chatID: "c1"}
	start := turnStart{
		ChatID:   "c1",
		Model:    "m",
		Messages: json.RawMessage(`[{"role":"user","content":"which camp appears most often?"}]`),
		Tools:    json.RawMessage(`[{"type":"function","function":{"name":"calculate","parameters":{}}}]`),
	}
	return at, tm.runLoop(context.Background(), at, start)
}

// 2026-11-19: a model re-issued one calculate call about a hundred times; the
// repeat guard answered each from cache and the loop never ended. Two rounds of
// nothing but repeats now take the tools away, and the model answers.
func TestRunLoop_RepeatedCallRutEnds(t *testing.T) {
	srv, stats := loopUpstream(t, false, false)
	at, err := runLoopAgainst(t, srv.URL)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	rounds, choices := stats()
	// Round 1 runs the call, rounds 2-3 repeat it, round 4 has no tools.
	if rounds != 2+turnMaxDupRounds {
		t.Errorf("rounds = %d, want %d (choices %q)", rounds, 2+turnMaxDupRounds, choices)
	}
	if last := choices[len(choices)-1]; last != "none" {
		t.Errorf("final round tool_choice = %q, want none", last)
	}
	if got := at.answerText(); !strings.Contains(got, "camp 17") {
		t.Errorf("answer = %q, want the model's answer", got)
	}
}

// New calls each round never trip the repeat check; the round cap does.
func TestRunLoop_RoundCapEndsVaryingCalls(t *testing.T) {
	srv, stats := loopUpstream(t, true, false)
	if _, err := runLoopAgainst(t, srv.URL); err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if rounds, _ := stats(); rounds != turnMaxRounds+1 {
		t.Errorf("rounds = %d, want %d", rounds, turnMaxRounds+1)
	}
}

// A model that calls a tool even with tool_choice "none" gets no more rounds.
func TestRunLoop_ToolCallAfterToolsOffEndsTurn(t *testing.T) {
	srv, stats := loopUpstream(t, false, true)
	at, err := runLoopAgainst(t, srv.URL)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	if rounds, _ := stats(); rounds != 2+turnMaxDupRounds {
		t.Errorf("rounds = %d, want %d", rounds, 2+turnMaxDupRounds)
	}
	if got := at.answerText(); !strings.Contains(got, toolLoopMarker) {
		t.Errorf("answer = %q, want the stop marker", got)
	}
}
