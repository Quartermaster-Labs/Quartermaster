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
// It blocks until the server shuts down. "Exit" triggers the same graceful
// shutdown as SIGTERM. A goroutine quits the tray once exitChan closes, so
// runTray returns after the server's teardown completes.
//
// onOpenApp, when non-nil, adds an "Open app" item above the browser one and
// makes it the default: with a native window running, the browser becomes the
// secondary way in rather than the only one. It is nil when there is no window
// (no -app, or WebView2 missing), and then the menu is exactly what it was.
func runTray(openURL string, onOpenApp func(), onExit func(), exitChan <-chan struct{}) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("quartermaster")
		systray.SetTooltip("quartermaster")

		var mApp *systray.MenuItem
		if onOpenApp != nil {
			mApp = systray.AddMenuItem("Open Quartermaster", "Bring the app window back")
		}
		mOpen := systray.AddMenuItem("Open dashboard in browser", "Open the dashboard in your browser")
		systray.AddSeparator()
		mExit := systray.AddMenuItem("Exit", "Stop the server and quit")

		go func() {
			<-exitChan
			systray.Quit()
		}()

		// A nil channel blocks forever in a select, which is exactly the
		// behaviour wanted when there is no app-window item to click.
		var appClicks chan struct{}
		if mApp != nil {
			appClicks = mApp.ClickedCh
		}

		go func() {
			for {
				select {
				case <-appClicks:
					onOpenApp()
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
