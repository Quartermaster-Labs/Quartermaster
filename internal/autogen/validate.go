package autogen

// Flag validation for the launch-arguments editor (ui-svelte/launch-args.md).
// Composition never blocks on this: the text is the user's and is stored
// verbatim. Validation exists so the editor can say "llama-server will refuse to
// start" before a save that looks successful and a spawn that dies, and it needs
// BOTH sources: the checked-in table knows what quartermaster emits, while the
// selected binary's --help knows what this build actually accepts.

import (
	"sort"
	"strings"
)

// Issue kinds for a composed launch command. The editor treats IssueUnknown as
// blocking (it confirms before saving) and the other two as warnings.
const (
	// IssueUnknown: the flag is in neither the checked-in table nor the selected
	// backend's --help, so a spawn carrying it dies with "invalid argument".
	IssueUnknown = "unknown"
	// IssueUnverified: the flag is not in the table and the backend's --help
	// could not be read, so nothing is known about it either way.
	IssueUnverified = "unverified"
	// IssueBackendMissing: the table knows the flag, this binary's --help does
	// not, normally a backend build older than the emitter.
	IssueBackendMissing = "backend-missing"
)

// CmdIssue is one complaint about a composed command, keyed to the token that
// caused it. The editor renders these under the custom text and confirms before
// saving while an IssueUnknown stands.
type CmdIssue struct {
	Token   string `json:"token"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Suggestions are the closest known spellings, for typos. Empty when nothing
	// is close enough to be worth naming.
	Suggestions []string `json:"suggestions,omitempty"`
}

// ValidateTokens checks every flag token of a composed command. backendFlags is
// the selected binary's --help spellings; nil means the probe did not run, which
// downgrades an unknown flag from "the spawn will fail" to "could not be
// checked" instead of inventing certainty.
//
// Non-flag arguments are skipped, and the value after a known value-taking flag
// is consumed (inline --flag=value included), so a negative number never reads
// as a flag. A flag the binary lists but the table does not is fine: the table
// describes what quartermaster emits, the union describes what will run.
func (ft *FlagTable) ValidateTokens(tokens []CmdToken, backendFlags []string) []CmdIssue {
	var help map[string]bool
	if backendFlags != nil {
		help = make(map[string]bool, len(backendFlags))
		for _, f := range backendFlags {
			help[f] = true
		}
	}
	var issues []CmdIssue
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i].Text
		if !strings.HasPrefix(tok, "-") || tok == "-" || tok == "--" {
			continue
		}
		name, _, inline := SplitFlagToken(tok)
		if def, known := ft.Lookup(name); known {
			if help != nil && !anySpellingInHelp(def, help) {
				issues = append(issues, CmdIssue{
					Token:   name,
					Kind:    IssueBackendMissing,
					Message: "this backend build does not list this flag",
				})
			}
			if def.Value && !inline {
				i++
			}
			continue
		}
		if help != nil && help[name] {
			// The binary accepts a spelling the table does not model. Whether it
			// takes a value is unknown, so do not consume the next token.
			continue
		}
		issue := CmdIssue{Token: name, Suggestions: ft.suggestFlags(name, backendFlags)}
		if help == nil {
			issue.Kind = IssueUnverified
			issue.Message = "not a flag quartermaster knows; the backend --help could not be read"
		} else {
			issue.Kind = IssueUnknown
			issue.Message = ft.Backend + " does not accept this flag, so it will refuse to start"
		}
		issues = append(issues, issue)
	}
	return issues
}

// ValidateTokens checks a llama-server command. The diffusion path calls the
// method with SdFlags instead, so an sd-server flag is checked against
// sd-server's table and its --help rather than being reported as unknown.
func ValidateTokens(tokens []CmdToken, backendFlags []string) []CmdIssue {
	return LlamaFlags.ValidateTokens(tokens, backendFlags)
}

// anySpellingInHelp reports whether the binary advertises any of a known flag's
// spellings. The check is per spelling, not per name: a build that lists only
// --checkpoint-min-step still accepts the -cms the emitter writes.
func anySpellingInHelp(def FlagDef, help map[string]bool) bool {
	if help[def.Name] {
		return true
	}
	for _, a := range def.Aliases {
		if help[a] {
			return true
		}
	}
	return false
}

// suggestFlags names the closest known spellings for a flag neither source
// knows. A spelling that differs only in dashes wins outright (---cache-ram ->
// -cram, --cache-ram, since llama.cpp pairs those names); otherwise the closest
// names by edit distance, capped so a wild guess is never dressed up as a
// suggestion. backendFlags may be nil.
func (ft *FlagTable) suggestFlags(name string, backendFlags []string) []string {
	want := strings.ToLower(strings.TrimLeft(name, "-"))
	if want == "" {
		return nil
	}
	// Dash-count mistakes: same letters, one knob. Return every spelling the
	// union offers for it so the user sees both the short and the long form.
	for _, d := range ft.Defs {
		spellings := append([]string{d.Name}, d.Aliases...)
		for _, sp := range spellings {
			if strings.ToLower(strings.TrimLeft(sp, "-")) == want {
				return spellings
			}
		}
	}
	type cand struct {
		spelling string
		dist     int
	}
	var cands []cand
	seen := map[string]bool{}
	add := func(sp string) {
		if sp == "" || seen[sp] {
			return
		}
		seen[sp] = true
		cands = append(cands, cand{spelling: sp, dist: editDistance(want, strings.ToLower(strings.TrimLeft(sp, "-")))})
	}
	for _, d := range ft.Defs {
		add(d.Name)
		for _, a := range d.Aliases {
			add(a)
		}
	}
	for _, f := range backendFlags {
		add(f)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].spelling < cands[j].spelling
	})
	limit := 3
	if len(want) <= 4 {
		limit = 1
	}
	var out []string
	for _, c := range cands {
		if c.dist == 0 || c.dist > limit {
			continue
		}
		out = append(out, c.spelling)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// validateComposed runs the editor's check over a composed command. Only a
// command carrying custom text is checked: with an empty box the command is the
// emitter's own, which the golden flag-table test holds to the table and which
// internal/server/flagcheck.go checks against the installed binary at boot.
// When there IS custom text the whole command is walked, so a backend older than
// the emitter shows up as a warning next to the user's own flags.
func validateComposedWith(ft *FlagTable, cc ComposedCmd, backendExe string) []CmdIssue {
	if len(cc.Tokens) == 0 || strings.TrimSpace(cc.Custom) == "" {
		return nil
	}
	var flags []string
	if fs, err := BackendFlags(backendExe); err == nil {
		flags = fs
	}
	return ft.ValidateTokens(cc.Tokens, flags)
}

// validateComposed is the llama-server spelling, kept for its callers.
func validateComposed(cc ComposedCmd, backendExe string) []CmdIssue {
	return validateComposedWith(LlamaFlags, cc, backendExe)
}

// editDistance is the Levenshtein distance, two rows of DP. Flags are short and
// this runs only for a token neither source recognized.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
