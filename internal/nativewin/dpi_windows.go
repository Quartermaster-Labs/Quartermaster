//go:build windows

package nativewin

// Per-monitor DPI awareness.
//
// Without it Windows renders the process at 96 DPI and bitmap-stretches the
// result to the display's scale: the window is the right SIZE on a 125% or
// 150% laptop, and everything in it is blurry. That is the whole bug this file
// exists to fix, and it is entirely invisible on a 100% display, which is why
// it survived this long.
//
// Awareness is process-wide and one-way, and it changes three things at once:
//
//  1. every dimension the process gives or receives becomes PHYSICAL pixels, so
//     a 940-wide window asked for at 150% comes out two thirds the intended
//     size unless the caller scales it (Px);
//  2. GetSystemMetrics stops answering for the display the window is on, so the
//     frame maths in window_windows.go has to ask the ...ForDpi variants
//     instead (systemMetric);
//  3. dragging between monitors of different scales now delivers WM_DPICHANGED,
//     which the process must act on or the window keeps its old physical size
//     and the page inside it visibly changes scale.
//
// All three are handled here and in window_windows.go. WebView2 itself needs no
// help: once the process is aware, it renders the page at the real device scale
// and the CSS layout follows deviceScaleFactor as it does in a browser.

import (
	"sync"
	"syscall"
	"unsafe"
)

const (
	wmDpiChanged = 0x02E0

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, which is -4 as a handle.
	// V2 rather than V1 because only V2 DPI-scales the non-client frame and
	// sends WM_DPICHANGED for the whole top-level tree, which is what keeps the
	// frame insets in wndProc consistent with the rects it is handed.
	dpiPerMonitorV2 = ^uintptr(3)

	// PROCESS_PER_MONITOR_DPI_AWARE, the Windows 8.1 spelling of the same idea.
	processPerMonitorDPIAware = 2

	logPixelsX = 88 // GetDeviceCaps index

	defaultDPI = 96
)

var (
	shcore = syscall.NewLazyDLL("shcore.dll")

	setProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	setProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	setProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	getDpiForSystem               = user32.NewProc("GetDpiForSystem")
	getDpiForWindow               = user32.NewProc("GetDpiForWindow")
	getSystemMetricsForDpi        = user32.NewProc("GetSystemMetricsForDpi")
	getDC                         = user32.NewProc("GetDC")
	releaseDC                     = user32.NewProc("ReleaseDC")
	getDeviceCaps                 = gdi32.NewProc("GetDeviceCaps")

	dpiOnce sync.Once
)

// EnableDPIAwareness makes this process per-monitor DPI aware.
//
// It MUST run before the first window exists: awareness is fixed for a window
// when it is created, so a call after webview2.NewWithOptions leaves the window
// that matters behind, aware process or not. Idempotent, and safe to call from
// more than one entry point, because both binaries that show a window want it
// and neither should have to know whether the other already asked.
//
// Every failure is silent and harmless. Three APIs are tried oldest-last: the
// per-monitor-v2 context (Windows 10 1703+), the 8.1 per-monitor enum, then the
// original system-wide switch. If all three fail -- an OS older than any of
// them, or an embedded manifest that already fixed the awareness and makes the
// call ERROR_ACCESS_DENIED -- the process simply stays where it was, which is
// today's behaviour: blurry, not broken.
func EnableDPIAwareness() {
	dpiOnce.Do(func() {
		if setProcessDpiAwarenessContext.Find() == nil {
			if ok, _, _ := setProcessDpiAwarenessContext.Call(dpiPerMonitorV2); ok != 0 {
				return
			}
		}
		if setProcessDpiAwareness.Find() == nil {
			if hr, _, _ := setProcessDpiAwareness.Call(processPerMonitorDPIAware); hr == 0 {
				return
			}
		}
		_, _, _ = setProcessDPIAware.Call()
	})
}

// SystemDPI is the DPI a window will be created at, before there is a window to
// ask about. It is the primary display's, which is where a Center:true window
// lands; a second monitor at a different scale is corrected by WM_DPICHANGED
// the moment the window arrives there.
func SystemDPI() int {
	if getDpiForSystem.Find() == nil {
		if dpi, _, _ := getDpiForSystem.Call(); dpi != 0 {
			return int(dpi)
		}
	}
	// Windows 8.1 and older, and the belt-and-braces path if the call above
	// ever returns 0: the screen DC's pixels-per-inch is the same number.
	hdc, _, _ := getDC.Call(0)
	if hdc == 0 {
		return defaultDPI
	}
	defer releaseDC.Call(0, hdc)
	if dpi, _, _ := getDeviceCaps.Call(hdc, logPixelsX); dpi != 0 {
		return int(dpi)
	}
	return defaultDPI
}

// windowDPI is the DPI of the display a window is currently on.
func windowDPI(hwnd uintptr) int {
	if hwnd != 0 && getDpiForWindow.Find() == nil {
		if dpi, _, _ := getDpiForWindow.Call(hwnd); dpi != 0 {
			return int(dpi)
		}
	}
	return SystemDPI()
}

// Px converts a dimension written at 96 DPI into the physical pixels an aware
// process has to ask for.
//
// This is what keeps a window size in the source readable: the constants stay
// the size the design was drawn at, and the one place that talks to Win32
// converts. Rounding is to the nearest pixel, so 940 at 150% is 1410 rather
// than 1409.
func Px(n int) int {
	return (n*SystemDPI() + defaultDPI/2) / defaultDPI
}

// systemMetric reads a system metric for the display hwnd is on.
//
// GetSystemMetrics answers for the process's awareness context, which for a
// per-monitor-aware process means the PRIMARY display: on a laptop at 150%
// driving an external 100% panel it returns the wrong frame thickness for
// whichever of the two the window is not on. GetSystemMetricsForDpi is the
// per-monitor spelling, and falls back to the old call on Windows 8.1.
func systemMetric(hwnd uintptr, index int) int {
	if getSystemMetricsForDpi.Find() == nil {
		v, _, _ := getSystemMetricsForDpi.Call(uintptr(index), uintptr(windowDPI(hwnd)))
		return int(int32(v))
	}
	v, _, _ := getSystemMetrics.Call(uintptr(index))
	return int(int32(v))
}

// applyDpiChange moves and resizes the window to the rect Windows suggests when
// it crosses onto a display with a different scale.
//
// The suggested rect is not advice to be improved on: it preserves the window's
// apparent size to the user, and ignoring it leaves a window that keeps its old
// physical size while the page inside it re-lays out at the new scale, which
// looks exactly like the app resizing itself at random.
func applyDpiChange(hwnd, lparam uintptr) {
	if lparam == 0 {
		return
	}
	r := (*winRect)(unsafe.Pointer(lparam))
	_, _, _ = setWindowPos.Call(hwnd, 0,
		uintptr(r.Left), uintptr(r.Top),
		uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
		swpNoZOrder|swpNoActivate)
}
