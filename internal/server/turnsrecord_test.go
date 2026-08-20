package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func recTail() ([]json.RawMessage, []string) {
	tail := []json.RawMessage{
		mustJSON(map[string]any{
			"role": "assistant", "content": "Let me read the docs first.\n\n",
			"tool_calls": []any{map[string]any{
				"id": "d8ZR8LqgLQhS", "type": "function",
				"function": map[string]any{"name": "fetch_page", "arguments": `{"url":"https://example.com/docs"}`},
			}},
		}),
		mustJSON(map[string]any{
			"role": "tool", "tool_call_id": "d8ZR8LqgLQhS",
			"content": "## [1] Docs\nfull page body, all 13k of it\n\nWhen you use any of the above in your answer, cite it inline",
		}),
	}
	return tail, []string{"Let me read the docs first.\n\n"}
}

func recSearches() []turnSearch {
	return []turnSearch{{
		Query: "Docs", Results: "## [1] Docs\nfull page body, all 13k of it", Kind: "page",
		Sources: []turnSource{{Title: "Docs", URL: "https://example.com/docs"}},
	}}
}

// The turn a client sends back must be replayed with the bytes that turn was
// actually served with — anything rebuilt differs from what upstream holds and
// rewrites the prompt prefix retroactively, costing the conversation its KV.
func TestTurnRecordReplaysVerbatim(t *testing.T) {
	tm := &turnManager{replays: newReplayStore()}
	tail, spoken := recTail()
	tm.recordTurn(&activeTurn{searches: recSearches()}, "chat-1", tail, spoken)

	in := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"what are these?"}`),
		mustJSON(map[string]any{
			"role":     "assistant",
			"content":  "Let me read the docs first.\n\n\n\n<think>weighing it up</think>They are the docs. [1]",
			"searches": recSearches(),
		}),
		json.RawMessage(`{"role":"user","content":"and the rest?"}`),
	}
	got := replayToolCalls(in, tm.replayLookup("chat-1"))
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5: %s", len(got), got)
	}
	for i, want := range tail {
		if string(got[1+i]) != string(want) {
			t.Errorf("message %d not verbatim:\n got %s\nwant %s", 1+i, got[1+i], want)
		}
	}
	// The prose the recorded tool_calls message already carries is taken off the
	// front of the client's one stored answer, so it is not sent twice.
	var ans struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(got[3], &ans); err != nil {
		t.Fatalf("bad answer message: %v", err)
	}
	if ans.Role != "assistant" || ans.Content != "<think>weighing it up</think>They are the docs. [1]" {
		t.Errorf("answer message = %+v", ans)
	}
	if string(got[4]) != string(in[2]) {
		t.Errorf("trailing user message altered: %s", got[4])
	}
}

// A miss — different chat, edited history, a record evicted or lost to a restart
// — must still produce a usable history: the rebuild, i.e. today's behaviour.
func TestTurnRecordFallsBackToRebuild(t *testing.T) {
	tm := &turnManager{replays: newReplayStore()}
	tail, spoken := recTail()
	tm.recordTurn(&activeTurn{searches: recSearches()}, "chat-1", tail, spoken)

	in := []json.RawMessage{
		mustJSON(map[string]any{"role": "assistant", "content": "They are the docs. [1]", "searches": recSearches()}),
	}
	for _, chat := range []string{"chat-2", "chat-1"} {
		got := msgsOf(t, replayToolCalls(in, tm.replayLookup(chat)))
		if len(got) != 3 {
			t.Fatalf("%s: got %d messages, want 3", chat, len(got))
		}
		rebuilt := strings.Contains(got[1]["content"].(string), replayNote)
		if want := chat == "chat-2"; rebuilt != want {
			t.Errorf("%s: rebuilt = %v, want %v (%v)", chat, rebuilt, want, got[1])
		}
	}
}

// The key is the turn's own search metadata, not its position: compaction and
// edits shift indices, and a shifted index would splice one turn's results under
// another turn's answer.
func TestReplayRecordKey(t *testing.T) {
	s := recSearches()
	if replayRecordKey("chat-1", s) != replayRecordKey("chat-1", recSearches()) {
		t.Errorf("key not stable for identical searches")
	}
	if replayRecordKey("chat-1", s) == replayRecordKey("chat-2", s) {
		t.Errorf("key ignores the chat id")
	}
	if replayRecordKey("chat-1", nil) != "" {
		t.Errorf("a turn with no searches must have no key")
	}
	edited := recSearches()
	edited[0].Results += " (edited)"
	if replayRecordKey("chat-1", s) == replayRecordKey("chat-1", edited) {
		t.Errorf("key survived a changed result")
	}
}

func TestReplayStoreBounds(t *testing.T) {
	s := newReplayStore()
	s.maxEntries = 2
	rec := func(n int) *turnRecord {
		return &turnRecord{msgs: []json.RawMessage{json.RawMessage(`"x"`)}, size: n}
	}
	s.put("a", rec(10))
	s.put("b", rec(10))
	if s.get("a") == nil { // refreshes a's recency, so c evicts b
		t.Fatalf("a evicted early")
	}
	s.put("c", rec(10))
	if s.get("b") != nil {
		t.Errorf("LRU kept the least recently used entry")
	}
	if s.get("a") == nil || s.get("c") == nil {
		t.Errorf("LRU dropped a live entry")
	}

	// One oversized turn must not evict the whole store to store itself.
	s.maxBytes = 100
	s.put("huge", rec(1000))
	if s.get("huge") != nil {
		t.Errorf("oversized record stored")
	}
	if s.get("c") == nil {
		t.Errorf("oversized record evicted a live entry")
	}
}

// A turn with nothing to replay must not take a slot in the store.
func TestTurnRecordSkipsEmpty(t *testing.T) {
	tm := &turnManager{replays: newReplayStore()}
	tail, spoken := recTail()
	tm.recordTurn(&activeTurn{}, "chat-1", tail, spoken) // no searches → no key
	tm.recordTurn(&activeTurn{searches: recSearches()}, "chat-1", nil, nil)
	if n := len(tm.replays.items); n != 0 {
		t.Errorf("store holds %d records, want 0", n)
	}
}
