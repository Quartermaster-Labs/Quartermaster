package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser points the user's default browser at the wizard.
//
// On Windows this goes through rundll32 rather than `cmd /c start`: `start`
// treats its first quoted argument as a window title, so a URL containing an
// ampersand either loses everything after it or opens a blank window, and the
// workarounds are all quoting folklore. url.dll's FileProtocolHandler takes the
// URL as a plain argument and hands it to the registered handler.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", url, err)
	}
	// Not waited on: xdg-open in particular can block for the lifetime of the
	// browser it spawns, and the wizard's own Done channel is what tells us
	// when to exit.
	go func() { _ = cmd.Wait() }()
	return nil
}
