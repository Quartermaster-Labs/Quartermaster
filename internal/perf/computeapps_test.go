package perf

import "testing"

func TestParseComputeApps(t *testing.T) {
	out := `1234, 5120, C:\bin\llama-server.exe
5678, 800, /usr/bin/sd-server
9999, [N/A], weird-driver
` // trailing newline + an [N/A] row that must be skipped
	got := parseComputeApps(out)
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(got), got)
	}
	if got[0].PID != 1234 || got[0].MemMB != 5120 || got[0].Name != `C:\bin\llama-server.exe` {
		t.Errorf("row0 mismatch: %+v", got[0])
	}
	if got[1].PID != 5678 || got[1].MemMB != 800 {
		t.Errorf("row1 mismatch: %+v", got[1])
	}
}
