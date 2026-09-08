package autogen

// backenddev.go answers a question the rest of the multi-GPU path assumes away:
// when the plan says "device 1", which card does the BACKEND think that is?
//
// gpuset.go derives --tensor-split positions and --main-gpu from the TELEMETRY
// ordinal (nvidia-smi / rocm-smi / sysfs / DXGI order). The backend enumerates
// its own devices independently, and the two lists are neither the same order
// nor the same set:
//
//   - Order. On CUDA the runtime defaults to CUDA_DEVICE_ORDER=FASTEST_FIRST,
//     which cudaOrderEnv pins back to bus order. No such pin exists for Vulkan
//     or ROCm, where ggml enumerates through the loader and sorts discrete
//     adapters ahead of integrated ones. A box whose iGPU sorts first in
//     telemetry gets the whole ratio applied reversed, silently.
//   - Set. ggml-vulkan reports integrated adapters advertising a slice of system
//     RAM (16GB is common), which sails over minInferenceVramGB. Worse, a device
//     WE filter out is still counted by llama.cpp when it reads --tensor-split
//     and --main-gpu, so filtering shifts every position after it.
//
// Both problems disappear once the ordinal stops being implied. llama-server and
// sd-server each expose --list-devices with stable ids ("CUDA0", "Vulkan1",
// "ROCm0") and each accept those ids back, llama-server as --device and
// sd-server as --backend. Naming the devices makes our order authoritative on
// every backend rather than only on the one that happens to have an env var.
//
// The safety property here is refusal. A probe that cannot run, cannot be
// parsed, or cannot be matched confidently against the resolved GpuSet yields
// nothing, and the caller emits no device flags at all: the behaviour we had
// before, rather than a placement built on a guess.

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BackendDevice is one adapter as the backend binary names it.
//
// TotalGB is 0 when the backend reports no size (sd-server lists id and
// description only), which the matcher reads as "match on name alone".
type BackendDevice struct {
	ID      string
	Name    string
	TotalGB float64
}

// backendProbeTimeout bounds --list-devices. Enumerating adapters initialises
// the compute backend (a driver call, not a model load) and exits, so this is
// deliberately generous rather than tight: a cold Vulkan loader on a headless
// box is slow, and a timeout here costs a correct split for the whole config.
const backendProbeTimeout = 15 * time.Second

// llamaDeviceRe matches llama-server's listing:
//
//	Vulkan0: AMD Radeon RX 7900 XTX (24560 MiB, 23748 MiB free)
//
// The id is captured apart from the description because the id is what --device
// accepts and the description is what we match telemetry against.
var llamaDeviceRe = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9]*[0-9])\s*:\s*(.+?)\s*\((\d+)\s*MiB`)

var (
	backendDevMu    sync.Mutex
	backendDevCache = map[string][]BackendDevice{}
)

// ListBackendDevices runs `exe --list-devices` and parses the result, memoised
// per binary. The cache key carries size and mtime so a backend upgraded in
// place is re-probed instead of answering from a stale list.
//
// An error is an ordinary outcome, not a fault: older builds have no
// --list-devices, and a packaged install may name a binary that is not there
// yet. Callers degrade to unnamed placement.
func ListBackendDevices(exe string) ([]BackendDevice, error) {
	if strings.TrimSpace(exe) == "" {
		return nil, fmt.Errorf("no backend exe")
	}
	key := exe
	if st, err := os.Stat(exe); err == nil {
		key = fmt.Sprintf("%s|%d|%d", exe, st.Size(), st.ModTime().UnixNano())
	}

	backendDevMu.Lock()
	cached, hit := backendDevCache[key]
	backendDevMu.Unlock()
	if hit {
		if len(cached) == 0 {
			return nil, fmt.Errorf("no devices listed by %s", exe)
		}
		return cached, nil
	}

	devs, err := probeBackendDevices(exe)

	backendDevMu.Lock()
	// A failed probe is cached too. The failure is a property of this binary (no
	// such flag, missing runtime), and re-running a 15s probe once per model
	// would dominate generation on a box where it can never succeed.
	backendDevCache[key] = devs
	backendDevMu.Unlock()

	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, fmt.Errorf("no devices listed by %s", exe)
	}
	return devs, nil
}

func probeBackendDevices(exe string) ([]BackendDevice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backendProbeTimeout)
	defer cancel()

	// Some backends print the listing on stderr and the banner on stdout, and
	// which does what has moved across releases. Read the pair.
	out, err := exec.CommandContext(ctx, exe, "--list-devices").CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%s --list-devices: %w", exe, err)
	}
	return parseBackendDevices(string(out)), nil
}

// parseBackendDevices reads both listing shapes we ship against:
//
//	llama-server:  "  Vulkan0: AMD Radeon RX 7900 XTX (24560 MiB, 23748 MiB free)"
//	sd-server:     "vulkan0\tAMD Radeon RX 7900 XTX"
//
// CPU entries are dropped. They are never a placement target for a split, and
// leaving them in would shift every position after them.
func parseBackendDevices(out string) []BackendDevice {
	var devs []BackendDevice
	seen := map[string]bool{}

	add := func(id, name string, totalGB float64) {
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if id == "" || seen[id] || isCPUDeviceID(id) {
			return
		}
		seen[id] = true
		devs = append(devs, BackendDevice{ID: id, Name: name, TotalGB: totalGB})
	}

	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := llamaDeviceRe.FindStringSubmatch(line); m != nil {
			mib, _ := strconv.ParseFloat(m[3], 64)
			add(m[1], m[2], mib/1024.0)
			continue
		}
		// sd-server: "id<TAB>description", no size. The tab is the guard, so
		// prose in the same stream cannot be read as a device.
		if id, name, ok := strings.Cut(line, "\t"); ok {
			if strings.TrimSpace(name) != "" && !strings.ContainsAny(strings.TrimSpace(id), " :(") {
				add(id, name, 0)
			}
		}
	}
	return devs
}

// isCPUDeviceID reports whether an id names the CPU backend rather than an
// adapter. Tested on the id, which is a stable token, not the description.
func isCPUDeviceID(id string) bool {
	return strings.HasPrefix(strings.ToLower(id), "cpu")
}

// backendMatchToleranceGB is how far a backend's reported VRAM may sit from
// telemetry's and still be the same card. The two read one adapter through
// different APIs, so they disagree by driver reservations rather than by a lot;
// a wider gap means a different device.
const backendMatchToleranceGB = 1.5

// BackendIDs maps the set onto the ids the backend uses, positionally: the
// result is parallel to g, so it can be emitted beside a --tensor-split derived
// from the same ordering.
//
// Returns nil unless EVERY device matched unambiguously. A partial mapping is
// the dangerous case rather than a useful one, since a device list missing a
// card silently drops it from the plan the sizer already budgeted for. The
// whole mapping is refused and the caller falls back to unnamed placement.
//
// Matching is by name, with size as the tiebreak. Identical cards are genuinely
// interchangeable, so ties resolve in list order: swapping two adapters of the
// same model and capacity cannot change the outcome.
func (g GpuSet) BackendIDs(devs []BackendDevice) []string {
	if len(g) == 0 || len(devs) == 0 {
		return nil
	}
	out := make([]string, len(g))
	used := make([]bool, len(devs))

	for i, d := range g {
		best, bestScore := -1, math.Inf(1)
		for j, bd := range devs {
			if used[j] || !deviceNamesMatch(d.Name, bd.Name) {
				continue
			}
			// Size is the tiebreak, not the gate: a backend reporting no size
			// (sd-server) still matches on an unambiguous name.
			score := 0.0
			if bd.TotalGB > 0 {
				if score = math.Abs(bd.TotalGB - d.TotalGB); score > backendMatchToleranceGB {
					continue
				}
			}
			if score < bestScore {
				best, bestScore = j, score
			}
		}
		if best < 0 {
			return nil
		}
		used[best] = true
		out[i] = devs[best].ID
	}
	return out
}

// deviceNamesMatch compares adapter names across two APIs that describe the same
// silicon differently: nvidia-smi says "NVIDIA GeForce RTX 4070 Ti SUPER" where
// ggml may drop the vendor, and Windows adds "(TM)". Containment in either
// direction is the loosest rule that still cannot confuse two cards on one box,
// since a 4070 beside a 4070 Ti has neither name contained in the other.
func deviceNamesMatch(a, b string) bool {
	na, nb := normaliseDeviceName(a), normaliseDeviceName(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || strings.Contains(na, nb) || strings.Contains(nb, na)
}

// deviceNameNoise strips the parts that differ per API rather than per card:
// vendor and brand words, trademark marks, and separators. What survives is the
// model designation, which is what actually distinguishes two adapters.
var deviceNameNoise = strings.NewReplacer(
	"(tm)", "", "(r)", "", "®", "", "™", "",
	"nvidia", "", "amd", "", "intel", "", "corporation", "",
	"geforce", "", "radeon", "", "arc", "",
	" ", "", "-", "", "_", "",
)

func normaliseDeviceName(s string) string {
	return deviceNameNoise.Replace(strings.ToLower(strings.TrimSpace(s)))
}

// DeviceFlagFor resolves the `--device` value for a multi-GPU launch of exe,
// together with the POSITION that `--main-gpu` must name once the list is
// pinned. Returns "" and -1 when the devices cannot be named, and the caller
// then emits what it emitted before.
//
// Naming the devices changes what --main-gpu and --tensor-split index into.
// Without --device they are positions in the BACKEND's full device list, which
// includes adapters we filtered out and need not be in our order; with it they
// are positions in the list we just handed over, so main becomes g's own
// position rather than a telemetry ordinal.
//
// The probe doubles as the capability check. A build old enough to lack
// --device is also old enough to lack --list-devices, so it fails to parse and
// gets no flag: there is no version to compare and no way to emit an argument
// the binary will reject.
//
// Cost: one memoised subprocess per backend binary per process. The UI's launch
// preview renders through the same path, so the first render after a backend
// swap can pay the probe.
func DeviceFlagFor(exe string, g GpuSet, mainIndex int) (string, int) {
	if !g.Multi() {
		return "", -1
	}
	pos := -1
	for i, d := range g {
		if d.Index == mainIndex {
			pos = i
			break
		}
	}
	if pos < 0 {
		return "", -1
	}
	devs, err := ListBackendDevices(exe)
	if err != nil {
		return "", -1
	}
	ids := g.BackendIDs(devs)
	if len(ids) != len(g) {
		return "", -1
	}
	return strings.Join(ids, ","), pos
}
