package setup

import (
	"os"
	"regexp"
	"strings"
)

// minimalGenerate is written when there is no example to seed from.
//
// autogen.Settings.applyDefaults fills every zero-valued knob, so a file with
// nothing but modelsRoot is fully functional — what is lost is the example's
// explanatory comments, not behaviour. The pointer to the example is there so
// a user who wants the annotated version knows one exists.
const minimalGenerate = `# Quartermaster autogen control file, created by the setup wizard.
# Every unset knob falls back to its built-in default; see
# quartermaster-generate.example.yaml in the repository for the annotated form.
settings:
  modelsRoot: ""
`

// settingsKeyRe matches an indented "key:" line inside a block.
func settingsKeyRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^(\s+)` + regexp.QuoteMeta(key) + `:.*$`)
}

var topLevelSettingsRe = regexp.MustCompile(`^settings:\s*$`)

// setSettingsKey sets settings.<key> in a generate file, in place.
//
// This edits LINES rather than round-tripping through a YAML marshaller, and
// that is deliberate: the generate file is the user's, it is heavily commented,
// and yaml.v3 discards comments on any node it rebuilds. Re-emitting the file
// would silently strip every explanation of every knob the first time the
// wizard touched it.
//
// The scan is scoped to the top-level `settings:` block and stops at the next
// unindented line. A whole-file regex (which is what the PowerShell installer
// did) would also rewrite a same-named key inside the trailing `overrides:`
// list, where per-model entries legitimately repeat setting names.
func setSettingsKey(path, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Normalise separators: the generate file is read on every platform and a
	// Windows path with backslashes is a stack of escape sequences to a YAML
	// double-quoted scalar. Forward slashes work everywhere, including Windows.
	value = strings.ReplaceAll(value, `\`, `/`)

	// Preserve the file's existing line ending; a CRLF file that grows one LF
	// line renders as a stray glyph in Notepad, which is where users edit this.
	nl := "\n"
	if strings.Contains(string(raw), "\r\n") {
		nl = "\r\n"
	}
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// Split leaves a trailing "" for any file that ends in a newline, and that
	// empty element is not a line: appending after it puts a blank line between
	// the settings block and the new key, and costs the file its final newline.
	// Strip it here, restore it at the end.
	trailingNL := strings.HasSuffix(body, "\n")
	body = strings.TrimSuffix(body, "\n")
	lines := strings.Split(body, "\n")

	keyRe := settingsKeyRe(key)
	inSettings, done := false, false
	out := make([]string, 0, len(lines)+1)

	for _, l := range lines {
		switch {
		case topLevelSettingsRe.MatchString(l):
			inSettings = true
			out = append(out, l)
			continue
		case inSettings && !done && keyRe.MatchString(l):
			indent := keyRe.FindStringSubmatch(l)[1]
			out = append(out, indent+key+": "+quoteScalar(value))
			done = true
			continue
		case inSettings && strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t"):
			// Left the settings block without finding the key: insert it as the
			// block's last line, before whatever top-level key starts here.
			if !done {
				out = append(out, "  "+key+": "+quoteScalar(value))
				done = true
			}
			inSettings = false
		}
		out = append(out, l)
	}
	if !done {
		if !inSettings {
			out = append(out, "settings:")
		}
		out = append(out, "  "+key+": "+quoteScalar(value))
	}
	joined := strings.Join(out, nl)
	if trailingNL {
		joined += nl
	}
	return os.WriteFile(path, []byte(joined), 0o644)
}

// quoteScalar double-quotes a value that YAML would otherwise misread.
//
// An empty string must be `""` or the key parses as null, and a Windows path
// like C:/Models starts with a token that a bare scalar is fine with but a
// leading-space or comment character is not. Quoting when in doubt costs
// nothing; guessing wrong writes a file that fails to load at next boot.
func quoteScalar(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, "#:{}[],&*?|<>=!%@`\"'") || strings.TrimSpace(v) != v {
		return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return v
}
