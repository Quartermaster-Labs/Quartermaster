//go:build windows

package nativewin

import (
	"net/url"
	"os/exec"
	"syscall"
)

// OpenExternal hands a URL to the user's default browser.
//
// The window has no address bar, no back button and no tabs, so a page that
// isn't ours -- a Hugging Face model card, a GitHub release -- has no business
// loading inside it. Sending it out to the real browser is the only honest
// answer.
//
// The scheme allow-list is not paranoia about our own pages: this is a shell
// execution path reachable from JavaScript, and FileProtocolHandler will just
// as happily launch a local executable path or a file: URL as it will open a
// web page. Anything that is not plain http(s) with a host is dropped.
func OpenExternal(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return
	}
	// rundll32 rather than `cmd /c start`: start would need the URL escaped
	// against cmd's own metacharacters, and it flashes a console window.
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}
