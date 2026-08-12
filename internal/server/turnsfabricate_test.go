package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The links a model invents are syntactically perfect, so detection has to be
// exact about what a YouTube video link is — and must not fire on prose.
func TestYtLinkIDs(t *testing.T) {
	in := `See https://www.youtube.com/watch?v=dQw4w9WgXcQ and https://youtu.be/oeCpDqPfAbs?t=90
	and [x](https://www.youtube.com/shorts/hAsicFeHmsQ), https://m.youtube.com/watch?list=PL1&v=8eBqvOLSvG0
	dup: https://www.youtube.com/watch?v=dQw4w9WgXcQ
	not a video: https://www.youtube.com/@Im-fable5/videos https://example.com/watch?v=aaaaaaaaaaa`
	got := ytLinkIDs(in)
	want := []string{"dQw4w9WgXcQ", "oeCpDqPfAbs", "hAsicFeHmsQ", "8eBqvOLSvG0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
	if ids := ytLinkIDs("nothing here at all"); len(ids) != 0 {
		t.Errorf("false positives: %v", ids)
	}
}

func TestUnverifiedYtIDs(t *testing.T) {
	seen := map[string]bool{"dQw4w9WgXcQ": true}
	answer := "real https://youtu.be/dQw4w9WgXcQ fake https://www.youtube.com/watch?v=1fZ5Z7Z7Z7Z"
	bad := unverifiedYtIDs(answer, seen)
	if len(bad) != 1 || bad[0] != "1fZ5Z7Z7Z7Z" {
		t.Errorf("got %v, want [1fZ5Z7Z7Z7Z]", bad)
	}
	if bad := unverifiedYtIDs("https://youtu.be/dQw4w9WgXcQ", seen); len(bad) != 0 {
		t.Errorf("verified link flagged: %v", bad)
	}
}

// pastedNewVideo gates a forced tool call, which takes away the model's option
// to answer or to ask — so it must fire ONLY on a link the model has not seen.
func TestPastedNewVideo(t *testing.T) {
	user := func(s string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"role": "user", "content": s})
		return b
	}
	asst := func(s string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"role": "assistant", "content": s})
		return b
	}
	cases := []struct {
		name string
		msgs []json.RawMessage
		want bool
	}{
		{"fresh link", []json.RawMessage{user("what's in https://youtu.be/dQw4w9WgXcQ ?")}, true},
		{"link in vision parts", []json.RawMessage{json.RawMessage(
			`{"role":"user","content":[{"type":"text","text":"see https://www.youtube.com/watch?v=8eBqvOLSvG0"}]}`)}, true},
		// Already covered earlier in the thread — a follow-up question wants the
		// transcript that was read, not a second fetch.
		{"link seen before", []json.RawMessage{
			user("https://youtu.be/dQw4w9WgXcQ"),
			asst("It is about X. https://youtu.be/dQw4w9WgXcQ"),
			user("what did he say about the bridge?"),
		}, false},
		// Naming the site is a topic, not a task.
		{"youtube named only", []json.RawMessage{user("why does youtube compress so hard?")}, false},
		{"find me videos", []json.RawMessage{user("find videos by @imfable5 on youtube")}, false},
		{"no user message", []json.RawMessage{asst("hi")}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := pastedNewVideo(tc.msgs); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLastUserText(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"first"}`),
		json.RawMessage(`{"role":"assistant","content":"reply about youtube"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"look at"},{"type":"image_url"},{"type":"text","text":"this"}]}`),
	}
	if got := strings.TrimSpace(lastUserText(msgs)); got != "look at this" {
		t.Errorf("multipart = %q", got)
	}
	// The assistant's text must never be what gets tested for intent.
	if got := lastUserText(msgs[:2]); got != "first" {
		t.Errorf("string content = %q", got)
	}
	if got := lastUserText(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
}
