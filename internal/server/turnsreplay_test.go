package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func msgsOf(t *testing.T, out []json.RawMessage) []map[string]any {
	t.Helper()
	var got []map[string]any
	for _, raw := range out {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("bad message %s: %v", raw, err)
		}
		got = append(got, m)
	}
	return got
}

func TestReplayToolCalls(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":"sys"}`),
		json.RawMessage(`{"role":"user","content":"watch these"}`),
		mustJSON(map[string]any{
			"role":    "assistant",
			"content": "Here is what they say. [1]",
			"searches": []turnSearch{
				{Query: "I Live in Time Full of Holes", Results: "transcript body", Kind: "youtube",
					Sources: []turnSource{{Title: "I Live in Time Full of Holes", URL: "https://www.youtube.com/watch?v=S5piV9AcfQg"}}},
				{Query: "@Im-fable5", Results: "5 videos", Kind: "youtube-search"},
			},
		}),
		json.RawMessage(`{"role":"user","content":"and the rest?"}`),
	}
	got := msgsOf(t, replayToolCalls(in))
	if len(got) != 7 {
		t.Fatalf("got %d messages, want 7: %v", len(got), got)
	}
	// Untouched messages pass through unchanged.
	if got[0]["content"] != "sys" || got[1]["content"] != "watch these" || got[6]["content"] != "and the rest?" {
		t.Errorf("passthrough messages altered: %v", got)
	}
	calls, _ := got[2]["tool_calls"].([]any)
	if got[2]["role"] != "assistant" || len(calls) != 2 {
		t.Fatalf("call block = %v", got[2])
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "youtube_transcript" {
		t.Errorf("name = %v", fn["name"])
	}
	// The stored Query is the resolved TITLE — replaying it as a url would teach
	// the model to call fetch tools with titles.
	if !strings.Contains(fn["arguments"].(string), "watch?v=S5piV9AcfQg") {
		t.Errorf("arguments = %v, want the source URL", fn["arguments"])
	}
	fn2 := calls[1].(map[string]any)["function"].(map[string]any)
	if fn2["name"] != "youtube_search" || !strings.Contains(fn2["arguments"].(string), "Im-fable5") {
		t.Errorf("search call = %v", fn2)
	}
	// Results come back as tool messages, keyed to the call ids, and the answer
	// the model actually wrote follows them.
	for i, want := range []string{"transcript body", "5 videos"} {
		m := got[3+i]
		if m["role"] != "tool" || !strings.Contains(m["content"].(string), want) {
			t.Errorf("tool message %d = %v", i, m)
		}
		if m["tool_call_id"] != calls[i].(map[string]any)["id"] {
			t.Errorf("tool_call_id mismatch at %d", i)
		}
	}
	if got[5]["role"] != "assistant" || got[5]["content"] != "Here is what they say. [1]" {
		t.Errorf("answer message = %v", got[5])
	}
}

func TestReplayToolCallsPassthrough(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":"no tools here"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"parts"}]}`),
		// A quartermaster config action is not reference data and the kind does
		// not say which of the two QM tools ran — replaying it would put a call
		// in the history that never happened.
		mustJSON(map[string]any{
			"role": "assistant", "content": "changed the ctx",
			"searches": []turnSearch{{Query: "ctx", Results: "ok", Kind: "quartermaster"}},
		}),
	}
	out := replayToolCalls(in)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3", len(out))
	}
	for i := range in {
		if string(out[i]) != string(in[i]) {
			t.Errorf("message %d rewritten:\n got %s\nwant %s", i, out[i], in[i])
		}
	}
}

// A whole-history budget would re-trim old results as new turns spent it,
// changing the prefix and voiding the KV cache every message. Truncation is
// per-result and fixed, so the same history always renders the same bytes.
func TestTruncateResult(t *testing.T) {
	short := "line one\nline two"
	if truncateResult(short) != short {
		t.Errorf("short result altered")
	}
	long := strings.Repeat("a line of transcript text\n", 1000)
	cut := truncateResult(long)
	if len(cut) > replayResultMax+200 {
		t.Errorf("len = %d, want <= %d+note", len(cut), replayResultMax)
	}
	if !strings.Contains(cut, "truncated") {
		t.Errorf("truncation not announced: %q", cut[len(cut)-80:])
	}
	if cut != truncateResult(long) {
		t.Errorf("not deterministic")
	}
}
