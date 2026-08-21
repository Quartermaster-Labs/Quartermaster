//go:build windows

package nativewin

import "unsafe"

const (
	swShowMaximized = 3

	// MonitorFromRect with DEFAULTTONULL returns 0 when a rect touches no
	// display at all, which is the whole point: it is how a saved position on a
	// monitor that has since been unplugged is detected.
	monitorDefaultToNull = 0

	// A window may not be restored smaller than this. A saved rect can be
	// nonsense -- a 0x0 from a race during shutdown, or a size from a display
	// scaling the user has since changed -- and a window too small to show its
	// own title bar is one the user cannot resize back.
	minRestoreW = 640
	minRestoreH = 480
)

var (
	getWindowPlacement = user32.NewProc("GetWindowPlacement")
	monitorFromRect    = user32.NewProc("MonitorFromRect")
)

// windowPlacement is WINDOWPLACEMENT. length must be set before the call or the
// API rejects it -- that field is how Windows versions the struct.
type windowPlacement struct {
	length           uint32
	flags            uint32
	showCmd          uint32
	ptMinPosition    struct{ X, Y int32 }
	ptMaxPosition    struct{ X, Y int32 }
	rcNormalPosition winRect
}

// Placement is where a window was last time, in a form worth writing to disk.
type Placement struct {
	X, Y, W, H int32
	Maximized  bool
}

// GetPlacement reads the window's position for saving.
//
// GetWindowPlacement rather than GetWindowRect, because the two disagree in
// exactly the case that matters: for a maximised window GetWindowRect returns
// the maximised rect, so saving it would lose the size to restore DOWN to, and
// un-maximising after a restart would snap the window to the whole screen.
// rcNormalPosition is always the restored rect regardless of current state.
//
// The rect is in workspace coordinates (taskbar excluded), which is also what
// ApplyPlacement feeds back to SetWindowPos, so the round trip is exact.
func GetPlacement(hwnd uintptr) (Placement, bool) {
	if hwnd == 0 {
		return Placement{}, false
	}
	var wp windowPlacement
	wp.length = uint32(unsafe.Sizeof(wp))
	ok, _, _ := getWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	if ok == 0 {
		return Placement{}, false
	}
	// showCmd alone is not enough: hide-to-tray leaves it reading SW_HIDE, and
	// the placement is read after the window has been hidden, so a maximised
	// window would come back restored. IsZoomed reads the WS_MAXIMIZE style bit,
	// which ShowWindow(SW_HIDE) does not clear.
	zoomed, _, _ := isZoomed.Call(hwnd)
	r := wp.rcNormalPosition
	return Placement{
		X:         r.Left,
		Y:         r.Top,
		W:         r.Right - r.Left,
		H:         r.Bottom - r.Top,
		Maximized: wp.showCmd == swShowMaximized || zoomed != 0,
	}, true
}

// ApplyPlacement puts a window back where it was, refusing anything that would
// strand it.
//
// Two ways a saved placement goes bad between runs, both silent and both
// leaving the user with a window they cannot reach: the monitor it was on is
// gone (a laptop undocked), or the size is degenerate. Position and size are
// therefore validated SEPARATELY -- a window whose old monitor vanished keeps
// its remembered size and simply opens where the library centred it, which is
// a better answer than either ignoring the whole record or moving it to a
// corner of a display it never lived on.
//
// Minimised is deliberately never restored. A process that starts up already
// minimised looks like one that failed to start.
func ApplyPlacement(hwnd uintptr, p Placement) {
	if hwnd == 0 {
		return
	}
	sizeOK := p.W >= minRestoreW && p.H >= minRestoreH
	if sizeOK {
		r := winRect{Left: p.X, Top: p.Y, Right: p.X + p.W, Bottom: p.Y + p.H}
		if onScreen(r) {
			_, _, _ = setWindowPos.Call(hwnd, 0,
				uintptr(p.X), uintptr(p.Y), uintptr(p.W), uintptr(p.H),
				swpNoZOrder|swpNoActivate)
		} else {
			// Size only; SWP_NOMOVE leaves the centred position alone.
			_, _, _ = setWindowPos.Call(hwnd, 0, 0, 0, uintptr(p.W), uintptr(p.H),
				swpNoMove|swpNoZOrder|swpNoActivate)
		}
	}
	if p.Maximized {
		showWindow(hwnd, swMaximize)
	}
}

// onScreen reports whether a rect overlaps any attached display.
func onScreen(r winRect) bool {
	h, _, _ := monitorFromRect.Call(uintptr(unsafe.Pointer(&r)), monitorDefaultToNull)
	return h != 0
}
