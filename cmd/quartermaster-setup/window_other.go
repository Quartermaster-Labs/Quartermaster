//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

// runWindow has no native implementation off Windows, and that is a decision
// rather than a gap.
//
// A native webview on Linux is webkit2gtk and on macOS is WebKit, both of which
// need cgo and platform dev headers -- which would drag the setup binary out of
// the CGO_ENABLED=0 cross-compile matrix that lets every other artifact be
// built from one machine. The browser fallback in main() shows the identical
// wizard, and a Linux install is usually a headless box being configured over
// ssh anyway, where a native window would be useless even if it existed.
func runWindow(string, <-chan struct{}) error {
	return errors.New("no native window on this platform")
}

// showError falls back to stderr; unix builds are not -H=windowsgui, so a
// terminal is watching.
func showError(msg string) {
	_, _ = os.Stderr.WriteString(msg + "\n")
}

// announceURL puts the wizard's address on stderr.
//
// Unix builds are not -H=windowsgui, so a terminal is watching -- and it is the
// only thing that reliably is. openBrowser reports only whether the helper
// could be *started*: `open` on macOS and `xdg-open` on Linux both return
// success from Start and then fail afterwards over ssh, on a headless box, or
// when Launch Services refuses, and that failure is swallowed by the Wait
// goroutine. Without this line the process then sits on wiz.Done() having
// printed nothing at all, which is indistinguishable from a hang.
func announceURL(url string) {
	_, _ = fmt.Fprintf(os.Stderr,
		"Quartermaster setup is running at %s\n"+
			"A browser should have opened there. If it did not, open that address yourself.\n"+
			"Leave this terminal running until the wizard finishes.\n", url)
}
