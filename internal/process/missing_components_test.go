package process

import (
	"strings"
	"testing"
)

func TestProcessCommand_MissingComponentsNote(t *testing.T) {
	if got := missingComponentsNote(nil); got != "" {
		t.Errorf("nil roles = %q, want empty", got)
	}
	if got := missingComponentsNote([]string{" ", ""}); got != "" {
		t.Errorf("blank roles = %q, want empty", got)
	}
	got := missingComponentsNote([]string{"vae", "llm", "mystery"})
	for _, want := range []string{"VAE", "text encoder (LLM)", "mystery", "models folder"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q missing %q", got, want)
		}
	}
}
