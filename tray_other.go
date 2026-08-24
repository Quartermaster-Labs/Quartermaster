//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

// runTray has no system-tray implementation off Windows (avoids the GTK/CGO
// dependency on Linux builds). It just blocks until shutdown.
func runTray(openURL string, onOpenApp func(), onExit func(), exitChan <-chan struct{}) {
	<-exitChan
}

// openInBrowser hands the dashboard URL to the desktop's URL handler. This is
// the whole fallback off Windows: there is no native window, so the browser is
// not a second-best way in, it is the only one.
func openInBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// Not waited on: xdg-open can block for the lifetime of the browser it
	// starts, and nothing here cares whether it succeeded.
	_ = cmd.Start()
}
