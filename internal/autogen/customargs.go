package autogen

import (
	"sort"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// Custom launch arguments are applied to a rendered command in ONE direction:
// the user's text is never decomposed. For every knob the text sets, the
// generated occurrences of that knob are dropped, and the text is appended
// verbatim at the end, where llama-server's own last-flag-wins rule gives it
// the final say.
//
// The alternative (parsing the text back into structured fields, which is what
// the old launch-parameters box did) is a lossy round trip: every flag needs a
// parser case, every miss lands in the free-form bucket, and flags like -cram
// ended up emitted twice, once from the field and once from the text.

// Token provenance values for CmdToken.Source.
const (
	SourceGenerated = "generated"
	SourceCustom    = "custom"
)

// CmdToken is one token of a launch command with the provenance the editor
// needs to render the composed command without diffing strings. Suppressed
// tokens are generated tokens that a custom knob replaced; they are reported so
// the editor can strike them through or summarise the count.
type CmdToken struct {
	Text       string `json:"text"`
	Source     string `json:"source"`
	Knob       string `json:"knob,omitempty"`
	Suppressed bool   `json:"suppressed,omitempty"`
}

// ComposedCmd is a rendered command in layers.
type ComposedCmd struct {
	// Generated is the command as the generator emitted it (no custom text).
	Generated string `json:"generated"`
	// Effective is what will actually run.
	Effective string `json:"effective"`
	// Custom is the user's text, unchanged.
	Custom string `json:"custom,omitempty"`
	// Lines is the effective command split back into emittable lines. It is
	// byte-identical to the input lines when Custom is empty.
	Lines []string `json:"-"`
	// Tokens is every token of both layers, in order, with provenance.
	Tokens []CmdToken `json:"tokens,omitempty"`
	// Owned lists the knobs the custom text sets, sorted.
	Owned []string `json:"ownedKnobs,omitempty"`
}

// CustomArgsText returns the custom launch-argument text in effect: the new
// CustomArgs field when it carries anything, else the legacy ExtraArgs bucket
// written by the old two-way box. CustomArgsOff suppresses both without
// discarding them, so a disable/enable round trip in the editor loses nothing.
func (o *Override) CustomArgsText() string {
	if o == nil || o.CustomArgsOff {
		return ""
	}
	if strings.TrimSpace(o.CustomArgs) != "" {
		return o.CustomArgs
	}
	return o.ExtraArgs
}

// ClearOwnedFieldsFromText parses the override's effective custom text and
// zeroes the structured fields it shadows. This is the save-time half of the
// ownership rule: dropping the generated flag at emit time is not enough, since
// the stale field would resurface the moment the user deletes the flag from the
// text (issue #38's mmap reset). Unparseable text clears nothing.
func ClearOwnedFieldsFromText(ov *Override) []string {
	text := ov.CustomArgsText()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	toks, err := config.SanitizeCommand(text)
	if err != nil {
		return nil
	}
	return ClearOwnedFields(ov, OwnedKnobs(toks))
}

// ComposeCmd applies custom launch arguments to generated command lines. An
// empty (or comment-only) custom text is the identity: the lines are returned
// untouched, so a config saved before this feature existed regenerates
// byte-identically.
//
// Unparseable custom text is returned as an error with the command left
// unchanged; the caller decides whether to surface it (the preview does) or to
// emit it anyway and let the spawn fail as it would have before.
func ComposeCmd(lines []string, custom string) (ComposedCmd, error) {
	gen := strings.Join(lines, " ")
	cc := ComposedCmd{Generated: gen, Effective: gen, Lines: lines, Custom: custom}

	genToks, _ := config.SanitizeCommand(gen)
	appendGen := func(tok string, knob string, suppressed bool) {
		cc.Tokens = append(cc.Tokens, CmdToken{Text: tok, Source: SourceGenerated, Knob: knob, Suppressed: suppressed})
	}

	// Comment-only text is blank: the spawn strips comment lines, so it can
	// suppress nothing and must not turn into an empty-command error.
	if strings.TrimSpace(config.StripComments(custom)) == "" {
		for _, t := range genToks {
			appendGen(t, TokenKnob(t), false)
		}
		return cc, nil
	}

	customToks, err := config.SanitizeCommand(custom)
	if err != nil {
		// Emit the text unchanged (what the old extraArgs path did) so the
		// spawn fails with llama-server's own message; callers that can show a
		// dialog (the preview) surface err instead.
		if len(lines) > 0 {
			cc.Lines = append(append([]string{}, lines...), splitCustomLines(custom)...)
			cc.Effective = strings.Join(cc.Lines, " ")
		}
		return cc, err
	}
	if len(customToks) == 0 { // comment-only text
		for _, t := range genToks {
			appendGen(t, TokenKnob(t), false)
		}
		return cc, nil
	}

	owned := OwnedKnobs(customToks)
	for k := range owned {
		cc.Owned = append(cc.Owned, k)
	}
	sort.Strings(cc.Owned)

	out := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		toks, err := config.SanitizeCommand(line)
		if err != nil {
			// A line the spawner could not split either; keep it as written and
			// let the same failure happen at spawn time.
			out = append(out, line)
			for _, t := range toks {
				appendGen(t, TokenKnob(t), false)
			}
			continue
		}
		kept := make([]string, 0, len(toks))
		dropped := false
		for i := 0; i < len(toks); i++ {
			tok := toks[i]
			if !strings.HasPrefix(tok, "-") {
				kept = append(kept, tok)
				appendGen(tok, "", false)
				continue
			}
			name, _, inline := SplitFlagToken(tok)
			def, known := LookupFlag(name)
			if known && def.Knob != "" && owned[def.Knob] {
				dropped = true
				appendGen(tok, def.Knob, true)
				if def.Value && !inline && i+1 < len(toks) {
					i++
					appendGen(toks[i], def.Knob, true)
				}
				continue
			}
			kept = append(kept, tok)
			appendGen(tok, TokenKnob(tok), false)
			if known && def.Value && !inline && i+1 < len(toks) {
				i++
				kept = append(kept, toks[i])
				appendGen(toks[i], "", false)
			}
		}
		if len(kept) == 0 {
			continue
		}
		if dropped {
			// Only a line that actually lost a token is re-serialised; every
			// other line keeps the emitter's exact bytes.
			out = append(out, joinTokens(kept))
		} else {
			out = append(out, line)
		}
	}

	for _, line := range splitCustomLines(custom) {
		out = append(out, line)
	}
	for _, t := range customToks {
		cc.Tokens = append(cc.Tokens, CmdToken{Text: t, Source: SourceCustom, Knob: TokenKnob(t)})
	}
	if len(out) == 0 {
		return cc, nil
	}
	cc.Lines = out
	cc.Effective = strings.Join(out, " ")
	return cc, nil
}

// splitCustomLines prepares the user's text for the YAML cmd block: one
// indented line per non-empty source line, CR stripped. Comment lines are kept
// (the spawn's SanitizeCommand drops them, and so does the UI preview).
func splitCustomLines(custom string) []string {
	raw := strings.Split(strings.ReplaceAll(custom, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// joinTokens re-serialises a token list for the config file. Only tokens that
// need it are quoted; everything else keeps its spelling, so the round trip
// through SanitizeCommand at spawn time reproduces the same argv.
func joinTokens(toks []string) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteToken(t))
	}
	return b.String()
}

func quoteToken(tok string) string {
	if tok == "" || strings.ContainsAny(tok, " \t\"") {
		return `"` + strings.ReplaceAll(tok, `"`, "") + `"`
	}
	return tok
}
