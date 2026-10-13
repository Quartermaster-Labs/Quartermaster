package autogen

import (
	"fmt"
	"strings"
)

// Device / swap-group values for Override.Device and Override.SwapGroup.
const (
	DeviceGPU = "gpu"
	DeviceCPU = "cpu"
)

// cpuGroupName is the synthesised RAM-budgeted group CPU-pool models land in.
const cpuGroupName = "cpu"

// CpuDevice reports whether the model is pinned to run on the CPU alone.
func (o *Override) CpuDevice() bool {
	return o != nil && strings.EqualFold(strings.TrimSpace(o.Device), DeviceCPU)
}

// InCpuPool reports whether the model is admitted against the RAM budget (the
// cpu group) rather than the VRAM one. A blank SwapGroup follows Device. A GPU
// device pinned into the cpu group answers false: SwapGroupErr refuses that
// combination at save time, and a hand-edited file that carries it anyway is
// kept on the VRAM side, where its VRAM is at least charged.
func (o *Override) InCpuPool() bool {
	if o == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(o.SwapGroup)) {
	case DeviceCPU:
		return o.CpuDevice()
	case DeviceGPU:
		return false
	}
	return o.CpuDevice()
}

// SwapGroupErr validates a Device / SwapGroup pair as the editor saves it.
func SwapGroupErr(device, group string) error {
	d := strings.ToLower(strings.TrimSpace(device))
	g := strings.ToLower(strings.TrimSpace(group))
	if d != "" && d != DeviceGPU && d != DeviceCPU {
		return fmt.Errorf("device must be %q or %q, got %q", DeviceGPU, DeviceCPU, device)
	}
	if g != "" && g != DeviceGPU && g != DeviceCPU {
		return fmt.Errorf("swapGroup must be %q or %q, got %q", DeviceGPU, DeviceCPU, group)
	}
	if g == DeviceCPU && d != DeviceCPU {
		return fmt.Errorf("a GPU model cannot join the cpu swap group: its VRAM would never be charged")
	}
	return nil
}
