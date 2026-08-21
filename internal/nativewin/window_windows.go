//go:build windows

package nativewin

import (
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

// Win32 bits used to reshape and drive the window. Declared here rather than
// pulled from a helper package because this is the only file that needs them,
// and the list is short enough that a dependency would cost more than it saves.
const (
	gwlStyle    = -16
	gwlpWndProc = -4

	wsPopup        = 0x80000000
	wsThickFrame   = 0x00040000
	wsMinimizeBox  = 0x00020000
	wsMaximizeBox  = 0x00010000
	wsClipChildren = 0x02000000
	wsVisible      = 0x10000000

	swpFrameChanged = 0x0020
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010

	wmNcLButtonDown = 0x00A1
	htCaption       = 2
	wmSize          = 0x0005
	wmNcCalcSize    = 0x0083
	wmClose         = 0x0010

	// Windows 11 draws a 1px border around every window in the system accent
	// or a light grey. Around a chrome-less dark window that border is the
	// stray light line at the edge; DWMWA_COLOR_NONE removes it.
	dwmwaBorderColor  = 34
	dwmwaCaptionColor = 35
	dwmwaColorNone    = 0xFFFFFFFE

	swHide     = 0
	swMinimize = 6
	swMaximize = 3
	swRestore  = 9
	swShow     = 5
)

var (
	user32  = syscall.NewLazyDLL("user32.dll")
	dwmapi  = syscall.NewLazyDLL("dwmapi.dll")
	dwmAttr = dwmapi.NewProc("DwmSetWindowAttribute")

	messageBoxW       = user32.NewProc("MessageBoxW")
	getWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	callWindowProcW   = user32.NewProc("CallWindowProcW")
	setWindowPos      = user32.NewProc("SetWindowPos")
	showWindowProc    = user32.NewProc("ShowWindow")
	setForegroundWin  = user32.NewProc("SetForegroundWindow")
	releaseCapture    = user32.NewProc("ReleaseCapture")
	sendMessageW      = user32.NewProc("SendMessageW")
	postMessageW      = user32.NewProc("PostMessageW")
	isZoomed          = user32.NewProc("IsZoomed")
	isWindowVisible   = user32.NewProc("IsWindowVisible")
)

// Options tunes what Attach sets up. The zero value is the wizard's behaviour:
// a frameless window whose close button destroys it.
type Options struct {
	// HideOnClose turns the close button (and the page's own close verb) into
	// "hide", leaving the webview warm so reopening from a tray icon is
	// instant instead of a cold WebView2 start. The caller is then responsible
	// for ever showing it again -- a hidden window with no tray icon is a
	// process the user cannot reach.
	HideOnClose bool

	// OnClose replaces the default close action for the page's qmClose
	// binding. nil terminates the webview, which is what the wizard wants.
	OnClose func()
}

// state is package scope because a process has exactly one of these windows:
// the wizard is a program that shows one window and exits, and the app window
// is the single main window of the server process. Two would need this keyed
// by hwnd, and a second window is not a thing either program wants.
var (
	prevWndProc uintptr
	opts        Options
)

// Attach gives w the chrome-less look the page draws its own title bar into,
// binds the title-bar verbs the page needs, and returns the window handle.
//
// It must be called on the thread that created w and that will pump its
// message loop; every binding it registers marshals back onto that thread.
// Bindings are registered here rather than by the caller because go-webview2
// turns each one into a document-creation init script, so they have to exist
// before Navigate -- and callers that forget get a page whose title bar
// silently does nothing.
func Attach(w webview2.WebView, o Options) uintptr {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return 0
	}
	opts = o

	ApplyIcon(hwnd)
	frameless(hwnd)

	// Dispatch rather than a direct call: a binding runs on the UI thread from
	// inside a WebView2 event handler, and both dragging and closing enter a
	// modal loop of their own. Posting them keeps the handler shallow.
	_ = w.Bind("qmDrag", func() { w.Dispatch(func() { Drag(hwnd) }) })
	_ = w.Bind("qmMinimize", func() { w.Dispatch(func() { showWindow(hwnd, swMinimize) }) })
	_ = w.Bind("qmMaximize", func() { w.Dispatch(func() { ToggleMaximize(hwnd) }) })
	_ = w.Bind("qmClose", func() {
		w.Dispatch(func() {
			switch {
			case opts.OnClose != nil:
				opts.OnClose()
			default:
				w.Terminate()
			}
		})
	})
	// Returns "" when the user cancels, which the page treats as "keep what is
	// in the box" -- a cancelled picker must not blank a path already typed.
	_ = w.Bind("qmPickFolder", func(title, start string) (string, error) {
		return PickFolder(hwnd, title, start)
	})
	// Not dispatched: OpenExternal only spawns a process, and going through the
	// UI thread would put a browser launch behind whatever is rendering.
	_ = w.Bind("qmOpenExternal", func(u string) { OpenExternal(u) })
	// The page tells us what colour its title bar came out, because only it
	// knows the theme -- see SetCaptionColor.
	_ = w.Bind("qmCaptionColor", func(r, g, b int) { SetCaptionColor(hwnd, r, g, b) })
	return hwnd
}

// frameless turns the library's WS_OVERLAPPEDWINDOW into a window the page can
// draw its own header into.
//
// WS_THICKFRAME survives on purpose. It is the non-client area, so it costs no
// pixels the page can see, and it is what keeps edge resizing, Aero snap and
// the maximise animation working -- all of which a plain WS_POPUP silently
// loses. WS_MINIMIZEBOX/WS_MAXIMIZEBOX are likewise kept for the taskbar's
// right-click menu and for Win+Down, even though nothing draws them.
func frameless(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	style := uintptr(wsPopup | wsThickFrame | wsMinimizeBox | wsMaximizeBox |
		wsClipChildren | wsVisible)
	_, _, _ = setWindowLongPtrW.Call(hwnd, winIndex(gwlStyle), style)

	subclass(hwnd)
	// SWP_FRAMECHANGED is what makes Windows recompute the non-client area;
	// without it the caption stays on screen until something else resizes us.
	// It is sent after the subclass is installed so one recompute covers both
	// the style change and the WM_NCCALCSIZE override.
	_, _, _ = setWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate)

	// Dropping the caption grows the CLIENT area by its height while the window
	// itself keeps its size, so no WM_SIZE is generated and the embedded browser
	// stays laid out for the smaller rect it was created with. The window class
	// registers no background brush, so the strip of client area the browser no
	// longer covers is left holding whatever was last painted there -- the pale
	// bar along the edge. The library's WM_SIZE handler refits the browser from
	// GetClientRect and ignores the message parameters, so sending it a bare one
	// is the whole fix.
	_, _, _ = sendMessageW.Call(hwnd, wmSize, 0, 0)

	// Best effort: DWMWA_BORDER_COLOR is Windows 11 22000+, and on anything
	// older the call fails and leaves the border as it was.
	color := uint32(dwmwaColorNone)
	_, _, _ = dwmAttr.Call(hwnd, dwmwaBorderColor,
		uintptr(unsafe.Pointer(&color)), unsafe.Sizeof(color))
}

// SetCaptionColor dyes the window frame, which on this window is visible in
// exactly two pixels.
//
// Windows 11 rounds the window's corners, and DWM draws the frame under that
// rounded mask. With the top inset taken to 0 the client area covers the whole
// top edge -- except where the corner arc meets it, where a 1-2px stub of frame
// survives the mask and reads as a small white dot in each top corner. Measured
// by screenshotting the composited corners: DWMWCP_DONOTROUND removes both dots
// (at the price of square corners), giving the top edge 1px of non-client back
// turns each dot into a full white line across the top, and DWMWA_BORDER_COLOR
// does not touch them -- it colours the outline, which is already COLOR_NONE.
// Painting the frame the same colour as the title bar is what makes them
// disappear while keeping the platform's rounded corners.
//
// It has to come from the page: the frame colour has to match whatever the
// title bar actually rendered, and the theme lives in the browser's storage,
// not here. Until the page calls, the dots are the system default -- two pixels
// for the fraction of a second before the first paint.
func SetCaptionColor(hwnd uintptr, r, g, b int) {
	if hwnd == 0 {
		return
	}
	clamp := func(v int) uint32 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint32(v)
	}
	// COLORREF is 0x00BBGGRR, not RGB.
	color := clamp(r) | clamp(g)<<8 | clamp(b)<<16
	_, _, _ = dwmAttr.Call(hwnd, dwmwaCaptionColor,
		uintptr(unsafe.Pointer(&color)), unsafe.Sizeof(color))
}

// winRect is the Win32 RECT. Win32 rects are half-open and in screen
// coordinates here, which is what WM_NCCALCSIZE hands us.
type winRect struct{ Left, Top, Right, Bottom int32 }

// ncCalcSizeParams is NCCALCSIZE_PARAMS. Only rgrc[0] -- the proposed client
// rect -- is read or written; the other two and lppos are along for the ride
// because the struct is passed by pointer and has to match in size and layout.
type ncCalcSizeParams struct {
	rgrc  [3]winRect
	lppos uintptr
}

// subclass displaces the window procedure so we can answer two messages the
// library does not.
//
// WM_NCCALCSIZE removes the last 7 pixels of default window header. Clearing
// WS_CAPTION takes the title bar but NOT the frame's top edge: DWM draws a
// WS_THICKFRAME window's top border at SM_CYSIZEFRAME+SM_CXPADDEDBORDER (4+4 on
// a 100% display) while the other three edges get 1px. Measured on a stripped
// window: top=7 left=1 right=1 bottom=1. That asymmetric strip is the pale bar
// above the page, and DWMWA_BORDER_COLOR does not touch it -- that attribute
// colours the 1px outline, not the frame it surrounds. Letting the default
// handler inset all four edges and then putting the top back where it started
// is what reclaims it.
//
// The cost is the top edge as a resize handle. It cannot be given back with
// WM_NCHITTEST either: with no non-client area up there the hit test never
// reaches this window at all -- the WebView2 child owns those pixels and eats
// the mouse. Three edges, all four corners, Aero snap and maximise still work.
//
// WM_CLOSE is the close-to-tray hook; see Options.HideOnClose.
func subclass(hwnd uintptr) {
	if prevWndProc != 0 {
		return
	}
	prev, _, _ := getWindowLongPtrW.Call(hwnd, winIndex(gwlpWndProc))
	if prev == 0 {
		return
	}
	prevWndProc = prev
	_, _, _ = setWindowLongPtrW.Call(hwnd, winIndex(gwlpWndProc),
		syscall.NewCallback(wndProc))
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmClose:
		// Returning without chaining is what suppresses the destroy: the
		// window stays alive with its page loaded, so the next Show is
		// instant.
		if opts.HideOnClose {
			Hide(hwnd)
			return 0
		}

	case wmNcCalcSize:
		// wparam == 0 means the simple form, where lparam is a bare RECT and
		// there is nothing to preserve. Maximised is left alone on purpose: a
		// zoomed window's rect deliberately overhangs the work area by the
		// frame thickness, so the default inset is what lands the client
		// exactly on the screen edge. Overriding it there pushes the top of
		// the page off-screen.
		if wparam != 0 && lparam != 0 {
			if zoomed, _, _ := isZoomed.Call(hwnd); zoomed == 0 {
				p := (*ncCalcSizeParams)(unsafe.Pointer(lparam))
				top := p.rgrc[0].Top
				ret, _, _ := callWindowProcW.Call(prevWndProc, hwnd, msg, wparam, lparam)
				p.rgrc[0].Top = top
				return ret
			}
		}
	}
	ret, _, _ := callWindowProcW.Call(prevWndProc, hwnd, msg, wparam, lparam)
	return ret
}

// Drag hands the mouse to the window manager mid-click, which is how a custom
// title bar moves a window: ReleaseCapture drops the webview's grab, and
// WM_NCLBUTTONDOWN/HTCAPTION tells Windows to treat what follows as a drag of
// the caption it is no longer drawing.
func Drag(hwnd uintptr) {
	_, _, _ = releaseCapture.Call()
	_, _, _ = sendMessageW.Call(hwnd, wmNcLButtonDown, htCaption, 0)
}

// ToggleMaximize is the double-click-the-title-bar verb.
func ToggleMaximize(hwnd uintptr) {
	zoomed, _, _ := isZoomed.Call(hwnd)
	if zoomed != 0 {
		showWindow(hwnd, swRestore)
		return
	}
	showWindow(hwnd, swMaximize)
}

// Show makes a hidden window visible and puts it in front, which is what a
// tray "Open" has to do: SW_SHOW alone can leave it behind whatever the user
// was looking at.
func Show(hwnd uintptr) {
	showWindow(hwnd, swShow)
	_, _, _ = setForegroundWin.Call(hwnd)
}

// Hide removes the window from the screen and the taskbar without destroying
// it. Safe to call from any thread.
func Hide(hwnd uintptr) { showWindow(hwnd, swHide) }

// Visible reports whether the window is on screen.
func Visible(hwnd uintptr) bool {
	v, _, _ := isWindowVisible.Call(hwnd)
	return v != 0
}

// PostClose asks the window to close from another thread, taking whichever
// path Options.HideOnClose selected. Posted rather than sent so the caller
// does not block on the window thread.
func PostClose(hwnd uintptr) { _, _, _ = postMessageW.Call(hwnd, wmClose, 0, 0) }

func showWindow(hwnd, cmd uintptr) { _, _, _ = showWindowProc.Call(hwnd, cmd) }

// winIndex widens a negative Win32 index (GWL_*, GCLP_*) to the uintptr a
// syscall argument wants. It has to pass through a variable: converting a
// negative CONSTANT to uintptr is a compile-time overflow, while converting a
// negative int32 VALUE is the two's-complement widening these APIs expect.
func winIndex(i int32) uintptr { return uintptr(i) }

// MessageBox puts a message on screen. A -H=windowsgui binary has no console,
// so this is the only channel that reaches a user who double-clicked it.
func MessageBox(title, msg string) {
	const mbIconError = 0x00000010
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	body, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	_, _, _ = messageBoxW.Call(0,
		uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(t)), mbIconError)
}
