//go:build windows

package nativewin

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The native "pick a folder" dialog, driven through COM.
//
// This is IFileDialog with FOS_PICKFOLDERS, which is the modern Explorer-style
// browser -- the same one Windows' own installers show. The alternative,
// SHBrowseForFolder, is ten lines shorter and looks like Windows 2000: no
// address bar, no typing a path, no pinned places. Since the whole point of the
// window is that the wizard reads as a native app, the dialog it opens cannot
// be the one that gives that away.
//
// Shelling out to PowerShell's FolderBrowserDialog was the other option and is
// worse in every direction: a console flash, a second of startup, and a
// dependency on an execution policy that may be locked down.
const (
	coinitApartmentThreaded = 0x2
	rpcEChangedMode         = 0x80010106

	fosPickFolders     = 0x00000020
	fosForceFileSystem = 0x00000040
	fosPathMustExist   = 0x00000800
	fosNoChangeDir     = 0x00000008

	sigdnFileSysPath = 0x80058000

	// HRESULT_FROM_WIN32(ERROR_CANCELLED). Not an error: it is the user
	// answering "no folder", which the caller reports as an empty string.
	hresultCancelled = 0x800704C7
)

// Vtable slots. IFileOpenDialog inherits IFileDialog inherits IModalWindow
// inherits IUnknown, so the indices are fixed by that chain and are the only
// way to reach the methods without generating COM stubs.
const (
	vtRelease = 2

	vtShow       = 3
	vtSetOptions = 9
	vtSetFolder  = 12
	vtSetTitle   = 17
	vtGetResult  = 20

	vtGetDisplayName = 5 // IShellItem
)

var (
	ole32   = syscall.NewLazyDLL("ole32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")

	coInitializeEx              = ole32.NewProc("CoInitializeEx")
	coUninitialize              = ole32.NewProc("CoUninitialize")
	coCreateInstance            = ole32.NewProc("CoCreateInstance")
	coTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	shCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
		Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidShellItem = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE,
		Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

// comCall invokes the idx'th method on a COM interface pointer.
//
// Every COM object begins with a pointer to its vtable, and the vtable is a
// flat array of function pointers in interface-declaration order, so a method
// call is two dereferences and a stdcall with the object as the first argument.
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return ret
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, vtRelease)
	}
}

// PickFolder shows the folder browser and returns the chosen path, or "" if the
// user cancelled.
//
// owner makes the dialog modal to the calling window. Passing 0 would let the
// user click back to the window behind a dialog that is still open, which on
// Windows leaves the dialog stranded behind its own parent with no obvious way
// back.
func PickFolder(owner uintptr, title, start string) (string, error) {
	// The caller's thread is already an STA (WebView2 requires one), so this
	// almost always returns S_FALSE -- "already initialised", which still counts
	// as a reference and still has to be balanced. RPC_E_CHANGED_MODE means the
	// thread is an MTA and the call took no reference at all, so it must NOT be.
	hr, _, _ := coInitializeEx.Call(0, coinitApartmentThreaded)
	if uint32(hr) != rpcEChangedMode {
		defer coUninitialize.Call()
	}

	var dlg uintptr
	hr, _, _ = coCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidFileOpenDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if hr != 0 || dlg == 0 {
		return "", fmt.Errorf("could not open the folder browser (0x%08x)", uint32(hr))
	}
	defer comRelease(dlg)

	// FOS_FORCEFILESYSTEM alongside FOS_PICKFOLDERS: without it the dialog
	// happily returns a virtual place like "This PC" or a library, which has no
	// path to install into and would surface as a confusing error later.
	comCall(dlg, vtSetOptions, fosPickFolders|fosForceFileSystem|fosPathMustExist|fosNoChangeDir)

	if title != "" {
		if p, err := windows.UTF16PtrFromString(title); err == nil {
			comCall(dlg, vtSetTitle, uintptr(unsafe.Pointer(p)))
		}
	}
	// Best effort: a start folder that does not exist yet (the proposed install
	// dir usually does not) simply leaves the dialog wherever it would have
	// opened, which is better than refusing to open.
	if item := shellItem(start); item != 0 {
		comCall(dlg, vtSetFolder, item)
		comRelease(item)
	}

	if hr := comCall(dlg, vtShow, owner); hr != 0 {
		if uint32(hr) == hresultCancelled {
			return "", nil
		}
		return "", fmt.Errorf("folder browser failed (0x%08x)", uint32(hr))
	}

	var item uintptr
	if hr := comCall(dlg, vtGetResult, uintptr(unsafe.Pointer(&item))); hr != 0 || item == 0 {
		return "", errors.New("no folder was returned")
	}
	defer comRelease(item)

	var pathPtr *uint16
	if hr := comCall(item, vtGetDisplayName, sigdnFileSysPath,
		uintptr(unsafe.Pointer(&pathPtr))); hr != 0 || pathPtr == nil {
		return "", errors.New("that folder has no file-system path")
	}
	// The string is allocated by the shell, so it is freed with the shell's
	// allocator and not Go's.
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(pathPtr)))

	return windows.UTF16PtrToString(pathPtr), nil
}

// shellItem resolves a path to an IShellItem, or 0 if it cannot be resolved.
func shellItem(path string) uintptr {
	if path == "" {
		return 0
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var item uintptr
	hr, _, _ := shCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&iidShellItem)),
		uintptr(unsafe.Pointer(&item)),
	)
	if hr != 0 {
		return 0
	}
	return item
}
