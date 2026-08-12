package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTurns_noAnswerNudge(t *testing.T) {
	got := noAnswerNudge("  I should answer carefully.  ")
	if !strings.Contains(got, "I should answer carefully.") {
		t.Errorf("nudge dropped the reasoning: %q", got)
	}
	if !strings.Contains(got, "Write the final answer now") {
		t.Errorf("nudge missing the instruction: %q", got)
	}
}

// A model that inlines its <think> into content: the thought is the text
// BETWEEN the tags, so stripping must keep it (answerOnly would delete it).
func TestTurns_noAnswerNudgeInlineThink(t *testing.T) {
	got := noAnswerNudge("<think>weighing the options")
	if !strings.Contains(got, "weighing the options") {
		t.Errorf("inline thought lost: %q", got)
	}
	if strings.Contains(got, "<think>") {
		t.Errorf("think tag leaked into the nudge: %q", got)
	}
}

func TestTurns_noAnswerNudgeEmpty(t *testing.T) {
	if got := noAnswerNudge("   "); !strings.Contains(got, "(empty)") {
		t.Errorf("empty thought = %q, want the (empty) placeholder", got)
	}
}

// Long reasoning is truncated to its TAIL (where the model was heading) and
// stays valid UTF-8 even when the cut lands mid-rune.
func TestTurns_noAnswerNudgeTruncatesToTail(t *testing.T) {
	long := strings.Repeat("é", nudgeThoughtMax) + "THE-END"
	got := noAnswerNudge(long)
	if !strings.Contains(got, "THE-END") {
		t.Error("truncation dropped the tail of the reasoning")
	}
	if !utf8.ValidString(got) {
		t.Error("truncation produced invalid UTF-8")
	}
	if len(got) > nudgeThoughtMax+512 {
		t.Errorf("nudge not truncated: %d bytes", len(got))
	}
}
