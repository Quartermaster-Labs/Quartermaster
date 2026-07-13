//go:build windows

package perf

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"golang.org/x/sys/windows"
)

// DXGI is the vendor-neutral Windows VRAM backend used when nvidia-smi is
// absent (AMD/Intel GPUs). It reports dedicated VRAM total (from the adapter
// desc) and current usage (QueryVideoMemoryInfo, the same number Task Manager's
// "Dedicated GPU memory" shows). Temperature/fan/power are NOT available here —
// DXGI is a graphics API, not a sensor interface. GPU utilization is overlaid
// from the existing PDH "GPU Engine" counters, matched by adapter LUID.

var (
	dxgiDLL                = windows.NewLazySystemDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
)

// IID_IDXGIFactory1 {770aae78-f26f-4dba-a829-253c83d1b387}
var iidIDXGIFactory1 = windows.GUID{
	Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba,
	Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87},
}

// IID_IDXGIAdapter3 {645967A4-1392-4310-A798-8053CE3E93FD}
var iidIDXGIAdapter3 = windows.GUID{
	Data1: 0x645967A4, Data2: 0x1392, Data3: 0x4310,
	Data4: [8]byte{0xA7, 0x98, 0x80, 0x53, 0xCE, 0x3E, 0x93, 0xFD},
}

// dxgiAdapterDesc1 mirrors DXGI_ADAPTER_DESC1. SIZE_T fields are 8 bytes on x64.
type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorId              uint32
	DeviceId              uint32
	SubSysId              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           LUID
	Flags                 uint32
}

// dxgiQueryVideoMemoryInfo mirrors DXGI_QUERY_VIDEO_MEMORY_INFO.
type dxgiQueryVideoMemoryInfo struct {
	Budget                  uint64
	CurrentUsage            uint64
	AvailableForReservation uint64
	CurrentReservation      uint64
}

const dxgiAdapterFlagSoftware = 0x2

// minInferenceVramMB filters out integrated GPUs / basic-render adapters (e.g.
// an AMD iGPU's ~485MB carve-out) that aren't viable inference targets and would
// otherwise clutter the dashboard — and, because the UI shows the last-listed
// GPU, hide the real dGPU behind the iGPU. Any real discrete card exceeds this.
const minInferenceVramMB = 1024

func init() {
	// Guard the hand-written struct layout: a wrong offset silently reads
	// garbage VRAM numbers (same failure mode the PDH size assertion catches).
	if got := unsafe.Offsetof(dxgiAdapterDesc1{}.DedicatedVideoMemory); got != 272 {
		panic(fmt.Sprintf("dxgiAdapterDesc1.DedicatedVideoMemory offset %d != 272", got))
	}
	if got := unsafe.Offsetof(dxgiAdapterDesc1{}.AdapterLuid); got != 296 {
		panic(fmt.Sprintf("dxgiAdapterDesc1.AdapterLuid offset %d != 296", got))
	}
	if got := unsafe.Sizeof(dxgiQueryVideoMemoryInfo{}); got != 32 {
		panic(fmt.Sprintf("dxgiQueryVideoMemoryInfo size %d != 32", got))
	}
}

// comCall invokes a COM vtable method: args[0] is implicitly the object pointer.
func comCall(obj uintptr, method int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(method)*unsafe.Sizeof(uintptr(0))))
	ret, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return ret
}

// comRelease calls IUnknown::Release (vtable index 2).
func comRelease(obj uintptr) { comCall(obj, 2) }

// dxgiAdapter is one physical GPU. The AMD driver enumerates a card multiple
// times under distinct LUIDs (mirror entries with identical name/VRAM/budget);
// they're collapsed into one adapter here, but every LUID/interface is retained
// because live usage/utilization can surface on any one of the mirrors.
type dxgiAdapter struct {
	ptrs    []uintptr // IDXGIAdapter3*, one per mirror LUID
	luids   []LUID
	name    string
	totalMB int
}

// usedMB returns system-wide dedicated VRAM usage from the PDH "GPU Adapter
// Memory" counter (max across the adapter's mirror LUIDs, bytes→MB). DXGI's own
// QueryVideoMemoryInfo reports only the CALLING process's usage (≈0 for the
// monitor), so PDH is the source; the DXGI query is a last-resort fallback when
// PDH is unavailable.
func (a *dxgiAdapter) usedMB(mem map[LUID]float64) int {
	best := 0
	for _, l := range a.luids {
		if b, ok := mem[l]; ok {
			if mb := int(b / (1024 * 1024)); mb > best {
				best = mb
			}
		}
	}
	if best > 0 || len(mem) > 0 {
		return best
	}
	// PDH memory counter unavailable — fall back to per-process DXGI query.
	for _, p := range a.ptrs {
		var info dxgiQueryVideoMemoryInfo
		if comCall(p, 14, 0, 0, uintptr(unsafe.Pointer(&info))) != 0 {
			continue
		}
		if mb := int(info.CurrentUsage / (1024 * 1024)); mb > best {
			best = mb
		}
	}
	return best
}

func (a *dxgiAdapter) release() {
	for _, p := range a.ptrs {
		comRelease(p)
	}
}

// openDxgiAdapters enumerates hardware adapters with dedicated VRAM. The
// returned IDXGIAdapter3 pointers must be released by the caller.
func openDxgiAdapters() ([]dxgiAdapter, error) {
	var factory uintptr
	r, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if r != 0 || factory == 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1: 0x%x", r)
	}
	defer comRelease(factory)

	var adapters []dxgiAdapter
	byKey := map[string]int{} // name|totalMB -> index into adapters
	for i := uint32(0); ; i++ {
		var ad1 uintptr
		// IDXGIFactory1::EnumAdapters1 is vtable index 12; non-zero = NOT_FOUND, done.
		if comCall(factory, 12, uintptr(i), uintptr(unsafe.Pointer(&ad1))) != 0 || ad1 == 0 {
			break
		}

		var desc dxgiAdapterDesc1
		comCall(ad1, 10, uintptr(unsafe.Pointer(&desc))) // IDXGIAdapter1::GetDesc1

		var ad3 uintptr
		qr := comCall(ad1, 0, // IUnknown::QueryInterface
			uintptr(unsafe.Pointer(&iidIDXGIAdapter3)),
			uintptr(unsafe.Pointer(&ad3)))
		comRelease(ad1)
		if qr != 0 || ad3 == 0 {
			continue
		}

		name := windows.UTF16ToString(desc.Description[:])
		totalMB := int(uint64(desc.DedicatedVideoMemory) / (1024 * 1024))

		// Skip the software (WARP) adapter and non-inference adapters (iGPU /
		// basic-render) below the dedicated-VRAM floor.
		if desc.Flags&dxgiAdapterFlagSoftware != 0 || totalMB < minInferenceVramMB {
			comRelease(ad3)
			continue
		}
		// Collapse the driver's mirror entries (same card, different LUID) so the
		// dashboard shows one GPU, not four. Two genuinely distinct identical
		// cards merge too — consistent with autogen's single-GPU assumption.
		key := fmt.Sprintf("%s|%d", name, totalMB)
		if idx, ok := byKey[key]; ok {
			adapters[idx].ptrs = append(adapters[idx].ptrs, ad3)
			adapters[idx].luids = append(adapters[idx].luids, desc.AdapterLuid)
			continue
		}
		byKey[key] = len(adapters)
		adapters = append(adapters, dxgiAdapter{
			ptrs:    []uintptr{ad3},
			luids:   []LUID{desc.AdapterLuid},
			name:    name,
			totalMB: totalMB,
		})
	}
	return adapters, nil
}

// tryDxgiWindows polls DXGI VRAM on a ticker and feeds GpuStat snapshots,
// overlaying PDH GPU-engine utilization by adapter LUID. Returns ErrNoGpuTool
// when no hardware adapter with dedicated VRAM is present.
func tryDxgiWindows(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	adapters, err := openDxgiAdapters()
	if err != nil {
		return nil, err
	}
	if len(adapters) == 0 {
		return nil, ErrNoGpuTool
	}

	pdhUtil, pdhErr := initPdhGpuUtil()
	if pdhErr != nil {
		logger.Debugf("PDH GPU utilization not available: %s", pdhErr.Error())
	} else {
		logger.Info("using PDH performance counters for GPU utilization")
	}

	pdhMem, memErr := initPdhGpuMem()
	if memErr != nil {
		logger.Debugf("PDH GPU memory not available: %s", memErr.Error())
	} else {
		logger.Info("using PDH performance counters for GPU VRAM usage")
	}

	ch := make(chan []GpuStat, 1)

	go func() {
		// Pin to one OS thread: we hold COM interface pointers and call them
		// repeatedly across ticks.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(ch)
		defer func() {
			for i := range adapters {
				adapters[i].release()
			}
		}()
		if pdhUtil != nil {
			defer pdhUtil.close()
		}
		if pdhMem != nil {
			defer pdhMem.close()
		}

		emit := func() {
			var util, mem map[LUID]float64
			if pdhUtil != nil {
				util = pdhUtil.collect()
			}
			if pdhMem != nil {
				mem = pdhMem.collect()
			}
			stats := make([]GpuStat, 0, len(adapters))
			for i := range adapters {
				a := &adapters[i]
				used := a.usedMB(mem)
				st := GpuStat{
					Timestamp:  time.Now(),
					ID:         i,
					Name:       a.name,
					UUID:       fmt.Sprintf("luid_%08x_%08x", uint32(a.luids[0].HighPart), a.luids[0].LowPart),
					MemUsedMB:  used,
					MemTotalMB: a.totalMB,
				}
				if a.totalMB > 0 {
					st.MemUtilPct = float64(used) / float64(a.totalMB) * 100
				}
				// Util can land on any mirror LUID; take the busiest.
				for _, l := range a.luids {
					if u, ok := util[l]; ok && u > st.GpuUtilPct {
						st.GpuUtilPct = u
					}
				}
				stats = append(stats, st)
			}
			select {
			case ch <- stats:
			default:
			}
		}

		emit() // first sample immediately; ticker for the rest
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				emit()
			}
		}
	}()

	return ch, nil
}
