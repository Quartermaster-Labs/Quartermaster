package autogen

// backendhelp.go answers "does the selected binary know this flag?" for the
// launch-arguments editor (ui-svelte/launch-args.md). The checked-in table
// (flagtable.go) knows what OUR emitters write; it cannot know about a spelling
// the user's build adds, and it goes stale the day upstream drops one. `exe
// --help` fills the gap, memoised per binary the same way ListBackendDevices
// memoises its listing: the flag set is a property of one file, and a probe is
// a process launch.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// backendHelpTimeout bounds one --help. It does not touch the device-probe
// budget (backenddev.go): that budget exists to stop config generation hanging
// on wedged backends, while this probe belongs to an interactive edit, where a
// short wait is visible and a timeout is recoverable.
const backendHelpTimeout = 10 * time.Second

// backendFlagRe matches a flag spelling anywhere in a usage block:
//
//	-m, --model FNAME    path to model
//	--no-mmap            ...
//
// Descriptions that name another flag ("deprecated, use --flash-attn") match
// too. That is the right failure direction: an extra name can only make
// validation looser, never reject something the binary accepts.
var backendFlagRe = regexp.MustCompile(`(?:^|[\s,(\[])(-{1,2}[A-Za-z][A-Za-z0-9-]*)`)

var (
	backendHelpMu    sync.Mutex
	backendHelpCache = map[string][]string{}
)

// BackendFlags returns the flag spellings `exe --help` advertises, memoised per
// binary. The cache key carries size and mtime so a backend upgraded in place
// is re-probed instead of answering from a stale list.
//
// An error is an ordinary outcome: an older build may have no useful --help, and
// a packaged install may name a binary that is not there yet. Callers fall back
// to the checked-in table alone.
func BackendFlags(exe string) ([]string, error) {
	if strings.TrimSpace(exe) == "" {
		return nil, fmt.Errorf("no backend exe")
	}
	key := exe
	if st, err := os.Stat(exe); err == nil {
		key = fmt.Sprintf("%s|%d|%d", exe, st.Size(), st.ModTime().UnixNano())
	}

	backendHelpMu.Lock()
	cached, hit := backendHelpCache[key]
	backendHelpMu.Unlock()
	if hit {
		if len(cached) == 0 {
			return nil, fmt.Errorf("no flags listed by %s", exe)
		}
		return cached, nil
	}

	flags, err := probeBackendFlags(exe)

	backendHelpMu.Lock()
	// A failed probe is cached too: re-running it once per keystroke-sized
	// validation pass would dominate the editor on a box where it cannot work.
	backendHelpCache[key] = flags
	backendHelpMu.Unlock()

	if err != nil {
		return nil, err
	}
	if len(flags) == 0 {
		return nil, fmt.Errorf("no flags listed by %s", exe)
	}
	return flags, nil
}

func probeBackendFlags(exe string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backendHelpTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "--help")
	// Which stream gets the usage has moved across releases, and some builds
	// exit non-zero after printing it. Read the pair.
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%s --help: %w", exe, err)
	}
	return parseBackendHelp(string(out)), nil
}

// parseBackendHelp pulls the distinct flag spellings out of a usage block,
// keeping the order they first appear in (the editor lists them that way).
func parseBackendHelp(out string) []string {
	seen := map[string]bool{}
	var flags []string
	for _, m := range backendFlagRe.FindAllStringSubmatch(out, -1) {
		f := m[1]
		// A trailing hyphen is grammar ("--flag-") or an unfinished range, not a
		// flag; "--" alone is the end-of-options marker.
		if f == "-" || f == "--" || strings.HasSuffix(f, "-") || seen[f] {
			continue
		}
		seen[f] = true
		flags = append(flags, f)
	}
	return flags
}
