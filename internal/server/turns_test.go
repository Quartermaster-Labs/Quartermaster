package server

import "testing"

// A turn that thinks before it speaks keeps the field-based reasoning box; one
// that speaks first gets its later thinking inlined into content, in order.
func TestActiveTurn_ReasoningOrdering(t *testing.T) {
	t.Run("think first stays field-based", func(t *testing.T) {
		at := &activeTurn{}
		at.append("reasoning", "pondering")
		at.append("content", "answer")
		at.endInline()
		if at.reasoning != "pondering" {
			t.Fatalf("reasoning = %q, want %q", at.reasoning, "pondering")
		}
		if at.content != "answer" {
			t.Fatalf("content = %q, want %q", at.content, "answer")
		}
	})

	t.Run("answer first inlines later thinking", func(t *testing.T) {
		at := &activeTurn{}
		at.append("content", "hi")
		at.append("reasoning", "wait")
		at.append("content", "actually no")
		at.append("reasoning", "hmm")
		at.endInline()
		if at.reasoning != "" {
			t.Fatalf("reasoning = %q, want empty (all inlined)", at.reasoning)
		}
		want := "hi\n\n<think>wait</think>\n\nactually no\n\n<think>hmm</think>\n\n"
		if at.content != want {
			t.Fatalf("content = %q, want %q", at.content, want)
		}
		if at.thinkBytes != len("wait")+len("hmm") {
			t.Fatalf("thinkBytes = %d, want %d", at.thinkBytes, len("wait")+len("hmm"))
		}
	})

	// A search fired mid-inline-think must land inside the <think> span so the
	// UI nests it there instead of spilling it into the answer.
	t.Run("search offset lands inside the inline think", func(t *testing.T) {
		at := &activeTurn{}
		at.append("content", "hi")
		at.append("reasoning", "let me look")
		contentLen, _, during := at.lens()
		at.append("reasoning", " more")
		at.endInline()
		if during {
			t.Fatal("duringReasoning = true, want false (answer already started)")
		}
		open := len("hi\n\n<think>")
		if contentLen <= open || contentLen >= len(at.content)-len("</think>\n\n") {
			t.Fatalf("search offset %d not inside the think span of %q", contentLen, at.content)
		}
	})
}
