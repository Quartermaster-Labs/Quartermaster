package server

// Startup launch-flag check (ui-svelte/launch-args.md, "Validation").
//
// The editor validates the text a user is about to save, but a backend can drop
// a flag between releases while the generated commands on disk keep writing it,
// and a hand-edited config can carry a typo. A spawn then dies with "invalid
// argument" long after anything pointed at the cause. At boot every
// llama-server command in the running config is checked against the installed
// binary's own --help, and whatever no longer exists is named in the log.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// backendHelpProbe is autogen.BackendFlags, injected so the gathering half is
// testable without launching a real backend.
type backendHelpProbe func(exe string) ([]string, error)

// checkLaunchFlags runs launchFlagWarnings over the live config in the
// background. The check reads one --help per backend, and a probe is a process
// launch, so it must not sit in front of the listeners. autogen.BackendFlags
// memoises the result (and a failed probe) per binary.
func (s *Server) checkLaunchFlags() {
	models := s.config().Models
	go func() {
		for _, w := range launchFlagWarnings(models, autogen.BackendFlags) {
			s.proxylog.Warnf("%s", w)
		}
	}()
}

// launchFlagWarnings returns one line per model whose command carries a flag
// the selected llama-server build does not advertise, sorted by model id so the
// boot log is stable. Only flags that make the spawn fail are reported:
// autogen.IssueUnknown (in neither the table nor --help) and
// autogen.IssueBackendMissing (the emitter writes it, this build does not list
// it). A backend whose --help could not be read says nothing: "could not check"
// is not a failure, and the spawn itself will name the real problem.
func launchFlagWarnings(models map[string]config.ModelConfig, probe backendHelpProbe) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []string
	for _, id := range ids {
		argv := config.ParseCmd(models[id].Cmd).Argv
		if len(argv) == 0 || !isLlamaServerExe(argv[0]) {
			continue
		}
		flags, err := probe(argv[0])
		if err != nil {
			continue
		}
		tokens := make([]autogen.CmdToken, 0, len(argv))
		for _, a := range argv {
			tokens = append(tokens, autogen.CmdToken{Text: a})
		}
		var named []string
		for _, issue := range autogen.ValidateTokens(tokens, flags) {
			switch issue.Kind {
			case autogen.IssueBackendMissing:
				named = append(named, fmt.Sprintf("%s (this backend build does not list it)", issue.Token))
			case autogen.IssueUnknown:
				named = append(named, fmt.Sprintf("%s (not a llama-server flag)", issue.Token))
			}
		}
		if len(named) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"launch flag check: model %q will fail to start: %s. Update the backend, or remove the flag from its custom launch arguments",
			id, strings.Join(named, ", ")))
	}
	return out
}

// isLlamaServerExe reports whether a command's executable is a llama-server
// build. The check below knows only llama.cpp flags (the checked-in table plus
// that binary's --help), so sd-server / tts-server / ... entries must not be
// walked through it.
func isLlamaServerExe(path string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(path)), "llama-server")
}
