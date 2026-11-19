//go:build !windows

package perf

// ProcessGpuMemory has no source outside Windows. Demotion into system memory is
// a WDDM behaviour; Linux drivers fail or evict through other paths, and the
// fdinfo reader in computeapps_linux.go already reports VRAM and GTT per client.
func ProcessGpuMemory(pid int) (dedicatedMB, sharedMB int, ok bool) { return 0, 0, false }
