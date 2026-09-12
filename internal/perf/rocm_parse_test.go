package perf

import (
	"strings"
	"testing"
)

// On an APU the dedicated pool rocm-smi calls VRAM is only the BIOS carve-out;
// the memory the GPU actually allocates from is GTT (system RAM behind the
// graphics translation table). Both must reach GpuStat, separately, or a sizer
// that sees only the carve-out makes the machine's only GPU invisible (#37).
func TestParseRocmSmiCSV_WideTableKeepsBothPools(t *testing.T) {
	out := strings.Join([]string{
		"device,GPU Memory Allocated (VRAM%),VRAM Total Memory (B),VRAM Total Used Memory (B)," +
			"GTT Total Memory (B),GTT Total Used Memory (B),Temperature (Sensor edge) (C)," +
			"Temperature (Sensor memory) (C),Fan speed (%),Average Graphics Package Power (W)," +
			"GPU use (%),Device Name,Card Series,GFX Version",
		"card0,12,2147483648,257698037,17179869184,1073741824,45,52,30,12.5,7," +
			"AMD Radeon 780M,Ryzen 7 7840U,gfx1103",
	}, "\n")

	stats := parseRocmSmiCSV([]byte(out))
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1: %+v", len(stats), stats)
	}
	s := stats[0]
	if s.ID != 0 {
		t.Errorf("ID = %d, want 0", s.ID)
	}
	if s.MemTotalMB != 2048 || s.MemUsedMB != 245 {
		t.Errorf("dedicated = %d/%d MB, want 2048/245", s.MemTotalMB, s.MemUsedMB)
	}
	if s.SharedTotalMB != 16384 || s.SharedUsedMB != 1024 {
		t.Errorf("shared = %d/%d MB, want 16384/1024", s.SharedTotalMB, s.SharedUsedMB)
	}
	if s.TempC != 45 || s.VramTempC != 52 || s.GpuUtilPct != 7 {
		t.Errorf("sensors = %d/%d/%v, want 45/52/7", s.TempC, s.VramTempC, s.GpuUtilPct)
	}
}

// rocm-smi can print one CSV table per requested memory type instead of one
// wide table. The rows must MERGE into a single device: appending them leaves
// two stats with the same id and an identical timestamp, and gpuSetFromStats
// keeps only the first of those, so the shared pool would vanish in the one
// place the fix is supposed to show up. A table that repeats a pool must not
// add it twice either.
func TestParseRocmSmiCSV_SplitTablesAreMerged(t *testing.T) {
	out := strings.Join([]string{
		"device,GPU Memory Allocated (VRAM%),VRAM Total Memory (B),VRAM Total Used Memory (B)," +
			"Temperature (Sensor edge) (C),Fan speed (%),Average Graphics Package Power (W)," +
			"GPU use (%),Device Name,Card Series,GFX Version",
		"card0,12,2147483648,257698037,45,30,12.5,7,AMD Radeon 780M,Ryzen 7 7840U,gfx1103",
		"device,VRAM Total Memory (B),VRAM Total Used Memory (B)",
		"card0,2147483648,257698037",
		"device,GTT Total Memory (B),GTT Total Used Memory (B)",
		"card0,17179869184,1073741824",
	}, "\n")

	stats := parseRocmSmiCSV([]byte(out))
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1 merged device: %+v", len(stats), stats)
	}
	s := stats[0]
	if s.MemTotalMB != 2048 || s.MemUsedMB != 245 {
		t.Errorf("dedicated = %d/%d MB, want 2048/245 (repeated table must not add)", s.MemTotalMB, s.MemUsedMB)
	}
	if s.SharedTotalMB != 16384 || s.SharedUsedMB != 1024 {
		t.Errorf("shared = %d/%d MB, want 16384/1024", s.SharedTotalMB, s.SharedUsedMB)
	}
	if s.TempC != 45 || s.GpuUtilPct != 7 {
		t.Errorf("sensor columns lost in the merge: %+v", s)
	}
}

// Some builds prefix the label with "GPU". Only GTT is matched loosely, so a
// renamed shared pool still reaches the sizer instead of silently reporting the
// dedicated pool alone.
func TestParseRocmSmiCSV_GTTPrefixedLabels(t *testing.T) {
	out := strings.Join([]string{
		"device,VRAM Total Memory (B),GPU GTT Total Memory (B),GPU GTT Total Used Memory (B)",
		"card1,2147483648,17179869184,1073741824",
	}, "\n")

	stats := parseRocmSmiCSV([]byte(out))
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1", len(stats))
	}
	if stats[0].ID != 1 {
		t.Errorf("ID = %d, want 1", stats[0].ID)
	}
	if stats[0].SharedTotalMB != 16384 || stats[0].SharedUsedMB != 1024 {
		t.Errorf("shared = %d/%d MB, want 16384/1024", stats[0].SharedTotalMB, stats[0].SharedUsedMB)
	}
}
