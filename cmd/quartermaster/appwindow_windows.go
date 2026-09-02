//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/quartermaster-labs/quartermaster/internal/nativewin"
)

// Default window geometry. Wider than the wizard's: this one holds the
// dashboard's sidebar plus a chat column, and anything narrower opens with the
// sidebar already collapsed.
const (
	appWinWidth  = 1280
	appWinHeight = 820
)

// How long the window waits for the page to say it mounted before reloading it,
// and how many times it will do that.
//
// Generous on purpose. The load being watched is the worst one the app ever
// does: a cold WebView2 profile, an empty HTTP cache and the whole dashboard
// bundle, on a laptop that has just finished running an installer. Reloading a
// page that was two seconds from painting is a bigger regression than the bug
// this catches, so the grace is set well past a slow-but-working start.
const (
	firstPaintGrace   = 12 * time.Second
	firstPaintRetries = 2
)

// appWindow is the native dashboard window: a WebView2 pointed at our own
// loopback listener. It is a FRONTEND CLIENT and nothing more -- it holds no
// state, and everything it shows comes over HTTP from the same server the
// browser talks to. Closing it stops nothing.
//
// The window owns an OS thread for its whole lifetime. That is not a style
// choice: systray.Run takes the main thread and pumps a message loop of its
// own, and go-webview2's Run wants a UI thread with a loop of its own, so the
// two cannot share. Every call into the webview from anywhere else has to
// arrive via Dispatch, which posts a message onto that thread.
type appWindow struct {
	mu   sync.Mutex
	w    webview2.WebView
	hwnd uintptr

	ready chan struct{} // closed once w/hwnd are set (or the window failed)
	done  chan struct{} // closed once Run returns and the webview is destroyed
	err   error
}

// startAppWindow opens the window on its own thread and returns immediately.
// Callers wait on Ready() before using Show or Close.
func startAppWindow(url string) *appWindow {
	aw := &appWindow{
		ready: make(chan struct{}),
		done:  make(chan struct{}),
	}
	go aw.run(url)
	return aw
}

func (aw *appWindow) run(url string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var readyOnce sync.Once
	markReady := func() { readyOnce.Do(func() { close(aw.ready) }) }
	defer close(aw.done)
	defer markReady()

	// A missing WebView2 runtime returns nil, but a half-registered install can
	// panic inside the COM plumbing instead. Both mean "no window", and neither
	// may take the server down with it -- the user still has the browser.
	defer func() {
		if r := recover(); r != nil {
			aw.err = fmt.Errorf("webview2 failed: %v", r)
		}
	}()

	// Before the window exists, and it has to be: a window's DPI awareness is
	// fixed when it is created, so this is the difference between a crisp app
	// and a bitmap-stretched one on any display that is not at 100%.
	nativewin.EnableDPIAwareness()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("QM_APP_DEBUG") != "",
		AutoFocus: true,
		// Kept out of the install directory, which may be read-only or
		// Program Files. A profile that cannot be created is a window that
		// never appears.
		DataPath: filepath.Join(appDataDir(), "webview2"),
		WindowOptions: webview2.WindowOptions{
			// No caption is drawn, but this is what Alt-Tab, the taskbar
			// preview and every window-list tool show.
			Title: "Quartermaster",
			// Px, because an aware process is talking physical pixels: the
			// constants stay the size the design was drawn at and this is the
			// one place that converts. Without it the window would come out
			// two thirds of its intended size on a 150% display.
			Width:  uint(nativewin.Px(appWinWidth)),
			Height: uint(nativewin.Px(appWinHeight)),
			Center: true,
		},
	})
	if w == nil {
		aw.err = fmt.Errorf("the WebView2 runtime is not installed")
		return
	}
	defer w.Destroy()

	aw.mu.Lock()
	aw.w = w
	// HideOnClose, and no OnClose override: the X hides the window and leaves
	// the server running, which is what a tray app means. Quitting is the
	// tray's Exit, deliberately -- closing a window must not stop model
	// downloads or evict what is loaded.
	aw.hwnd = nativewin.Attach(w, nativewin.Options{HideOnClose: true})
	aw.mu.Unlock()

	// Applied AFTER Attach, so it lands on the frameless window: reshaping
	// afterwards would recompute the non-client area and move what we just set.
	if p, ok := loadPlacement(); ok {
		nativewin.ApplyPlacement(aw.hwnd, p)
	}

	// The page's own "I am up" signal, bound BEFORE the navigation so it is
	// installed in the document that navigation creates. It carries no data and
	// means one thing: the bundle executed and Svelte mounted, so whatever the
	// window is showing, it is not blank.
	painted := make(chan struct{})
	var paintedOnce sync.Once
	_ = w.Bind("qmAppReady", func() { paintedOnce.Do(func() { close(painted) }) })

	// HintMin, not HintFixed: the dashboard is a responsive layout and a user
	// who cannot resize is stuck with whatever the default clips.
	w.SetSize(nativewin.Px(appWinWidth), nativewin.Px(appWinHeight), webview2.HintMin)
	w.Navigate(url)
	markReady()

	// WebView2 gives a failed navigation no reload button and no retry, so a
	// first load that goes wrong -- refused, hung, or a bundle that threw before
	// it mounted -- is a white window for the life of the process. That is
	// exactly what the first launch straight out of the installer showed, and
	// why closing it from the tray and starting again "fixed" it: the second
	// process navigated a second time. This is that second navigation, without
	// the restart.
	//
	// close(stopWatch) is registered AFTER the deferred Destroy, so it runs
	// FIRST: dispatching onto a webview that has already been destroyed is a
	// use-after-free, and the watchdog is the one thing here that outlives Run.
	stopWatch := make(chan struct{})
	go watchFirstPaint(w, url, painted, stopWatch)
	defer close(stopWatch)

	w.Run() // blocks, pumping THIS thread's message loop, until Terminate

	// Saved here rather than on every WM_MOVE/WM_SIZE: those fire continuously
	// during a drag, and a disk write per frame to record a position the user is
	// still choosing is pure waste. Run has returned, so this is the last state
	// the window was in, read on the thread that owns it.
	savePlacement(aw.hwnd)
}

// watchFirstPaint reloads the page if it never reported that it mounted.
//
// Deliberately blind to WHY. NavigationCompleted is not exposed by
// go-webview2, and the failures worth surviving here do not all show up as a
// failed navigation anyway -- a document that loads and then throws is a
// perfectly successful navigation and an equally white window. What the page
// itself can say is that it got as far as mounting, so that is the signal, and
// its absence is the fault condition regardless of cause.
func watchFirstPaint(w webview2.WebView, url string, painted, stop <-chan struct{}) {
	for i := 0; i < firstPaintRetries; i++ {
		select {
		case <-painted:
			return
		case <-stop:
			return
		case <-time.After(firstPaintGrace):
		}
		select {
		case <-stop:
			return
		default:
		}
		w.Dispatch(func() { w.Navigate(url) })
	}
}

// placementFile is small enough that a torn write only costs the window its
// remembered position -- loadPlacement treats an unreadable or malformed file
// as "no saved placement", which is the same path a first run takes.
func placementFile() string { return filepath.Join(appDataDir(), "window.json") }

func loadPlacement() (nativewin.Placement, bool) {
	var p nativewin.Placement
	b, err := os.ReadFile(placementFile())
	if err != nil || json.Unmarshal(b, &p) != nil {
		return p, false
	}
	// No dpi field means the file was written by a build that was not DPI-aware,
	// so the rect is in virtualized 96-DPI units and restoring it verbatim would
	// open a two-thirds-size window on a 150% display. Discarding it costs the
	// user one centred window, once, and is right at every scale; rescaling it
	// would not be, because the position is in virtual-desktop coordinates that
	// moved when the process became aware.
	if p.DPI == 0 {
		return nativewin.Placement{}, false
	}
	return p, true
}

func savePlacement(hwnd uintptr) {
	p, ok := nativewin.GetPlacement(hwnd)
	if !ok {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	// Best effort throughout: a window that cannot remember where it was is a
	// papercut, and this runs during shutdown where there is nobody to tell.
	_ = os.WriteFile(placementFile(), b, 0o644)
}

// Ready blocks until the window is up, and reports why it is not if it failed.
func (aw *appWindow) Ready() error {
	<-aw.ready
	return aw.err
}

// Show brings the window back from the tray. Safe from any thread: the work is
// dispatched onto the window's own, because SetForegroundWindow from a thread
// that owns no window is one of the calls Windows quietly ignores.
func (aw *appWindow) Show() {
	aw.mu.Lock()
	w, hwnd := aw.w, aw.hwnd
	aw.mu.Unlock()
	if w == nil || hwnd == 0 {
		return
	}
	w.Dispatch(func() { nativewin.Show(hwnd) })
}

// Close tears the window down for real and waits for its thread to release.
// Called during shutdown, after which the process is exiting anyway.
func (aw *appWindow) Close() {
	aw.mu.Lock()
	w := aw.w
	aw.mu.Unlock()
	if w == nil {
		return
	}
	w.Dispatch(w.Terminate)
	<-aw.done
}

// appDataDir is where the window keeps per-user state that is not the user's
// to edit: the WebView2 profile, and later the saved geometry. Deliberately
// NOT beside the executable, unlike the config and generate files -- an install
// under Program Files is not writable, and a profile that cannot be created is
// a window that never appears. os.UserCacheDir is %LOCALAPPDATA% on Windows;
// TEMP is the fallback for the rare account that has neither.
func appDataDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Quartermaster")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
