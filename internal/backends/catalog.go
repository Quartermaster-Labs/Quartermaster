// Package backends manages downloadable inference-backend binaries: it knows
// which upstream GitHub project ships each backend, picks the right release
// asset for the host's GPU, unpacks it into a versioned folder under the
// bundle's bin/ directory, and reports what is installed.
//
// It is the in-app replacement for packaging/windows/fetch-backend.ps1, which
// could only run at install time and only wrote the generate YAML. The catalog
// below is the same "one maintenance point" as that script's $ASSET_PATTERNS:
// if upstream renames its release assets, update the regexes here.
//
// Deliberately NOT covered: backends with no published release (qwentts.cpp's
// tts-server, the SAM3 wrapper) or with a hand-patched runtime (an sd-server
// carrying vendored gfx1100 Tensile kernels — distinct from the stock upstream
// ROCm build, which IS installable here). Those stay manual "bring your own path" rows
// in the backend registry, which managed installs sit alongside rather than
// replace.
package backends

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

// Variant is one GPU-runtime flavour of a component's release assets. Patterns
// and Extra are keyed by GOOS; a component with no entry for the host OS cannot
// be installed there.
type Variant struct {
	ID    string `json:"id"`    // vulkan | cuda | rocm | metal | cpu | any
	Label string `json:"label"` // human name for the picker
	Note  string `json:"note,omitempty"`
	// Patterns are asset-name regexes, most-preferred first. Hand-written for the
	// built-in catalog; for a user-tracked source they are derived from the asset
	// the user picked (see derive.go) and never shown.
	Patterns map[string][]string `json:"patterns,omitempty"`
	// Exemplar records, per GOOS, the asset name a derived pattern came from. Set
	// only for tracked sources. It is what lets the UI say "you picked X" and what
	// ClosestAsset compares against when upstream renames its assets.
	Exemplar map[string]string `json:"exemplar,omitempty"`
	// Extra assets are unpacked into the same directory as the primary one (the
	// CUDA runtime zip llama.cpp ships separately). Best-effort: a miss warns.
	// A pattern may contain the placeholder "{v}", which is replaced with the
	// PairKey capture from the primary asset name and tried first — see PairKey.
	Extra map[string][]string `json:"extra,omitempty"`
	// PairKey is a regex with one capture group, run against the chosen primary
	// asset name. It exists because a release can ship several builds of the same
	// variant (llama.cpp publishes CUDA 12.4 and 13.3 side by side, each with its
	// own cudart zip) and pairing them by list order would eventually ship the
	// wrong runtime. Empty => extras are matched by pattern alone.
	PairKey string `json:"pairKey,omitempty"`
}

// Component is one installable backend (or helper binary).
type Component struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
	Repo  string `json:"repo"` // owner/name on github.com
	// Kind is the autogen backend-registry kind this component registers as
	// ("llama", "sd", "upscale", …). Empty => a helper binary that is installed
	// but never added to the registry (yt-dlp).
	Kind string `json:"kind"`
	// Exe is the executable file name per GOOS.
	Exe map[string]string `json:"-"`
	// Bare marks a release asset that IS the executable rather than an archive.
	Bare bool `json:"-"`
	// Manual marks an engine Quartermaster can drive but cannot install, because
	// upstream publishes no self-contained executable. It is listed anyway:
	// autogen has a first-class emitter for it, so leaving it out of the catalog
	// makes a supported engine look unsupported. The UI shows Setup in place of
	// the install controls, and Install() refuses.
	Manual bool `json:"manual"`
	// Setup is the short "how do I actually get this" shown for a Manual entry.
	Setup string `json:"setup,omitempty"`
	// Custom marks a component the user added by tracking a repo, rather than one
	// from the built-in table. Its patterns are derived, not curated, so the UI
	// offers edit/remove and shows which asset each variant resolves to.
	Custom bool `json:"custom,omitempty"`
	// AllowPrerelease lets "latest" resolve to a prerelease. Off for the built-in
	// catalog (upstream cuts stable releases); set automatically for a tracked
	// repo that publishes nothing but nightlies, which would otherwise resolve to
	// nothing installable.
	AllowPrerelease bool      `json:"allowPrerelease,omitempty"`
	Variants        []Variant `json:"variants"`
}

// ExeName returns the component's executable file name on the host OS.
func (c Component) ExeName() string {
	if n, ok := c.Exe[runtime.GOOS]; ok {
		return n
	}
	return c.Exe["default"]
}

// Variant looks a flavour up by id.
func (c Component) Variant(id string) (Variant, bool) {
	for _, v := range c.Variants {
		if strings.EqualFold(v.ID, id) {
			return v, true
		}
	}
	return Variant{}, false
}

// SupportedOn reports whether any variant publishes assets for goos.
func (c Component) SupportedOn(goos string) bool {
	for _, v := range c.Variants {
		if len(v.Patterns[goos]) > 0 {
			return true
		}
	}
	return false
}

// win/linux/darwin keys used throughout the catalog.
const (
	osWin   = "windows"
	osLinux = "linux"
	osMac   = "darwin"
)

// catalog is the static component table. Ported from fetch-backend.ps1 and
// extended with Linux/macOS assets and the ESRGAN upscaler.
var catalog = []Component{
	{
		ID:    "llama-server",
		Name:  "llama.cpp",
		Blurb: "llama-server - text, vision and embedding GGUFs.",
		Repo:  "ggml-org/llama.cpp",
		Kind:  "llama",
		Exe:   map[string]string{osWin: "llama-server.exe", "default": "llama-server"},
		Variants: []Variant{
			{
				ID: "vulkan", Label: "Vulkan", Note: "Any GPU (AMD / Intel / NVIDIA). The safe pick on non-NVIDIA hardware.",
				Patterns: map[string][]string{
					osWin:   {`^llama-.*-bin-win-vulkan-x64\.zip$`},
					osLinux: {`^llama-.*-bin-ubuntu-vulkan-x64\.tar\.gz$`, `^llama-.*-bin-ubuntu-vulkan-x64\.zip$`},
				},
			},
			{
				ID: "cuda", Label: "CUDA", Note: "NVIDIA only, Windows only. Pulls the matching cudart runtime alongside. Upstream publishes no Linux CUDA build, so an NVIDIA card on Linux uses the Vulkan variant.",
				Patterns: map[string][]string{
					// No osLinux entry on purpose: llama.cpp's release assets carry
					// ubuntu builds for vulkan/rocm/cpu but none for CUDA, so there
					// is nothing to match; DefaultVariant's len(Patterns[goos])
					// guard is what steers a Linux NVIDIA host to Vulkan instead.
					// Newest CUDA major first: upstream ships 12.x and 13.x of the
					// same build, and the newer toolkit is the better default on a
					// current driver.
					osWin: {`^llama-.*-bin-win-cuda-13\..*x64\.zip$`, `^llama-.*-bin-win-cuda-.*x64\.zip$`},
				},
				// {v} is the CUDA version captured from the primary asset, so a
				// 13.3 build never gets paired with the 12.4 runtime.
				PairKey: `-cuda-([0-9]+\.[0-9]+)-`,
				Extra: map[string][]string{
					osWin: {`^cudart-llama-bin-win-cuda-{v}-x64\.zip$`, `^cudart-llama.*-x64\.zip$`, `^cudart-.*cuda.*\.zip$`},
				},
			},
			{
				ID: "rocm", Label: "ROCm / HIP", Note: "AMD only. Usually faster than Vulkan on RDNA3, and not subject to Vulkan's 2 GB allocation cap.",
				// Upstream renamed the Windows asset from -bin-win-hip-radeon-x64
				// to -bin-win-rocm-<toolkit>-x64 (b10733 onwards); the old name is
				// kept as a fallback for an older tag the user might pin, but it
				// appears in none of the 60 newest releases. With only the old
				// pattern this variant matched nothing on Windows at all - the
				// picker offered ROCm and every install failed to resolve.
				Patterns: map[string][]string{
					osWin:   {`^llama-.*-bin-win-rocm-.*x64\.zip$`, `^llama-.*-bin-win-hip-radeon-x64\.zip$`, `^llama-.*-bin-win-hip-.*x64\.zip$`},
					osLinux: {`^llama-.*-bin-ubuntu-rocm-.*-x64\.tar\.gz$`, `^llama-.*-bin-ubuntu-rocm-.*-x64\.zip$`},
				},
			},
			{
				ID: "metal", Label: "Metal", Note: "Apple silicon only. The macOS arm64 build is GPU-accelerated through Metal; there is nothing else to install on an M-series Mac.",
				Patterns: map[string][]string{
					// Upstream builds macos-arm64 with Metal ON and macos-x64 with it
					// OFF, so the arm64 asset is the GPU build and belongs here rather
					// than under CPU, where it used to sit unlabelled.
					osMac: {`^llama-.*-bin-macos-arm64\.tar\.gz$`, `^llama-.*-bin-macos-arm64\.zip$`},
				},
			},
			{
				ID: "cpu", Label: "CPU", Note: "No GPU acceleration. On macOS this is the Intel (x64) build.",
				Patterns: map[string][]string{
					osWin:   {`^llama-.*-bin-win-cpu-x64\.zip$`, `^llama-.*-bin-win-avx2-x64\.zip$`},
					osLinux: {`^llama-.*-bin-ubuntu-x64\.tar\.gz$`, `^llama-.*-bin-ubuntu-x64\.zip$`},
					osMac:   {`^llama-.*-bin-macos-x64\.tar\.gz$`, `^llama-.*-bin-macos-x64\.zip$`},
				},
			},
		},
	},
	{
		// vLLM is a real backend here — autogen emits `vllm serve` commands and the
		// registry has a "vllm" kind — but it can never be a managed install. Its
		// releases attach Python wheels, not executables
		// (vllm-0.26.0+cu129-cp38-abi3-manylinux_2_28_x86_64.whl), so "installing"
		// it means provisioning a Python environment and letting pip pull torch and
		// the CUDA runtime from PyPI. There is no Windows wheel at all, and the
		// ROCm build is source- or container-only.
		ID:     "vllm",
		Name:   "vLLM",
		Blurb:  "vllm serve - high-throughput GPU serving for full-precision and AWQ/GPTQ models.",
		Repo:   "vllm-project/vllm",
		Kind:   "vllm",
		Manual: true,
		Setup: "Linux or WSL2 only - there is no Windows build. Install it into a Python 3.12 environment " +
			"(`uv pip install vllm`, NVIDIA) or use the ROCm container on AMD, then add the `vllm` " +
			"executable as a backend path below. Quartermaster generates the serve command for it either way.",
	},
	{
		ID:    "sd-server",
		Name:  "stable-diffusion.cpp",
		Blurb: "sd-server - diffusion models (SD / SDXL / Flux / Qwen-Image).",
		Repo:  "leejet/stable-diffusion.cpp",
		Kind:  "sd",
		Exe:   map[string]string{osWin: "sd-server.exe", "default": "sd-server"},
		Variants: []Variant{
			// stable-diffusion.cpp capitalises its platform segment ("Linux-Ubuntu",
			// "Darwin-macOS"), hence the (?i) on every pattern here.
			{
				ID: "vulkan", Label: "Vulkan", Note: "Any GPU. Note the 2 GB single-allocation cap on AMD drivers at high resolutions.",
				Patterns: map[string][]string{
					osWin:   {`(?i)^sd-.*-bin-win-vulkan-x64\.zip$`, `(?i)^sd-.*vulkan.*\.zip$`},
					osLinux: {`(?i)^sd-.*ubuntu.*vulkan.*\.zip$`},
				},
			},
			{
				ID: "cuda", Label: "CUDA", Note: "NVIDIA only, Windows only. Pulls the matching cudart runtime alongside. Upstream publishes no Linux CUDA build, so an NVIDIA card on Linux uses the Vulkan variant.",
				Patterns: map[string][]string{
					// Windows-only for the same reason as llama.cpp above: the
					// upstream release carries no ubuntu CUDA asset to match.
					osWin: {`(?i)^sd-.*-bin-win-cuda[0-9]*-x64\.zip$`, `(?i)^sd-.*cuda.*\.zip$`},
				},
				PairKey: `(?i)-cuda([0-9]+)-`,
				Extra: map[string][]string{
					osWin: {`(?i)^cudart-sd-bin-win-cu{v}-x64\.zip$`, `(?i)^cudart-sd.*\.zip$`},
				},
			},
			{
				ID: "rocm", Label: "ROCm / HIP", Note: "AMD only. Avoids Vulkan's 2 GB single-allocation cap, so high-resolution generation works.",
				Patterns: map[string][]string{
					osWin:   {`(?i)^sd-.*-bin-win-rocm-.*-x64\.zip$`, `(?i)^sd-.*win.*(rocm|hip).*\.zip$`},
					osLinux: {`(?i)^sd-.*ubuntu.*(rocm|hip).*\.zip$`},
				},
			},
			{
				ID: "metal", Label: "Metal", Note: "Apple silicon only. Upstream's single macOS build links Metal and MetalKit, so it is the accelerated one - there is nothing else to install on a Mac.",
				Patterns: map[string][]string{
					// Upstream's macOS job passes no -DSD_METAL, but ggml defaults
					// GGML_METAL to ON on Apple, and the shipped
					// libstable-diffusion.dylib does link Metal.framework and carry
					// the ggml-metal kernels. One asset, and it is a GPU build, so
					// darwin has no cpu entry at all rather than a mislabelled one.
					osMac: {`(?i)^sd-.*(darwin|macos).*\.zip$`},
				},
			},
			{
				ID: "cpu", Label: "CPU", Note: "No GPU acceleration - very slow for diffusion.",
				Patterns: map[string][]string{
					osWin:   {`(?i)^sd-.*-bin-win-cpu-x64\.zip$`, `(?i)^sd-.*-bin-win-avx2-x64\.zip$`, `(?i)^sd-.*avx2.*\.zip$`},
					osLinux: {`(?i)^sd-.*bin-linux-ubuntu-[0-9.]+-x86_64\.zip$`, `(?i)^sd-.*ubuntu.*(avx2|cpu).*\.zip$`},
				},
			},
		},
	},
	{
		ID:    "upscaler",
		Name:  "Real-ESRGAN (ncnn)",
		Blurb: "realesrgan-ncnn-vulkan - the exec-per-request image upscaler.",
		Repo:  "xinntao/Real-ESRGAN",
		Kind:  "upscale",
		Exe:   map[string]string{osWin: "realesrgan-ncnn-vulkan.exe", "default": "realesrgan-ncnn-vulkan"},
		Variants: []Variant{
			{
				ID: "any", Label: "Vulkan", Note: "One Vulkan build per OS; ships its own ncnn model files.",
				Patterns: map[string][]string{
					osWin:   {`^realesrgan-ncnn-vulkan-.*windows\.zip$`},
					osLinux: {`^realesrgan-ncnn-vulkan-.*ubuntu\.zip$`},
					osMac:   {`^realesrgan-ncnn-vulkan-.*macos\.zip$`},
				},
			},
		},
	},
	{
		ID:    "yt-dlp",
		Name:  "yt-dlp",
		Blurb: "Helper for the chat media_transcript tool. Not an inference backend.",
		Repo:  "yt-dlp/yt-dlp",
		Kind:  "", // never registered as a backend
		Exe:   map[string]string{osWin: "yt-dlp.exe", osMac: "yt-dlp_macos", "default": "yt-dlp"},
		Bare:  true,
		Variants: []Variant{
			{
				ID: "any", Label: "Standalone", Note: "Single self-contained executable (bundles its own Python).",
				Patterns: map[string][]string{
					osWin: {`^yt-dlp\.exe$`},
					// yt-dlp_linux bundles its own Python; the bare "yt-dlp" zipapp
					// needs a system interpreter, so it is only the fallback.
					osLinux: {`^yt-dlp_linux$`, `^yt-dlp$`},
					osMac:   {`^yt-dlp_macos$`},
				},
			},
		},
	},
}

// Catalog returns the installable components.
func Catalog() []Component { return catalog }

// Find returns the component with the given id.
func Find(id string) (Component, bool) {
	for _, c := range catalog {
		if strings.EqualFold(c.ID, id) {
			return c, true
		}
	}
	return Component{}, false
}

// SelectAsset returns the first asset name matching any pattern, in pattern
// order (so a component can express a preference across naming schemes).
func SelectAsset(names []string, patterns []string) string {
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		for _, n := range names {
			if re.MatchString(n) {
				return n
			}
		}
	}
	return ""
}

// MatchAssets picks the primary asset (and any extras) for one variant on one
// OS out of a release's asset names.
func (c Component) MatchAssets(variantID, goos string, names []string) (primary string, extra []string, err error) {
	v, ok := c.Variant(variantID)
	if !ok {
		return "", nil, fmt.Errorf("%s: unknown variant %q", c.ID, variantID)
	}
	pats := v.Patterns[goos]
	if len(pats) == 0 {
		return "", nil, fmt.Errorf("%s: no %s build published for %s", c.ID, v.ID, goos)
	}
	primary = SelectAsset(names, pats)
	if primary == "" {
		return "", nil, fmt.Errorf("%s: this release has no %s asset for %s", c.ID, v.ID, goos)
	}
	if e := SelectAsset(names, expandPairKey(v.Extra[goos], v.PairKey, primary)); e != "" {
		extra = append(extra, e)
	}
	return primary, extra, nil
}

// expandPairKey resolves the "{v}" placeholder in extra-asset patterns from the
// primary asset's name (see Variant.PairKey). Patterns that still contain "{v}"
// after a failed capture are dropped rather than matched literally, so a naming
// change upstream degrades to the generic fallback pattern instead of silently
// pairing the wrong runtime.
func expandPairKey(patterns []string, pairKey, primary string) []string {
	if len(patterns) == 0 {
		return nil
	}
	key := ""
	if pairKey != "" {
		if re, err := regexp.Compile(pairKey); err == nil {
			if m := re.FindStringSubmatch(primary); len(m) > 1 {
				key = m[1]
			}
		}
	}
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if !strings.Contains(p, "{v}") {
			out = append(out, p)
			continue
		}
		if key == "" {
			continue
		}
		out = append(out, strings.ReplaceAll(p, "{v}", regexp.QuoteMeta(key)))
	}
	return out
}

// SuggestVariant maps the host's GPU names onto the variant a fresh install
// should preselect. Apple silicon gets Metal, NVIDIA gets CUDA, everything else
// with a discrete GPU gets Vulkan, and a machine with no detected GPU gets CPU.
// The user can always override in the picker — this only decides the default
// selection.
//
// AMD deliberately defaults to Vulkan rather than ROCm even though ROCm is
// usually faster: the Vulkan build runs on any driver, while the ROCm one is
// tied to specific gfx targets and can fail at load on an unsupported card.
// ROCm is offered in the picker as an informed opt-in.
func SuggestVariant(gpuNames []string) string {
	seen := ""
	for _, n := range gpuNames {
		l := strings.ToLower(n)
		switch {
		case strings.Contains(l, "nvidia") || strings.Contains(l, "geforce") ||
			strings.Contains(l, "quadro") || strings.Contains(l, "tesla") || strings.Contains(l, "rtx"):
			return "cuda" // a discrete NVIDIA card wins outright
		case strings.Contains(l, "apple"):
			return "metal" // Apple silicon has no other accelerated path
		case strings.Contains(l, "amd") || strings.Contains(l, "radeon") || strings.Contains(l, "intel") ||
			strings.Contains(l, "arc"):
			seen = "vulkan"
		default:
			if strings.TrimSpace(l) != "" && seen == "" {
				seen = "vulkan" // unknown but present GPU: Vulkan is the portable bet
			}
		}
	}
	if seen == "" {
		return "cpu"
	}
	return seen
}

// DefaultVariant is the variant preselected for a component given the host's
// GPUs, falling back to whatever the component actually publishes (an upscaler
// with only an "any" build ignores the GPU suggestion).
func (c Component) DefaultVariant(gpuNames []string, goos string) string {
	want := SuggestVariant(gpuNames)
	// On macOS, Metal is the only acceleration there is, and GPU names are not
	// always reported. Preferring a published Metal build over the suggestion
	// keeps an Apple-silicon host off the Intel CPU asset when the probe came
	// back empty; the picker still lists everything.
	if goos == osMac {
		if v, ok := c.Variant("metal"); ok && len(v.Patterns[goos]) > 0 {
			return v.ID
		}
	}
	if v, ok := c.Variant(want); ok && len(v.Patterns[goos]) > 0 {
		return want
	}
	for _, v := range c.Variants {
		if len(v.Patterns[goos]) > 0 {
			return v.ID
		}
	}
	return ""
}
