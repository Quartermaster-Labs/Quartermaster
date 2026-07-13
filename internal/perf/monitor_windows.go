package perf

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// parseNvidiaSmiLineLite parses the trimmed Windows nvidia-smi query that omits
// utilization.gpu and power.draw (see getGpuStats for why). GpuUtilPct and
// PowerDrawW are left at zero; MemUtilPct is still derived from used/total.
// Format: index,name,uuid,temperature.gpu,memory.used,memory.total,fan.speed
func parseNvidiaSmiLineLite(line string) *GpuStat {
	fields := strings.Split(line, ",")
	if len(fields) < 7 {
		return nil
	}

	id, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
	name := strings.TrimSpace(fields[1])
	uuid := strings.TrimSpace(fields[2])
	tempC, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
	memUsed, _ := strconv.Atoi(strings.TrimSpace(fields[4]))
	memTotal, _ := strconv.Atoi(strings.TrimSpace(fields[5]))
	fanSpeed, _ := strconv.ParseFloat(strings.TrimSpace(fields[6]), 64)

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
		MemUtilPct:  memUtil,
		MemUsedMB:   memUsed,
		MemTotalMB:  memTotal,
		FanSpeedPct: fanSpeed,
	}
}

func getGpuStats(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	// nvidia-smi reads NVML directly and reports dedicated VRAM/power correctly,
	// including on Optimus/hybrid laptops where the discrete GPU's memory is
	// routed through the WDDM aperture and is invisible to D3DKMT segment
	// queries. The D3DKMT backend was removed for that reason.
	if ch, err := tryNvidiaSmiWindows(ctx, every, logger); err == nil {
		logger.Info("using nvidia-smi for GPU monitoring")
		return ch, nil
	} else {
		logger.Debugf("nvidia-smi: %s", err.Error())
	}

	// Vendor-neutral fallback (AMD/Intel): DXGI for VRAM + PDH for utilization.
	if ch, err := tryDxgiWindows(ctx, every, logger); err == nil {
		logger.Info("using DXGI for GPU VRAM monitoring")
		return ch, nil
	} else {
		logger.Debugf("DXGI: %s", err.Error())
	}

	return nil, ErrNoGpuTool
}

// tryNvidiaSmiWindows starts nvidia-smi in loop mode on Windows and returns
// a channel receiving GPU stat snapshots. Returns ErrNoGpuTool if nvidia-smi
// is not available.
func tryNvidiaSmiWindows(ctx context.Context, every time.Duration, logger *logmon.Monitor) (chan []GpuStat, error) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil, ErrNoGpuTool
	}

	sec := int(every.Seconds())
	if sec < 1 {
		sec = 1
	}

	// utilization.gpu and power.draw are deliberately omitted: on Windows/WDDM
	// those two fields force the driver to sample the GPU's hardware perf
	// counters, which preempts an in-flight llama.cpp generation and shows up as
	// token-stream stalls / late requests. memory/temperature/fan are cheap
	// driver bookkeeping + sensor reads and do not stall. (See parseNvidiaSmiLineLite.)
	cmd := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,name,uuid,temperature.gpu,memory.used,memory.total,fan.speed",
		"--format=csv,noheader,nounits",
		"--loop", fmt.Sprintf("%d", sec),
	)
	hideConsole(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi stdout pipe failed: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("nvidia-smi start failed: %w", err)
	}

	// GPU utilization comes from PDH "GPU Engine" counters (Task Manager's
	// source) rather than nvidia-smi's utilization.gpu, which would reintroduce
	// the WDDM perf-counter stall. Best-effort: if PDH is unavailable, util stays 0.
	pdhUtil, pdhErr := initPdhGpuUtil()
	if pdhErr != nil {
		logger.Debugf("PDH GPU utilization not available: %s", pdhErr.Error())
	} else {
		logger.Info("using PDH performance counters for GPU utilization")
	}

	ch := make(chan []GpuStat, 1)

	go func() {
		defer close(ch)
		if pdhUtil != nil {
			defer pdhUtil.close()
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			stat := parseNvidiaSmiLineLite(line)
			if stat != nil {
				if pdhUtil != nil {
					if util := pdhUtil.busiest(); util >= 0 {
						stat.GpuUtilPct = util
					}
				}
				select {
				case ch <- []GpuStat{*stat}:
				default:
				}
			}
		}
		cmd.Wait()
	}()

	return ch, nil
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

	netIO := make([]NetIOStat, 0)
	if ioCounters, err := net.IOCounters(true); err == nil {
		for _, ioc := range ioCounters {
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
		NetIO:          netIO,
	}, nil
}
