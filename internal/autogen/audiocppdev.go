package autogen

// Which adapter an audio.cpp model loads onto.
//
// audiocpp_server takes `--backend <cuda|hip|vulkan|metal|cpu> --device <index>`
// and, left alone, takes device 0 of whatever backend it was built for. That is
// wrong on any box where the integrated GPU enumerates first, which is the
// normal case for a Vulkan build on a laptop or on an AMD desktop whose CPU has
// graphics: the model loads onto shared system memory and runs at a fraction of
// the speed, with nothing in the log saying so. audio.cpp v0.8.0 publishes no
// ROCm build, so on AMD the Vulkan listing IS the device list and this is the
// only lever.
//
// The listing has its own shape, which is why none of backenddev.go's parser is
// reused:
//
//	available_devices=3
//	Vulkan:0 "AMD Radeon RX 7900 XTX" [GPU]
//	Vulkan:1 "AMD Radeon(TM) Graphics" [IGPU]
//	CPU:0 "..." [CPU]
//	select with: --backend <cuda|hip|vulkan|metal|cpu> --device <index>
//
// Two properties of that shape do the work here. It carries an explicit KIND
// tag, so a discrete GPU is read off the binary's own judgement rather than
// guessed from the name (a heuristic that would have to know every iGPU marketing
// string). And it is GLOBAL - one run lists every backend - so the probe is
// memoized per exe and filtered by flavour afterwards, costing one 0.15s run per
// generate no matter how many audio models are discovered.
//
// The refusal rule from backenddev.go carries over unchanged: a listing that
// cannot be parsed, a binary that will not run, or a backend with no discrete
// GPU in it yields NO flag, leaving audio.cpp's own default in place. Emitting a
// guessed index would be worse than emitting nothing, because a wrong --device
// is a hard launch failure where a missing one is merely the old behaviour.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// audioCppDevice is one row of audiocpp_server --list-devices.
type audioCppDevice struct {
	Backend string // "Vulkan", "CUDA", "CPU", ... as printed
	Index   int    // the number --device takes
	Name    string
	Kind    string // "GPU" | "IGPU" | "CPU", upstream's own tag
}

// Vulkan:0 "AMD Radeon RX 7900 XTX" [GPU]
var audioCppDeviceRe = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_-]*)\s*:\s*(\d+)\s+"([^"]*)"\s*\[([A-Za-z]+)\]`)

func parseAudioCppDevices(out string) []audioCppDevice {
	var devs []audioCppDevice
	for _, line := range strings.Split(out, "\n") {
		m := audioCppDeviceRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		devs = append(devs, audioCppDevice{
			Backend: m[1],
			Index:   idx,
			Name:    strings.TrimSpace(m[3]),
			Kind:    strings.ToUpper(m[4]),
		})
	}
	return devs
}

var (
	audioCppDevMu    sync.Mutex
	audioCppDevCache = map[string][]audioCppDevice{}
)

// audioCppListDevices runs `exe --list-devices` once per binary. Memoized on the
// same exe|size|mtime key ListBackendDevices uses (so a backend UPDATE re-probes)
// and drawing on the same shared, never-refilled probe budget, so an audio
// backend that hangs cannot spend a generate's whole allowance on its own.
// Failures are cached: they are a property of the binary, not of the call.
func audioCppListDevices(exe string) []audioCppDevice {
	if strings.TrimSpace(exe) == "" {
		return nil
	}
	key := exe
	if st, err := os.Stat(exe); err == nil {
		key = fmt.Sprintf("%s|%d|%d", exe, st.Size(), st.ModTime().UnixNano())
	}

	audioCppDevMu.Lock()
	cached, hit := audioCppDevCache[key]
	audioCppDevMu.Unlock()
	if hit {
		return cached
	}

	devs := probeAudioCppDevices(exe)

	audioCppDevMu.Lock()
	audioCppDevCache[key] = devs
	audioCppDevMu.Unlock()
	return devs
}

func probeAudioCppDevices(exe string) []audioCppDevice {
	budget := takeProbeBudget()
	if budget <= 0 {
		return nil
	}
	started := time.Now()
	defer func() { spendProbeBudget(time.Since(started)) }()

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "--list-devices")
	hideConsole(cmd)
	cmd.Env = probeEnv()
	// The listing and the ggml banner land on different streams, and which is
	// which has moved between releases. Read the pair.
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil
	}
	return parseAudioCppDevices(string(out))
}

// audioCppDeviceBackendMatches reports whether a listing row belongs to the
// --backend value we are about to emit. hip and rocm are the same backend under
// two names; audio.cpp accepts "hip" and has printed both.
func audioCppDeviceBackendMatches(row, flavour string) bool {
	r, f := strings.ToLower(row), strings.ToLower(flavour)
	if r == f {
		return true
	}
	isHip := func(s string) bool { return s == "hip" || s == "rocm" }
	return isHip(r) && isHip(f)
}

// audioCppDeviceIndex picks the --device index for a flavour, or reports false
// when the flag should be left off.
//
// Off in four cases, each for its own reason:
//   - flavour "" (a hand-entered exe with no recorded variant): we do not know
//     which backend the index would be numbered within.
//   - flavour "cpu": there is nothing to place.
//   - the backend lists ONE device: there is no choice to make, and a flag that
//     cannot change the outcome is just another thing that can be wrong.
//   - no row tagged [GPU]: the only devices are integrated or the probe failed.
//     Refuse rather than fall back to index 0, which is what we were trying to
//     avoid naming in the first place.
func audioCppDeviceIndex(exe, flavour string) (int, bool) {
	if f := strings.ToLower(strings.TrimSpace(flavour)); f == "" || f == "cpu" {
		// Checked before the probe, not inside pickAudioCppDevice: there is no
		// point paying for a listing whose answer we already know.
		return 0, false
	}
	return pickAudioCppDevice(audioCppListDevices(exe), flavour)
}

// pickAudioCppDevice is the choice itself, split out so it can be tested against
// a recorded listing without running a binary.
func pickAudioCppDevice(devs []audioCppDevice, flavour string) (int, bool) {
	flavour = strings.ToLower(strings.TrimSpace(flavour))
	if flavour == "" || flavour == "cpu" {
		return 0, false
	}
	var inBackend []audioCppDevice
	for _, d := range devs {
		if audioCppDeviceBackendMatches(d.Backend, flavour) {
			inBackend = append(inBackend, d)
		}
	}
	if len(inBackend) < 2 {
		return 0, false
	}
	for _, d := range inBackend {
		if d.Kind == "GPU" {
			return d.Index, true
		}
	}
	return 0, false
}

// audioCppDeviceArg is what the emitter calls: the override wins over the probe
// and skips it entirely, so pinning a device by hand also costs nothing. A
// negative override means "emit nothing", which is how the editor's knob is
// turned back off without having to know what the probe would have said.
func audioCppDeviceArg(exe, flavour string, ov *Override) (string, bool) {
	if ov != nil && ov.AudioDevice != nil {
		if *ov.AudioDevice < 0 {
			return "", false
		}
		return fmt.Sprintf("--device %d", *ov.AudioDevice), true
	}
	if idx, ok := audioCppDeviceIndex(exe, flavour); ok {
		return fmt.Sprintf("--device %d", idx), true
	}
	return "", false
}
