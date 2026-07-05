//go:build windows

package main

import (
	_ "embed"
	"os/exec"

	"github.com/getlantern/systray"
)

//go:embed favicon.ico
var trayIcon []byte

// runTray takes over the main thread with a system-tray icon (Windows only).
// It blocks until the server shuts down. "Open" launches the dashboard in the
// default browser; "Exit" triggers the same graceful shutdown as SIGTERM.
// A goroutine quits the tray once exitChan closes, so runTray returns after the
// server's teardown completes.
func runTray(openURL string, onExit func(), exitChan <-chan struct{}) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("llama-quartermaster")
		systray.SetTooltip("llama-quartermaster")

		mOpen := systray.AddMenuItem("Open dashboard", "Open the dashboard in your browser")
		systray.AddSeparator()
		mExit := systray.AddMenuItem("Exit", "Stop the server and quit")

		go func() {
			<-exitChan
			systray.Quit()
		}()

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					// rundll32 opens the URL without flashing a console window.
					_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", openURL).Start()
				case <-mExit.ClickedCh:
					onExit()
				}
			}
		}()
	}, nil)
}
