//go:build windows

package nativewin

import "syscall"

// Icon plumbing.
//
// The .exe carries its icon as a Win32 resource (see versioninfo.json, compiled
// into resource_windows_amd64.syso by `make versioninfo-setup`). Explorer picks
// that up on its own; the WINDOW does not. A window's icon comes from its class
// or from WM_SETICON, and go-webview2 registers its class with IDI_APPLICATION,
// so without this the taskbar button and Alt-Tab entry show the generic Windows
// icon while the file on disk shows ours.
const (
	imageIcon   = 1
	lrShared    = 0x8000
	wmSetIcon   = 0x0080
	iconSmall   = 0
	iconBig     = 1
	gclpHIcon   = -14
	gclpHIconSm = -34
	smCXIcon    = 11
	smCYIcon    = 12
	smCXSmIcon  = 49
	smCYSmIcon  = 50

	// The id goversioninfo gives the RT_GROUP_ICON it writes from -icon.
	// RT_ICON (the individual frames) is a different resource TYPE with its own
	// id space, so however many sizes the .ico carries, the group stays 1.
	// Verified against the generated resource_windows_amd64.syso.
	iconResourceID = 1
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	getModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	loadImageW       = user32.NewProc("LoadImageW")
	setClassLongPtrW = user32.NewProc("SetClassLongPtrW")
	getSystemMetrics = user32.NewProc("GetSystemMetrics")
)

// applyWindowIcon points the window at the icon compiled into this binary.
//
// Both sizes are set explicitly rather than letting Windows scale one: the big
// icon is the Alt-Tab and taskbar-preview one, the small icon is the taskbar
// button, and a downscaled 256px icon in a 16px slot is visibly mushy next to
// the properly authored 16px frame that is already in the .ico.
//
// Silently doing nothing is the correct failure mode. A dev build has no .syso,
// and a window with the default icon is a cosmetic problem, not a reason to
// refuse to open.
func ApplyIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	hinst, _, _ := getModuleHandleW.Call(0)
	big := loadIconResource(hinst, smCXIcon, smCYIcon)
	small := loadIconResource(hinst, smCXSmIcon, smCYSmIcon)
	if big == 0 && small == 0 {
		return
	}
	if big == 0 {
		big = small
	}
	if small == 0 {
		small = big
	}
	// WM_SETICON covers this window; the class icon covers any window the class
	// creates later and is what some shells read instead.
	_, _, _ = sendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	_, _, _ = sendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	_, _, _ = setClassLongPtrW.Call(hwnd, winIndex(gclpHIcon), big)
	_, _, _ = setClassLongPtrW.Call(hwnd, winIndex(gclpHIconSm), small)
}

// loadIconResource loads the binary's icon at the size Windows wants for a
// given slot, picking the closest frame in the .ico rather than rescaling one.
//
// Returns 0 in a dev build, which has no .syso linked in at all.
func loadIconResource(hinst uintptr, cxMetric, cyMetric int) uintptr {
	cx, _, _ := getSystemMetrics.Call(uintptr(cxMetric))
	cy, _, _ := getSystemMetrics.Call(uintptr(cyMetric))
	// LR_SHARED: the handle is owned by the module and must not be destroyed,
	// which suits an icon that lives as long as the process.
	h, _, _ := loadImageW.Call(hinst, iconResourceID, imageIcon, cx, cy, lrShared)
	return h
}
