//go:build !windows && !linux

package perf

import "context"

// computeAppsPlatform has no vendor-neutral per-process VRAM source outside
// Windows and Linux; foreign-VRAM detection stays nvidia-smi-only there. Linux
// uses DRM fdinfo (computeapps_linux.go), which needs no tool at all.
func computeAppsPlatform(_ context.Context) []GpuProc { return nil }
