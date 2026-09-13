//go:build unix && !darwin

package perf

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

func getGpuStats(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	if ch, err := tryLACT(ctx, every, logger); err == nil {
		logger.Info("using LACT for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("LACT: %s", err.Error())
	}

	if ch, err := tryNvidiaSmi(ctx, every, logger); err == nil {
		logger.Info("using nvidia-smi for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("nvidia-smi: %s", err.Error())
	}

	if ch, err := tryAmdSmi(ctx, every, logger); err == nil {
		logger.Info("using amd-smi for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("amd-smi: %s", err.Error())
	}

	if ch, err := tryRocmSmi(ctx, every, logger); err == nil {
		logger.Info("using rocm-smi for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("rocm-smi: %s", err.Error())
	}

	if ch, err := trySysfs(ctx, every, logger); err == nil {
		logger.Info("using sysfs for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("sysfs: %s", err.Error())
	}

	return nil, ErrNoGpuTool
}

func tryLACT(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	socketPath := lactSocketPath()
	if socketPath == "" {
		return nil, ErrNoGpuTool
	}

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to LACT socket: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	devices, err := lactListDevices(conn)
	if err != nil {
		return nil, fmt.Errorf("LACT ListDevices failed: %w", err)
	}

	if len(devices) == 0 {
		return nil, fmt.Errorf("LACT returned no devices")
	}

	ch := make(chan []GpuStat, 1)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(every)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				socketPath := lactSocketPath()
				if socketPath == "" {
					continue
				}

				conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
				if err != nil {
					continue
				}
				conn.SetDeadline(time.Now().Add(5 * time.Second))

				devices, err := lactListDevices(conn)
				if err != nil {
					conn.Close()
					continue
				}

				stats := make([]GpuStat, 0, len(devices))
				for i, d := range devices {
					stat, err := lactGetDeviceStats(conn, d.ID, d.Name, i)
					if err != nil {
						continue
					}
					if stat.MemTotalMB == 0 {
						continue
					}
					stats = append(stats, stat)
				}
				conn.Close()

				if len(stats) > 0 {
					select {
					case ch <- stats:
					default:
					}
				}
			}
		}
	}()

	return ch, nil
}

func tryNvidiaSmi(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil, ErrNoGpuTool
	}

	sec := int(every.Seconds())
	if sec < 1 {
		sec = 1
	}

	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,name,uuid,temperature.gpu,utilization.gpu,memory.used,memory.total,fan.speed,power.draw",
		"--format=csv,noheader,nounits",
		"--loop", fmt.Sprintf("%d", sec),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi stdout pipe failed: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("nvidia-smi start failed: %w", err)
	}

	ch := make(chan []GpuStat, 1)

	go func() {
		defer close(ch)
		scanNvidiaSmi(stdout, func(stats []GpuStat) {
			select {
			case ch <- stats:
			default:
			}
		})
		cmd.Wait()
	}()

	return ch, nil
}

// scanNvidiaSmi reads --loop output and calls send once per PASS, with every
// adapter of that pass in one slice.
//
// This used to send one slice per LINE, which made every snapshot describe a
// single adapter. The live Monitor hid it, because it accumulates messages into
// a ring and so eventually holds them all; the one-shot probes did not. They
// take a single receive (SampleFreeVramGB, SampleGpuSet, SampleTotalVramGB), so
// on a two-card box they saw whichever adapter nvidia-smi happened to list
// first and budgeted the whole machine against it: a 12 GB + 16 GB pair seeded
// targetVramGB from the 12 GB card and left half the VRAM unreachable, on a
// build whose pooling arithmetic was correct and was simply never handed the
// second device (issue #4). The LACT and rocm-smi readers already send whole
// snapshots; this is the odd one out.
//
// nvidia-smi marks no pass boundary, so the boundary is inferred from the index
// column: a line whose index does not increase is the next pass starting. That
// costs one poll interval of latency on the FIRST snapshot only (a pass is
// published when the next one begins) and nothing thereafter, which is well
// inside every probe's timeout. Reading a device count up front would remove
// even that, but only by adding a query field an older driver could reject, and
// the failure there is silent loss of all GPU telemetry.
func scanNvidiaSmi(r io.Reader, send func([]GpuStat)) {
	flush := func(batch []GpuStat) {
		if len(batch) > 0 {
			send(batch)
		}
	}

	var batch []GpuStat
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		stat := ParseNvidiaSmiLine(line)
		if stat == nil {
			continue
		}
		if len(batch) > 0 && stat.ID <= batch[0].ID {
			flush(batch)
			batch = nil
		}
		batch = append(batch, *stat)
	}
	// The last pass has no successor to close it, so a bounded run (no --loop)
	// still reports.
	flush(batch)
}

func tryRocmSmi(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	if _, err := exec.LookPath("rocm-smi"); err != nil {
		return nil, ErrNoGpuTool
	}
	if every < time.Second {
		every = time.Second
	}
	const pollTimeout = 5 * time.Second

	ch := make(chan []GpuStat, 1)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(every)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
				cmd := exec.CommandContext(pollCtx, "rocm-smi", "-i", "-P", "-t", "-f", "-u", "--showmemuse", "--showmeminfo", "all", "--showproductname", "--csv")
				out, err := cmd.Output()
				timedOut := pollCtx.Err() == context.DeadlineExceeded
				cancel()
				if err != nil {
					if timedOut {
						logger.Debug("rocm-smi timed out")
					}
					continue
				}

				stats := make([]GpuStat, 0)
				scanner := bufio.NewScanner(strings.NewReader(string(out)))
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
					if stat != nil {
						stats = append(stats, *stat)
					}
				}

				if len(stats) > 0 {
					select {
					case ch <- stats:
					default:
					}
				}
			}
		}
	}()

	return ch, nil
}

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
	var nodeID int = -1
	var vramTotalB, vramUsedB uint64
	var gttTotalB, gttUsedB uint64

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
			vramTotalB, _ = strconv.ParseUint(val, 10, 64)
		case "VRAM Total Used Memory (B)":
			vramUsedB, _ = strconv.ParseUint(val, 10, 64)
		case "GTT Total Memory (B)":
			gttTotalB, _ = strconv.ParseUint(val, 10, 64)
		case "GTT Total Used Memory (B)":
			gttUsedB, _ = strconv.ParseUint(val, 10, 64)
		case "Node ID":
			nodeID, _ = strconv.Atoi(val)
		case "Card Series":
			cardSeries = val
		case "GFX Version":
			gfxVersion = val
		}
	}

	if result.ID == -1 {
		return nil
	}

	// APUs have no dedicated local memory (KFD local_mem_size == 0) and run
	// workloads in GTT (system RAM). Combine VRAM+GTT to report the real
	// addressable budget; fall back to VRAM-only if KFD is unavailable.
	if isAPU, ok := kfdIsAPU(nodeID); ok && isAPU {
		result.MemTotalMB = int((vramTotalB + gttTotalB) / toMB)
		result.MemUsedMB = int((vramUsedB + gttUsedB) / toMB)
	} else {
		result.MemTotalMB = int(vramTotalB / toMB)
		result.MemUsedMB = int(vramUsedB / toMB)
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

// kfdIsAPU reports whether the KFD topology node at nodeID has no dedicated
// local memory (local_mem_size == 0), which is the kernel's explicit marker for
// an APU / iGPU that uses shared system RAM rather than its own VRAM.
// Returns (false, false) when KFD is unavailable or the field is absent.
func kfdIsAPU(nodeID int) (bool, bool) {
	if nodeID < 0 {
		return false, false
	}
	path := fmt.Sprintf("/sys/class/kfd/kfd/topology/nodes/%d/properties", nodeID)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var localMem uint64
	var hasLocalMem bool
	var simdCount uint64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "simd_count":
			simdCount = v
		case "local_mem_size":
			localMem = v
			hasLocalMem = true
		}
	}
	if !hasLocalMem || simdCount == 0 {
		return false, false
	}
	return localMem == 0, true
}

// kfdIsAPUByBDF looks up the KFD topology node for the GPU at the given PCIe
// BDF (e.g. "0000:65:00.0") and returns whether it is an APU.
// location_id in KFD properties encodes the BDF as (bus<<8)|(dev<<3)|fn.
func kfdIsAPUByBDF(bdf string) (bool, bool) {
	parts := strings.Split(bdf, ":")
	if len(parts) < 2 {
		return false, false
	}
	busStr := parts[len(parts)-2]
	devFn := parts[len(parts)-1]
	df := strings.SplitN(devFn, ".", 2)
	if len(df) != 2 {
		return false, false
	}
	bus, err1 := strconv.ParseUint(busStr, 16, 64)
	dev, err2 := strconv.ParseUint(df[0], 16, 64)
	fn, err3 := strconv.ParseUint(df[1], 16, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return false, false
	}
	wantLoc := (bus << 8) | (dev << 3) | fn

	nodes, _ := filepath.Glob("/sys/class/kfd/kfd/topology/nodes/*/properties")
	for _, p := range nodes {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var locationID, simdCount, localMem uint64
		var hasLocalMem bool
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "location_id":
				locationID = v
			case "simd_count":
				simdCount = v
			case "local_mem_size":
				localMem = v
				hasLocalMem = true
			}
		}
		if simdCount == 0 || locationID != wantLoc || !hasLocalMem {
			continue
		}
		return localMem == 0, true
	}
	return false, false
}

// sysfsReadInt64 reads a single integer value from a sysfs file.
func sysfsReadInt64(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return v, err == nil
}

// sysfsReadHwmon reads a hwmon attribute (e.g. "temp1_input") for a DRM card
// device directory. It picks the first matching hwmon instance.
func sysfsReadHwmon(cardDevDir, attr string) (int64, bool) {
	matches, _ := filepath.Glob(cardDevDir + "/hwmon/hwmon*/" + attr)
	if len(matches) == 0 {
		return 0, false
	}
	return sysfsReadInt64(matches[0])
}

// findDRMCardByBDF returns the sysfs device directory for the DRM card whose
// uevent PCI_SLOT_NAME matches bdf (e.g. "0000:65:00.0"), or "" if not found.
func findDRMCardByBDF(bdf string) string {
	ueventPaths, _ := filepath.Glob("/sys/class/drm/card*/device/uevent")
	for _, p := range ueventPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "PCI_SLOT_NAME="+bdf {
				return filepath.Dir(p)
			}
		}
	}
	return ""
}

// amdSmiGPUMeta holds per-GPU static information built once at tryAmdSmi startup.
type amdSmiGPUMeta struct {
	index   int
	name    string
	uuid    string
	isAPU   bool
	cardDir string // /sys/class/drm/cardN/device
}

func tryAmdSmi(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	if _, err := exec.LookPath("amd-smi"); err != nil {
		return nil, ErrNoGpuTool
	}
	if every < time.Second {
		every = time.Second
	}

	const probeTimeout = 5 * time.Second
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	metas, err := amdSmiEnumerate(probeCtx)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("amd-smi enumerate: %w", err)
	}
	if len(metas) == 0 {
		return nil, fmt.Errorf("amd-smi found no GPUs")
	}

	// Verify the memory query path works before committing to this backend.
	verifyCtx, cancel2 := context.WithTimeout(ctx, probeTimeout)
	_, err = amdSmiQueryStats(verifyCtx, metas)
	cancel2()
	if err != nil {
		return nil, fmt.Errorf("amd-smi mem query: %w", err)
	}

	const pollTimeout = 5 * time.Second
	ch := make(chan []GpuStat, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
				stats, err := amdSmiQueryStats(pollCtx, metas)
				cancel()
				if err != nil || len(stats) == 0 {
					continue
				}
				select {
				case ch <- stats:
				default:
				}
			}
		}
	}()
	return ch, nil
}

func amdSmiEnumerate(ctx context.Context) ([]amdSmiGPUMeta, error) {
	listOut, err := exec.CommandContext(ctx, "amd-smi", "list", "--json").Output()
	if err != nil {
		return nil, err
	}
	var listEntries []struct {
		GPU    int    `json:"gpu"`
		BDF    string `json:"bdf"`
		UUID   string `json:"uuid"`
		NodeID int    `json:"node_id"`
	}
	if err := json.Unmarshal(listOut, &listEntries); err != nil {
		return nil, err
	}

	// Fetch market names from the asic block; non-fatal if it fails.
	nameMap := map[int]string{}
	if staticOut, err := exec.CommandContext(ctx, "amd-smi", "static", "--asic", "--json").Output(); err == nil {
		var staticResp struct {
			GPUData []struct {
				GPU  int `json:"gpu"`
				ASIC struct {
					MarketName string `json:"market_name"`
				} `json:"asic"`
			} `json:"gpu_data"`
		}
		if json.Unmarshal(staticOut, &staticResp) == nil {
			for _, d := range staticResp.GPUData {
				if d.ASIC.MarketName != "" && d.ASIC.MarketName != "N/A" {
					nameMap[d.GPU] = d.ASIC.MarketName
				}
			}
		}
	}

	metas := make([]amdSmiGPUMeta, 0, len(listEntries))
	for _, e := range listEntries {
		isAPU, _ := kfdIsAPU(e.NodeID)
		name := nameMap[e.GPU]
		if name == "" {
			name = fmt.Sprintf("AMD GPU %d", e.GPU)
		}
		metas = append(metas, amdSmiGPUMeta{
			index:   e.GPU,
			name:    name,
			uuid:    e.UUID,
			isAPU:   isAPU,
			cardDir: findDRMCardByBDF(e.BDF),
		})
	}
	return metas, nil
}

func amdSmiQueryStats(ctx context.Context, metas []amdSmiGPUMeta) ([]GpuStat, error) {
	out, err := exec.CommandContext(ctx, "amd-smi", "metric", "--mem-usage", "--json").Output()
	if err != nil {
		return nil, err
	}
	var resp struct {
		GPUData []struct {
			GPU      int `json:"gpu"`
			MemUsage struct {
				TotalVRAM struct{ Value int `json:"value"` } `json:"total_vram"`
				UsedVRAM  struct{ Value int `json:"value"` } `json:"used_vram"`
				TotalGTT  struct{ Value int `json:"value"` } `json:"total_gtt"`
				UsedGTT   struct{ Value int `json:"value"` } `json:"used_gtt"`
			} `json:"mem_usage"`
		} `json:"gpu_data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}

	metaByIdx := make(map[int]*amdSmiGPUMeta, len(metas))
	for i := range metas {
		metaByIdx[metas[i].index] = &metas[i]
	}

	stats := make([]GpuStat, 0, len(resp.GPUData))
	for _, d := range resp.GPUData {
		m, ok := metaByIdx[d.GPU]
		if !ok {
			continue
		}
		var totalMB, usedMB int
		if m.isAPU {
			totalMB = d.MemUsage.TotalVRAM.Value + d.MemUsage.TotalGTT.Value
			usedMB = d.MemUsage.UsedVRAM.Value + d.MemUsage.UsedGTT.Value
		} else {
			totalMB = d.MemUsage.TotalVRAM.Value
			usedMB = d.MemUsage.UsedVRAM.Value
		}

		var memUtil float64
		if totalMB > 0 {
			memUtil = float64(usedMB) / float64(totalMB) * 100
		}

		stat := GpuStat{
			Timestamp:  time.Now(),
			ID:         d.GPU,
			Name:       m.name,
			UUID:       m.uuid,
			MemTotalMB: totalMB,
			MemUsedMB:  usedMB,
			MemUtilPct: memUtil,
		}

		// Supplement from sysfs: utilization, temperature, and power are not
		// available via amd-smi on APUs (returns N/A).
		if m.cardDir != "" {
			if util, ok2 := sysfsReadInt64(m.cardDir + "/gpu_busy_percent"); ok2 {
				stat.GpuUtilPct = float64(util)
			}
			if temp, ok2 := sysfsReadHwmon(m.cardDir, "temp1_input"); ok2 {
				stat.TempC = int(temp / 1000)
			}
			if pwr, ok2 := sysfsReadHwmon(m.cardDir, "power1_average"); ok2 {
				stat.PowerDrawW = float64(pwr) / 1_000_000
			}
		}

		stats = append(stats, stat)
	}
	return stats, nil
}

func trySysfs(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	stats, err := readSysfs()
	if err != nil {
		return nil, err
	}
	if len(stats) == 0 {
		return nil, ErrNoGpuTool
	}
	if every < time.Second {
		every = time.Second
	}

	ch := make(chan []GpuStat, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats, err := readSysfs()
				if err != nil || len(stats) == 0 {
					continue
				}
				select {
				case ch <- stats:
				default:
				}
			}
		}
	}()
	return ch, nil
}

func lactSocketPath() string {
	if p := os.Getenv("LACT_DAEMON_SOCKET_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	rootPath := "/run/lactd.sock"
	if _, err := os.Stat(rootPath); err == nil {
		return rootPath
	}

	u, err := user.Current()
	if err != nil {
		return ""
	}
	userPath := filepath.Join("/run/user", u.Uid, "lactd.sock")
	if _, err := os.Stat(userPath); err == nil {
		return userPath
	}

	return ""
}

type lactRequest struct {
	Command string      `json:"command"`
	Args    interface{} `json:"args,omitempty"`
}

type lactResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

type lactDeviceEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lactDeviceStats struct {
	Fan struct {
		PwmCurrent *uint8 `json:"pwm_current"`
	} `json:"fan"`
	Vram struct {
		Total *uint64 `json:"total"`
		Used  *uint64 `json:"used"`
	} `json:"vram"`
	Power struct {
		Average *float64 `json:"average"`
		Current *float64 `json:"current"`
	} `json:"power"`
	Temps       map[string]lactTempEntry `json:"temps"`
	BusyPercent *uint8                   `json:"busy_percent"`
}

type lactTempEntry struct {
	Current *float64 `json:"current"`
}

func lactSendRequest(conn net.Conn, req lactRequest) (json.RawMessage, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp lactResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}

	if resp.Status != "ok" {
		return nil, fmt.Errorf("LACT error: %s", string(resp.Data))
	}

	return resp.Data, nil
}

func lactListDevices(conn net.Conn) ([]lactDeviceEntry, error) {
	data, err := lactSendRequest(conn, lactRequest{Command: "list_devices"})
	if err != nil {
		return nil, err
	}

	var devices []lactDeviceEntry
	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, err
	}

	return devices, nil
}

func lactGetDeviceStats(conn net.Conn, id string, name string, index int) (GpuStat, error) {
	data, err := lactSendRequest(conn, lactRequest{
		Command: "device_stats",
		Args: struct {
			ID string `json:"id"`
		}{ID: id},
	})
	if err != nil {
		return GpuStat{}, err
	}

	var stats lactDeviceStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return GpuStat{}, err
	}

	var memUsedMB, memTotalMB int
	if stats.Vram.Used != nil {
		memUsedMB = int(*stats.Vram.Used / 1024 / 1024)
	}
	if stats.Vram.Total != nil {
		memTotalMB = int(*stats.Vram.Total / 1024 / 1024)
	}

	var memUtil float64
	if memTotalMB > 0 {
		memUtil = float64(memUsedMB) / float64(memTotalMB) * 100
	}

	var gpuUtil float64
	if stats.BusyPercent != nil {
		gpuUtil = float64(*stats.BusyPercent)
	}

	var fanSpeed float64
	if stats.Fan.PwmCurrent != nil {
		fanSpeed = float64(*stats.Fan.PwmCurrent) / 255.0 * 100.0
	}

	var powerDraw float64
	if stats.Power.Average != nil && *stats.Power.Average > 0 {
		powerDraw = *stats.Power.Average
	} else if stats.Power.Current != nil {
		powerDraw = *stats.Power.Current
	}

	var tempC int
	if t, ok := stats.Temps["edge"]; ok && t.Current != nil {
		tempC = int(*t.Current)
	} else if t, ok := stats.Temps["junction"]; ok && t.Current != nil {
		tempC = int(*t.Current)
	} else {
		for _, t := range stats.Temps {
			if t.Current != nil {
				tempC = int(*t.Current)
				break
			}
		}
	}

	var vramTempC int
	// nvidia uses "VRAM", amd "mem"
	for _, key := range []string{"mem", "VRAM"} {
		if t, ok := stats.Temps[key]; ok && t.Current != nil && *t.Current > 0 {
			vramTempC = int(*t.Current)
			break
		}
	}

	return GpuStat{
		Timestamp:   time.Now(),
		ID:          index,
		Name:        name,
		UUID:        id,
		TempC:       tempC,
		VramTempC:   vramTempC,
		GpuUtilPct:  gpuUtil,
		MemUtilPct:  memUtil,
		MemUsedMB:   memUsedMB,
		MemTotalMB:  memTotalMB,
		FanSpeedPct: fanSpeed,
		PowerDrawW:  powerDraw,
	}, nil
}

func readSysfs() ([]GpuStat, error) {
	ueventPaths, _ := filepath.Glob("/sys/class/drm/card*/device/uevent")
	if len(ueventPaths) == 0 {
		return nil, ErrNoGpuTool
	}

	var stats []GpuStat
	for _, ueventPath := range ueventPaths {
		data, err := os.ReadFile(ueventPath)
		if err != nil {
			continue
		}

		var driver, pciSlot, pciID string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "DRIVER="):
				driver = strings.TrimPrefix(line, "DRIVER=")
			case strings.HasPrefix(line, "PCI_SLOT_NAME="):
				pciSlot = strings.TrimPrefix(line, "PCI_SLOT_NAME=")
			case strings.HasPrefix(line, "PCI_ID="):
				pciID = strings.TrimPrefix(line, "PCI_ID=")
			}
		}
		if driver != "amdgpu" {
			continue
		}

		cardDevDir := filepath.Dir(ueventPath)
		cardName := filepath.Base(filepath.Dir(cardDevDir))
		cardIndex, _ := strconv.Atoi(strings.TrimPrefix(cardName, "card"))

		vramTotal, _ := sysfsReadInt64(cardDevDir + "/mem_info_vram_total")
		vramUsed, _ := sysfsReadInt64(cardDevDir + "/mem_info_vram_used")
		gttTotal, _ := sysfsReadInt64(cardDevDir + "/mem_info_gtt_total")
		gttUsed, _ := sysfsReadInt64(cardDevDir + "/mem_info_gtt_used")
		if vramTotal == 0 && gttTotal == 0 {
			continue
		}

		isAPU, _ := kfdIsAPUByBDF(pciSlot)

		const toMB = 1024 * 1024
		var totalMB, usedMB int
		if isAPU {
			totalMB = int((vramTotal + gttTotal) / toMB)
			usedMB = int((vramUsed + gttUsed) / toMB)
			// On AMD APU, ROCm/HIP can allocate "fine-grained unified memory"
			// via KFD that bypasses DRM TTM entirely, leaving mem_info_gtt_used
			// flat even as the model occupies gigabytes.  Add whatever the KFD
			// per-process totals show beyond what gtt_used already accounts for.
			usedMB += kfdExtraMemMB(gttUsed)
		} else {
			totalMB = int(vramTotal / toMB)
			usedMB = int(vramUsed / toMB)
		}

		var memUtil float64
		if totalMB > 0 {
			memUtil = float64(usedMB) / float64(totalMB) * 100
		}

		var gpuUtil float64
		if util, ok := sysfsReadInt64(cardDevDir + "/gpu_busy_percent"); ok {
			gpuUtil = float64(util)
		}

		var tempC int
		if t, ok := sysfsReadHwmon(cardDevDir, "temp1_input"); ok {
			tempC = int(t / 1000)
		}

		var powerW float64
		if p, ok := sysfsReadHwmon(cardDevDir, "power1_average"); ok {
			powerW = float64(p) / 1_000_000
		}

		name := "amdgpu"
		if pciID != "" {
			name = "amdgpu [" + pciID + "]"
		}

		stats = append(stats, GpuStat{
			Timestamp:  time.Now(),
			ID:         cardIndex,
			Name:       name,
			TempC:      tempC,
			GpuUtilPct: gpuUtil,
			MemUtilPct: memUtil,
			MemUsedMB:  usedMB,
			MemTotalMB: totalMB,
			PowerDrawW: powerW,
		})
	}

	if len(stats) == 0 {
		return nil, ErrNoGpuTool
	}
	return stats, nil
}

// kfdExtraMemMB returns the KFD fine-grained unified memory (in MB) that is
// NOT already counted in the DRM gtt_used counter.  On AMD APU with ROCm, the
// HIP runtime can allocate model weights through the KFD HSA driver using
// fine-grained system memory that bypasses DRM TTM — those bytes never appear
// in mem_info_gtt_used.  This function reads
// /sys/class/kfd/kfd/proc/*/vram_* (Linux KFD sysfs), sums all per-process
// KFD allocations, then returns max(0, kfdTotal − gttUsedBytes) so that only
// the portion that the DRM counter misses is added to usedMB.  Returns 0 on
// any system where the KFD sysfs path is absent (BSD, no ROCm, etc.).
func kfdExtraMemMB(gttUsedBytes int64) int {
	entries, err := os.ReadDir("/sys/class/kfd/kfd/proc")
	if err != nil {
		return 0
	}
	var kfdTotal int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		procDir := "/sys/class/kfd/kfd/proc/" + e.Name()
		fds, err := os.ReadDir(procDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if !strings.HasPrefix(fd.Name(), "vram_") {
				continue
			}
			b, err := os.ReadFile(procDir + "/" + fd.Name())
			if err != nil {
				continue
			}
			v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			kfdTotal += v
		}
	}
	extra := kfdTotal - gttUsedBytes
	if extra <= 0 {
		return 0
	}
	const toMB = 1024 * 1024
	return int(extra / toMB)
}

func readSysStats() (SysStat, error) {
	cpuPcts, err := cpu.Percent(0, true)
	if err != nil {
		return SysStat{}, err
	}

	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return SysStat{}, err
	}

	const toMB = 1024 * 1024

	var swapTotalMB, swapUsedMB int
	if swapStat, err := mem.SwapMemory(); err == nil {
		swapTotalMB = int(swapStat.Total / toMB)
		swapUsedMB = int(swapStat.Used / toMB)
	}

	var loadAvg1, loadAvg5, loadAvg15 float64
	if loadStat, err := load.Avg(); err == nil {
		loadAvg1 = loadStat.Load1
		loadAvg5 = loadStat.Load5
		loadAvg15 = loadStat.Load15
	}

	netIO := make([]NetIOStat, 0)
	if ioCounters, err := psnet.IOCounters(true); err == nil {
		for _, ioc := range ioCounters {
			if ioc.Name == "lo" {
				continue
			}
			netIO = append(netIO, NetIOStat{
				Name:      ioc.Name,
				BytesRecv: ioc.BytesRecv,
				BytesSent: ioc.BytesSent,
			})
		}
	}

	return SysStat{
		Timestamp:      time.Now(),
		CpuUtilPerCore: cpuPcts,
		MemTotalMB:     int(vmStat.Total / toMB),
		MemUsedMB:      int(vmStat.Used / toMB),
		MemFreeMB:      int(vmStat.Free / toMB),
		SwapTotalMB:    swapTotalMB,
		SwapUsedMB:     swapUsedMB,
		LoadAvg1:       loadAvg1,
		LoadAvg5:       loadAvg5,
		LoadAvg15:      loadAvg15,
		NetIO:          netIO,
	}, nil
}
