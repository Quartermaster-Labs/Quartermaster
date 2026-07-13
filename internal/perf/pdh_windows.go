//go:build windows

package perf

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LUID identifies a graphics adapter. PDH GPU Engine instance names embed it;
// it is the only thing that lets us group engine counters by physical adapter.
type LUID struct {
	LowPart  uint32
	HighPart int32
}

var (
	pdhDLL                          = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery                = pdhDLL.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter        = pdhDLL.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdhDLL.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArray = pdhDLL.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery               = pdhDLL.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble = 0x00000200
	pdhMoreData  = 0x800007D2
	pdhNoData    = 0x800007D5
)

type pdhCounterValue struct {
	CStatus uint32
	DblVal  float64
}

type pdhCounterValueItem struct {
	SzName   *uint16
	FmtValue pdhCounterValue
}

func init() {
	var item pdhCounterValueItem
	if unsafe.Sizeof(item) != 24 {
		panic(fmt.Sprintf("pdhCounterValueItem size %d != expected 24 on x64", unsafe.Sizeof(item)))
	}
}

// pdhGpuUtil reads a Windows per-adapter GPU PDH counter whose instance names
// embed the adapter LUID. Two counters are used: "GPU Engine\Utilization
// Percentage" (util, clamped to 100%) and "GPU Adapter Memory\Dedicated Usage"
// (bytes of dedicated VRAM in use system-wide — the number Task Manager shows,
// which DXGI's per-process QueryVideoMemoryInfo cannot provide). Reading these
// does not sample hardware perf counters, so it does not stall generation.
type pdhGpuUtil struct {
	query    uintptr
	counter  uintptr
	clampPct bool // clamp per-adapter sum to 100 (utilization only)
}

// initPdhGpuUtil creates a PDH query for the GPU Engine utilization counter.
func initPdhGpuUtil() (*pdhGpuUtil, error) {
	return initPdhLuidCounter(`\GPU Engine(*)\Utilization Percentage`, "GPU Engine", true)
}

// initPdhGpuMem creates a PDH query for system-wide dedicated VRAM usage (bytes).
func initPdhGpuMem() (*pdhGpuUtil, error) {
	return initPdhLuidCounter(`\GPU Adapter Memory(*)\Dedicated Usage`, "GPU Adapter Memory", false)
}

// initPdhLuidCounter opens a PDH query for a per-adapter counter and returns nil
// with an error if PDH or the counter is unavailable.
func initPdhLuidCounter(counterPath, label string, clampPct bool) (*pdhGpuUtil, error) {
	var query uintptr
	if ret, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&query))); ret != 0 {
		return nil, fmt.Errorf("PdhOpenQuery: 0x%x", ret)
	}

	path, _ := windows.UTF16PtrFromString(counterPath)
	var counter uintptr
	if ret, _, _ := procPdhAddEnglishCounter.Call(
		query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&counter)),
	); ret != 0 {
		procPdhCloseQuery.Call(query)
		return nil, fmt.Errorf("PdhAddEnglishCounter(%s): 0x%x", label, ret)
	}

	procPdhCollectQueryData.Call(query)

	return &pdhGpuUtil{query: query, counter: counter, clampPct: clampPct}, nil
}

// close releases the PDH query handle.
func (p *pdhGpuUtil) close() {
	if p.query != 0 {
		procPdhCloseQuery.Call(p.query)
		p.query = 0
	}
}

// collect reads the PDH counter and returns a map of adapter LUID to the value
// summed across all instances per adapter. Utilization counters are clamped to
// 100%; memory (bytes) counters are returned raw.
func (p *pdhGpuUtil) collect() map[LUID]float64 {
	ret, _, _ := procPdhCollectQueryData.Call(p.query)
	if ret != 0 && ret != pdhNoData {
		return nil
	}

	var bufSize uint32
	var itemCount uint32
	ret, _, _ = procPdhGetFormattedCounterArray.Call(
		p.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		0,
	)
	if ret != pdhMoreData || itemCount == 0 {
		return nil
	}

	buf := make([]byte, bufSize)
	ret, _, _ = procPdhGetFormattedCounterArray.Call(
		p.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if ret != 0 {
		return nil
	}

	itemSize := uint32(unsafe.Sizeof(pdhCounterValueItem{}))
	result := make(map[LUID]float64)

	for i := uint32(0); i < itemCount; i++ {
		item := (*pdhCounterValueItem)(unsafe.Pointer(&buf[i*itemSize]))
		if item.FmtValue.CStatus != 0 {
			continue
		}
		luid, ok := parsePdhLuid(windows.UTF16PtrToString(item.SzName))
		if !ok {
			continue
		}
		result[luid] += item.FmtValue.DblVal
	}

	if p.clampPct {
		for luid := range result {
			if result[luid] > 100.0 {
				result[luid] = 100.0
			}
		}
	}

	return result
}

// busiest returns the utilization of the most-active adapter, or -1 if no
// counter data is available. With a discrete GPU + iGPU, the active GPU during
// inference is the busiest one, so its value best represents "GPU util". (The
// nvidia-smi path is single-stat-per-GPU; mapping PDH LUIDs to nvidia indices
// is not attempted, so this is reported for the primary GPU.)
func (p *pdhGpuUtil) busiest() float64 {
	m := p.collect()
	if len(m) == 0 {
		return -1
	}
	max := -1.0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// parsePdhLuid extracts the adapter LUID (high and low parts) from a PDH
// GPU Engine instance name (e.g. "pid_1234_luid_0x00000000_0x000148BF_phys_0_eng_2_engtype_Compute").
func parsePdhLuid(name string) (LUID, bool) {
	idx := strings.Index(name, "luid_0x")
	if idx < 0 {
		return LUID{}, false
	}
	rest := name[idx+7:]
	parts := strings.SplitN(rest, "_", 4)
	if len(parts) < 3 {
		return LUID{}, false
	}
	hp, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return LUID{}, false
	}
	lpStr := strings.TrimPrefix(parts[1], "0x")
	lp, err := strconv.ParseUint(lpStr, 16, 32)
	if err != nil {
		return LUID{}, false
	}
	return LUID{LowPart: uint32(lp), HighPart: int32(hp)}, true
}
