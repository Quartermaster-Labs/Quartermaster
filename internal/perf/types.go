package perf

import "time"

type GpuStat struct {
	Timestamp time.Time `json:"timestamp"`

	ID         int     `json:"id"`
	Name       string  `json:"name"`
	UUID       string  `json:"uuid"`
	TempC      int     `json:"temp_c"`
	VramTempC  int     `json:"vram_temp_c"`
	GpuUtilPct float64 `json:"gpu_util_pct"`
	MemUtilPct float64 `json:"mem_util_pct"`
	MemUsedMB  int     `json:"mem_used_mb"`
	MemTotalMB int     `json:"mem_total_mb"`
	// SharedTotalMB / SharedUsedMB describe the SYSTEM-MEMORY pool a GPU can
	// address, on platforms that have one: AMD's GTT on Linux (rocm-smi's
	// "GTT Total Memory"), Windows' shared video memory. Zero when the platform
	// has no such notion or the reader does not supply it.
	//
	// Deliberately separate from MemTotalMB/MemUsedMB, which keep meaning the
	// DEDICATED pool on every platform. On a discrete card the shared pool is a
	// host-side aperture and adding it to the card's own VRAM invents budget; on
	// an APU (a 780M, a Strix Halo) it is the memory the GPU actually allocates
	// from, and a sizer that ignores it makes the only GPU in the machine
	// invisible (issue #37). Which of the two applies is a policy call, so it
	// lives in autogen, not here.
	SharedTotalMB int     `json:"shared_total_mb"`
	SharedUsedMB  int     `json:"shared_used_mb"`
	FanSpeedPct   float64 `json:"fan_speed_pct"`
	PowerDrawW    float64 `json:"power_draw_w"`
}

type NetIOStat struct {
	Name      string `json:"name"`
	BytesRecv uint64 `json:"bytes_recv"`
	BytesSent uint64 `json:"bytes_sent"`
}

type SysStat struct {
	Timestamp time.Time `json:"timestamp"`

	CpuUtilPerCore []float64   `json:"cpu_util_per_core"`
	MemTotalMB     int         `json:"mem_total_mb"`
	MemUsedMB      int         `json:"mem_used_mb"`
	MemFreeMB      int         `json:"mem_free_mb"`
	SwapTotalMB    int         `json:"swap_total_mb"`
	SwapUsedMB     int         `json:"swap_used_mb"`
	LoadAvg1       float64     `json:"load_avg_1"`
	LoadAvg5       float64     `json:"load_avg_5"`
	LoadAvg15      float64     `json:"load_avg_15"`
	NetIO          []NetIOStat `json:"net_io"`
}
