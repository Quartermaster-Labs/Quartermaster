package autogen

import "testing"

// The listing below is verbatim from `audiocpp_server --list-devices` on the
// dev box (audio.cpp v0.8.0, Vulkan build, RX 7900 XTX + Ryzen iGPU). It is the
// contract this parser is written against, so it is recorded rather than
// paraphrased.
const audioCppListing = `audio.cpp is optimized for CUDA. The vulkan server backend is intended for portability and testing.
available_devices=3
Vulkan:0 "AMD Radeon RX 7900 XTX" [GPU]
Vulkan:1 "AMD Radeon(TM) Graphics" [IGPU]
CPU:0 "AMD Ryzen 9 7950X" [CPU]
select with: --backend <cuda|hip|vulkan|metal|cpu> --device <index>
`

func TestAutogen_parseAudioCppDevices(t *testing.T) {
	devs := parseAudioCppDevices(audioCppListing)
	if len(devs) != 3 {
		t.Fatalf("parsed %d devices, want 3: %+v", len(devs), devs)
	}
	want := []audioCppDevice{
		{Backend: "Vulkan", Index: 0, Name: "AMD Radeon RX 7900 XTX", Kind: "GPU"},
		{Backend: "Vulkan", Index: 1, Name: "AMD Radeon(TM) Graphics", Kind: "IGPU"},
		{Backend: "CPU", Index: 0, Name: "AMD Ryzen 9 7950X", Kind: "CPU"},
	}
	for i, w := range want {
		if devs[i] != w {
			t.Errorf("device %d = %+v, want %+v", i, devs[i], w)
		}
	}
	// The banner and the "select with:" footer must not look like devices.
	if got := parseAudioCppDevices("select with: --backend <cuda|hip|vulkan> --device <index>"); len(got) != 0 {
		t.Errorf("footer parsed as devices: %+v", got)
	}
}

func TestAutogen_pickAudioCppDevice(t *testing.T) {
	devs := parseAudioCppDevices(audioCppListing)

	if idx, ok := pickAudioCppDevice(devs, "vulkan"); !ok || idx != 0 {
		t.Errorf("vulkan pick = %d,%v; want 0,true (the discrete card)", idx, ok)
	}
	// Same listing with the iGPU enumerated first: the whole point of the flag.
	flipped := []audioCppDevice{
		{Backend: "Vulkan", Index: 0, Name: "AMD Radeon(TM) Graphics", Kind: "IGPU"},
		{Backend: "Vulkan", Index: 1, Name: "AMD Radeon RX 7900 XTX", Kind: "GPU"},
	}
	if idx, ok := pickAudioCppDevice(flipped, "vulkan"); !ok || idx != 1 {
		t.Errorf("flipped pick = %d,%v; want 1,true", idx, ok)
	}

	// Refusals.
	if _, ok := pickAudioCppDevice(devs, "cpu"); ok {
		t.Error("cpu flavour should emit no --device")
	}
	if _, ok := pickAudioCppDevice(devs, ""); ok {
		t.Error("unknown flavour should emit no --device")
	}
	if _, ok := pickAudioCppDevice(devs, "cuda"); ok {
		t.Error("a backend with no rows in the listing should emit no --device")
	}
	single := []audioCppDevice{{Backend: "CUDA", Index: 0, Name: "RTX 4090", Kind: "GPU"}}
	if _, ok := pickAudioCppDevice(single, "cuda"); ok {
		t.Error("a single-device backend has nothing to choose, want no --device")
	}
	igpuOnly := []audioCppDevice{
		{Backend: "Vulkan", Index: 0, Name: "Intel UHD", Kind: "IGPU"},
		{Backend: "Vulkan", Index: 1, Name: "Intel Arc", Kind: "IGPU"},
	}
	if _, ok := pickAudioCppDevice(igpuOnly, "vulkan"); ok {
		t.Error("no discrete GPU listed, want no --device rather than a guess")
	}
	// hip and rocm are one backend under two names.
	hip := []audioCppDevice{
		{Backend: "ROCm", Index: 0, Name: "iGPU", Kind: "IGPU"},
		{Backend: "ROCm", Index: 1, Name: "RX 7900 XTX", Kind: "GPU"},
	}
	if idx, ok := pickAudioCppDevice(hip, "hip"); !ok || idx != 1 {
		t.Errorf("hip/rocm pick = %d,%v; want 1,true", idx, ok)
	}
}

func TestAutogen_audioCppDeviceArg_override(t *testing.T) {
	// The override wins WITHOUT probing: exe is deliberately a path that does
	// not exist, so a probe would have to answer false.
	pin := 1
	if arg, ok := audioCppDeviceArg("no-such-binary", "vulkan", &Override{AudioDevice: &pin}); !ok || arg != "--device 1" {
		t.Errorf("pinned arg = %q,%v; want \"--device 1\",true", arg, ok)
	}
	off := -1
	if arg, ok := audioCppDeviceArg("no-such-binary", "vulkan", &Override{AudioDevice: &off}); ok {
		t.Errorf("negative pin should emit nothing, got %q", arg)
	}
	if _, ok := audioCppDeviceArg("", "vulkan", nil); ok {
		t.Error("no exe should emit no --device")
	}
}
