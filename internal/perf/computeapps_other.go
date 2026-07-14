//go:build !windows

package perf

import "context"

// computeAppsPlatform has no vendor-neutral per-process VRAM source outside
// Windows; foreign-VRAM detection stays nvidia-smi-only there.
func computeAppsPlatform(_ context.Context) []GpuProc { return nil }
