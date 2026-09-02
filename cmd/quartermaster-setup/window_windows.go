//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	webview2 "github.com/jchv/go-webview2"

	"github.com/quartermaster-labs/quartermaster/internal/nativewin"
)

// Window geometry. Sized like an installer dialog rather than a browser: the
// whole point of the native window is that it does not read as a web page.
const (
	winWidth  = 940
	winHeight = 660
)

// runWindow shows the wizard in a frameless WebView2 window and blocks until
// either the user closes it or the wizard signals it is finished.
//
// An error here is expected, not exceptional: WebView2 ships with Windows 11
// and with Edge on Windows 10, but Server SKUs and stripped images may lack it.
// Every failure mode returns an error so main can fall back to the browser.
func runWindow(url string, done <-chan struct{}) (err error) {
	// go-webview2 reports a missing runtime by returning nil, but the COM
	// plumbing underneath can also panic on a half-registered install. Both mean
	// the same thing to us -- no window -- so both become the same error.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("webview2 failed: %v", r)
		}
	}()

	// Before the window exists: a window's DPI awareness is fixed when it is
	// created. The wizard is the first thing a user ever sees of this program,
	// and on a scaled laptop panel an unaware one is visibly blurry.
	nativewin.EnableDPIAwareness()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("QM_SETUP_DEBUG") != "",
		AutoFocus: true,
		// WebView2 needs a writable profile directory and defaults to one
		// beside the executable. The setup binary is typically run straight out
		// of Downloads, or from a read-only mount, so it is pointed at TEMP
		// instead -- a failure to create the profile is a failure to show a
		// window at all.
		DataPath: filepath.Join(os.TempDir(), "quartermaster-setup-webview2"),
		WindowOptions: webview2.WindowOptions{
			// Still set even though no caption is drawn: this is what Alt-Tab,
			// the taskbar preview and any window-list tool show.
			Title: "Quartermaster Setup",
			// Physical pixels now that the process is aware; the constants stay
			// the size the layout was designed at. See nativewin.Px.
			Width:  uint(nativewin.Px(winWidth)),
			Height: uint(nativewin.Px(winHeight)),
			Center: true,
		},
	})
	if w == nil {
		return errors.New("the WebView2 runtime is not installed")
	}
	defer w.Destroy()

	// Zero Options: the wizard's close button really does close it. Hiding to a
	// tray would strand a process with no icon to reach it by -- the wizard has
	// no tray.
	nativewin.Attach(w, nativewin.Options{})

	// HintMin, not HintFixed: the wizard's content is a normal responsive
	// layout, and a user on a 125% display who cannot resize is stuck with
	// whatever the fixed size clips.
	w.SetSize(nativewin.Px(winWidth), nativewin.Px(winHeight), webview2.HintMin)
	w.Navigate(url)

	// Close the window when the wizard says it is done. The goroutine is joined
	// through stopped so it cannot outlive Run when the user closes the window
	// first -- Dispatch on a destroyed webview is a use-after-free.
	stopped := make(chan struct{})
	go func() {
		select {
		case <-done:
			w.Dispatch(w.Terminate)
		case <-stopped:
		}
	}()

	w.Run()
	close(stopped)
	return nil
}

// showError puts a message on screen. The binary is built -H=windowsgui, so
// this is the only channel that reaches a user who double-clicked it.
func showError(msg string) { nativewin.MessageBox("Quartermaster Setup", msg) }
