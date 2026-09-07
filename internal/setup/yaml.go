package setup

import (
	"os"
	"regexp"
	"strings"
)

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
	lines, nl, trailingNL, err := readYamlLines(path)
	if err != nil {
		return err
	}
	// Normalise separators: the generate file is read on every platform and a
	// Windows path with backslashes is a stack of escape sequences to a YAML
	// double-quoted scalar. Forward slashes work everywhere, including Windows.
	value = strings.ReplaceAll(value, `\`, `/`)

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
	return writeYamlLines(path, out, nl, trailingNL)
}

// appBlockRe matches the "app:" line that opens the process-level block inside
// settings.
var appBlockRe = regexp.MustCompile(`^(\s+)app:\s*$`)

// setAppKey sets settings.app.<key> in a generate file, in place.
//
// This is setSettingsKey one level deeper, and it exists because the
// process-level block is nested: ports, TLS files and the remote-access policy
// live under settings.app (see autogen.AppSettings), while everything
// setSettingsKey writes is a scalar directly under settings.
//
// It writes the GENERATE FILE rather than the dashboard's sidecar, which is a
// precedence choice, not a convenience one. The order is
// argv > sidecar > settings.app > default, so a value written here is the
// install's baseline and anything the operator later saves from the dashboard
// still wins. Writing the sidecar instead would mean a re-run of the setup
// binary silently overruling the running install's own settings.
//
// The block, and settings itself, are created when absent, so this works on a
// minimal generate file that carries neither.
func setAppKey(path, key, value string) error {
	lines, nl, trailingNL, err := readYamlLines(path)
	if err != nil {
		return err
	}
	value = strings.ReplaceAll(value, `\`, `/`)

	keyRe := settingsKeyRe(key)
	inSettings, inApp, done := false, false, false
	appIndent := ""
	out := make([]string, 0, len(lines)+2)

	for _, l := range lines {
		if topLevelSettingsRe.MatchString(l) {
			inSettings = true
			out = append(out, l)
			continue
		}
		if inSettings && !inApp && appIndent == "" && appBlockRe.MatchString(l) {
			inApp = true
			appIndent = appBlockRe.FindStringSubmatch(l)[1]
			out = append(out, l)
			continue
		}
		if inApp && strings.TrimSpace(l) != "" {
			switch {
			case len(indentOf(l)) > len(appIndent) && !done && keyRe.MatchString(l):
				out = append(out, indentOf(l)+key+": "+quoteScalar(value))
				done = true
				continue
			case len(indentOf(l)) <= len(appIndent):
				// Left the app block: the key belongs to it, so it goes in
				// before whatever starts here.
				if !done {
					out = append(out, appIndent+"  "+key+": "+quoteScalar(value))
					done = true
				}
				inApp = false
			}
		}
		if inSettings && !inApp && strings.TrimSpace(l) != "" && indentOf(l) == "" {
			if !done {
				out = append(out, "  app:", "    "+key+": "+quoteScalar(value))
				done = true
			}
			inSettings = false
		}
		out = append(out, l)
	}
	if !done {
		switch {
		case inApp:
			out = append(out, appIndent+"  "+key+": "+quoteScalar(value))
		case inSettings:
			out = append(out, "  app:", "    "+key+": "+quoteScalar(value))
		default:
			out = append(out, "settings:", "  app:", "    "+key+": "+quoteScalar(value))
		}
	}
	return writeYamlLines(path, out, nl, trailingNL)
}

// indentOf returns a line's leading whitespace, which is how the nested walk
// above tells "still inside this block" from "the block ended".
func indentOf(l string) string {
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}

// readYamlLines loads a generate file as lines, reporting the line ending to
// write back with and whether the file ended in one.
//
// Both are preserved rather than normalised: a CRLF file that grows one LF line
// renders as a stray glyph in Notepad, which is where users edit this, and
// Split leaves a trailing "" for any file ending in a newline that is not a
// line at all: appending after it would cost the file its final newline and
// leave a blank line in the middle.
func readYamlLines(path string) (lines []string, nl string, trailingNL bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false, err
	}
	nl = "\n"
	if strings.Contains(string(raw), "\r\n") {
		nl = "\r\n"
	}
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	trailingNL = strings.HasSuffix(body, "\n")
	body = strings.TrimSuffix(body, "\n")
	return strings.Split(body, "\n"), nl, trailingNL, nil
}

// writeYamlLines is readYamlLines' inverse.
func writeYamlLines(path string, lines []string, nl string, trailingNL bool) error {
	joined := strings.Join(lines, nl)
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
