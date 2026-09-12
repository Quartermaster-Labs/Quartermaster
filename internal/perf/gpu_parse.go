package perf

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseNvidiaSmiLine parses a single line from nvidia-smi CSV output.
// Format: index,name,uuid,temperature.gpu,utilization.gpu,memory.used,memory.total,fan.speed,power.draw
func ParseNvidiaSmiLine(line string) *GpuStat {
	fields := strings.Split(line, ",")
	if len(fields) < 9 {
		return nil
	}

	id, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
	name := strings.TrimSpace(fields[1])
	uuid := strings.TrimSpace(fields[2])
	tempC, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
	gpuUtil, _ := strconv.ParseFloat(strings.TrimSpace(fields[4]), 64)
	memUsed, _ := strconv.Atoi(strings.TrimSpace(fields[5]))
	memTotal, _ := strconv.Atoi(strings.TrimSpace(fields[6]))
	fanSpeed, _ := strconv.ParseFloat(strings.TrimSpace(fields[7]), 64)
	powerDraw, _ := strconv.ParseFloat(strings.TrimSpace(fields[8]), 64)

	var memUtil float64
	if memTotal > 0 {
		memUtil = float64(memUsed) / float64(memTotal) * 100
	}

	return &GpuStat{
		Timestamp:   time.Now(),
		ID:          id,
		Name:        name,
		UUID:        uuid,
		TempC:       tempC,
		GpuUtilPct:  gpuUtil,
		MemUtilPct:  memUtil,
		MemUsedMB:   memUsed,
		MemTotalMB:  memTotal,
		FanSpeedPct: fanSpeed,
		PowerDrawW:  powerDraw,
	}
}

// mactopOutput maps the subset of mactop's headless JSON output that is
// relevant to GpuStat. Note that mactop's memory object is whole-system memory,
// not GPU-attributed; the darwin monitor overlays ioreg's GPU-attributed
// unified memory (see overlayIoregMem) so both backends report consistent
// memory figures.
type mactopOutput struct {
	SocMetrics struct {
		GPUPower float64 `json:"gpu_power"`
		GPUFreq  int     `json:"gpu_freq_mhz"`
		GPUTemp  float64 `json:"gpu_temp"`
	} `json:"soc_metrics"`
	Memory struct {
		Total uint64 `json:"total"`
		Used  uint64 `json:"used"`
	} `json:"memory"`
	GPUUsage   float64 `json:"gpu_usage"`
	SystemInfo struct {
		Name         string `json:"name"`
		GPUCoreCount int    `json:"gpu_core_count"`
	} `json:"system_info"`
	Fans []struct {
		RPM    int `json:"rpm"`
		MinRPM int `json:"min_rpm"`
		MaxRPM int `json:"max_rpm"`
	} `json:"fans"`
	Temperatures []struct {
		Group string  `json:"group"`
		Avg   float64 `json:"avg_celsius"`
	} `json:"temperatures"`
}

// ioreg output uses ` = ` (with spaces) for top-level device properties and
// `=` (no spaces) for values inside nested dictionaries such as
// PerformanceStatistics.
var (
	reIoregModel     = regexp.MustCompile(`"model"\s*=\s*"([^"]+)"`)
	reIoregCoreCount = regexp.MustCompile(`"gpu-core-count"\s*=\s*(\d+)`)
	reIoregUtil      = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	reIoregMemUsed   = regexp.MustCompile(`"In use system memory"=(\d+)`)
)

// ParseIoregOutput parses `ioreg -r -c IOGPU -d 1 -f` output into a GpuStat for
// the Apple Silicon integrated GPU. This is a fallback for when mactop is not
// installed: utilization and used memory are available, but power, temperature,
// and fan speed are not exposed by ioreg. memTotalMB is the unified memory size
// supplied by the caller, since Apple Silicon shares memory between CPU and GPU.
// Returns nil if no GPU device is found in the output.
func ParseIoregOutput(out []byte, memTotalMB int) *GpuStat {
	utilMatch := reIoregUtil.FindSubmatch(out)
	memMatch := reIoregMemUsed.FindSubmatch(out)
	if utilMatch == nil && memMatch == nil {
		return nil
	}

	var gpuUtil float64
	if utilMatch != nil {
		gpuUtil, _ = strconv.ParseFloat(string(utilMatch[1]), 64)
	}

	const toMB = 1024 * 1024
	var memUsedMB int
	if memMatch != nil {
		memUsedBytes, _ := strconv.ParseInt(string(memMatch[1]), 10, 64)
		memUsedMB = int(memUsedBytes / toMB)
	}

	var memUtil float64
	if memTotalMB > 0 {
		memUtil = float64(memUsedMB) / float64(memTotalMB) * 100
	}

	name := "Apple GPU"
	if m := reIoregModel.FindSubmatch(out); m != nil {
		name = string(m[1])
	}
	if m := reIoregCoreCount.FindSubmatch(out); m != nil {
		if cores, err := strconv.Atoi(string(m[1])); err == nil && cores > 0 {
			name = fmt.Sprintf("%s (%d-core GPU)", name, cores)
		}
	}

	return &GpuStat{
		Timestamp:  time.Now(),
		ID:         0,
		Name:       name,
		GpuUtilPct: gpuUtil,
		MemUtilPct: memUtil,
		MemUsedMB:  memUsedMB,
		MemTotalMB: memTotalMB,
	}
}

// ParseMactopLine parses a single line of mactop headless JSON output into a
// GpuStat for the Apple Silicon integrated GPU. Returns nil if the line cannot
// be parsed.
func ParseMactopLine(line string) *GpuStat {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	var out mactopOutput
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		return nil
	}

	const toMB = 1024 * 1024
	memUsedMB := int(out.Memory.Used / toMB)
	memTotalMB := int(out.Memory.Total / toMB)

	var memUtil float64
	if memTotalMB > 0 {
		memUtil = float64(memUsedMB) / float64(memTotalMB) * 100
	}

	name := out.SystemInfo.Name
	if name == "" {
		name = "Apple GPU"
	}
	if out.SystemInfo.GPUCoreCount > 0 {
		name = fmt.Sprintf("%s (%d-core GPU)", name, out.SystemInfo.GPUCoreCount)
	}

	// Unified memory has no dedicated VRAM sensor; use the memory temperature
	// group when mactop exposes it.
	var vramTempC int
	for _, t := range out.Temperatures {
		if strings.EqualFold(t.Group, "Memory") {
			vramTempC = int(math.Round(t.Avg))
			break
		}
	}

	// Average fan load across all fans as a percentage of their RPM range.
	var fanSpeed float64
	var fanCount int
	for _, f := range out.Fans {
		if f.MaxRPM > f.MinRPM {
			pct := float64(f.RPM-f.MinRPM) / float64(f.MaxRPM-f.MinRPM) * 100
			if pct < 0 {
				pct = 0
			}
			fanSpeed += pct
			fanCount++
		}
	}
	if fanCount > 0 {
		fanSpeed /= float64(fanCount)
	}

	return &GpuStat{
		Timestamp:   time.Now(),
		ID:          0,
		Name:        name,
		TempC:       int(math.Round(out.SocMetrics.GPUTemp)),
		VramTempC:   vramTempC,
		GpuUtilPct:  out.GPUUsage,
		MemUtilPct:  memUtil,
		MemUsedMB:   memUsedMB,
		MemTotalMB:  memTotalMB,
		FanSpeedPct: fanSpeed,
		PowerDrawW:  out.SocMetrics.GPUPower,
	}
}

// parseRocmSmiLine parses one row of rocm-smi --csv output against its header,
// matching columns by LABEL rather than position (the label set depends on which
// flags were passed). Lives here, untagged, with the other pure parsers so it is
// testable on every platform.
//
// The dedicated pool is "VRAM Total Memory (B)" — on an APU that is only the
// BIOS carve-out. "GTT Total Memory (B)" is the shared pool (system RAM the GPU
// can address) and fills SharedTotalMB/SharedUsedMB; see GpuStat for why the two
// are kept apart instead of summed here.
func parseRocmSmiLine(header string, line string) *GpuStat {
	if header == "" || line == "" {
		return nil
	}
	labels := strings.Split(header, ",")
	fields := strings.Split(line, ",")
	if len(labels) != len(fields) {
		return nil
	}

	result := &GpuStat{
		Timestamp: time.Now(),
		ID:        -1,
	}

	var device string
	var deviceName string
	var cardSeries string
	var gfxVersion string

	const toMB = 1024 * 1024

	for i, col := range labels {
		val := strings.TrimSpace(fields[i])
		switch col {
		case "device":
			device = val
			id, err := strconv.Atoi(strings.TrimPrefix(val, "card"))
			if err != nil {
				return nil
			}
			result.ID = id
		case "Device Name":
			deviceName = val
		case "GUID":
			result.UUID = val
		case "Temperature (Sensor edge) (C)":
			tempC, _ := strconv.ParseFloat(val, 64)
			result.TempC = int(tempC)
		case "Temperature (Sensor memory) (C)":
			vramTempC, _ := strconv.ParseFloat(val, 64)
			result.VramTempC = int(vramTempC)
		case "Fan speed (%)":
			fanSpeed, _ := strconv.ParseFloat(val, 64)
			result.FanSpeedPct = fanSpeed
		case "Current Socket Graphics Package Power (W)":
			fallthrough
		case "Average Graphics Package Power (W)":
			powerDraw, _ := strconv.ParseFloat(val, 64)
			result.PowerDrawW = powerDraw
		case "GPU use (%)":
			gpuUtil, _ := strconv.ParseFloat(val, 64)
			result.GpuUtilPct = gpuUtil
		case "GPU Memory Allocated (VRAM%)":
			memUtil, _ := strconv.ParseFloat(val, 64)
			result.MemUtilPct = memUtil
		case "VRAM Total Memory (B)":
			memTotal, _ := strconv.ParseUint(val, 10, 64)
			result.MemTotalMB = int(memTotal / toMB)
		case "VRAM Total Used Memory (B)":
			memUsed, _ := strconv.ParseUint(val, 10, 64)
			result.MemUsedMB = int(memUsed / toMB)
		case "GTT Total Memory (B)":
			gttTotal, _ := strconv.ParseUint(val, 10, 64)
			result.SharedTotalMB = int(gttTotal / toMB)
		case "GTT Total Used Memory (B)":
			gttUsed, _ := strconv.ParseUint(val, 10, 64)
			result.SharedUsedMB = int(gttUsed / toMB)
		case "Card Series":
			cardSeries = val
		case "GFX Version":
			gfxVersion = val
		default:
			// Label-drift guard: some rocm-smi versions prefix these with "GPU"
			// ("GPU GTT Total Memory (B)"). Only GTT is matched loosely — the
			// VRAM columns are what every existing install depends on, and a
			// loose match there could bind a future column to the wrong field.
			// A dropped GTT column is what this fix exists to prevent; a
			// mis-bound VRAM column is the bug it already had.
			if strings.Contains(col, "GTT") {
				if u, err := strconv.ParseUint(val, 10, 64); err == nil {
					if strings.Contains(col, "Used") {
						result.SharedUsedMB = int(u / toMB)
					} else {
						result.SharedTotalMB = int(u / toMB)
					}
				}
			}
		}
	}

	if result.ID == -1 {
		return nil
	}

	name := device
	if cardSeries != "" && cardSeries != "N/A" {
		name = cardSeries + " " + device + " (" + gfxVersion + ")"
	} else if deviceName != "" && deviceName != "N/A" {
		name = deviceName + " " + device + " (" + gfxVersion + ")"
	}
	result.Name = name

	return result
}

// mergeGpuStat folds two CSV rows that describe the SAME device. rocm-smi prints
// one table per requested memory type, so vram and gtt can arrive as two rows
// (sensor columns repeated in each) instead of one wide row. The merge FILLS
// what the first row lacks and never adds, so it is correct both for a build
// that reports both pools in one row and for one that splits them across two.
func mergeGpuStat(cur, next GpuStat) GpuStat {
	if cur.Name == "" {
		cur.Name = next.Name
	}
	if cur.UUID == "" {
		cur.UUID = next.UUID
	}
	if cur.TempC == 0 {
		cur.TempC = next.TempC
	}
	if cur.VramTempC == 0 {
		cur.VramTempC = next.VramTempC
	}
	if cur.GpuUtilPct == 0 {
		cur.GpuUtilPct = next.GpuUtilPct
	}
	if cur.MemTotalMB == 0 {
		cur.MemTotalMB = next.MemTotalMB
	}
	if cur.MemUsedMB == 0 {
		cur.MemUsedMB = next.MemUsedMB
	}
	if cur.SharedTotalMB == 0 {
		cur.SharedTotalMB = next.SharedTotalMB
	}
	if cur.SharedUsedMB == 0 {
		cur.SharedUsedMB = next.SharedUsedMB
	}
	if cur.MemUtilPct == 0 {
		cur.MemUtilPct = next.MemUtilPct
	}
	if cur.FanSpeedPct == 0 {
		cur.FanSpeedPct = next.FanSpeedPct
	}
	if cur.PowerDrawW == 0 {
		cur.PowerDrawW = next.PowerDrawW
	}
	return cur
}

// parseRocmSmiCSV turns rocm-smi's --csv output into one GpuStat per device.
//
// Rows are keyed by device id, not appended. rocm-smi may print a separate CSV
// table, with its own "device," header, for each requested memory type rather
// than one wide table, so the same device can arrive twice with part of its
// memory picture in each row. Appending those would drop the second row
// downstream, where gpuSetFromStats keeps the first sample per id on a timestamp
// tie: a fix that looks right and reports nothing.
func parseRocmSmiCSV(out []byte) []GpuStat {
	byID := make(map[int]GpuStat)
	order := make([]int, 0, 2)

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var header string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "device,") {
			header = line
			continue
		}

		stat := parseRocmSmiLine(header, line)
		if stat == nil {
			continue
		}
		if prev, seen := byID[stat.ID]; seen {
			byID[stat.ID] = mergeGpuStat(prev, *stat)
			continue
		}
		byID[stat.ID] = *stat
		order = append(order, stat.ID)
	}

	stats := make([]GpuStat, 0, len(order))
	for _, id := range order {
		stats = append(stats, byID[id])
	}
	return stats
}
