package autogen

import (
	"slices"
	"testing"
)

func tokList(texts ...string) []CmdToken {
	out := make([]CmdToken, 0, len(texts))
	for _, t := range texts {
		out = append(out, CmdToken{Text: t})
	}
	return out
}

func TestValidateTokens_UnknownFlagSuggestsSpellings(t *testing.T) {
	help := []string{"-cms", "--checkpoint-min-step", "-c", "--ctx-size", "-t", "--threads"}
	issues := ValidateTokens(tokList("--cms", "512"), help)
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(issues), issues)
	}
	got := issues[0]
	if got.Kind != IssueUnknown {
		t.Errorf("kind = %q, want %q", got.Kind, IssueUnknown)
	}
	if got.Token != "--cms" {
		t.Errorf("token = %q, want --cms", got.Token)
	}
	want := []string{"-cms", "--checkpoint-min-step"}
	if !slices.Equal(got.Suggestions, want) {
		t.Errorf("suggestions = %v, want %v", got.Suggestions, want)
	}
}

func TestValidateTokens_UnknownWithoutHelpIsUnverified(t *testing.T) {
	issues := ValidateTokens(tokList("--cms", "512"), nil)
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(issues), issues)
	}
	if issues[0].Kind != IssueUnverified {
		t.Errorf("kind = %q, want %q", issues[0].Kind, IssueUnverified)
	}
}

func TestValidateTokens_BackendMissingIsWarning(t *testing.T) {
	// No spelling of the -cms knob in this build's help: the emitter would write
	// a flag the binary does not list.
	help := []string{"-c", "--ctx-size"}
	issues := ValidateTokens(tokList("-cms", "256"), help)
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(issues), issues)
	}
	if issues[0].Kind != IssueBackendMissing || issues[0].Token != "-cms" {
		t.Errorf("got %+v, want backend-missing -cms", issues[0])
	}
}

func TestValidateTokens_BackendListsOnlyTheLongSpelling(t *testing.T) {
	// Help names the knob only by its long form; llama.cpp registers the pair
	// together, so the -cms the emitter writes is still fine.
	help := []string{"--checkpoint-min-step", "-c"}
	if issues := ValidateTokens(tokList("-cms", "256"), help); len(issues) != 0 {
		t.Fatalf("got %+v, want no issues", issues)
	}
}

func TestValidateTokens_BackendOnlyFlagIsAccepted(t *testing.T) {
	// The table has no --foo, but this binary advertises it; nothing to report.
	help := []string{"--foo", "-c"}
	if issues := ValidateTokens(tokList("--foo", "bar"), help); len(issues) != 0 {
		t.Fatalf("got %+v, want no issues", issues)
	}
}

func TestValidateTokens_ValuesAreNotFlags(t *testing.T) {
	// -0.5 is --temp's value and must not be read as a flag.
	if issues := ValidateTokens(tokList("--temp", "-0.5"), nil); len(issues) != 0 {
		t.Fatalf("got %+v, want no issues", issues)
	}
	// Inline values leave no separate token behind either.
	if issues := ValidateTokens(tokList("--ctx-size=32768"), nil); len(issues) != 0 {
		t.Fatalf("inline known flag: got %+v, want no issues", issues)
	}
	if issues := ValidateTokens(tokList("--cms=512"), nil); len(issues) != 1 {
		t.Fatalf("inline unknown flag: got %+v, want 1 issue", issues)
	}
}

func TestValidateTokens_ComposedCommand(t *testing.T) {
	cc, err := ComposeCmd([]string{"-m /models/x.gguf -cms 256 -t 7 --metrics"}, "--cms 512")
	if err != nil {
		t.Fatalf("ComposeCmd: %v", err)
	}
	help := []string{"-m", "--model", "-cms", "--checkpoint-min-step", "-t", "--threads", "--metrics"}
	issues := ValidateTokens(cc.Tokens, help)
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(issues), issues)
	}
	if issues[0].Token != "--cms" || issues[0].Kind != IssueUnknown {
		t.Errorf("got %+v, want unknown --cms", issues[0])
	}
}

func TestSuggestFlags(t *testing.T) {
	cases := []struct {
		name string
		tok  string
		help []string
		want []string
	}{
		{"dash count names one knob", "---cache-ram", nil, []string{"-cram", "--cache-ram"}},
		{"one-character typo", "--ctx-siz", nil, []string{"--ctx-size"}},
		{"backend-only candidate", "--foo-barr", []string{"--foo-bar"}, []string{"--foo-bar"}},
		{"nothing close", "--zzzzzzzzzzzz", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LlamaFlags.suggestFlags(c.tok, c.help); !slices.Equal(got, c.want) {
				t.Errorf("LlamaFlags.suggestFlags(%q, %v) = %v, want %v", c.tok, c.help, got, c.want)
			}
		})
	}
}
