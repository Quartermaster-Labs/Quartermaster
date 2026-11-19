package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const digestOverflowErr = `upstream 400 Bad Request: {"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error","n_prompt_tokens":180123,"n_ctx":100000}}`

func TestDigest_SplitChunks(t *testing.T) {
	prose := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 60)
	cases := []struct {
		name string
		s    string
		size int
	}{
		{"empty", "", 50},
		{"shorter than size", "short text", 50},
		{"prose small window", prose, 60},
		{"prose medium window", prose, 400},
		{"prose odd size", prose, 137},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pieces := splitChunks(tc.s, tc.size)
			if got := strings.Join(pieces, ""); got != tc.s {
				t.Fatalf("concatenated pieces != input (got %d bytes, want %d)", len(got), len(tc.s))
			}
			for i, p := range pieces {
				if len(p) > tc.size {
					t.Errorf("piece %d is %d bytes, over size %d", i, len(p), tc.size)
				}
				if i < len(pieces)-1 && !(strings.HasSuffix(p, " ") || strings.HasSuffix(p, "\n")) {
					t.Errorf("piece %d ends mid-word: %q", i, p)
				}
			}
		})
	}
}

func TestDigest_SplitChunksParagraphPreferred(t *testing.T) {
	// Window for limit 100 is bytes [60,100). A paragraph break at 70 sits in it,
	// and a single space at 91 comes later. The paragraph break must win.
	s := strings.Repeat("a", 70) + "\n\n" + strings.Repeat("b", 19) + " " + strings.Repeat("c", 200)
	pieces := splitChunks(s, 100)
	if len(pieces) == 0 {
		t.Fatal("no pieces")
	}
	if want := s[:72]; pieces[0] != want {
		t.Errorf("first piece = %d bytes, want %d (cut after the paragraph break)", len(pieces[0]), len(want))
	}
}

func TestDigest_SplitChunksMultibyte(t *testing.T) {
	s := strings.Repeat("日本語", 500)
	for _, size := range []int{100, 250, 1000} {
		pieces := splitChunks(s, size)
		if got := strings.Join(pieces, ""); got != s {
			t.Fatalf("size %d: concatenated pieces != input", size)
		}
		for i, p := range pieces {
			if len(p) > size {
				t.Errorf("size %d: piece %d is %d bytes", size, i, len(p))
			}
			if !utf8.ValidString(p) {
				t.Errorf("size %d: piece %d is invalid UTF-8", size, i)
			}
		}
	}
}

func TestDigest_TailCut(t *testing.T) {
	if got := tailCut("abc", 3); got != 0 {
		t.Errorf("tailCut(size == len) = %d, want 0", got)
	}
	if got := tailCut("abc", 10); got != 0 {
		t.Errorf("tailCut(size > len) = %d, want 0", got)
	}

	prose := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 60)
	for _, size := range []int{20, 50, 100, 400} {
		idx := tailCut(prose, size)
		if idx <= 0 || idx > len(prose) {
			t.Fatalf("size %d: tailCut = %d, out of range", size, idx)
		}
		if n := len(prose) - idx; n > size {
			t.Errorf("size %d: tail is %d bytes, over size", size, n)
		}
		if b := prose[idx-1]; b != ' ' && b != '\n' {
			t.Errorf("size %d: tail opens mid-word at %d: %q", size, idx, prose[idx:min(len(prose), idx+10)])
		}
	}
}

func TestDigest_CutAt(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		limit int
		want  int
	}{
		{"limit above length", "hello", 100, 5},
		{"limit equals length", "hello", 5, 5},
		{"cuts after a space", "hello world foo bar baz", 12, 12},
		{"no separator backs off to rune start", strings.Repeat("日", 10), 10, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutAt(tc.s, tc.limit); got != tc.want {
				t.Errorf("cutAt(%q, %d) = %d, want %d", tc.s, tc.limit, got, tc.want)
			}
		})
	}
}

func TestDigest_PullTasks(t *testing.T) {
	cases := []struct {
		name      string
		out       string
		wantTasks []string
		wantNote  string
	}{
		{"no tags", "Just notes.", nil, "Just notes."},
		{"single task", "Notes.\n<task>Summarise it</task>\nMore.", []string{"Summarise it"}, "Notes.\n\nMore."},
		{"multi-line task", "<task>Line one\nline two</task>\nNotes", []string{"Line one\nline two"}, "Notes"},
		{"case-insensitive tags", "<TASK>Upper</TASK> and <Task>mixed</Task>", []string{"Upper", "mixed"}, "and"},
		{"NONE skipped", "<task>NONE</task>\nNotes", nil, "Notes"},
		{"lowercase none skipped", "<task>none</task>Notes", nil, "Notes"},
		{"NONE beside a real task", "<task>NONE</task><task>Real</task>", []string{"Real"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks, note := pullTasks(tc.out)
			if len(tasks) != len(tc.wantTasks) {
				t.Fatalf("tasks = %q, want %q", tasks, tc.wantTasks)
			}
			for i := range tasks {
				if tasks[i] != tc.wantTasks[i] {
					t.Errorf("task %d = %q, want %q", i, tasks[i], tc.wantTasks[i])
				}
			}
			if note != tc.wantNote {
				t.Errorf("note = %q, want %q", note, tc.wantNote)
			}
		})
	}
}

func TestDigest_FirstTask(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"NONE", "<task>NONE</task>", ""},
		{"lowercase none", "<task>none</task>", ""},
		{"real task", "<task>Summarise the report</task>", "Summarise the report"},
		{"first real of several", "<task>A</task>\n<task>B</task>", "A"},
		{"NONE then real", "<task>NONE</task>\n<task>B</task>", "B"},
		{"no tags", "nothing here", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstTask(tc.out); got != tc.want {
				t.Errorf("firstTask(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

func TestDigest_ContextOverflow(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantOver   bool
		wantTokens int
	}{
		{"llama-server overflow", errors.New(digestOverflowErr), true, 180123},
		{"phrase only", errors.New("the request exceeds the available context size"), true, 0},
		{"nil", nil, false, 0},
		{"unrelated", errors.New("connection refused"), false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextOverflow(tc.err); got != tc.wantOver {
				t.Errorf("isContextOverflow = %v, want %v", got, tc.wantOver)
			}
			if got := overflowPromptTokens(tc.err); got != tc.wantTokens {
				t.Errorf("overflowPromptTokens = %d, want %d", got, tc.wantTokens)
			}
		})
	}
}

func TestDigest_SplitJoinContent(t *testing.T) {
	t.Run("string round-trips", func(t *testing.T) {
		text, media := splitContent("hello world")
		if text != "hello world" || len(media) != 0 {
			t.Fatalf("splitContent = (%q, %d media), want (\"hello world\", 0)", text, len(media))
		}
		got, ok := joinContent(text, media).(string)
		if !ok || got != "hello world" {
			t.Errorf("joinContent = %v, want \"hello world\"", got)
		}
	})

	t.Run("array content", func(t *testing.T) {
		const raw = `[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"b"}]`
		var content any
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			t.Fatal(err)
		}
		text, media := splitContent(content)
		if text != "a\n\nb" {
			t.Errorf("text = %q, want %q", text, "a\n\nb")
		}
		if len(media) != 1 {
			t.Fatalf("media parts = %d, want 1", len(media))
		}
		if mm, ok := media[0].(map[string]any); !ok || mm["type"] != "image_url" {
			t.Errorf("media[0] = %v, want the image_url part", media[0])
		}

		joined, ok := joinContent("z", media).([]any)
		if !ok || len(joined) != 2 {
			t.Fatalf("joinContent = %v, want a 2-element []any", joinContent("z", media))
		}
		first, ok := joined[0].(map[string]any)
		if !ok || first["type"] != "text" || first["text"] != "z" {
			t.Errorf("joined[0] = %v, want {type:text, text:z}", joined[0])
		}
	})
}

func TestDigest_GroupNotes(t *testing.T) {
	var notes []string
	for i := 0; i < 40; i++ {
		notes = append(notes, fmt.Sprintf("note %d %s", i, strings.Repeat("x", i*7%50)))
	}
	withBig := append(append([]string{}, notes[:20]...), strings.Repeat("y", 500))
	withBig = append(withBig, notes[20:]...)

	cases := []struct {
		name  string
		notes []string
		size  int
	}{
		{"empty", nil, 100},
		{"small groups", notes, 60},
		{"large groups", notes, 400},
		{"oversized note in the middle", withBig, 200},
		{"everything fits one group", notes, 100000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := groupNotes(tc.notes, tc.size)

			var flat []string
			for gi, g := range groups {
				parts := strings.Split(g, "\n\n")
				if len(parts) > 1 && len(g) > tc.size {
					t.Errorf("group %d is %d bytes with %d notes, over size %d", gi, len(g), len(parts), tc.size)
				}
				flat = append(flat, parts...)
			}
			if len(flat) != len(tc.notes) {
				t.Fatalf("flattened %d notes, want %d", len(flat), len(tc.notes))
			}
			for i := range flat {
				if flat[i] != tc.notes[i] {
					t.Errorf("note %d out of order or altered", i)
					break
				}
			}
		})
	}
}

func TestDigest_LastUserIndex(t *testing.T) {
	cases := []struct {
		name string
		msgs []json.RawMessage
		want int
	}{
		{"last user at 3", []json.RawMessage{
			json.RawMessage(`{"role":"system","content":"s"}`),
			json.RawMessage(`{"role":"user","content":"u1"}`),
			json.RawMessage(`{"role":"assistant","content":"a1"}`),
			json.RawMessage(`{"role":"user","content":"u2"}`),
			json.RawMessage(`{"role":"assistant","content":"a2"}`),
		}, 3},
		{"no user message", []json.RawMessage{
			json.RawMessage(`{"role":"system","content":"s"}`),
			json.RawMessage(`{"role":"assistant","content":"a"}`),
		}, -1},
		{"empty", nil, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastUserIndex(tc.msgs); got != tc.want {
				t.Errorf("lastUserIndex = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDigest_BuildDigest(t *testing.T) {
	notes := []string{"first note", "second note"}
	const tail = "the last words"

	t.Run("task and part-labelled notes", func(t *testing.T) {
		out := buildDigest(182000, 100000, 2, "Summarise the report", notes, false, tail)
		for _, want := range []string{
			"Request found in the message: Summarise the report",
			`<notes part="1" of="2">`,
			`<notes part="2" of="2">`,
			"first note",
			"second note",
			"<end_of_message>\n" + tail + "\n</end_of_message>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("digest missing %q", want)
			}
		}
		if strings.Contains(out, "No explicit request") {
			t.Error("digest claims no request although a task was given")
		}
		if !strings.HasSuffix(out, "</end_of_message>") {
			t.Error("digest does not end with </end_of_message>")
		}
	})

	t.Run("merged notes use section labels", func(t *testing.T) {
		out := buildDigest(182000, 100000, 2, "Summarise the report", notes, true, tail)
		if !strings.Contains(out, `<notes section="1" of="2">`) || !strings.Contains(out, `<notes section="2" of="2">`) {
			t.Error("merged digest missing section= labels")
		}
		if strings.Contains(out, "<notes part=") {
			t.Error("merged digest still uses part= labels")
		}
	})

	t.Run("no task", func(t *testing.T) {
		out := buildDigest(182000, 100000, 2, "", notes, false, tail)
		if !strings.Contains(out, "No explicit request") {
			t.Error("digest with empty task missing the no-request line")
		}
		if strings.Contains(out, "Request found in the message: ") {
			t.Error("digest with empty task still names a request")
		}
		if !strings.HasSuffix(out, "</end_of_message>") || !strings.Contains(out, tail) {
			t.Error("digest does not carry the tail inside </end_of_message>")
		}
	})
}

// Isabella's book on a 151k window: ~115k tokens of note room over parts of
// ~68k. The old flat 3000-token cap used 12k of it. Now each note may have a
// sixth of its part, and the hard stop leaves room for the model's overrun.
func TestDigest_NoteSizing(t *testing.T) {
	const room, chunk = 115000, 67700
	noteCap := chunk / digestMinCompression
	if got := noteTarget(room, 4, noteCap); got != noteCap {
		t.Errorf("4 parts: target %d, want the part ceiling %d", got, noteCap)
	}
	// Many parts share the room; notes that all overrun to the hard stop
	// still fit it, so an overrun never forces a merge pass.
	for _, n := range []int{9, 16, 40} {
		target := noteTarget(room, n, noteCap)
		if total := n * noteMaxTokens(target); target > digestMinNoteTokens && total > room {
			t.Errorf("%d parts: %d notes at max_tokens %d = %d, over the room %d", n, n, noteMaxTokens(target), total, room)
		}
	}
	if got := noteTarget(1000, 40, noteCap); got != digestMinNoteTokens {
		t.Errorf("starved room: target %d, want the floor %d", got, digestMinNoteTokens)
	}
	// One call holds a part and its longest note inside the window.
	if part, note := chunk, noteMaxTokens(noteCap); float64(part+note) > float64(chunk)/digestChunkShare {
		t.Errorf("part %d + note %d overflows the window", part, note)
	}
}
